package domain

import (
	"bro-bot/internal/ports"
	"bro-bot/internal/storage"
	"bro-bot/internal/utils"
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// TaskStatus определяет текущий жизненный цикл задачи.
type TaskStatus string

const (
	TaskStatusQueued          TaskStatus = "queued"
	TaskStatusPlanning        TaskStatus = "planning"
	TaskStatusWaitingApproval TaskStatus = "waiting_approval"
	TaskStatusRunning         TaskStatus = "running"
	TaskStatusWaitingInput    TaskStatus = "waiting_input"
	TaskStatusPaused          TaskStatus = "paused"
	TaskStatusCompleted       TaskStatus = "completed"
	TaskStatusCancelled       TaskStatus = "cancelled"
	TaskStatusFailed          TaskStatus = "failed"
)

func (s TaskStatus) RussianTitle() string {
	switch s {
	case TaskStatusQueued:
		return "⏳ В очереди"
	case TaskStatusPlanning:
		return "📝 Составление плана"
	case TaskStatusWaitingApproval:
		return "📋 Ожидает утверждения плана"
	case TaskStatusRunning:
		return "⚙️ Выполняется"
	case TaskStatusWaitingInput:
		return "❓ Ждёт вашего ответа"
	case TaskStatusPaused:
		return "⏸ Приостановлена"
	case TaskStatusCompleted:
		return "✅ Завершена"
	case TaskStatusCancelled:
		return "🛑 Отменена"
	case TaskStatusFailed:
		return "❌ Ошибка"
	default:
		return string(s)
	}
}

func (s TaskStatus) Emoji() string {
	switch s {
	case TaskStatusQueued:
		return "⏳"
	case TaskStatusPlanning:
		return "📝"
	case TaskStatusWaitingApproval:
		return "📋"
	case TaskStatusRunning:
		return "⚙️"
	case TaskStatusWaitingInput:
		return "❓"
	case TaskStatusPaused:
		return "⏸"
	case TaskStatusCompleted:
		return "✅"
	case TaskStatusCancelled:
		return "🛑"
	case TaskStatusFailed:
		return "❌"
	default:
		return "•"
	}
}

// TaskSession хранит полное состояние отдельной задачи.
type TaskSession struct {
	// mu приватный: снаружи домена состояние задачи читается через Snapshot и
	// правится через Update, чтобы забытая блокировка не превращалась в гонку.
	mu               sync.Mutex
	ID               int
	Project          string
	Model            string
	Agent            string
	InitialPrompt    string
	CurrentPrompt    string
	Status           TaskStatus
	RequiresPlan     bool
	Plan             string
	PlanApproved     bool
	StartedAt        time.Time
	FinishedAt       time.Time
	RecentLogs       []string
	PendingFollowups []string
	LastPRURL        string
	FullOutput       strings.Builder
	LiveMsg          *ports.MessageRef
	Chat             ports.ChatID
	LastModelUsed    string
	LastTokensUsed   string
	ConversationID   string
	LastQuestion     string
	QuestionOptions  []string
	QuestionAskedAt  time.Time
	AnswerChan       chan string
	PauseChan        chan struct{}
	TokenMetrics     *TaskTokenMetrics
	storage          storage.Storage

	// process — живой процесс текущего шага, nil между шагами. Домен видит только
	// интерфейс: как остановить агента (сигнал группе у CLI, закрытие pipe у API),
	// решает адаптер. cancel отменяет контекст шага и тем самым обрывает API-стрим.
	// Тот же приём, что у ChatSession.
	process ports.AgentProcess
	cancel  context.CancelFunc

	// outputTruncated — FullOutput упёрся в потолок; дальше вывод не копится.
	outputTruncated bool
	// logSink принимает строки лога для записи в хранилище пачками; nil — писать
	// синхронно в storage (менеджер без писателя).
	logSink func(taskID int, line string)
}

// Потолок накопленного вывода шага. Длинный шаг с многословным агентом копил бы
// сотни мегабайт: для плана и отчёта хватает первого мегабайта.
const (
	maxFullOutputBytes    = 1 << 20
	outputTruncatedMarker = "\n… (вывод обрезан: превышен лимит хранения)\n"
)

// AppendOutputLocked дописывает текст в FullOutput с учётом потолка (мьютекс должен
// быть уже захвачен). Обрезка проходит по границе руны.
func (t *TaskSession) AppendOutputLocked(text string) {
	if t.outputTruncated || text == "" {
		return
	}
	room := maxFullOutputBytes - t.FullOutput.Len()
	if len(text) <= room {
		t.FullOutput.WriteString(text)
		return
	}
	if room > 0 {
		cut := room
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		t.FullOutput.WriteString(text[:cut])
	}
	t.FullOutput.WriteString(outputTruncatedMarker)
	t.outputTruncated = true
}

// ResetOutputLocked очищает накопленный вывод шага (мьютекс должен быть уже захвачен).
func (t *TaskSession) ResetOutputLocked() {
	t.FullOutput.Reset()
	t.outputTruncated = false
}

// OutputTruncated сообщает, был ли вывод шага обрезан по потолку.
func (t *TaskSession) OutputTruncated() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.outputTruncated
}

// AttachProcess привязывает к задаче процесс шага и отмену его контекста.
// Возвращает false, если задача уже отменена: /cancel мог прийти между запуском
// процесса и привязкой, и тогда вызывающий код должен остановить процесс сам.
func (t *TaskSession) AttachProcess(p ports.AgentProcess, cancel context.CancelFunc) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Status == TaskStatusCancelled {
		return false
	}
	t.process = p
	t.cancel = cancel
	return true
}

// DetachProcess снимает привязку по завершении шага и закрывает stdin.
func (t *TaskSession) DetachProcess() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.process != nil {
		_ = t.process.Stdin().Close()
	}
	t.process = nil
	t.cancel = nil
}

// HasLiveProcess сообщает, выполняется ли сейчас шаг задачи.
func (t *TaskSession) HasLiveProcess() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hasLiveProcessLocked()
}

// hasLiveProcessLocked — то же без захвата мьютекса.
func (t *TaskSession) hasLiveProcessLocked() bool {
	return t.process != nil
}

// terminateLocked останавливает процесс шага: отмена контекста обрывает API-стрим и
// убивает CLI-процесс через CommandContext, Kill добивает группу процессов у CLI и
// закрывает pipe у API. Все вызовы неблокирующие, держать мьютекс безопасно.
func (t *TaskSession) terminateLocked() {
	if t.cancel != nil {
		t.cancel()
	}
	if t.process != nil {
		_ = t.process.Kill()
	}
	t.process = nil
	t.cancel = nil
}

// writeStdinLocked передаёт строку в stdin агента, если процесс запущен.
func (t *TaskSession) writeStdinLocked(text string) {
	if t.process == nil {
		return
	}
	_, _ = io.WriteString(t.process.Stdin(), text+"\n")
}

// WorkerPID возвращает PID процесса шага или 0, если процесса в ОС нет (api-режим).
func (t *TaskSession) WorkerPID() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.workerPIDLocked()
}

