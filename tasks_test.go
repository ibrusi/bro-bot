package main

import (
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"
)

type dummyRecipient struct{}

var _ tele.Recipient = dummyRecipient{}

func (d dummyRecipient) Recipient() string { return "12345" }

func TestTaskManagerCreateAndGet(t *testing.T) {
	tm := NewTaskManager()

	t1 := tm.CreateTask("project-a", "model-1", "Prompt 1", dummyRecipient{})
	if t1.ID != 1 {
		t.Fatalf("expected ID 1, got %d", t1.ID)
	}
	if t1.Project != "project-a" {
		t.Fatalf("expected project-a, got %s", t1.Project)
	}

	t2 := tm.CreateTask("project-b", "model-2", "Prompt 2", dummyRecipient{})
	if t2.ID != 2 {
		t.Fatalf("expected ID 2, got %d", t2.ID)
	}

	active := tm.GetActiveTask()
	if active == nil || active.ID != 1 {
		t.Fatalf("expected active task 1, got %v", active)
	}

	got := tm.GetTask(2)
	if got == nil || got.ID != 2 {
		t.Fatalf("expected to get task 2, got %v", got)
	}
}

func TestTaskManagerSwitchActiveTask(t *testing.T) {
	tm := NewTaskManager()
	_ = tm.CreateTask("p1", "m1", "task 1", dummyRecipient{})
	_ = tm.CreateTask("p2", "m2", "task 2", dummyRecipient{})

	// Switch to 2
	switched, err := tm.SetActiveTask(2)
	if err != nil {
		t.Fatalf("unexpected error switching to task 2: %v", err)
	}
	if switched.ID != 2 {
		t.Fatalf("expected switched task ID 2, got %d", switched.ID)
	}

	active := tm.GetActiveTask()
	if active == nil || active.ID != 2 {
		t.Fatalf("expected active task to be 2, got %v", active)
	}

	// Switch to non-existent
	_, err = tm.SetActiveTask(999)
	if err == nil {
		t.Fatalf("expected error switching to non-existent task")
	}
}

func TestTaskManagerProjectQueuing(t *testing.T) {
	tm := NewTaskManager()

	// Task 1 in proj-1 is running
	t1 := tm.CreateTask("proj-1", "m1", "t1", dummyRecipient{})
	t1.Status = TaskStatusRunning

	if !tm.HasRunningTaskInProject("proj-1") {
		t.Fatalf("expected proj-1 to have running task")
	}
	if tm.HasRunningTaskInProject("proj-2") {
		t.Fatalf("expected proj-2 to NOT have running task")
	}

	// Task 2 in proj-1 is queued
	t2 := tm.CreateTask("proj-1", "m1", "t2", dummyRecipient{})
	t2.Status = TaskStatusQueued

	queued := tm.GetNextQueuedTaskForProject("proj-1")
	if queued == nil || queued.ID != t2.ID {
		t.Fatalf("expected queued task %d, got %v", t2.ID, queued)
	}

	queued2 := tm.GetNextQueuedTaskForProject("proj-2")
	if queued2 != nil {
		t.Fatalf("expected no queued task for proj-2, got %v", queued2)
	}
}

func TestTaskManagerAddFollowup(t *testing.T) {
	tm := NewTaskManager()
	t1 := tm.CreateTask("proj-1", "m1", "first task", dummyRecipient{})
	t1.Status = TaskStatusRunning

	t2 := tm.CreateTask("proj-2", "m1", "second task", dummyRecipient{})
	t2.Status = TaskStatusRunning

	// Add followup specifically to task 1
	task, qLen, isAnswer, err := tm.AddFollowup(1, "followup for 1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.ID != 1 || qLen != 1 || isAnswer {
		t.Fatalf("unexpected result: id=%d qLen=%d isAnswer=%v", task.ID, qLen, isAnswer)
	}

	// Add second followup to task 1
	_, qLen2, _, _ := tm.AddFollowup(1, "another followup for 1")
	if qLen2 != 2 {
		t.Fatalf("expected queue len 2, got %d", qLen2)
	}

	// Verify task 2 is unaffected
	t2.Lock()
	t2Followups := len(t2.PendingFollowups)
	t2.Unlock()
	if t2Followups != 0 {
		t.Fatalf("expected task 2 to have 0 followups, got %d", t2Followups)
	}

	// Add followup to task 2
	_, qLenT2, _, _ := tm.AddFollowup(2, "followup for 2")
	if qLenT2 != 1 {
		t.Fatalf("expected task 2 queue len 1, got %d", qLenT2)
	}

	// Try adding to cancelled task
	t1.Lock()
	t1.Status = TaskStatusCancelled
	t1.Unlock()
	_, _, _, err = tm.AddFollowup(1, "will fail")
	if err == nil {
		t.Fatalf("expected error adding to cancelled task")
	}
}

