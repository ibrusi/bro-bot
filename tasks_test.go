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