func (t *TaskSession) workerPIDLocked() int {
	if t.process == nil {
		return 0
	}
	return t.process.PID()
}

// durationLocked возвращает время работы задачи без захвата мьютекса (мьютекс должен быть уже захвачен вызывающим кодом).
func (t *TaskSession) durationLocked() time.Duration {
	if t.StartedAt.IsZero() {
		return 0
	}
	if !t.FinishedAt.IsZero() {
		return t.FinishedAt.Sub(t.StartedAt).Round(time.Second)
	}
	return time.Since(t.StartedAt).Round(time.Second)
}

// Duration возвращает время работы задачи.
func (t *TaskSession) Duration() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.durationLocked()
}

// isActiveLocked проверяет статус без захвата мьютекса.
func (t *TaskSession) isActiveLocked() bool {
	return t.Status == TaskStatusRunning ||
		t.Status == TaskStatusWaitingInput ||
		t.Status == TaskStatusQueued ||
		t.Status == TaskStatusPlanning ||
		t.Status == TaskStatusWaitingApproval
}

// IsActive возвращает true, если задача выполняется или находится в очереди.
func (t *TaskSession) IsActive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.isActiveLocked()
}

// TaskView — согласованный снимок состояния задачи для чтения вне домена.
//
// Все поля описывают один момент времени: обработчику больше не нужно брать мьютекс
// вручную, а значит и забыть его нельзя. Срезы в снимке — копии, править их бесполезно:
// изменения задачи делаются через Update.
type TaskView struct {
	ID            int
	Project       string
	Model         string
	Agent         string
	InitialPrompt string
	CurrentPrompt string
	Status        TaskStatus
	RequiresPlan  bool
	Plan          string
	PlanApproved  bool

	StartedAt       time.Time
	FinishedAt      time.Time
	QuestionAskedAt time.Time
	Duration        time.Duration

	// IsActive и HasLiveProcess лежат в снимке, чтобы статус задачи и наличие живого
	// процесса читались вместе: раздельные вызовы могли бы застать разные моменты.
	IsActive       bool
	HasLiveProcess bool

	RecentLogs       []string
	PendingFollowups []string
	QuestionOptions  []string

	LastPRURL      string
	LastModelUsed  string
	LastTokensUsed string
	ConversationID string
	LastQuestion   string

	// Output — накопленный вывод шага (бывший FullOutput.String()).
	Output          string
	OutputTruncated bool

	Chat         ports.ChatID
	LiveMsg      *ports.MessageRef
	TokenMetrics *TaskTokenMetrics
}

// Snapshot возвращает согласованный снимок состояния задачи.
func (t *TaskSession) Snapshot() TaskView {
	t.mu.Lock()
	defer t.mu.Unlock()

	return TaskView{
		ID:            t.ID,
		Project:       t.Project,
		Model:         t.Model,
		Agent:         t.Agent,
		InitialPrompt: t.InitialPrompt,
		CurrentPrompt: t.CurrentPrompt,
		Status:        t.Status,
		RequiresPlan:  t.RequiresPlan,
		Plan:          t.Plan,
		PlanApproved:  t.PlanApproved,

		StartedAt:       t.StartedAt,
		FinishedAt:      t.FinishedAt,
		QuestionAskedAt: t.QuestionAskedAt,
		Duration:        t.durationLocked(),

		IsActive:       t.isActiveLocked(),
		HasLiveProcess: t.hasLiveProcessLocked(),

		RecentLogs:       append([]string(nil), t.RecentLogs...),
		PendingFollowups: append([]string(nil), t.PendingFollowups...),
		QuestionOptions:  append([]string(nil), t.QuestionOptions...),

		LastPRURL:      t.LastPRURL,
		LastModelUsed:  t.LastModelUsed,
		LastTokensUsed: t.LastTokensUsed,
		ConversationID: t.ConversationID,
		LastQuestion:   t.LastQuestion,

		Output:          t.FullOutput.String(),
		OutputTruncated: t.outputTruncated,

		Chat:         t.Chat,
		LiveMsg:      t.LiveMsg,
		TokenMetrics: t.TokenMetrics,
	}
}

// Update выполняет fn под мьютексом задачи: все правки внутри видны как одно изменение.
// Значения, нужные после правки, забираются захватом переменной — тогда чтение и запись
// остаются одним атомарным куском:
//
//	var proj string
//	task.Update(func(t *domain.TaskSession) {
//	    t.Status = domain.TaskStatusRunning
//	    proj = t.Project
//	})
//
// Внутри fn нельзя вызывать методы задачи (AppendLog, Snapshot, DetachProcess и другие):
// мьютекс уже захвачен, получится взаимоблокировка. Поля правятся напрямую.
func (t *TaskSession) Update(fn func(*TaskSession)) {
	if fn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	fn(t)
}

// AnswerChannel — канал ответов пользователя на вопрос агента (для select в пайплайне).
func (t *TaskSession) AnswerChannel() <-chan string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.AnswerChan
}

// PauseChannel — канал сигнала о паузе или отмене задачи (для select в пайплайне).
func (t *TaskSession) PauseChannel() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.PauseChan
}

// AppendLog безопасно добавляет запись в лог с ограничением глубины и сохраняет в БД.
func (t *TaskSession) AppendLog(line string) {
	t.mu.Lock()
	t.RecentLogs = append(t.RecentLogs, line)
	if len(t.RecentLogs) > 20 {
		t.RecentLogs = t.RecentLogs[1:]
	}
	sink := t.logSink
	s := t.storage
	id := t.ID
	t.mu.Unlock()

	// Запись в хранилище уходит в писатель логов: пайплайн зовёт AppendLog на каждое
	// событие агента, и синхронный INSERT на каждую строку тормозил бы чтение потока.
	if sink != nil {
		sink(id, line)
		return
	}
	if s != nil {
		if err := s.AppendLog(context.Background(), id, line); err != nil {
			log.Printf("Предупреждение: не удалось записать лог задачи #%d: %v", id, err)
		}
	}
}

// ClearPendingFollowups очищает очередь правок и синхронизирует с хранилищем.
func (t *TaskSession) ClearPendingFollowups() {
	t.mu.Lock()
	t.PendingFollowups = nil
	s := t.storage
	id := t.ID
	t.mu.Unlock()

	if s != nil {
		_ = s.ClearFollowups(context.Background(), id)
	}
}

// DeliverAnswer безопасно передаёт ответ пользователя на вопрос агента.
func (t *TaskSession) DeliverAnswer(answer string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.writeStdinLocked(answer)

	if t.AnswerChan == nil {
		t.AnswerChan = make(chan string, 1)
	}

	select {
	case t.AnswerChan <- answer:
		return true
	default:
		select {
		case <-t.AnswerChan:
		default:
		}
		t.AnswerChan <- answer
		return true
	}
}

// PauseTask переводит ожидающую ввода задачу в режим паузы.
func (t *TaskSession) PauseTask() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.Status == TaskStatusWaitingInput {
		t.Status = TaskStatusPaused
		t.terminateLocked()
		if t.PauseChan != nil {
			select {
			case t.PauseChan <- struct{}{}:
			default:
			}
		}
		return true
	}
	return false
}