func TestTaskManagerCancelTask(t *testing.T) {
	tm := NewTaskManager()
	t1 := tm.CreateTask("proj-1", "m1", "cancel me", dummyRecipient{})
	t1.Status = TaskStatusRunning
	t1.PendingFollowups = []string{"pending1"}

	cancelled, err := tm.CancelTask(1)
	if err != nil {
		t.Fatalf("unexpected cancel error: %v", err)
	}
	if cancelled.Status != TaskStatusCancelled {
		t.Fatalf("expected status cancelled, got %s", cancelled.Status)
	}
	if len(cancelled.PendingFollowups) != 0 {
		t.Fatalf("expected pending followups cleared")
	}

	// Cancelling again should error
	_, err = tm.CancelTask(1)
	if err == nil {
		t.Fatalf("expected error cancelling already cancelled task")
	}
}

func TestTaskManagerMessageTracking(t *testing.T) {
	tm := NewTaskManager()
	t1 := tm.CreateTask("proj-1", "m1", "t1", dummyRecipient{})

	tm.RegisterMessageTask(100500, t1.ID)

	got := tm.GetTaskByMessageID(100500)
	if got == nil || got.ID != t1.ID {
		t.Fatalf("expected task %d for msg 100500, got %v", t1.ID, got)
	}

	notGot := tm.GetTaskByMessageID(999999)
	if notGot != nil {
		t.Fatalf("expected nil for unknown msg, got %v", notGot)
	}
}

func TestFormatTasksListAndDetails(t *testing.T) {
	tm := NewTaskManager()
	t1 := tm.CreateTask("proj-alpha", "gemini-3.8-flash", "Fix bug in main", dummyRecipient{})
	t1.Status = TaskStatusRunning
	t1.StartedAt = time.Now().Add(-2 * time.Minute)
	t1.AppendLog("Running tests...")

	t2 := tm.CreateTask("proj-alpha", "gemini-3.8-flash", "Add documentation", dummyRecipient{})
	t2.Status = TaskStatusQueued

	msg, menu := FormatTasksList(tm)
	if !strings.Contains(msg, "#1") || !strings.Contains(msg, "proj-alpha") {
		t.Errorf("expected msg to contain #1 and proj-alpha, got: %s", msg)
	}
	if !strings.Contains(msg, "#2") || !strings.Contains(msg, "В очереди") {
		t.Errorf("expected msg to contain #2 queued, got: %s", msg)
	}
	if menu == nil {
		t.Errorf("expected inline menu with task buttons")
	}

	details := FormatTaskDetails(t1, true)
	if !strings.Contains(details, "Задача #1") || !strings.Contains(details, "в фокусе") {
		t.Errorf("expected details to contain task #1 and focus badge, got: %s", details)
	}
	if !strings.Contains(details, "Running tests...") {
		t.Errorf("expected details to contain recent log, got: %s", details)
	}
}

func TestTaskManagerMultiProjectConcurrency(t *testing.T) {
	tm := NewTaskManager()

	t1 := tm.CreateTask("proj-a", "model-1", "task 1", dummyRecipient{})
	t1.Status = TaskStatusRunning

	t2 := tm.CreateTask("proj-b", "model-2", "task 2", dummyRecipient{})
	t2.Status = TaskStatusRunning

	t3 := tm.CreateTask("proj-a", "model-1", "task 3", dummyRecipient{})
	t3.Status = TaskStatusQueued

	if !tm.HasRunningTaskInProject("proj-a") {
		t.Errorf("expected proj-a to have running task")
	}
	if !tm.HasRunningTaskInProject("proj-b") {
		t.Errorf("expected proj-b to have running task")
	}
	if tm.HasRunningTaskInProject("proj-c") {
		t.Errorf("expected proj-c to have no running task")
	}

	queuedA := tm.GetNextQueuedTaskForProject("proj-a")
	if queuedA == nil || queuedA.ID != t3.ID {
		t.Errorf("expected queued task 3 in proj-a, got %v", queuedA)
	}

	queuedB := tm.GetNextQueuedTaskForProject("proj-b")
	if queuedB != nil {
		t.Errorf("expected no queued task in proj-b, got %v", queuedB)
	}
}

