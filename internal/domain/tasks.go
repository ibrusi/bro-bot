package domain

import (
	"context"
	"fmt"
	"html"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"tg-agent-bot/internal/storage"
	"tg-agent-bot/internal/utils"
	"time"

	tele "gopkg.in/telebot.v3"
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
	sync.Mutex
	ID               int
	Project          string
	Model            string
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
	Cmd              *exec.Cmd
	Stdin            io.WriteCloser
	LiveMsg          *tele.Message
	Recipient        tele.Recipient
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
}

// stringRecipient реализует tele.Recipient для строкового ID получателя.
type stringRecipient string

func (r stringRecipient) Recipient() string {
	return string(r)
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
	t.Lock()
	defer t.Unlock()
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
	t.Lock()
	defer t.Unlock()
	return t.isActiveLocked()
}

// AppendLog безопасно добавляет запись в лог с ограничением глубины и сохраняет в БД.
func (t *TaskSession) AppendLog(line string) {
	t.Lock()
	t.RecentLogs = append(t.RecentLogs, line)
	if len(t.RecentLogs) > 20 {
		t.RecentLogs = t.RecentLogs[1:]
	}
	s := t.storage
	id := t.ID
	t.Unlock()

	if s != nil {
		_ = s.AppendLog(context.Background(), id, line)
	}
}

// ClearPendingFollowups очищает очередь правок и синхронизирует с хранилищем.
func (t *TaskSession) ClearPendingFollowups() {
	t.Lock()
	t.PendingFollowups = nil
	s := t.storage
	id := t.ID
	t.Unlock()

	if s != nil {
		_ = s.ClearFollowups(context.Background(), id)
	}
}

