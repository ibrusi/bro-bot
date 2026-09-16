package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func newTestStorage(t *testing.T) *SQLiteStorage {
	t.Helper()
	s, err := NewSQLiteStorage(":memory:")
	if err != nil {
		t.Fatalf("failed to create test memory storage: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func TestStorageTasksCRUD(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	now := time.Now().Round(time.Second)
	task := &TaskRecord{
		Project:         "test-proj",
		Model:           "gemini-3.1-pro-high",
		InitialPrompt:   "Hello world task",
		CurrentPrompt:   "Hello world task step 1",
		Status:          "queued",
		RequiresPlan:    true,
		Plan:            "## Step 1\nDo something\n## Step 2\nFinish",
		PlanApproved:    false,
		StartedAt:       now,
		RecipientID:     "12345",
		QuestionOptions: []string{"Option A", "Option B"},
	}

	id, err := s.CreateTask(ctx, task)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if id != 1 {
		t.Fatalf("expected ID 1, got %d", id)
	}

	got, err := s.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if got == nil {
		t.Fatalf("expected task, got nil")
	}
	if got.Project != "test-proj" || got.Status != "queued" || !got.RequiresPlan {
		t.Fatalf("task fields mismatch: %+v", got)
	}
	if len(got.QuestionOptions) != 2 || got.QuestionOptions[0] != "Option A" {
		t.Fatalf("question options mismatch: %+v", got.QuestionOptions)
	}
	if got.Plan != task.Plan {
		t.Fatalf("plan mismatch: got %q, want %q", got.Plan, task.Plan)
	}

	// Update status and plan
	newPlan := "## Updated Plan\nDetailed architecture"
	if err := s.UpdateTaskPlan(ctx, id, newPlan, true); err != nil {
		t.Fatalf("UpdateTaskPlan failed: %v", err)
	}
	if err := s.UpdateTaskStatus(ctx, id, "running"); err != nil {
		t.Fatalf("UpdateTaskStatus failed: %v", err)
	}

	gotUpdated, err := s.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask after update failed: %v", err)
	}
	if gotUpdated.Plan != newPlan || !gotUpdated.PlanApproved || gotUpdated.Status != "running" {
		t.Fatalf("updated fields mismatch: %+v", gotUpdated)
	}

	// Update finished
	finishedTime := now.Add(5 * time.Minute)
	if err := s.UpdateTaskFinished(ctx, id, "completed", finishedTime, "https://github.com/pull/1"); err != nil {
		t.Fatalf("UpdateTaskFinished failed: %v", err)
	}

	gotFinal, err := s.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask after finish failed: %v", err)
	}
	if gotFinal.Status != "completed" || gotFinal.LastPRURL != "https://github.com/pull/1" {
		t.Fatalf("final task fields mismatch: %+v", gotFinal)
	}

	// List tasks
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task in list, got %d", len(tasks))
	}
}

func TestStorageLongPlanHandling(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	// Сгенерируем очень длинный Markdown-план (10 000+ символов)
	var bldr strings.Builder
	for i := 1; i <= 50; i++ {
		bldr.WriteString(fmt.Sprintf("### Секция плана %d\nОписание сложных действий с символами <>&'\" и кодом `func Do%d() error`.\n\n", i, i))
	}
	longPlan := bldr.String()

	task := &TaskRecord{
		Project:       "long-plan-proj",
		Model:         "flash",
		InitialPrompt: "Big task",
		CurrentPrompt: "Big task",
		Status:        "waiting_approval",
		RequiresPlan:  true,
		Plan:          longPlan,
	}

	id, err := s.CreateTask(ctx, task)
	if err != nil {
		t.Fatalf("CreateTask with long plan failed: %v", err)
	}

	got, err := s.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if got.Plan != longPlan {
		t.Fatalf("expected plan length %d, got %d", len(longPlan), len(got.Plan))
	}
}

func TestStorageFollowups(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	task := &TaskRecord{
		Project:       "p",
		Model:         "m",
		InitialPrompt: "prompt",
		CurrentPrompt: "prompt",
		Status:        "running",
	}
	taskID, err := s.CreateTask(ctx, task)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	_ = s.AddFollowup(ctx, taskID, "followup 1", 0)
	_ = s.AddFollowup(ctx, taskID, "followup 2", 1)
	_ = s.AddFollowup(ctx, taskID, "followup 3", 2)

	followups, err := s.GetFollowups(ctx, taskID)
	if err != nil {
		t.Fatalf("GetFollowups failed: %v", err)
	}
	if len(followups) != 3 {
		t.Fatalf("expected 3 followups, got %d", len(followups))
	}
	if followups[0] != "followup 1" || followups[1] != "followup 2" || followups[2] != "followup 3" {
		t.Fatalf("order mismatch: %+v", followups)
	}

	if err := s.ClearFollowups(ctx, taskID); err != nil {
		t.Fatalf("ClearFollowups failed: %v", err)
	}
	followupsAfter, err := s.GetFollowups(ctx, taskID)
	if err != nil {
		t.Fatalf("GetFollowups after clear failed: %v", err)
	}
	if len(followupsAfter) != 0 {
		t.Fatalf("expected 0 followups after clear, got %d", len(followupsAfter))
	}
}