func TestSyncLegacySession(t *testing.T) {
	tm := NewTaskManager()
	t1 := tm.CreateTask("proj-test", "model-test", "test prompt", dummyRecipient{})
	t1.Status = TaskStatusRunning
	t1.StartedAt = time.Now()
	t1.RecentLogs = []string{"step 1", "step 2"}
	t1.PendingFollowups = []string{"fix 1"}
	t1.LastPRURL = "https://github.com/test/pr/1"

	syncLegacySession(t1)

	session.Lock()
	running := session.isRunning
	proj := session.currentProject
	logsCount := len(session.recentLogs)
	pr := session.lastPRURL
	session.Unlock()

	if !running {
		t.Errorf("expected session.isRunning to be true")
	}
	if proj != "proj-test" {
		t.Errorf("expected proj-test, got %s", proj)
	}
	if logsCount != 2 {
		t.Errorf("expected 2 logs, got %d", logsCount)
	}
	if pr != "https://github.com/test/pr/1" {
		t.Errorf("expected pr url, got %s", pr)
	}

	syncLegacySession(nil)
	session.Lock()
	runningAfterNil := session.isRunning
	session.Unlock()

	if runningAfterNil {
		t.Errorf("expected session.isRunning to be false after nil sync")
	}
}

func TestTaskManagerCreateTaskWithPlan(t *testing.T) {
	tm := NewTaskManager()
	task := tm.CreateTaskWithPlan("proj-plan", "model-plan", "Implement feature X", dummyRecipient{}, true)

	if !task.RequiresPlan {
		t.Errorf("expected RequiresPlan to be true")
	}
	if task.PlanApproved {
		t.Errorf("expected PlanApproved to be false")
	}
	if task.Plan != "" {
		t.Errorf("expected Plan to be empty initially")
	}

	task.Status = TaskStatusPlanning
	if !task.IsActive() {
		t.Errorf("expected task to be active during planning")
	}
	if task.Status.RussianTitle() != "📝 Составление плана" {
		t.Errorf("unexpected RussianTitle: %s", task.Status.RussianTitle())
	}
	if task.Status.Emoji() != "📝" {
		t.Errorf("unexpected Emoji: %s", task.Status.Emoji())
	}

	if !tm.HasRunningTaskInProject("proj-plan") {
		t.Errorf("expected HasRunningTaskInProject to be true when planning")
	}

	task.Status = TaskStatusWaitingApproval
	task.Plan = "1. First step\n2. Second step"
	if !task.IsActive() {
		t.Errorf("expected task to be active when waiting approval")
	}
	if task.Status.RussianTitle() != "📋 Ожидает утверждения плана" {
		t.Errorf("unexpected RussianTitle: %s", task.Status.RussianTitle())
	}
	if task.Status.Emoji() != "📋" {
		t.Errorf("unexpected Emoji: %s", task.Status.Emoji())
	}
	if !tm.HasRunningTaskInProject("proj-plan") {
		t.Errorf("expected HasRunningTaskInProject to be true when waiting approval")
	}
}

func TestFormatTasksListAndDetailsWithPlan(t *testing.T) {
	tm := NewTaskManager()
	task := tm.CreateTaskWithPlan("proj-plan", "model-plan", "Build feature Y", dummyRecipient{}, true)
	task.Status = TaskStatusWaitingApproval
	task.Plan = "1. Create models\n2. Add endpoints"

	listMsg, menu := FormatTasksList(tm)
	if !strings.Contains(listMsg, "📋") {
		t.Errorf("expected listMsg to contain 📋 emoji")
	}
	if !strings.Contains(listMsg, "/approve") {
		t.Errorf("expected listMsg to contain /approve")
	}
	if menu == nil {
		t.Errorf("expected inline menu for active tasks")
	}

	details := FormatTaskDetails(task, true)
	if !strings.Contains(details, "Ожидает утверждения") {
		t.Errorf("expected details to mention 'Ожидает утверждения'")
	}
	if !strings.Contains(details, "План реализации") {
		t.Errorf("expected details to contain 'План реализации'")
	}
	if !strings.Contains(details, "/approve") {
		t.Errorf("expected details to contain /approve")
	}
}