// TaskManager управляет жизненным циклом множества задач и переключением активной задачи.
type TaskManager struct {
	sync.RWMutex
	tasks        map[int]*TaskSession
	taskOrder    []int
	activeTaskID int
	nextID       int
	msgToTask    map[string]int // messageID -> taskID
	storage      storage.Storage
	logs         *logWriter
}

var GlobalTaskManager = NewTaskManager()

func taskRecordToSession(rec *storage.TaskRecord, s storage.Storage) *TaskSession {
	return &TaskSession{
		ID:              rec.ID,
		Project:         rec.Project,
		Model:           rec.Model,
		Agent:           rec.Agent,
		InitialPrompt:   rec.InitialPrompt,
		CurrentPrompt:   rec.CurrentPrompt,
		Status:          TaskStatus(rec.Status),
		RequiresPlan:    rec.RequiresPlan,
		Plan:            rec.Plan,
		PlanApproved:    rec.PlanApproved,
		StartedAt:       rec.StartedAt,
		FinishedAt:      rec.FinishedAt,
		LastPRURL:       rec.LastPRURL,
		ConversationID:  rec.ConversationID,
		LastQuestion:    rec.LastQuestion,
		QuestionOptions: rec.QuestionOptions,
		QuestionAskedAt: rec.QuestionAskedAt,
		LastModelUsed:   rec.LastModelUsed,
		LastTokensUsed:  rec.LastTokensUsed,
		Chat:            ports.ChatID(rec.RecipientID),
		AnswerChan:      make(chan string, 1),
		PauseChan:       make(chan struct{}, 1),
		storage:         s,
	}
}

// NewTaskManager создаёт новый менеджер задач без постоянного хранилища (в памяти).
func NewTaskManager() *TaskManager {
	return NewTaskManagerWithStorage(nil)
}

// NewTaskManagerWithStorage создаёт менеджер задач с подключённым хранилищем.
func NewTaskManagerWithStorage(s storage.Storage) *TaskManager {
	tm := &TaskManager{
		tasks:     make(map[int]*TaskSession),
		taskOrder: make([]int, 0),
		msgToTask: make(map[string]int),
		nextID:    1,
	}
	if s != nil {
		tm.InitWithStorage(s)
	}
	return tm
}

// InitWithStorage инициализирует менеджер задачами из постоянного хранилища.
func (tm *TaskManager) InitWithStorage(s storage.Storage) {
	tm.Lock()
	defer tm.Unlock()

	// Переинициализация полностью перестраивает состояние по данным хранилища,
	// а не накапливает его поверх предыдущего.
	tm.storage = s
	tm.tasks = make(map[int]*TaskSession)
	tm.taskOrder = nil
	tm.msgToTask = make(map[string]int)
	tm.activeTaskID = 0
	tm.nextID = 1

	if tm.logs != nil {
		tm.logs.close()
		tm.logs = nil
	}
	if s == nil {
		return
	}
	tm.logs = newLogWriter(s)

	ctx := context.Background()
	_, _ = s.RecoverInterruptedTasks(ctx)

	records, err := s.ListTasks(ctx)
	if err == nil {
		maxID := 0
		for _, rec := range records {
			sess := taskRecordToSession(rec, s)
			sess.logSink = tm.logSinkLocked()
			if fws, err := s.GetFollowups(ctx, rec.ID); err == nil {
				sess.PendingFollowups = fws
			}
			if logs, err := s.GetRecentLogs(ctx, rec.ID, 20); err == nil {
				sess.RecentLogs = logs
			}
			if m, err := s.GetMetrics(ctx, rec.ID); err == nil && m != nil {
				sess.TokenMetrics = &TaskTokenMetrics{
					Project:         rec.Project,
					Model:           m.Model,
					PRURL:           m.PRURL,
					ConversationID:  m.ConversationID,
					DurationSeconds: m.DurationSeconds,
					Turns:           m.Turns,
					ToolCallsCount:  m.ToolCallsCount,
					Usage: UsageStats{
						InputTokens:     m.InputTokens,
						OutputTokens:    m.OutputTokens,
						ThinkingTokens:  m.ThinkingTokens,
						CacheReadTokens: m.CacheReadTokens,
						TotalTokens:     m.TotalTokens,
					},
					LastStepUsage: UsageStats{
						InputTokens:     m.LastStepInputTokens,
						OutputTokens:    m.LastStepOutputTokens,
						ThinkingTokens:  m.LastStepThinkingTokens,
						CacheReadTokens: m.LastStepCacheReadTokens,
						TotalTokens:     m.LastStepTotalTokens,
					},
				}
			}

			tm.tasks[rec.ID] = sess
			tm.taskOrder = append(tm.taskOrder, rec.ID)
			if rec.ID > maxID {
				maxID = rec.ID
			}
		}
		if maxID >= tm.nextID {
			tm.nextID = maxID + 1
		}
	}

	if msgMap, err := s.ListAllMessageTasks(ctx); err == nil {
		for msgID, taskID := range msgMap {
			tm.msgToTask[msgID] = taskID
		}
	}

	if val, err := s.GetSetting(ctx, "active_task_id"); err == nil && val != "" {
		if id, err := strconv.Atoi(val); err == nil {
			if _, ok := tm.tasks[id]; ok {
				tm.activeTaskID = id
			}
		}
	}
	if tm.activeTaskID == 0 && len(tm.taskOrder) > 0 {
		tm.activeTaskID = tm.taskOrder[len(tm.taskOrder)-1]
	}
}

// Storage возвращает используемое хранилище.
func (tm *TaskManager) Storage() storage.Storage {
	tm.RLock()
	defer tm.RUnlock()
	return tm.storage
}

// CreateTask создаёт задачу без обязательного плана и регистрирует её в менеджере.
func (tm *TaskManager) CreateTask(project, model, prompt string, chat ports.ChatID) *TaskSession {
	return tm.CreateTaskWithPlanAndAgent(project, model, "agy", prompt, chat, false)
}

// CreateTaskWithPlan создаёт задачу с возможностью требования предварительного плана (по умолчанию агент agy).
func (tm *TaskManager) CreateTaskWithPlan(project, model, prompt string, chat ports.ChatID, requiresPlan bool) *TaskSession {
	return tm.CreateTaskWithPlanAndAgent(project, model, "agy", prompt, chat, requiresPlan)
}