func TestStorageLogs(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	taskID, _ := s.CreateTask(ctx, &TaskRecord{Project: "p", Model: "m", Status: "running"})

	for i := 1; i <= 30; i++ {
		_ = s.AppendLog(ctx, taskID, fmt.Sprintf("Log line %d", i))
	}

	logs, err := s.GetRecentLogs(ctx, taskID, 10)
	if err != nil {
		t.Fatalf("GetRecentLogs failed: %v", err)
	}
	if len(logs) != 10 {
		t.Fatalf("expected 10 logs, got %d", len(logs))
	}
	if logs[0] != "Log line 21" || logs[9] != "Log line 30" {
		t.Fatalf("expected last 10 logs in chronological order, got first: %s, last: %s", logs[0], logs[9])
	}
}

func TestStorageMetricsAndAggregate(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	taskID1, _ := s.CreateTask(ctx, &TaskRecord{Project: "p1", Model: "m1", Status: "completed"})
	taskID2, _ := s.CreateTask(ctx, &TaskRecord{Project: "p2", Model: "m2", Status: "completed"})

	m1 := &TokenMetricsRecord{
		TaskID:          taskID1,
		InputTokens:     1000,
		OutputTokens:    500,
		ThinkingTokens:  100,
		CacheReadTokens: 400,
		TotalTokens:     1500,
		DurationSeconds: 10.5,
		Turns:           2,
		ToolCallsCount:  3,
		Model:           "gemini-3.1-pro-high",
		PRURL:           "https://github.com/pr/1",
	}
	if err := s.SaveMetrics(ctx, m1); err != nil {
		t.Fatalf("SaveMetrics m1 failed: %v", err)
	}

	m2 := &TokenMetricsRecord{
		TaskID:          taskID2,
		InputTokens:     2000,
		OutputTokens:    1000,
		ThinkingTokens:  200,
		CacheReadTokens: 800,
		TotalTokens:     3000,
		DurationSeconds: 20.0,
		Turns:           4,
		ToolCallsCount:  5,
		Model:           "gemini-3.1-pro-high",
	}
	if err := s.SaveMetrics(ctx, m2); err != nil {
		t.Fatalf("SaveMetrics m2 failed: %v", err)
	}

	got1, err := s.GetMetrics(ctx, taskID1)
	if err != nil {
		t.Fatalf("GetMetrics failed: %v", err)
	}
	if got1 == nil || got1.TotalTokens != 1500 || got1.PRURL != "https://github.com/pr/1" {
		t.Fatalf("metrics mismatch: %+v", got1)
	}

	agg, err := s.GetAggregateMetrics(ctx)
	if err != nil {
		t.Fatalf("GetAggregateMetrics failed: %v", err)
	}
	if agg.TotalTasks != 2 || agg.TotalTokens != 4500 || agg.InputTokens != 3000 || agg.OutputTokens != 1500 {
		t.Fatalf("aggregate mismatch: %+v", agg)
	}
}

func TestStorageMetricsLastStep(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	taskID, _ := s.CreateTask(ctx, &TaskRecord{Project: "tg-bot-agent", Model: "gemini-3.8-flash-high", Status: "completed"})

	m := &TokenMetricsRecord{
		TaskID:                  taskID,
		InputTokens:             2814924,
		OutputTokens:            122747,
		ThinkingTokens:          75642,
		CacheReadTokens:         22423778,
		TotalTokens:             2937671,
		DurationSeconds:         1687.0,
		Turns:                   4,
		ToolCallsCount:          6,
		Model:                   "gemini-3.8-flash-high",
		PRURL:                   "https://github.com/ibrusi/tg-bot-agent/pull/35",
		ConversationID:          "3fac122e-6fca-4987-a072-b2c88051ac5e",
		LastStepInputTokens:     3706,
		LastStepOutputTokens:    1660,
		LastStepThinkingTokens:  1566,
		LastStepCacheReadTokens: 118134,
		LastStepTotalTokens:     5366,
	}

	if err := s.SaveMetrics(ctx, m); err != nil {
		t.Fatalf("SaveMetrics failed: %v", err)
	}

	got, err := s.GetMetrics(ctx, taskID)
	if err != nil {
		t.Fatalf("GetMetrics failed: %v", err)
	}
	if got == nil {
		t.Fatalf("expected non-nil metrics")
	}

	if got.LastStepInputTokens != 3706 || got.LastStepOutputTokens != 1660 ||
		got.LastStepThinkingTokens != 1566 || got.LastStepCacheReadTokens != 118134 ||
		got.LastStepTotalTokens != 5366 {
		t.Errorf("LastStep metrics mismatch: got %+v", got)
	}
}

