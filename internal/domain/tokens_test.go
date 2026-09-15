package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFormatThousands(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0"},
		{5, "5"},
		{42, "42"},
		{999, "999"},
		{1000, "1 000"},
		{14500, "14 500"},
		{123456, "123 456"},
		{1234567, "1 234 567"},
		{-12345, "-12 345"},
	}

	for _, tc := range tests {
		actual := formatThousands(tc.input)
		if actual != tc.expected {
			t.Errorf("formatThousands(%d) = %q, expected %q", tc.input, actual, tc.expected)
		}
	}
}

func TestFormatCompact(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0"},
		{500, "500"},
		{999, "999"},
		{1000, "1.0k"},
		{14200, "14k"},
		{145000, "145k"},
		{1500000, "1.5M"},
	}

	for _, tc := range tests {
		actual := formatCompact(tc.input)
		if actual != tc.expected {
			t.Errorf("formatCompact(%d) = %q, expected %q", tc.input, actual, tc.expected)
		}
	}
}

func TestFormatDurationHuman(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{0 * time.Second, "0с"},
		{15 * time.Second, "15с"},
		{59 * time.Second, "59с"},
		{60 * time.Second, "1м"},
		{65 * time.Second, "1м 05с"},
		{3600 * time.Second, "1ч 00м"},
		{3665 * time.Second, "1ч 01м"},
	}

	for _, tc := range tests {
		actual := FormatDurationHuman(tc.input)
		if actual != tc.expected {
			t.Errorf("FormatDurationHuman(%v) = %q, expected %q", tc.input, actual, tc.expected)
		}
	}
}

func TestUsageStatsAddAndCache(t *testing.T) {
	u1 := UsageStats{
		InputTokens:     1000,
		OutputTokens:    200,
		ThinkingTokens:  150,
		CacheReadTokens: 500,
		TotalTokens:     1200,
	}

	u2 := UsageStats{
		InputTokens:     2000,
		OutputTokens:    300,
		ThinkingTokens:  250,
		CacheReadTokens: 1000,
		TotalTokens:     2300,
	}

	u1.Add(u2)

	if u1.InputTokens != 3000 {
		t.Errorf("InputTokens = %d, expected 3000", u1.InputTokens)
	}
	if u1.OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, expected 500", u1.OutputTokens)
	}
	if u1.ThinkingTokens != 400 {
		t.Errorf("ThinkingTokens = %d, expected 400", u1.ThinkingTokens)
	}
	if u1.CacheReadTokens != 1500 {
		t.Errorf("CacheReadTokens = %d, expected 1500", u1.CacheReadTokens)
	}
	if u1.TotalTokens != 3500 {
		t.Errorf("TotalTokens = %d, expected 3500", u1.TotalTokens)
	}

	// CacheHitRate: 1500 / (3000 + 1500) = 1500 / 4500 = 33.333%
	rate := u1.CacheHitRate()
	if rate < 33.3 || rate > 33.4 {
		t.Errorf("CacheHitRate = %f, expected ~33.33", rate)
	}
}

func TestTaskTokenMetricsTPS(t *testing.T) {
	m := TaskTokenMetrics{
		DurationSeconds: 10.0,
		Usage: UsageStats{
			InputTokens:  10000,
			OutputTokens: 2000,
			TotalTokens:  12000,
		},
	}

	if m.TokensPerSecond() != 200.0 {
		t.Errorf("TokensPerSecond = %f, expected 200.0", m.TokensPerSecond())
	}
	if m.TotalTokensPerSecond() != 1200.0 {
		t.Errorf("TotalTokensPerSecond = %f, expected 1200.0", m.TotalTokensPerSecond())
	}
}