// CreateTaskWithPlanAndAgent создаёт задачу с указанием агента и требования предварительного плана.
func (tm *TaskManager) CreateTaskWithPlanAndAgent(project, model, agent, prompt string, chat ports.ChatID, requiresPlan bool) *TaskSession {
	tm.Lock()
	defer tm.Unlock()

	if agent == "" {
		agent = "agy"
	}

	id := tm.nextID
	tm.nextID++

	task := &TaskSession{
		ID:            id,
		Project:       project,
		Model:         model,
		Agent:         agent,
		InitialPrompt: prompt,
		CurrentPrompt: prompt,
		Status:        TaskStatusQueued,
		RequiresPlan:  requiresPlan,
		Chat:          chat,
		AnswerChan:    make(chan string, 1),
		PauseChan:     make(chan struct{}, 1),
		storage:       tm.storage,
		logSink:       tm.logSinkLocked(),
	}

	if tm.storage != nil {
		rec := &storage.TaskRecord{
			Project:       project,
			Model:         model,
			Agent:         agent,
			InitialPrompt: prompt,
			CurrentPrompt: prompt,
			Status:        string(TaskStatusQueued),
			RequiresPlan:  requiresPlan,
		}
		if chat != "" {
			rec.RecipientID = string(chat)
		}
		dbID, err := tm.storage.CreateTask(context.Background(), rec)
		if err == nil && dbID > 0 {
			task.ID = dbID
			id = dbID
			if dbID >= tm.nextID {
				tm.nextID = dbID + 1
			}
		}
	}

	tm.tasks[id] = task
	tm.taskOrder = append(tm.taskOrder, id)

	// Если нет активной задачи или предыдущая активная задача завершена, делаем новую активной
	curTask := tm.tasks[tm.activeTaskID]
	if curTask == nil || !curTask.IsActive() {
		tm.activeTaskID = id
		if tm.storage != nil {
			_ = tm.storage.SetSetting(context.Background(), "active_task_id", strconv.Itoa(id))
		}
	}

	return task
}

// GetTask возвращает задачу по ID.
func (tm *TaskManager) GetTask(id int) *TaskSession {
	tm.RLock()
	defer tm.RUnlock()
	return tm.tasks[id]
}

// GetActiveTask возвращает задачу, находящуюся в фокусе пользователя.
func (tm *TaskManager) GetActiveTask() *TaskSession {
	tm.RLock()
	defer tm.RUnlock()

	// 1. Если задача в активном фокусе существует и активна, возвращаем её
	if task, ok := tm.tasks[tm.activeTaskID]; ok && task.IsActive() {
		return task
	}

	// 2. Иначе ищем первую активную задачу
	for _, id := range tm.taskOrder {
		task := tm.tasks[id]
		if task != nil && task.IsActive() {
			return task
		}
	}

	// 3. Фолбэк: если нет активных задач, возвращаем задачу по activeTaskID
	if task, ok := tm.tasks[tm.activeTaskID]; ok {
		return task
	}

	// 4. Фолбэк: последняя созданная задача
	if len(tm.taskOrder) > 0 {
		lastID := tm.taskOrder[len(tm.taskOrder)-1]
		return tm.tasks[lastID]
	}

	return nil
}

// SetActiveTask переключает активный фокус на указанную задачу.
func (tm *TaskManager) SetActiveTask(id int) (*TaskSession, error) {
	tm.Lock()
	defer tm.Unlock()

	task, ok := tm.tasks[id]
	if !ok {
		return nil, fmt.Errorf("задача #%d не найдена", id)
	}

	tm.activeTaskID = id
	if tm.storage != nil {
		_ = tm.storage.SetSetting(context.Background(), "active_task_id", strconv.Itoa(id))
	}
	return task, nil
}

// ListTasks возвращает все задачи в хронологическом порядке.
func (tm *TaskManager) ListTasks() []*TaskSession {
	tm.RLock()
	defer tm.RUnlock()

	result := make([]*TaskSession, 0, len(tm.taskOrder))
	for _, id := range tm.taskOrder {
		if task, ok := tm.tasks[id]; ok {
			result = append(result, task)
		}
	}
	return result
}

// GetActiveOrQueuedTasks возвращает список всех незавершённых задач.
func (tm *TaskManager) GetActiveOrQueuedTasks() []*TaskSession {
	tm.RLock()
	defer tm.RUnlock()

	var result []*TaskSession
	for _, id := range tm.taskOrder {
		task := tm.tasks[id]
		if task != nil && task.IsActive() {
			result = append(result, task)
		}
	}
	return result
}

// HasRunningTaskInProject проверяет, выполняется ли прямо сейчас задача в проекте (или ожидает утверждения плана).
func (tm *TaskManager) HasRunningTaskInProject(project string) bool {
	tm.RLock()
	defer tm.RUnlock()

	for _, task := range tm.tasks {
		task.mu.Lock()
		isRunning := (task.Project == project) &&
			(task.Status == TaskStatusRunning || task.Status == TaskStatusWaitingInput || task.Status == TaskStatusPlanning || task.Status == TaskStatusWaitingApproval)
		task.mu.Unlock()
		if isRunning {
			return true
		}
	}
	return false
}

// GetNextQueuedTaskForProject ищет следующую задачу в очереди для конкретного проекта.
func (tm *TaskManager) GetNextQueuedTaskForProject(project string) *TaskSession {
	tm.RLock()
	defer tm.RUnlock()

	for _, id := range tm.taskOrder {
		task := tm.tasks[id]
		if task != nil {
			task.mu.Lock()
			isQueued := task.Project == project && task.Status == TaskStatusQueued
			task.mu.Unlock()
			if isQueued {
				return task
			}
		}
	}
	return nil
}

// CancelTask отменяет задачу по ID.
func (tm *TaskManager) CancelTask(id int) (*TaskSession, error) {
	tm.Lock()
	task, ok := tm.tasks[id]
	tm.Unlock()

	if !ok {
		return nil, fmt.Errorf("задача #%d не найдена", id)
	}

	task.mu.Lock()
	if task.Status == TaskStatusCompleted || task.Status == TaskStatusCancelled {
		statusTitle := task.Status.RussianTitle()
		task.mu.Unlock()
		return task, fmt.Errorf("задача #%d уже %s", id, statusTitle)
	}

	task.terminateLocked()

	task.Status = TaskStatusCancelled
	task.FinishedAt = time.Now()
	task.PendingFollowups = nil
	if task.PauseChan != nil {
		select {
		case task.PauseChan <- struct{}{}:
		default:
		}
	}
	finAt := task.FinishedAt
	prURL := task.LastPRURL
	task.mu.Unlock()

	tm.RLock()
	s := tm.storage
	tm.RUnlock()
	if s != nil {
		_ = s.UpdateTaskFinished(context.Background(), id, string(TaskStatusCancelled), finAt, prURL)
		_ = s.ClearFollowups(context.Background(), id)
	}

	return task, nil
}