// DeliverAnswer безопасно передаёт ответ пользователя на вопрос агента.
func (t *TaskSession) DeliverAnswer(answer string) bool {
	t.Lock()
	defer t.Unlock()

	if t.Stdin != nil {
		_, _ = io.WriteString(t.Stdin, answer+"\n")
	}

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
	t.Lock()
	defer t.Unlock()

	if t.Status == TaskStatusWaitingInput {
		t.Status = TaskStatusPaused
		if t.Cmd != nil && t.Cmd.Process != nil {
			_ = syscall.Kill(-t.Cmd.Process.Pid, syscall.SIGKILL)
		}
		if t.Stdin != nil {
			_ = t.Stdin.Close()
			t.Stdin = nil
		}
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
	msgToTask    map[int]int // messageID -> taskID
	storage      storage.Storage
}

var GlobalTaskManager = NewTaskManager()

func taskRecordToSession(rec *storage.TaskRecord, s storage.Storage) *TaskSession {
	var recipient tele.Recipient
	if rec.RecipientID != "" {
		recipient = stringRecipient(rec.RecipientID)
	}
	return &TaskSession{
		ID:              rec.ID,
		Project:         rec.Project,
		Model:           rec.Model,
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
		Recipient:       recipient,
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
		msgToTask: make(map[int]int),
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

	tm.storage = s
	if s == nil {
		return
	}

	ctx := context.Background()
	_, _ = s.RecoverInterruptedTasks(ctx)

	records, err := s.ListTasks(ctx)
	if err == nil {
		maxID := 0
		for _, rec := range records {
			sess := taskRecordToSession(rec, s)
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
func (tm *TaskManager) CreateTask(project, model, prompt string, recipient tele.Recipient) *TaskSession {
	return tm.CreateTaskWithPlan(project, model, prompt, recipient, false)
}

// CreateTaskWithPlan создаёт задачу с возможностью требования предварительного плана.
func (tm *TaskManager) CreateTaskWithPlan(project, model, prompt string, recipient tele.Recipient, requiresPlan bool) *TaskSession {
	tm.Lock()
	defer tm.Unlock()

	id := tm.nextID
	tm.nextID++

	task := &TaskSession{
		ID:            id,
		Project:       project,
		Model:         model,
		InitialPrompt: prompt,
		CurrentPrompt: prompt,
		Status:        TaskStatusQueued,
		RequiresPlan:  requiresPlan,
		Recipient:     recipient,
		AnswerChan:    make(chan string, 1),
		PauseChan:     make(chan struct{}, 1),
		storage:       tm.storage,
	}

	if tm.storage != nil {
		rec := &storage.TaskRecord{
			Project:       project,
			Model:         model,
			InitialPrompt: prompt,
			CurrentPrompt: prompt,
			Status:        string(TaskStatusQueued),
			RequiresPlan:  requiresPlan,
		}
		if recipient != nil {
			rec.RecipientID = recipient.Recipient()
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
		task.Lock()
		isRunning := (task.Project == project) &&
			(task.Status == TaskStatusRunning || task.Status == TaskStatusWaitingInput || task.Status == TaskStatusPlanning || task.Status == TaskStatusWaitingApproval)
		task.Unlock()
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
			task.Lock()
			isQueued := task.Project == project && task.Status == TaskStatusQueued
			task.Unlock()
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

	task.Lock()
	if task.Status == TaskStatusCompleted || task.Status == TaskStatusCancelled {
		statusTitle := task.Status.RussianTitle()
		task.Unlock()
		return task, fmt.Errorf("задача #%d уже %s", id, statusTitle)
	}

	if task.Cmd != nil && task.Cmd.Process != nil {
		_ = syscall.Kill(-task.Cmd.Process.Pid, syscall.SIGKILL)
	}
	if task.Stdin != nil {
		_ = task.Stdin.Close()
		task.Stdin = nil
	}

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
	task.Unlock()

	tm.RLock()
	s := tm.storage
	tm.RUnlock()
	if s != nil {
		_ = s.UpdateTaskFinished(context.Background(), id, string(TaskStatusCancelled), finAt, prURL)
		_ = s.ClearFollowups(context.Background(), id)
	}

	return task, nil
}

// ResumeTask возобновляет задачу, находившуюся в статусе паузы или ожидания ввода.
func (tm *TaskManager) ResumeTask(id int, answer string) (*TaskSession, error) {
	tm.Lock()

	task, ok := tm.tasks[id]
	if !ok {
		tm.Unlock()
		return nil, fmt.Errorf("задача #%d не найдена", id)
	}

	task.Lock()
	if task.Status != TaskStatusPaused && task.Status != TaskStatusWaitingInput && task.Status != TaskStatusFailed {
		statusTitle := task.Status.RussianTitle()
		task.Unlock()
		tm.Unlock()
		return task, fmt.Errorf("задача #%d не находится на паузе или в ошибке (текущий статус: %s)", id, statusTitle)
	}

	// Если задача всё ещё ждёт ввода в живом пайплайне
	if task.Status == TaskStatusWaitingInput {
		cmdIsNil := (task.Cmd == nil || task.Cmd.Process == nil)
		if !cmdIsNil {
			if answer != "" {
				if task.Stdin != nil {
					_, _ = io.WriteString(task.Stdin, answer+"\n")
				}
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
			task.Unlock()
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
			other.Lock()
			busy := (other.Project == projectName) &&
				(other.Status == TaskStatusRunning || other.Status == TaskStatusWaitingInput || other.Status == TaskStatusPlanning || other.Status == TaskStatusWaitingApproval)
			other.Unlock()
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
	task.Unlock()
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

	task.Lock()
	if task.ConversationID == convID {
		task.Unlock()
		return
	}
	task.ConversationID = convID
	task.Unlock()

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

	task.Lock()
	task.ConversationID = ""
	task.Unlock()

	if s != nil {
		_ = s.UpdateTaskConversationID(context.Background(), id, "")
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

	task.Lock()
	if task.Status == TaskStatusCompleted || task.Status == TaskStatusCancelled {
		statusTitle := task.Status.RussianTitle()
		task.Unlock()
		return task, 0, false, fmt.Errorf("задача #%d уже %s", id, statusTitle)
	}

	// Если задача на паузе — возобновляем её с переданным ответом
	if task.Status == TaskStatusPaused {
		task.Unlock()
		resumedTask, err := tm.ResumeTask(id, text)
		return resumedTask, 0, true, err
	}

	// Если задача ждёт ответа на вопрос (ask_question)
	if task.Status == TaskStatusWaitingInput {
		if task.Stdin != nil {
			_, _ = io.WriteString(task.Stdin, text+"\n")
		}
		task.Unlock()
		task.DeliverAnswer(text)
		return task, 0, true, nil
	}

	// Добавляем в очередь правок
	orderIdx := len(task.PendingFollowups)
	task.PendingFollowups = append(task.PendingFollowups, text)
	queueLen := len(task.PendingFollowups)
	task.Unlock()

	tm.RLock()
	s := tm.storage
	tm.RUnlock()
	if s != nil {
		_ = s.AddFollowup(context.Background(), id, text, orderIdx)
	}
	return task, queueLen, false, nil
}

// RegisterMessageTask связывает ID сообщения Telegram с ID задачи.
func (tm *TaskManager) RegisterMessageTask(msgID int, taskID int) {
	tm.Lock()
	tm.msgToTask[msgID] = taskID
	s := tm.storage
	tm.Unlock()

	if s != nil {
		_ = s.RegisterMessageTask(context.Background(), msgID, 0, taskID)
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

	task.Lock()
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
	if task.Recipient != nil {
		rec.RecipientID = task.Recipient.Recipient()
	}
	task.Unlock()

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
		TaskID:          taskID,
		InputTokens:     metrics.Usage.InputTokens,
		OutputTokens:    metrics.Usage.OutputTokens,
		ThinkingTokens:  metrics.Usage.ThinkingTokens,
		CacheReadTokens: metrics.Usage.CacheReadTokens,
		TotalTokens:     metrics.Usage.TotalTokens,
		DurationSeconds: metrics.EffectiveDuration(),
		Turns:           metrics.Turns,
		ToolCallsCount:  metrics.ToolCallsCount,
		Model:           metrics.Model,
		PRURL:           metrics.PRURL,
		ConversationID:  metrics.ConversationID,
	}
	_ = s.SaveMetrics(context.Background(), rec)
}


// GetTaskByMessageID возвращает задачу, к которой относится сообщение Telegram.
func (tm *TaskManager) GetTaskByMessageID(msgID int) *TaskSession {
	tm.RLock()
	defer tm.RUnlock()
	taskID, ok := tm.msgToTask[msgID]
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
		task.Lock()
		pid := 0
		if task.Cmd != nil && task.Cmd.Process != nil {
			pid = task.Cmd.Process.Pid
		}
		isRunning := task.Status == TaskStatusRunning || task.Status == TaskStatusWaitingInput || task.Status == TaskStatusPlanning
		task.Unlock()

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
func FormatTasksList(tm *TaskManager) (string, *tele.ReplyMarkup) {
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
			t.Lock()
			id := t.ID
			proj := t.Project
			status := t.Status
			prompt := t.InitialPrompt
			followupsCount := len(t.PendingFollowups)
			durStr := FormatDurationHuman(t.durationLocked())
			t.Unlock()
			focusBadge := "  "
			if id == activeID {
				focusBadge = "👉 🎯 "
			}

			bldr.WriteString(fmt.Sprintf("%s<b>#%d</b> %s <code>%s</code> — <b>%s</b>\n",
				focusBadge, id, status.Emoji(), html.EscapeString(proj), status.RussianTitle()))
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
			t.Lock()
			id := t.ID
			proj := t.Project
			status := t.Status
			prompt := t.InitialPrompt
			prURL := t.LastPRURL
			t.Unlock()

			focusBadge := "  "
			if id == activeID {
				focusBadge = "👉 🎯 "
			}

			prSnippet := ""
			if prURL != "" {
				prSnippet = fmt.Sprintf(" | 🔗 <a href=\"%s\">PR</a>", html.EscapeString(prURL))
			}

			bldr.WriteString(fmt.Sprintf("%s<b>#%d</b> %s <code>%s</code> — %s%s\n",
				focusBadge, id, status.Emoji(), html.EscapeString(proj), status.RussianTitle(), prSnippet))
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
	menu := &tele.ReplyMarkup{}
	var buttons []tele.Btn
	for _, t := range activeList {
		t.Lock()
		id := t.ID
		proj := t.Project
		emoji := t.Status.Emoji()
		t.Unlock()

		badge := ""
		if id == activeID {
			badge = "🎯 "
		}
		btnText := fmt.Sprintf("%s#%d %s %s", badge, id, emoji, utils.TruncateString(proj, 12))
		btn := menu.Data(btnText, "task_sel", strconv.Itoa(id))
		buttons = append(buttons, btn)
	}

	if len(buttons) > 0 {
		var rows []tele.Row
		// По 2 кнопки в ряд
		for i := 0; i < len(buttons); i += 2 {
			if i+1 < len(buttons) {
				rows = append(rows, menu.Row(buttons[i], buttons[i+1]))
			} else {
				rows = append(rows, menu.Row(buttons[i]))
			}
		}
		menu.Inline(rows...)
		return bldr.String(), menu
	}

	return bldr.String(), nil
}

// MaxTaskDetailsPromptRunes задает максимальную длину текста задачи в карточке статуса.
const MaxTaskDetailsPromptRunes = 250

// MaxTaskDetailsPlanRunes задает максимальную длину текста плана в карточке статуса перед обрезкой.
const MaxTaskDetailsPlanRunes = 400

// FormatTaskDetails формирует подробную карточку статуса задачи.
func FormatTaskDetails(task *TaskSession, isActiveFocus bool) string {
	task.Lock()
	id := task.ID
	proj := task.Project
	model := task.Model
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
	task.Unlock()

	var bldr strings.Builder
	focusTitle := ""
	if isActiveFocus {
		focusTitle = " 🎯 <i>(в фокусе)</i>"
	}
	bldr.WriteString(fmt.Sprintf("📊 <b>Задача #%d:</b> <code>%s</code>%s\n\n", id, html.EscapeString(proj), focusTitle))
	bldr.WriteString(fmt.Sprintf("• <b>Статус:</b> %s <b>%s</b>\n", status.Emoji(), status.RussianTitle()))
	bldr.WriteString(fmt.Sprintf("• <b>Модель:</b> <code>%s</code>\n", html.EscapeString(model)))
	bldr.WriteString(fmt.Sprintf("• <b>Время:</b> <code>%s</code>\n", durStr))
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
func BuildTaskDetailsMarkup(task *TaskSession) *tele.ReplyMarkup {
	task.Lock()
	id := task.ID
	hasPlan := task.Plan != ""
	status := task.Status
	task.Unlock()

	menu := &tele.ReplyMarkup{}
	var rows []tele.Row

	if hasPlan {
		btnDoc := menu.Data("📄 Скачать план (.md)", "plan_doc", strconv.Itoa(id))
		rows = append(rows, menu.Row(btnDoc))
	}

	if status == TaskStatusWaitingApproval {
		btnApprove := menu.Data("✅ Утвердить план", "plan_approve", strconv.Itoa(id))
		btnCancel := menu.Data("❌ Отменить", "plan_cancel", strconv.Itoa(id))
		rows = append(rows, menu.Row(btnApprove, btnCancel))
	} else if status == TaskStatusPaused {
		btnResume := menu.Data("▶️ Возобновить", "q_resume", strconv.Itoa(id))
		btnCancel := menu.Data("❌ Отменить", "plan_cancel", strconv.Itoa(id))
		rows = append(rows, menu.Row(btnResume, btnCancel))
	}

	if len(rows) > 0 {
		menu.Inline(rows...)
		return menu
	}
	return nil
}

// BuildTaskPlanMarkup создает инлайн-кнопку скачивания полного файла плана задачи.
func BuildTaskPlanMarkup(taskID int) *tele.ReplyMarkup {
	menu := &tele.ReplyMarkup{}
	btnDoc := menu.Data("📄 Скачать план (.md)", "plan_doc", strconv.Itoa(taskID))
	menu.Inline(menu.Row(btnDoc))
	return menu
}