func TestStorageMessageMapping(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	taskID, _ := s.CreateTask(ctx, &TaskRecord{Project: "p", Model: "m", Status: "running"})

	if err := s.RegisterMessageTask(ctx, "12345", "999", taskID); err != nil {
		t.Fatalf("RegisterMessageTask failed: %v", err)
	}

	gotTaskID, err := s.GetTaskIDByMessage(ctx, "999")
	if err != nil {
		t.Fatalf("GetTaskIDByMessage failed: %v", err)
	}
	if gotTaskID != taskID {
		t.Fatalf("expected task ID %d, got %d", taskID, gotTaskID)
	}

	all, err := s.ListAllMessageTasks(ctx)
	if err != nil {
		t.Fatalf("ListAllMessageTasks failed: %v", err)
	}
	if len(all) != 1 || all["999"] != taskID {
		t.Fatalf("ListAllMessageTasks mismatch: %+v", all)
	}
}

func TestStorageSettings(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	val, err := s.GetSetting(ctx, "current_project")
	if err != nil {
		t.Fatalf("GetSetting failed: %v", err)
	}
	if val != "" {
		t.Fatalf("expected empty for unset key, got %q", val)
	}

	if err := s.SetSetting(ctx, "current_project", "tg-bot-agent"); err != nil {
		t.Fatalf("SetSetting failed: %v", err)
	}

	val, err = s.GetSetting(ctx, "current_project")
	if err != nil {
		t.Fatalf("GetSetting after set failed: %v", err)
	}
	if val != "tg-bot-agent" {
		t.Fatalf("expected 'tg-bot-agent', got %q", val)
	}

	// Update setting
	_ = s.SetSetting(ctx, "current_project", "another-project")
	val, _ = s.GetSetting(ctx, "current_project")
	if val != "another-project" {
		t.Fatalf("expected updated setting, got %q", val)
	}
}

func TestStorageRecovery(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	t1, _ := s.CreateTask(ctx, &TaskRecord{Project: "p1", Model: "m", Status: "running"})
	t2, _ := s.CreateTask(ctx, &TaskRecord{Project: "p2", Model: "m", Status: "planning"})
	t3, _ := s.CreateTask(ctx, &TaskRecord{Project: "p3", Model: "m", Status: "waiting_approval"})
	t4, _ := s.CreateTask(ctx, &TaskRecord{Project: "p4", Model: "m", Status: "completed"})
	t5, _ := s.CreateTask(ctx, &TaskRecord{Project: "p5", Model: "m", Status: "waiting_input"})

	recovered, err := s.RecoverInterruptedTasks(ctx)
	if err != nil {
		t.Fatalf("RecoverInterruptedTasks failed: %v", err)
	}
	if len(recovered) != 3 {
		t.Fatalf("expected 3 recovered tasks, got %d (%+v)", len(recovered), recovered)
	}

	got1, _ := s.GetTask(ctx, t1)
	got2, _ := s.GetTask(ctx, t2)
	got3, _ := s.GetTask(ctx, t3)
	got4, _ := s.GetTask(ctx, t4)
	got5, _ := s.GetTask(ctx, t5)

	if got1.Status != "paused" || got2.Status != "paused" || got5.Status != "paused" {
		t.Fatalf("expected t1, t2, t5 to be paused, got %s, %s, %s", got1.Status, got2.Status, got5.Status)
	}
	if got3.Status != "waiting_approval" {
		t.Fatalf("expected t3 to remain waiting_approval, got %s", got3.Status)
	}
	if got4.Status != "completed" {
		t.Fatalf("expected t4 to remain completed, got %s", got4.Status)
	}

	logs1, _ := s.GetRecentLogs(ctx, t1, 5)
	if len(logs1) == 0 || !strings.Contains(logs1[0], "перезапуском бота") {
		t.Fatalf("expected recovery log entry for t1, got %+v", logs1)
	}
}

func TestStorageUpdateTaskConversationID(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	task := &TaskRecord{
		Project:       "conv-proj",
		Model:         "flash",
		InitialPrompt: "Task without convID initially",
		Status:        "running",
	}

	id, err := s.CreateTask(ctx, task)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	// Verify conversation_id is initially empty
	got, err := s.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if got.ConversationID != "" {
		t.Fatalf("expected empty ConversationID, got %q", got.ConversationID)
	}

	// Update conversation_id immediately
	testConvID := "conv-test-uuid-4567-89ab"
	if err := s.UpdateTaskConversationID(ctx, id, testConvID); err != nil {
		t.Fatalf("UpdateTaskConversationID failed: %v", err)
	}

	// Verify conversation_id is persisted
	got2, err := s.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if got2.ConversationID != testConvID {
		t.Fatalf("expected ConversationID %q, got %q", testConvID, got2.ConversationID)
	}
}