// ResumeTask возобновляет задачу из любого статуса.
// Для задач с активным процессом (Running, Planning, WaitingApproval, Queued, Completed)
// автоматически останавливает текущий процесс перед перезапуском.
func (tm *TaskManager) ResumeTask(id int, answer string) (*TaskSession, error) {
	tm.Lock()

	task, ok := tm.tasks[id]
	if !ok {
		tm.Unlock()
		return nil, fmt.Errorf("задача #%d не найдена", id)
	}

	task.mu.Lock()

	// Для задач с активным процессом — останавливаем текущий процесс
	if task.Status == TaskStatusRunning || task.Status == TaskStatusPlanning ||
		task.Status == TaskStatusWaitingApproval || task.Status == TaskStatusQueued ||
		task.Status == TaskStatusCompleted {
		task.terminateLocked()
	}

	// Если задача всё ещё ждёт ввода в живом пайплайне
	if task.Status == TaskStatusWaitingInput {
		if task.hasLiveProcessLocked() {
			if answer != "" {
				task.writeStdinLocked(answer)
				if task.AnswerChan == nil {
					task.AnswerChan = make(chan string, 1)
				}
				select {
				case task.AnswerChan <- answer:
				default:
					select {
					case <-task.AnswerChan:
					default:
					}
					task.AnswerChan <- answer
				}
			}
			task.mu.Unlock()
			tm.Unlock()
			return task, nil
		}
		// Если процесс не запущен (например, после рестарта или завершения шага), продолжаем логику возобновления
	}

	// Задача была в TaskStatusPaused
	projectName := task.Project
	isProjectBusy := false
	for _, otherID := range tm.taskOrder {
		if otherID == task.ID {
			continue
		}
		other := tm.tasks[otherID]
		if other != nil {
			other.mu.Lock()
			busy := (other.Project == projectName) &&
				(other.Status == TaskStatusRunning || other.Status == TaskStatusWaitingInput || other.Status == TaskStatusPlanning || other.Status == TaskStatusWaitingApproval)
			other.mu.Unlock()
			if busy {
				isProjectBusy = true
				break
			}
		}
	}

	if answer != "" {
		task.CurrentPrompt = answer
	} else if task.ConversationID != "" || task.CurrentPrompt == "" {
		if task.RequiresPlan && !task.PlanApproved {
			task.CurrentPrompt = "Продолжай исследование репозитория и заверши составление детального плана реализации задачи."
		} else {
			task.CurrentPrompt = "Продолжай автономное выполнение задачи по утвержденному плану в текущей ветке git. Заверши необходимые изменения, запусти тесты и линтеры, закоммить изменения и открой Pull Request."
		}
	}
	task.LastQuestion = ""
	task.QuestionOptions = nil

	if isProjectBusy {
		task.Status = TaskStatusQueued
	} else {
		if task.RequiresPlan && !task.PlanApproved {
			task.Status = TaskStatusPlanning
		} else {
			task.Status = TaskStatusRunning
		}
		task.StartedAt = time.Now()
		task.FinishedAt = time.Time{}
		task.RecentLogs = nil
	}
	task.mu.Unlock()
	tm.Unlock()

	tm.SaveTask(task)

	return task, nil
}

// SetTaskConversationID сохраняет ID сессии agy для задачи и немедленно персистирует его в хранилище.
func (tm *TaskManager) SetTaskConversationID(id int, convID string) {
	if tm == nil || convID == "" {
		return
	}
	tm.RLock()
	task, ok := tm.tasks[id]
	s := tm.storage
	tm.RUnlock()

	if !ok || task == nil {
		return
	}

	task.mu.Lock()
	if task.ConversationID == convID {
		task.mu.Unlock()
		return
	}
	task.ConversationID = convID
	task.mu.Unlock()

	if s != nil {
		_ = s.UpdateTaskConversationID(context.Background(), id, convID)
	}
}

// ClearTaskConversationID очищает сохраненную сессию agy задачи (для повтора с нуля).
func (tm *TaskManager) ClearTaskConversationID(id int) {
	if tm == nil {
		return
	}
	tm.RLock()
	task, ok := tm.tasks[id]
	s := tm.storage
	tm.RUnlock()

	if !ok || task == nil {
		return
	}

	task.mu.Lock()
	task.ConversationID = ""
	task.mu.Unlock()

	if s != nil {
		_ = s.UpdateTaskConversationID(context.Background(), id, "")
	}
}

// SetTaskAgent обновляет привязку агента для задачи и персистирует её в хранилище.
func (tm *TaskManager) SetTaskAgent(id int, agent string) {
	if tm == nil || agent == "" {
		return
	}
	tm.RLock()
	task, ok := tm.tasks[id]
	s := tm.storage
	tm.RUnlock()

	if !ok || task == nil {
		return
	}

	task.mu.Lock()
	if task.Agent == agent {
		task.mu.Unlock()
		return
	}
	task.Agent = agent
	task.mu.Unlock()

	if s != nil {
		_ = s.UpdateTaskAgent(context.Background(), id, agent)
	}
}

// AddFollowup добавляет дополнение к конкретной задаче или отправляет ответ в stdin / AnswerChan, если задача ждёт ввода или на паузе.
func (tm *TaskManager) AddFollowup(id int, text string) (*TaskSession, int, bool, error) {
	tm.RLock()
	task, ok := tm.tasks[id]
	tm.RUnlock()

	if !ok {
		return nil, 0, false, fmt.Errorf("задача #%d не найдена", id)
	}

	task.mu.Lock()
	if task.Status == TaskStatusCompleted {
		statusTitle := task.Status.RussianTitle()
		task.mu.Unlock()
		return task, 0, false, fmt.Errorf("задача #%d уже %s", id, statusTitle)
	}

	// Если задача на паузе или отменена — возобновляем её с переданным ответом
	if task.Status == TaskStatusPaused || task.Status == TaskStatusCancelled {
		task.mu.Unlock()
		resumedTask, err := tm.ResumeTask(id, text)
		return resumedTask, 0, true, err
	}

	// Если задача ждёт ответа на вопрос (ask_question): DeliverAnswer сам пишет в stdin.
	if task.Status == TaskStatusWaitingInput {
		task.mu.Unlock()
		task.DeliverAnswer(text)
		return task, 0, true, nil
	}

	// Добавляем в очередь правок
	orderIdx := len(task.PendingFollowups)
	task.PendingFollowups = append(task.PendingFollowups, text)
	queueLen := len(task.PendingFollowups)
	task.mu.Unlock()

	tm.RLock()
	s := tm.storage
	tm.RUnlock()
	if s != nil {
		_ = s.AddFollowup(context.Background(), id, text, orderIdx)
	}
	return task, queueLen, false, nil
}

// RegisterMessageTask связывает отправленное сообщение с ID задачи.
func (tm *TaskManager) RegisterMessageTask(ref ports.MessageRef, taskID int) {
	if ref.ID == "" {
		return
	}
	msgID := string(ref.ID)

	tm.Lock()
	tm.msgToTask[msgID] = taskID
	s := tm.storage
	tm.Unlock()

	if s != nil {
		_ = s.RegisterMessageTask(context.Background(), string(ref.Chat), msgID, taskID)
	}
}