func TestTokenTrackerLifecycle(t *testing.T) {
	tracker := NewTokenTracker()

	// 1. Initial state
	msg := tracker.GetTokensCommandMessage()
	if !strings.Contains(msg, "Задачи ещё не запускались") {
		t.Errorf("Expected initial empty message, got %s", msg)
	}

	// 2. Start Task
	tracker.StartTask("my-repo", "gemini-3.8-flash-medium", "create feature")

	// 3. Step 1 update
	tracker.RecordStepUsage(1, UsageStats{
		InputTokens:     1000,
		OutputTokens:    100,
		ThinkingTokens:  50,
		CacheReadTokens: 200,
		TotalTokens:     1100,
	})

	snippet := tracker.GetLiveStatusSnippet()
	if snippet == "" || !strings.Contains(snippet, "Токены:") {
		t.Errorf("Expected live status snippet, got %q", snippet)
	}

	// 4. Result of Step 1
	tracker.RecordResultUsage(UsageStats{
		InputTokens:     1000,
		OutputTokens:    100,
		ThinkingTokens:  50,
		CacheReadTokens: 200,
		TotalTokens:     1100,
	}, 2.0, 1)

	// 5. Check active /tokens message
	activeMsg := tracker.GetTokensCommandMessage()
	if !strings.Contains(activeMsg, "Активная задача в работе") {
		t.Errorf("Expected active task message, got %s", activeMsg)
	}
	if !strings.Contains(activeMsg, "gemini-3.8-flash-medium") {
		t.Errorf("Expected model in active task message, got %s", activeMsg)
	}

	// 6. Finish Task
	completed := tracker.FinishTask("https://github.com/org/repo/pull/1")
	if completed.PRURL != "https://github.com/org/repo/pull/1" {
		t.Errorf("PRURL not recorded properly")
	}
	if completed.Usage.TotalTokens != 1100 {
		t.Errorf("Completed usage total tokens = %d, expected 1100", completed.Usage.TotalTokens)
	}

	// 7. Check idle /tokens message with history and session totals
	idleMsg := tracker.GetTokensCommandMessage()
	if !strings.Contains(idleMsg, "Статистика последней задачи:") {
		t.Errorf("Expected last task stats, got %s", idleMsg)
	}
	if !strings.Contains(idleMsg, "Общая статистика сессии бота:") {
		t.Errorf("Expected session stats, got %s", idleMsg)
	}
	if !strings.Contains(idleMsg, "Выполнено задач: <code>1</code>") {
		t.Errorf("Expected 1 task run, got %s", idleMsg)
	}

	// 8. Short last task for /usage
	shortLast := tracker.FormatShortLastTask()
	if !strings.Contains(shortLast, "1 100") {
		t.Errorf("Expected 1 100 in short last, got %q", shortLast)
	}
}

func TestStreamEventParsing(t *testing.T) {
	stepJSON := `{"event":"step_update","step_update":{"conversation_id":"abc-123","step_index":1,"state":"DONE","step_type":"agent_response","duration_seconds":1.5,"usage":{"input_tokens":14000,"output_tokens":500,"thinking_tokens":300,"cache_read_tokens":1000,"total_tokens":14500}}}`
	var stepEvt StreamEvent
	if err := json.Unmarshal([]byte(stepJSON), &stepEvt); err != nil {
		t.Fatalf("Failed to parse step update JSON: %v", err)
	}
	if stepEvt.Event != "step_update" || stepEvt.StepUpdate == nil {
		t.Fatalf("Malformed step update parsed")
	}
	if stepEvt.StepUpdate.Usage.OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, expected 500", stepEvt.StepUpdate.Usage.OutputTokens)
	}

	resultJSON := `{"event":"result","result":{"conversation_id":"abc-123","status":"SUCCESS","response":"Done! PR_URL: https://github.com/foo/bar/pull/5","duration_seconds":4.2,"num_turns":2,"usage":{"input_tokens":28000,"output_tokens":1000,"thinking_tokens":600,"cache_read_tokens":2000,"total_tokens":29000}}}`
	var resEvt StreamEvent
	if err := json.Unmarshal([]byte(resultJSON), &resEvt); err != nil {
		t.Fatalf("Failed to parse result JSON: %v", err)
	}
	if resEvt.Result == nil || resEvt.Result.Usage.TotalTokens != 29000 {
		t.Errorf("TotalTokens = %d, expected 29000", resEvt.Result.Usage.TotalTokens)
	}
}

func TestFormatToolAction(t *testing.T) {
	infoCmd := &StreamToolInfo{
		Name: "run_command",
		Parameters: map[string]interface{}{
			"CommandLine": "git status",
		},
	}
	resCmd := FormatToolAction("run_command", infoCmd)
	if !strings.Contains(resCmd, "git status") {
		t.Errorf("Expected 'git status' in tool action, got %q", resCmd)
	}

	infoEdit := &StreamToolInfo{
		Name: "replace_file_content",
		Parameters: map[string]interface{}{
			"TargetFile": "/path/to/main.go",
		},
	}
	resEdit := FormatToolAction("replace_file_content", infoEdit)
	if !strings.Contains(resEdit, "main.go") {
		t.Errorf("Expected 'main.go' in tool action, got %q", resEdit)
	}
}

func TestModelContextWindow(t *testing.T) {
	tests := []struct {
		model    string
		expected int64
	}{
		{"gemini-3.8-flash-high", 1_048_576},
		{"flash", 1_048_576},
		{"pro", 1_048_576},
		{"claude-sonnet-4-6", 200_000},
		{"claude-opus-4-6-thinking", 200_000},
		{"opus", 200_000},
		{"gpt-oss-120b-medium", 131_072},
		{"120b", 131_072},
		{"unknown-model", 1_048_576},
	}

	for _, tc := range tests {
		actual := ModelContextWindow(tc.model)
		if actual != tc.expected {
			t.Errorf("ModelContextWindow(%q) = %d, expected %d", tc.model, actual, tc.expected)
		}
	}
}