func TestTaskSessionDurationAndLocking(t *testing.T) {
	task := &TaskSession{
		ID:        1,
		Status:    TaskStatusRunning,
		StartedAt: time.Now().Add(-5 * time.Second),
	}

	// 1. Duration() without prior lock
	dur := task.Duration()
	if dur < 4*time.Second || dur > 7*time.Second {
		t.Errorf("expected ~5s duration, got %v", dur)
	}

	// 2. durationLocked() while task.Lock() is held (simulating ticker goroutine)
	task.Lock()
	durLocked := task.durationLocked()
	task.Unlock()
	if durLocked < 4*time.Second || durLocked > 7*time.Second {
		t.Errorf("expected ~5s durationLocked, got %v", durLocked)
	}

	// 3. Completed task duration
	task.Lock()
	task.FinishedAt = task.StartedAt.Add(12 * time.Second)
	durFinished := task.durationLocked()
	task.Unlock()
	if durFinished != 12*time.Second {
		t.Errorf("expected 12s, got %v", durFinished)
	}
}

func TestTaskManagerLiveQueriesDuringTaskExecution(t *testing.T) {
	tm := NewTaskManager()
	t1 := tm.CreateTask("proj-live", "gemini-3.8-flash", "test concurrency", dummyRecipient{})
	t1.Lock()
	t1.Status = TaskStatusRunning
	t1.StartedAt = time.Now().Add(-10 * time.Second)
	t1.RecentLogs = []string{"initializing...", "running tool test"}
	t1.Unlock()

	syncLegacySession(t1)

	// Run concurrent queries that would hang if any lock was deadlocked
	done := make(chan bool)
	go func() {
		for i := 0; i < 20; i++ {
			// Simulate ticker reading durationLocked under lock
			t1.Lock()
			_ = t1.durationLocked()
			t1.Unlock()

			t1.AppendLog("another step")

			// Concurrently format tasks list (what /tasks does)
			msg, _ := FormatTasksList(tm)
			if !strings.Contains(msg, "#1") {
				t.Errorf("expected #1 in tasks list")
			}

			// Concurrently format details (what /status does)
			det := FormatTaskDetails(t1, true)
			if !strings.Contains(det, "#1") {
				t.Errorf("expected #1 in details")
			}

			// Concurrently get worker PIDs (what /top does)
			_, _ = tm.GetRunningWorkerPids()

			// Concurrently collect resource report
			_ = CollectResourceReport(false)
		}
		done <- true
	}()

	select {
	case <-done:
		// Success, no deadlocks
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock detected during concurrent task queries!")
	}
}

func TestTaskStatusWaitingInputAndDeliverAnswer(t *testing.T) {
	tm := NewTaskManager()
	task := tm.CreateTask("proj-test", "model-x", "do something", dummyRecipient{})
	task.Lock()
	task.Status = TaskStatusWaitingInput
	task.LastQuestion = "Какой цвет выбрать?"
	task.QuestionOptions = []string{"Красный", "Синий"}
	task.Unlock()

	if !task.IsActive() {
		t.Errorf("expected WaitingInput task to be active")
	}

	ok := task.DeliverAnswer("Синий")
	if !ok {
		t.Fatalf("expected DeliverAnswer to succeed")
	}

	select {
	case ans := <-task.AnswerChan:
		if ans != "Синий" {
			t.Errorf("expected 'Синий', got '%s'", ans)
		}
	default:
		t.Fatalf("expected answer in AnswerChan")
	}
}

func TestTaskStatusPausedAndQueueUnblocking(t *testing.T) {
	tm := NewTaskManager()

	// Task 1 in proj-1 is waiting input
	t1 := tm.CreateTask("proj-1", "m1", "task 1", dummyRecipient{})
	t1.Lock()
	t1.Status = TaskStatusWaitingInput
	t1.Unlock()

	if !tm.HasRunningTaskInProject("proj-1") {
		t.Fatalf("expected proj-1 to have running/waiting task")
	}

	// Task 2 in proj-1 is queued
	t2 := tm.CreateTask("proj-1", "m1", "task 2", dummyRecipient{})
	t2.Lock()
	t2.Status = TaskStatusQueued
	t2.Unlock()

	// t1 pauses
	paused := t1.PauseTask()
	if !paused {
		t.Fatalf("expected PauseTask to succeed")
	}

	if t1.Status != TaskStatusPaused {
		t.Errorf("expected status paused, got %s", t1.Status)
	}
	if t1.IsActive() {
		t.Errorf("expected paused task IsActive() to be false")
	}

	// Now proj-1 should NOT have running task
	if tm.HasRunningTaskInProject("proj-1") {
		t.Errorf("expected proj-1 to NOT have running task after t1 paused")
	}

	// Queued task t2 should now be picked up
	nextQueued := tm.GetNextQueuedTaskForProject("proj-1")
	if nextQueued == nil || nextQueued.ID != t2.ID {
		t.Fatalf("expected next queued task to be t2, got %v", nextQueued)
	}
}