// SaveTask сохраняет текущее состояние задачи в хранилище.
func (tm *TaskManager) SaveTask(task *TaskSession) {
	if tm == nil || task == nil {
		return
	}
	tm.RLock()
	s := tm.storage
	tm.RUnlock()
	if s == nil {
		return
	}

	task.mu.Lock()
	rec := &storage.TaskRecord{
		ID:              task.ID,
		Project:         task.Project,
		Model:           task.Model,
		InitialPrompt:   task.InitialPrompt,
		CurrentPrompt:   task.CurrentPrompt,
		Status:          string(task.Status),
		RequiresPlan:    task.RequiresPlan,
		Plan:            task.Plan,
		PlanApproved:    task.PlanApproved,
		StartedAt:       task.StartedAt,
		FinishedAt:      task.FinishedAt,
		LastPRURL:       task.LastPRURL,
		ConversationID:  task.ConversationID,
		LastQuestion:    task.LastQuestion,
		QuestionOptions: append([]string(nil), task.QuestionOptions...),
		QuestionAskedAt: task.QuestionAskedAt,
		LastModelUsed:   task.LastModelUsed,
		LastTokensUsed:  task.LastTokensUsed,
	}
	if task.Chat != "" {
		rec.RecipientID = string(task.Chat)
	}
	task.mu.Unlock()

	_ = s.UpdateTask(context.Background(), rec)
}

// SaveTaskMetrics сохраняет метрики задачи в хранилище.
func (tm *TaskManager) SaveTaskMetrics(taskID int, metrics *TaskTokenMetrics) {
	if tm == nil || metrics == nil {
		return
	}
	tm.RLock()
	s := tm.storage
	tm.RUnlock()
	if s == nil {
		return
	}

	rec := &storage.TokenMetricsRecord{
		TaskID:                  taskID,
		InputTokens:             metrics.Usage.InputTokens,
		OutputTokens:            metrics.Usage.OutputTokens,
		ThinkingTokens:          metrics.Usage.ThinkingTokens,
		CacheReadTokens:         metrics.Usage.CacheReadTokens,
		TotalTokens:             metrics.Usage.TotalTokens,
		DurationSeconds:         metrics.EffectiveDuration(),
		Turns:                   metrics.Turns,
		ToolCallsCount:          metrics.ToolCallsCount,
		Model:                   metrics.Model,
		PRURL:                   metrics.PRURL,
		ConversationID:          metrics.ConversationID,
		LastStepInputTokens:     metrics.LastStepUsage.InputTokens,
		LastStepOutputTokens:    metrics.LastStepUsage.OutputTokens,
		LastStepThinkingTokens:  metrics.LastStepUsage.ThinkingTokens,
		LastStepCacheReadTokens: metrics.LastStepUsage.CacheReadTokens,
		LastStepTotalTokens:     metrics.LastStepUsage.TotalTokens,
	}
	_ = s.SaveMetrics(context.Background(), rec)
}

// GetTaskByMessageID возвращает задачу, к которой относится сообщение.
func (tm *TaskManager) GetTaskByMessageID(msgID ports.MessageID) *TaskSession {
	tm.RLock()
	defer tm.RUnlock()
	taskID, ok := tm.msgToTask[string(msgID)]
	if !ok {
		return nil
	}
	return tm.tasks[taskID]
}

// GetRunningWorkerPids возвращает PID активного воркера и PID остальных воркеров.
func (tm *TaskManager) GetRunningWorkerPids() (int, []int) {
	tm.RLock()
	defer tm.RUnlock()

	var activePid int
	var otherPids []int

	for id, task := range tm.tasks {
		task.mu.Lock()
		pid := task.workerPIDLocked()
		isRunning := task.Status == TaskStatusRunning || task.Status == TaskStatusWaitingInput || task.Status == TaskStatusPlanning
		task.mu.Unlock()

		if isRunning && pid > 0 {
			if id == tm.activeTaskID {
				activePid = pid
			} else {
				otherPids = append(otherPids, pid)
			}
		}
	}

	// Если активная задача не имела PID, но есть другие воркеры, выбираем первый
	if activePid == 0 && len(otherPids) > 0 {
		activePid = otherPids[0]
		otherPids = otherPids[1:]
	}

	return activePid, otherPids
}

// FormatTasksList формирует сообщение со списком всех задач и кнопками быстрого переключения.
func FormatTasksList(tm *TaskManager) (string, *ports.Keyboard) {
	tasks := tm.ListTasks()
	activeTask := tm.GetActiveTask()
	activeID := 0
	if activeTask != nil {
		activeID = activeTask.ID
	}

	if len(tasks) == 0 {
		return "💤 <b>Список задач пуст.</b>\n\n💡 Отправьте текст в чат или используйте <code>/new &lt;задача&gt;</code>, чтобы начать.", nil
	}

	var bldr strings.Builder
	bldr.WriteString("📋 <b>Список задач агента:</b>\n\n")

	var activeList []*TaskSession
	var completedList []*TaskSession

	for _, t := range tasks {
		if t.IsActive() {
			activeList = append(activeList, t)
		} else {
			completedList = append(completedList, t)
		}
	}

	// Секция активных задач
	if len(activeList) > 0 {
		bldr.WriteString("⚡ <b>Активные и в очереди:</b>\n")
		for _, t := range activeList {
			t.mu.Lock()
			id := t.ID
			proj := t.Project
			status := t.Status
			prompt := t.InitialPrompt
			followupsCount := len(t.PendingFollowups)
			durStr := FormatDurationHuman(t.durationLocked())
			t.mu.Unlock()
			focusBadge := "  "
			if id == activeID {
				focusBadge = "👉 🎯 "
			}

			agentName := t.Agent
			if agentName == "" {
				agentName = "agy"
			}

			bldr.WriteString(fmt.Sprintf("%s<b>#%d</b> %s <code>%s</code> [<code>%s</code>] — <b>%s</b>\n",
				focusBadge, id, status.Emoji(), html.EscapeString(proj), html.EscapeString(agentName), status.RussianTitle()))
			bldr.WriteString(fmt.Sprintf("   📝 <i>«%s»</i>\n", html.EscapeString(utils.TruncateString(prompt, 60))))

			extraInfo := fmt.Sprintf("⏱ <code>%s</code>", durStr)
			if status == TaskStatusQueued {
				extraInfo = "⏳ <i>ожидает очереди проекта</i>"
			} else if status == TaskStatusWaitingApproval {
				extraInfo = fmt.Sprintf("📋 <i>ожидает утверждения плана (<code>/approve %d</code>)</i>", id)
			} else if status == TaskStatusPlanning {
				extraInfo = "📝 <i>составление плана...</i>"
			}
			if followupsCount > 0 {
				extraInfo += fmt.Sprintf(" | 📥 правок: %d", followupsCount)
			}
			bldr.WriteString(fmt.Sprintf("   %s\n\n", extraInfo))
		}
	} else {
		bldr.WriteString("💤 <i>Сейчас нет активных задач.</i>\n\n")
	}

	// Секция недавно завершённых задач (до 5 штук)
	if len(completedList) > 0 {
		bldr.WriteString("🏁 <b>Недавно завершённые:</b>\n")
		startIdx := 0
		if len(completedList) > 5 {
			startIdx = len(completedList) - 5
		}
		for i := len(completedList) - 1; i >= startIdx; i-- {
			t := completedList[i]
			t.mu.Lock()
			id := t.ID
			proj := t.Project
			agentName := t.Agent
			if agentName == "" {
				agentName = "agy"
			}
			status := t.Status
			prompt := t.InitialPrompt
			prURL := t.LastPRURL
			t.mu.Unlock()

			focusBadge := "  "
			if id == activeID {
				focusBadge = "👉 🎯 "
			}

			prSnippet := ""
			if prURL != "" {
				prSnippet = fmt.Sprintf(" | 🔗 <a href=\"%s\">PR</a>", html.EscapeString(prURL))
			}

			bldr.WriteString(fmt.Sprintf("%s<b>#%d</b> %s <code>%s</code> [<code>%s</code>] — %s%s\n",
				focusBadge, id, status.Emoji(), html.EscapeString(proj), html.EscapeString(agentName), status.RussianTitle(), prSnippet))
			bldr.WriteString(fmt.Sprintf("   📝 <i>«%s»</i>\n", html.EscapeString(utils.TruncateString(prompt, 50))))
		}
		bldr.WriteString("\n")
	}

	bldr.WriteString("💡 <b>Управление:</b>\n")
	bldr.WriteString("• <code>/task &lt;id&gt;</code> — переключить активную задачу\n")
	bldr.WriteString("• <code>/add &lt;id&gt; &lt;текст&gt;</code> — дополнить конкретную задачу\n")
	bldr.WriteString("• <code>/new &lt;текст&gt;</code> — создать новую задачу\n")
	bldr.WriteString("• <code>/plan &lt;текст&gt;</code> — составить план и утвердить перед реализацией\n")
	bldr.WriteString("• <code>/approve &lt;id&gt;</code> — утвердить план задачи\n")
	bldr.WriteString("• <code>/resume &lt;id&gt; [ответ]</code> — возобновить приостановленную задачу\n")
	bldr.WriteString("• <code>/cancel &lt;id&gt;</code> — отменить задачу")

	// Формируем инлайн-клавиатуру для активных задач
	var buttons []ports.Button
	for _, t := range activeList {
		t.mu.Lock()
		id := t.ID
		proj := t.Project
		emoji := t.Status.Emoji()
		t.mu.Unlock()

		badge := ""
		if id == activeID {
			badge = "🎯 "
		}
		btnText := fmt.Sprintf("%s#%d %s %s", badge, id, emoji, utils.TruncateString(proj, 12))
		buttons = append(buttons, ports.Button{Text: btnText, Action: "task_sel", Payload: strconv.Itoa(id)})
	}

	if len(buttons) > 0 {
		var rows [][]ports.Button
		// По 2 кнопки в ряд
		for i := 0; i < len(buttons); i += 2 {
			if i+1 < len(buttons) {
				rows = append(rows, []ports.Button{buttons[i], buttons[i+1]})
			} else {
				rows = append(rows, []ports.Button{buttons[i]})
			}
		}
		return bldr.String(), &ports.Keyboard{Rows: rows}
	}

	return bldr.String(), nil
}