func TestFormatContextLimit(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1_048_576, "1.0M"},
		{200_000, "200k"},
		{131_072, "131k"},
		{500, "500"},
	}

	for _, tc := range tests {
		actual := FormatContextLimit(tc.input)
		if actual != tc.expected {
			t.Errorf("FormatContextLimit(%d) = %q, expected %q", tc.input, actual, tc.expected)
		}
	}
}

func TestRenderContextBar(t *testing.T) {
	b0 := renderContextBar(0, 10)
	if b0 != "□□□□□□□□□□" {
		t.Errorf("renderContextBar(0, 10) = %q, expected all empty", b0)
	}

	b100 := renderContextBar(100, 10)
	if b100 != "■■■■■■■■■■" {
		t.Errorf("renderContextBar(100, 10) = %q, expected all filled", b100)
	}

	b50 := renderContextBar(50, 10)
	if b50 != "■■■■■□□□□□" {
		t.Errorf("renderContextBar(50, 10) = %q, expected half filled", b50)
	}
}

func TestGetContextCommandMessageIdle(t *testing.T) {
	tracker := NewTokenTracker()
	msg := tracker.GetContextCommandMessage(nil, "my-project", "flash")

	if !strings.Contains(msg, "Контекстное окно модели agy") {
		t.Errorf("Expected header in idle context message, got:\n%s", msg)
	}
	if !strings.Contains(msg, "1.0M") {
		t.Errorf("Expected 1.0M in idle context message, got:\n%s", msg)
	}
	if !strings.Contains(msg, "my-project") {
		t.Errorf("Expected project in idle context message, got:\n%s", msg)
	}
}

func TestGetContextCommandMessageActiveAndCompleted(t *testing.T) {
	tracker := NewTokenTracker()
	tracker.StartTask("tg-agent-bot", "flash", "Implement context")
	tracker.SetConversationID("conv-uuid-12345")
	tracker.RecordToolCall()

	tracker.RecordStepUsage(1, UsageStats{
		InputTokens:     10000,
		OutputTokens:    500,
		ThinkingTokens:  200,
		CacheReadTokens: 4000,
		TotalTokens:     10500,
	})

	task := &TaskSession{
		ID:             1,
		Project:        "tg-agent-bot",
		Model:          "gemini-3.8-flash-high",
		Status:         TaskStatusRunning,
		ConversationID: "conv-uuid-12345",
	}

	// 1. Active task
	activeMsg := tracker.GetContextCommandMessage(task, "tg-agent-bot", "flash")
	if !strings.Contains(activeMsg, "Контекст активной задачи #1") {
		t.Errorf("Expected active task header, got:\n%s", activeMsg)
	}
	if !strings.Contains(activeMsg, "1.0M") {
		t.Errorf("Expected 1.0M window, got:\n%s", activeMsg)
	}
	if !strings.Contains(activeMsg, "conv-uuid-12345") {
		t.Errorf("Expected conversation ID, got:\n%s", activeMsg)
	}
	if !strings.Contains(activeMsg, "Входной контекст") {
		t.Errorf("Expected input context section, got:\n%s", activeMsg)
	}
	if !strings.Contains(activeMsg, "Кэш промпта") {
		t.Errorf("Expected cache section, got:\n%s", activeMsg)
	}
	if !strings.Contains(activeMsg, "Ответы агента") {
		t.Errorf("Expected responses section, got:\n%s", activeMsg)
	}

	// 2. Complete task
	completed := tracker.FinishTask("https://github.com/org/repo/pull/10")
	task.Status = TaskStatusCompleted
	task.TokenMetrics = &completed

	compMsg := tracker.GetContextCommandMessage(task, "tg-agent-bot", "flash")
	if !strings.Contains(compMsg, "Контекст задачи #1") {
		t.Errorf("Expected completed task header, got:\n%s", compMsg)
	}
	if !strings.Contains(compMsg, "Pull Request") {
		t.Errorf("Expected PR link, got:\n%s", compMsg)
	}
}

func TestParseStreamEvent_ErrorResult(t *testing.T) {
	errJSON := `{"event":"result","result":{"conversation_id":"err-123","status":"ERROR","error":"invalid model selection: model xyz not found","response":"","duration_seconds":1.5,"num_turns":0}}`
	evt, err := ParseStreamEvent(errJSON)
	if err != nil {
		t.Fatalf("unexpected error parsing error result: %v", err)
	}
	if evt.Result == nil {
		t.Fatalf("expected Result to be non-nil")
	}
	if !evt.Result.IsError() {
		t.Errorf("expected IsError to return true for ERROR status")
	}
	if evt.Result.Error != "invalid model selection: model xyz not found" {
		t.Errorf("expected error message to match, got %q", evt.Result.Error)
	}
	if evt.Result.Status != "ERROR" {
		t.Errorf("expected status 'ERROR', got %q", evt.Result.Status)
	}
}