func TestTaskManagerResumeTask(t *testing.T) {
	tm := NewTaskManager()

	t1 := tm.CreateTask("proj-resume", "m1", "initial", dummyRecipient{})
	t1.Lock()
	t1.Status = TaskStatusPaused
	t1.ConversationID = "conv-abc-123"
	t1.LastQuestion = "Вы уверены?"
	t1.QuestionOptions = []string{"Да", "Нет"}
	t1.Unlock()

	// 1. Возобновление при свободном проекте
	resumed, err := tm.ResumeTask(t1.ID, "Да, уверен")
	if err != nil {
		t.Fatalf("unexpected error resuming task: %v", err)
	}
	if resumed.Status != TaskStatusRunning {
		t.Errorf("expected status Running, got %s", resumed.Status)
	}
	if resumed.ConversationID != "conv-abc-123" {
		t.Errorf("expected ConversationID to be preserved as 'conv-abc-123', got '%s'", resumed.ConversationID)
	}
	if resumed.CurrentPrompt != "Да, уверен" {
		t.Errorf("expected CurrentPrompt to be updated, got '%s'", resumed.CurrentPrompt)
	}

	// 2. Возобновление при занятом проекте
	t2 := tm.CreateTask("proj-resume", "m1", "another task", dummyRecipient{})
	t2.Lock()
	t2.Status = TaskStatusRunning
	t2.Unlock()

	// Снова ставим t1 на паузу для теста
	t1.Lock()
	t1.Status = TaskStatusPaused
	t1.Unlock()

	resumedQueued, err := tm.ResumeTask(t1.ID, "Новый ответ")
	if err != nil {
		t.Fatalf("unexpected error resuming task when project is busy: %v", err)
	}
	if resumedQueued.Status != TaskStatusQueued {
		t.Errorf("expected status Queued when project is busy, got %s", resumedQueued.Status)
	}
	if resumedQueued.CurrentPrompt != "Новый ответ" {
		t.Errorf("expected CurrentPrompt to be 'Новый ответ', got '%s'", resumedQueued.CurrentPrompt)
	}

	// 3. Тест AddFollowup для задачи на паузе
	t3 := tm.CreateTask("proj-free", "m1", "task 3", dummyRecipient{})
	t3.Lock()
	t3.Status = TaskStatusPaused
	t3.Unlock()

	resumedViaFollowup, _, isAnswer, err := tm.AddFollowup(t3.ID, "Ответ через AddFollowup")
	if err != nil {
		t.Fatalf("unexpected error from AddFollowup: %v", err)
	}
	if !isAnswer {
		t.Errorf("expected isAnswer to be true for paused task followup")
	}
	if resumedViaFollowup.Status != TaskStatusRunning {
		t.Errorf("expected task to be Running after followup, got %s", resumedViaFollowup.Status)
	}
}

func TestGetActiveTaskWithCompletedAndActiveTasks(t *testing.T) {
	tm := NewTaskManager()

	t1 := tm.CreateTask("proj-1", "m1", "task 1", dummyRecipient{})
	t1.Lock()
	t1.Status = TaskStatusRunning
	t1.Unlock()

	// Initially, t1 is active
	active := tm.GetActiveTask()
	if active == nil || active.ID != t1.ID {
		t.Fatalf("expected active task to be t1, got %v", active)
	}

	// Task 2 is queued with plan requirement
	t2 := tm.CreateTaskWithPlan("proj-1", "m1", "task 2", dummyRecipient{}, true)
	t2.Lock()
	t2.Status = TaskStatusQueued
	t2.Unlock()

	// Task 1 completes
	t1.Lock()
	t1.Status = TaskStatusCompleted
	t1.Unlock()

	// Task 2 transitions to planning
	t2.Lock()
	t2.Status = TaskStatusPlanning
	t2.Unlock()

	// GetActiveTask should now return t2 even though activeTaskID initially was t1
	active = tm.GetActiveTask()
	if active == nil || active.ID != t2.ID {
		t.Fatalf("expected active task to be t2 (planning), got %v", active)
	}

	// Task 2 transitions to waiting approval
	t2.Lock()
	t2.Status = TaskStatusWaitingApproval
	t2.Unlock()

	active = tm.GetActiveTask()
	if active == nil || active.ID != t2.ID {
		t.Fatalf("expected active task to be t2 (waiting approval), got %v", active)
	}

	// Task 2 completes, and no active tasks remain
	t2.Lock()
	t2.Status = TaskStatusCompleted
	t2.Unlock()

	active = tm.GetActiveTask()
	if active == nil {
		t.Fatalf("expected fallback task when none are active, got nil")
	}
}

