package main

import (
	"fmt"
	"html"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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

// AppendLog безопасно добавляет запись в лог с ограничением глубины.
func (t *TaskSession) AppendLog(line string) {
	t.Lock()
	defer t.Unlock()
	t.RecentLogs = append(t.RecentLogs, line)
	if len(t.RecentLogs) > 20 {
		t.RecentLogs = t.RecentLogs[1:]
	}
}

// TaskManager управляет жизненным циклом множества задач и переключением активной задачи.
type TaskManager struct {
	sync.RWMutex
	tasks        map[int]*TaskSession
	taskOrder    []int
	activeTaskID int
	nextID       int
	msgToTask    map[int]int // messageID -> taskID
}

var taskManager = NewTaskManager()

// NewTaskManager создаёт новый менеджер задач.
func NewTaskManager() *TaskManager {
	return &TaskManager{
		tasks:     make(map[int]*TaskSession),
		taskOrder: make([]int, 0),
		msgToTask: make(map[int]int),
		nextID:    1,
	}
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
	}

	tm.tasks[id] = task
	tm.taskOrder = append(tm.taskOrder, id)

	// Если нет активной задачи или предыдущая активная задача завершена, делаем новую активной
	curTask := tm.tasks[tm.activeTaskID]
	if curTask == nil || !curTask.IsActive() {
		tm.activeTaskID = id
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

	if task, ok := tm.tasks[tm.activeTaskID]; ok {
		return task
	}

	// Фолбэк: ищем первую активную задачу
	for _, id := range tm.taskOrder {
		task := tm.tasks[id]
		if task != nil && task.IsActive() {
			return task
		}
	}

	// Фолбэк: последняя созданная задача
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
	defer task.Unlock()

	if task.Status == TaskStatusCompleted || task.Status == TaskStatusCancelled {
		return task, fmt.Errorf("задача #%d уже %s", id, task.Status.RussianTitle())
	}

	if task.Cmd != nil && task.Cmd.Process != nil {
		_ = task.Cmd.Process.Kill()
	}
	if task.Stdin != nil {
		_ = task.Stdin.Close()
		task.Stdin = nil
	}

	task.Status = TaskStatusCancelled
	task.FinishedAt = time.Now()
	task.PendingFollowups = nil

	return task, nil
}

// AddFollowup добавляет дополнение к конкретной задаче или отправляет ответ в stdin, если задача ждёт ввода.
func (tm *TaskManager) AddFollowup(id int, text string) (*TaskSession, int, bool, error) {
	tm.RLock()
	task, ok := tm.tasks[id]
	tm.RUnlock()

	if !ok {
		return nil, 0, false, fmt.Errorf("задача #%d не найдена", id)
	}

	task.Lock()
	defer task.Unlock()

	if task.Status == TaskStatusCompleted || task.Status == TaskStatusCancelled {
		return task, 0, false, fmt.Errorf("задача #%d уже %s", id, task.Status.RussianTitle())
	}

	// Если задача ждёт ответа на вопрос (ask_question)
	if task.Status == TaskStatusWaitingInput && task.Stdin != nil {
		task.Status = TaskStatusRunning
		_, err := io.WriteString(task.Stdin, text+"\n")
		if err != nil {
			return task, 0, true, fmt.Errorf("ошибка отправки ответа: %w", err)
		}
		return task, 0, true, nil
	}

	// Добавляем в очередь правок
	task.PendingFollowups = append(task.PendingFollowups, text)
	queueLen := len(task.PendingFollowups)
	return task, queueLen, false, nil
}

// RegisterMessageTask связывает ID сообщения Telegram с ID задачи.
func (tm *TaskManager) RegisterMessageTask(msgID int, taskID int) {
	tm.Lock()
	defer tm.Unlock()
	tm.msgToTask[msgID] = taskID
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
			durStr := formatDurationHuman(t.durationLocked())
			t.Unlock()
			focusBadge := "  "
			if id == activeID {
				focusBadge = "👉 🎯 "
			}

			bldr.WriteString(fmt.Sprintf("%s<b>#%d</b> %s <code>%s</code> — <b>%s</b>\n",
				focusBadge, id, status.Emoji(), html.EscapeString(proj), status.RussianTitle()))
			bldr.WriteString(fmt.Sprintf("   📝 <i>«%s»</i>\n", html.EscapeString(truncateString(prompt, 60))))

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
			bldr.WriteString(fmt.Sprintf("   📝 <i>«%s»</i>\n", html.EscapeString(truncateString(prompt, 50))))
		}
		bldr.WriteString("\n")
	}

	bldr.WriteString("💡 <b>Управление:</b>\n")
	bldr.WriteString("• <code>/task &lt;id&gt;</code> — переключить активную задачу\n")
	bldr.WriteString("• <code>/add &lt;id&gt; &lt;текст&gt;</code> — дополнить конкретную задачу\n")
	bldr.WriteString("• <code>/new &lt;текст&gt;</code> — создать новую задачу\n")
	bldr.WriteString("• <code>/plan &lt;текст&gt;</code> — составить план и утвердить перед реализацией\n")
	bldr.WriteString("• <code>/approve &lt;id&gt;</code> — утвердить план задачи\n")
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
		btnText := fmt.Sprintf("%s#%d %s %s", badge, id, emoji, truncateString(proj, 12))
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
	durStr := formatDurationHuman(task.durationLocked())
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
	bldr.WriteString(fmt.Sprintf("• <b>Задача:</b> <i>«%s»</i>\n", html.EscapeString(initialPrompt)))

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
		bldr.WriteString(fmt.Sprintf("• <b>Текущий шаг:</b> <i>«%s»</i>\n", html.EscapeString(truncateString(curPrompt, 80))))
	}

	if prURL != "" {
		bldr.WriteString(fmt.Sprintf("• <b>PR:</b> 🔗 <a href=\"%s\">%s</a>\n", html.EscapeString(prURL), html.EscapeString(prURL)))
	}

	if plan != "" {
		planSnippet := plan
		if len(planSnippet) > 400 {
			planSnippet = planSnippet[:400] + "..."
		}
		bldr.WriteString(fmt.Sprintf("\n📋 <b>План реализации:</b>\n<i>%s</i>\n", html.EscapeString(planSnippet)))
	}

	if len(followups) > 0 {
		bldr.WriteString(fmt.Sprintf("\n📥 <b>В очереди дополнений (%d):</b>\n", len(followups)))
		for i, f := range followups {
			bldr.WriteString(fmt.Sprintf("%d. <i>«%s»</i>\n", i+1, html.EscapeString(f)))
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
	} else {
		bldr.WriteString("\n💡 <i>Дополнить: <code>/add ")
		bldr.WriteString(strconv.Itoa(id))
		bldr.WriteString(" &lt;текст&gt;</code> | Отменить: <code>/cancel ")
		bldr.WriteString(strconv.Itoa(id))
		bldr.WriteString("</code></i>")
	}

	return bldr.String()
}