// MaxTaskDetailsPromptRunes задает максимальную длину текста задачи в карточке статуса.
const MaxTaskDetailsPromptRunes = 250

// MaxTaskDetailsPlanRunes задает максимальную длину текста плана в карточке статуса перед обрезкой.
const MaxTaskDetailsPlanRunes = 400

// FormatTaskDetails формирует подробную карточку статуса задачи.
func FormatTaskDetails(task *TaskSession, isActiveFocus bool) string {
	task.mu.Lock()
	id := task.ID
	proj := task.Project
	model := task.Model
	agent := task.Agent
	if agent == "" {
		agent = "agy"
	}
	convID := task.ConversationID
	status := task.Status
	requiresPlan := task.RequiresPlan
	planApproved := task.PlanApproved
	plan := task.Plan
	initialPrompt := task.InitialPrompt
	curPrompt := task.CurrentPrompt
	followups := append([]string(nil), task.PendingFollowups...)
	logs := append([]string(nil), task.RecentLogs...)
	prURL := task.LastPRURL
	lastQuestion := task.LastQuestion
	durStr := FormatDurationHuman(task.durationLocked())
	task.mu.Unlock()

	var bldr strings.Builder
	focusTitle := ""
	if isActiveFocus {
		focusTitle = " 🎯 <i>(в фокусе)</i>"
	}
	bldr.WriteString(fmt.Sprintf("📊 <b>Задача #%d:</b> <code>%s</code>%s\n\n", id, html.EscapeString(proj), focusTitle))
	bldr.WriteString(fmt.Sprintf("• <b>Статус:</b> %s <b>%s</b>\n", status.Emoji(), status.RussianTitle()))
	bldr.WriteString(fmt.Sprintf("• <b>Агент:</b> <code>%s</code>\n", html.EscapeString(agent)))
	bldr.WriteString(fmt.Sprintf("• <b>Модель:</b> <code>%s</code>\n", html.EscapeString(model)))
	bldr.WriteString(fmt.Sprintf("• <b>Время:</b> <code>%s</code>\n", durStr))
	if convID != "" {
		bldr.WriteString(fmt.Sprintf("• 🧵 <b>Сессия %s:</b> <code>%s</code>\n", html.EscapeString(agent), html.EscapeString(convID)))
	}
	bldr.WriteString(fmt.Sprintf("• <b>Задача:</b> <i>«%s»</i>\n", html.EscapeString(utils.TruncateString(initialPrompt, MaxTaskDetailsPromptRunes))))

	if requiresPlan {
		if planApproved {
			bldr.WriteString("• <b>План:</b> ✅ Утверждён\n")
		} else if status == TaskStatusWaitingApproval {
			bldr.WriteString(fmt.Sprintf("• <b>План:</b> 📋 Ожидает утверждения (<code>/approve %d</code>)\n", id))
		} else if status == TaskStatusPlanning {
			bldr.WriteString("• <b>План:</b> 📝 Составляется агентом...\n")
		}
	}

	if curPrompt != initialPrompt && curPrompt != "" {
		bldr.WriteString(fmt.Sprintf("• <b>Текущий шаг:</b> <i>«%s»</i>\n", html.EscapeString(utils.TruncateString(curPrompt, 80))))
	}

	if prURL != "" {
		bldr.WriteString(fmt.Sprintf("• <b>PR:</b> 🔗 <a href=\"%s\">%s</a>\n", html.EscapeString(prURL), html.EscapeString(prURL)))
	}

	if lastQuestion != "" {
		bldr.WriteString(fmt.Sprintf("\n❓ <b>Вопрос агента:</b>\n<i>%s</i>\n", html.EscapeString(utils.TruncateString(lastQuestion, 300))))
	}

	if plan != "" {
		planRunes := []rune(plan)
		if len(planRunes) > MaxTaskDetailsPlanRunes {
			planSnippet := utils.TruncateString(plan, MaxTaskDetailsPlanRunes)
			bldr.WriteString(fmt.Sprintf("\n📋 <b>План реализации (кратко):</b>\n<i>%s</i>\n📄 <i>Полный план:</i> /planfile_%d\n",
				html.EscapeString(planSnippet), id))
		} else {
			bldr.WriteString(fmt.Sprintf("\n📋 <b>План реализации:</b>\n<i>%s</i>\n📄 <i>Полный план:</i> /planfile_%d\n",
				html.EscapeString(plan), id))
		}
	}

	if len(followups) > 0 {
		bldr.WriteString(fmt.Sprintf("\n📥 <b>В очереди дополнений (%d):</b>\n", len(followups)))
		for i, f := range followups {
			bldr.WriteString(fmt.Sprintf("%d. <i>«%s»</i>\n", i+1, html.EscapeString(utils.TruncateString(f, 120))))
		}
	}

	if len(logs) > 0 {
		rawTail := strings.Join(logs, "\n")
		if len(rawTail) > 1200 {
			rawTail = rawTail[len(rawTail)-1200:]
		}
		bldr.WriteString(fmt.Sprintf("\n📜 <b>Лог выполнения:</b>\n<pre>%s</pre>\n", html.EscapeString(rawTail)))
	}

	if status == TaskStatusWaitingApproval {
		bldr.WriteString(fmt.Sprintf("\n💡 <i>Утвердить: <code>/approve %d</code> | Дополнить: <code>/add %d &lt;правки&gt;</code> | Отменить: <code>/cancel %d</code></i>", id, id, id))
	} else if status == TaskStatusPaused {
		bldr.WriteString(fmt.Sprintf("\n💡 <i>Возобновить: <code>/resume %d &lt;ответ&gt;</code> | Отменить: <code>/cancel %d</code></i>", id, id))
	} else if status == TaskStatusWaitingInput {
		bldr.WriteString(fmt.Sprintf("\n💡 <i>Ответить: <code>/add %d &lt;ответ&gt;</code> | Отменить: <code>/cancel %d</code></i>", id, id))
	} else {
		bldr.WriteString("\n💡 <i>Дополнить: <code>/add ")
		bldr.WriteString(strconv.Itoa(id))
		bldr.WriteString(" &lt;текст&gt;</code> | Отменить: <code>/cancel ")
		bldr.WriteString(strconv.Itoa(id))
		bldr.WriteString("</code></i>")
	}

	return bldr.String()
}

// BuildTaskDetailsMarkup формирует инлайн-клавиатуру для карточки задачи,
// включая кнопку скачивания плана (если он есть) и управляющие кнопки по статусу.
func BuildTaskDetailsMarkup(task *TaskSession) *ports.Keyboard {
	task.mu.Lock()
	id := task.ID
	hasPlan := task.Plan != ""
	status := task.Status
	task.mu.Unlock()

	var rows [][]ports.Button

	if hasPlan {
		rows = append(rows, []ports.Button{{Text: "📄 Скачать план (.md)", Action: "plan_doc", Payload: strconv.Itoa(id)}})
	}

	if status == TaskStatusWaitingApproval {
		rows = append(rows, []ports.Button{
			{Text: "✅ Утвердить план", Action: "plan_approve", Payload: strconv.Itoa(id)},
			{Text: "❌ Отменить", Action: "plan_cancel", Payload: strconv.Itoa(id)},
		})
	} else if status == TaskStatusPaused {
		rows = append(rows, []ports.Button{
			{Text: "▶️ Возобновить", Action: "q_resume", Payload: strconv.Itoa(id)},
			{Text: "❌ Отменить", Action: "plan_cancel", Payload: strconv.Itoa(id)},
		})
	}

	if len(rows) > 0 {
		return &ports.Keyboard{Rows: rows}
	}
	return nil
}

// BuildTaskPlanMarkup создает инлайн-кнопку скачивания полного файла плана задачи.
func BuildTaskPlanMarkup(taskID int) *ports.Keyboard {
	return &ports.Keyboard{Rows: [][]ports.Button{
		{{Text: "📄 Скачать план (.md)", Action: "plan_doc", Payload: strconv.Itoa(taskID)}},
	}}
}

// logSinkLocked возвращает приёмник логов текущего писателя (tm должен быть захвачен).
func (tm *TaskManager) logSinkLocked() func(int, string) {
	if tm.logs == nil {
		return nil
	}
	return tm.logs.enqueue
}

// FlushLogs дожидается записи всех поставленных в очередь строк лога. Нужен тестам
// и перед перезапуском: чтение из хранилища должно видеть всё, что успели записать.
func (tm *TaskManager) FlushLogs() {
	tm.RLock()
	w := tm.logs
	tm.RUnlock()
	if w != nil {
		w.flush()
	}
}

// Параметры писателя логов: очередь на 1024 строки, пачки до 100 строк или 50 мс ожидания.
const (
	logQueueSize  = 1024
	logBatchMax   = 100
	logBatchDelay = 50 * time.Millisecond
	logFlushWait  = 5 * time.Second
)

type logEntry struct {
	taskID int
	line   string
}

// logWriter пишет строки логов в хранилище пачками из отдельной горутины.
// Порядок строк одной задачи сохраняется; ошибки записи попадают в лог процесса,
// а не глотаются.
type logWriter struct {
	storage storage.Storage
	queue   chan logEntry
	stop    chan struct{}
	done    chan struct{}
	pending atomic.Int64
}

func newLogWriter(s storage.Storage) *logWriter {
	w := &logWriter{
		storage: s,
		queue:   make(chan logEntry, logQueueSize),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go w.run()
	return w
}

// enqueue ставит строку в очередь. Полная очередь притормаживает вызывающего:
// терять строки или ломать порядок хуже, чем подождать писателя.
func (w *logWriter) enqueue(taskID int, line string) {
	w.pending.Add(1)
	select {
	case w.queue <- logEntry{taskID: taskID, line: line}:
	case <-w.stop:
		w.pending.Add(-1)
	}
}

func (w *logWriter) run() {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			w.drain()
			return
		case first := <-w.queue:
			batch := []logEntry{first}
			timer := time.NewTimer(logBatchDelay)
		collect:
			for len(batch) < logBatchMax {
				select {
				case e := <-w.queue:
					batch = append(batch, e)
				case <-timer.C:
					break collect
				}
			}
			timer.Stop()
			w.write(batch)
		}
	}
}

// drain дописывает всё, что осталось в очереди на момент остановки.
func (w *logWriter) drain() {
	for {
		select {
		case e := <-w.queue:
			w.write([]logEntry{e})
		default:
			return
		}
	}
}

// write группирует подряд идущие строки одной задачи и пишет каждую группу одной транзакцией.
func (w *logWriter) write(batch []logEntry) {
	defer w.pending.Add(-int64(len(batch)))

	ctx := context.Background()
	for i := 0; i < len(batch); {
		taskID := batch[i].taskID
		j := i
		var lines []string
		for j < len(batch) && batch[j].taskID == taskID {
			lines = append(lines, batch[j].line)
			j++
		}
		if err := w.storage.AppendLogs(ctx, taskID, lines); err != nil {
			log.Printf("Предупреждение: не удалось записать %d строк лога задачи #%d: %v", len(lines), taskID, err)
		}
		i = j
	}
}

// flush ждёт, пока очередь опустеет, но не дольше logFlushWait.
func (w *logWriter) flush() {
	deadline := time.Now().Add(logFlushWait)
	for w.pending.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

// close останавливает писателя, дописав очередь.
func (w *logWriter) close() {
	close(w.stop)
	<-w.done
}
