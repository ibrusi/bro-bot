package handlers

import (
	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/models"
	"bro-bot/internal/ports"
	"bro-bot/internal/storage"
	"bro-bot/internal/utils"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

const testChatID ports.ChatID = "12345"

func TestInitDefaultProject(t *testing.T) {
	tmpDir := t.TempDir()

	// Case 1: DEFAULT_PROJECT unset -> picks first found dir (aaa-project)
	if err := os.Mkdir(filepath.Join(tmpDir, "aaa-project"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmpDir, "bro-bot"), 0755); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("DEFAULT_PROJECT")
	initDefaultProject(tmpDir)

	config.ProjectState.RLock()
	cur := config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	if cur != "aaa-project" {
		t.Errorf("expected aaa-project, got %s", cur)
	}

	// Case 2: Custom DEFAULT_PROJECT env var
	os.Setenv("DEFAULT_PROJECT", "bro-bot")
	defer os.Unsetenv("DEFAULT_PROJECT")
	initDefaultProject(tmpDir)

	config.ProjectState.RLock()
	cur = config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	if cur != "bro-bot" {
		t.Errorf("expected bro-bot, got %s", cur)
	}

	// Case 3: Neither custom nor existing -> fallback to first dir
	otherDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(otherDir, "zzz-fallback"), 0755); err != nil {
		t.Fatal(err)
	}
	os.Setenv("DEFAULT_PROJECT", "non-existent")
	initDefaultProject(otherDir)

	config.ProjectState.RLock()
	cur = config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	if cur != "zzz-fallback" {
		t.Errorf("expected zzz-fallback, got %s", cur)
	}
}

func TestInitDefaultModel(t *testing.T) {
	if models.GlobalModelRegistry == nil {
		models.GlobalModelRegistry = models.NewModelRegistry(10 * time.Minute)
	}

	// Case 1: Custom DEFAULT_MODEL with alias
	os.Setenv("DEFAULT_MODEL", "flash")
	defer os.Unsetenv("DEFAULT_MODEL")
	initDefaultModel()

	config.ProjectState.RLock()
	cur := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	if cur != "gemini-3.8-flash-medium" {
		t.Errorf("expected gemini-3.8-flash-medium for alias 'flash', got %s", cur)
	}

	// Case 2: Custom explicit DEFAULT_MODEL
	os.Setenv("DEFAULT_MODEL", "gemini-3.1-pro-high")
	initDefaultModel()

	config.ProjectState.RLock()
	cur = config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	if cur != "gemini-3.1-pro-high" {
		t.Errorf("expected gemini-3.1-pro-high, got %s", cur)
	}
}

func TestInitDefaultModel_Unset(t *testing.T) {
	if os.Getenv("BE_CRASHER") == "1" {
		os.Unsetenv("DEFAULT_MODEL")
		initDefaultModel()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestInitDefaultModel_Unset")
	cmd.Env = append(os.Environ(), "BE_CRASHER=1", "DEFAULT_MODEL=")
	err := cmd.Run()
	if e, ok := err.(*exec.ExitError); ok && !e.Success() {
		return
	}
	t.Fatalf("process ran with err %v, want exit status 1", err)
}

func TestIsConfirmationText(t *testing.T) {
	positives := []string{
		"утверждаю",
		"УТВЕРДИТЬ",
		"подтверждаю",
		"согласовано",
		"ок",
		"OK",
		"approve",
		"APPROVE",
		"lgtm",
		"+",
		"да",
		"yes",
		"погнали",
		"делай",
		"start",
	}

	for _, p := range positives {
		if !isConfirmationText(p) {
			t.Errorf("expected '%s' to be recognized as confirmation", p)
		}
	}

	negatives := []string{
		"добавь тесты",
		"нет",
		"перепиши на rust",
		"не утверждаю",
		"измени шаг 2",
		"",
	}

	for _, n := range negatives {
		if isConfirmationText(n) {
			t.Errorf("expected '%s' to NOT be recognized as confirmation", n)
		}
	}
}

func TestPlanModeState(t *testing.T) {
	config.ProjectState.Lock()
	origMode := config.ProjectState.PlanMode
	config.ProjectState.PlanMode = true
	isPlan := config.ProjectState.PlanMode
	config.ProjectState.PlanMode = false
	isPlanFalse := config.ProjectState.PlanMode
	config.ProjectState.PlanMode = origMode
	config.ProjectState.Unlock()

	if !isPlan {
		t.Errorf("expected planMode to be true")
	}
	if isPlanFalse {
		t.Errorf("expected planMode to be false")
	}
}

func TestBuildQuestionMarkup(t *testing.T) {
	task := &domain.TaskSession{
		ID:              42,
		QuestionOptions: []string{"Вариант 1", "Вариант 2", "Вариант 3"},
	}

	menu := buildQuestionMarkup(task)
	if menu == nil || len(menu.Rows) == 0 {
		t.Fatalf("expected inline keyboard to be generated")
	}

	// Should contain option buttons and pause/cancel buttons
	totalButtons := 0
	foundChoice := false
	foundPause := false
	foundCancel := false

	for _, row := range menu.Rows {
		for _, btn := range row {
			totalButtons++
			if btn.Action == "q_choice" {
				foundChoice = true
			}
			if btn.Action == "q_pause" || btn.Text == "⏸ Приостановить" {
				foundPause = true
			}
			if btn.Action == "plan_cancel" || btn.Text == "❌ Отменить" {
				foundCancel = true
			}
		}
	}

	if !foundChoice {
		t.Errorf("expected choice buttons in question markup")
	}
	if !foundPause {
		t.Errorf("expected pause button in question markup")
	}
	if !foundCancel {
		t.Errorf("expected cancel button in question markup")
	}
}

func TestBuildResumeMarkup(t *testing.T) {
	menu := buildResumeMarkup(99)
	if menu == nil || len(menu.Rows) == 0 {
		t.Fatalf("expected resume markup")
	}

	foundResume := false
	for _, row := range menu.Rows {
		for _, btn := range row {
			if btn.Text == "▶️ Возобновить задачу" {
				foundResume = true
			}
		}
	}
	if !foundResume {
		t.Errorf("expected resume button in resume markup")
	}
}

func TestPlanApprovalWithVariantsMarkup(t *testing.T) {
	planWithVariants := `
### Варианты:
Вариант 1: Использовать Docker
Вариант 2: Использовать Systemd
`
	variants := utils.ExtractPlanVariantOptions(planWithVariants)
	if len(variants) != 2 {
		t.Fatalf("expected 2 variants, got %d", len(variants))
	}
	if !strings.Contains(variants[0], "Docker") {
		t.Errorf("expected Docker in variant 1, got %s", variants[0])
	}

	var rows [][]ports.Button
	for i, v := range variants {
		btnText := "Утвердить: " + v
		rows = append(rows, []ports.Button{{Text: btnText, Action: "plan_appr_var", Payload: "10:" + string(rune('0'+i))}})
	}
	rows = append(rows, []ports.Button{
		{Text: "✅ Утвердить и начать", Action: "plan_approve", Payload: "10"},
		{Text: "❌ Отменить", Action: "plan_cancel", Payload: "10"},
	})
	planMenu := &ports.Keyboard{Rows: rows}

	if len(planMenu.Rows) != 3 {
		t.Errorf("expected 3 rows in plan menu, got %d", len(planMenu.Rows))
	}
}

func TestGetDefaultCommands(t *testing.T) {
	commands := getDefaultCommands()
	if len(commands) == 0 {
		t.Fatalf("expected non-empty commands list")
	}
	if len(commands) > 100 {
		t.Errorf("Telegram allows at most 100 commands, got %d", len(commands))
	}

	expectedRequired := []string{
		"plan",
		"planmode",
		"approve",
		"resume",
		"retry",
		"tasks",
		"status",
		"task",
		"add",
		"new",
		"cancel",
		"tokens",
		"context",
		"top",
		"usage",
		"models",
		"model",
		"projects",
		"use",
		"clone",
		"restart",
		"rebuild",
		"start",
	}

	seen := make(map[string]bool)
	for _, cmd := range commands {
		if seen[cmd.Name] {
			t.Errorf("duplicate command found in getDefaultCommands: %s", cmd.Name)
		}
		seen[cmd.Name] = true

		if len(cmd.Name) < 1 || len(cmd.Name) > 32 {
			t.Errorf("invalid command length for '%s': %d (must be 1-32)", cmd.Name, len(cmd.Name))
		}
		if strings.ToLower(cmd.Name) != cmd.Name {
			t.Errorf("command text must be lowercase: %s", cmd.Name)
		}
		if len(cmd.Description) < 1 || len(cmd.Description) > 256 {
			t.Errorf("invalid description length for '%s': %d (must be 1-256)", cmd.Name, len(cmd.Description))
		}
	}

	for _, req := range expectedRequired {
		if !seen[req] {
			t.Errorf("expected command '%s' to be present in getDefaultCommands", req)
		}
	}
}

func TestPlanApprovalPreservesPendingFollowups(t *testing.T) {
	tm := domain.NewTaskManager()
	task := tm.CreateTaskWithPlan("test-proj", "flash", "Build feature", testChatID, true)
	task.Lock()
	task.Status = domain.TaskStatusWaitingApproval
	task.Plan = "1. Step one\n2. Step two"
	task.PendingFollowups = []string{"add extra validation", "include unit test"}
	task.Unlock()

	// Simulate plan approval logic
	task.Lock()
	task.PlanApproved = true
	task.Status = domain.TaskStatusRunning
	task.RecentLogs = nil
	// Verify that PendingFollowups is NOT cleared
	followupsCount := len(task.PendingFollowups)
	task.Unlock()

	if followupsCount != 2 {
		t.Fatalf("expected 2 pending followups to be preserved on plan approval, got %d", followupsCount)
	}
	if task.PendingFollowups[0] != "add extra validation" || task.PendingFollowups[1] != "include unit test" {
		t.Errorf("unexpected pending followups content: %v", task.PendingFollowups)
	}
}

func TestWaitForTaskInputPlanningStatusLogic(t *testing.T) {
	// When task requires plan and plan is not approved
	task := &domain.TaskSession{
		ID:           1,
		RequiresPlan: true,
		PlanApproved: false,
		Status:       domain.TaskStatusWaitingInput,
	}

	task.Lock()
	if task.RequiresPlan && !task.PlanApproved {
		task.Status = domain.TaskStatusPlanning
	} else {
		task.Status = domain.TaskStatusRunning
	}
	task.Unlock()

	if task.Status != domain.TaskStatusPlanning {
		t.Errorf("expected status domain.TaskStatusPlanning, got %s", task.Status)
	}

	// When task does not require plan or is approved
	task2 := &domain.TaskSession{
		ID:           2,
		RequiresPlan: true,
		PlanApproved: true,
		Status:       domain.TaskStatusWaitingInput,
	}

	task2.Lock()
	if task2.RequiresPlan && !task2.PlanApproved {
		task2.Status = domain.TaskStatusPlanning
	} else {
		task2.Status = domain.TaskStatusRunning
	}
	task2.Unlock()

	if task2.Status != domain.TaskStatusRunning {
		t.Errorf("expected status domain.TaskStatusRunning, got %s", task2.Status)
	}
}

func TestPTYLaunchProcessGroup(t *testing.T) {
	cmd := exec.Command("sleep", "1")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("expected pty.Start to succeed, got %v", err)
	}
	defer ptmx.Close()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("expected Getpgid to succeed, got %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Errorf("expected pgid %d to equal pid %d", pgid, cmd.Process.Pid)
	}
}

func TestPlanMenuForLongPlanIncludesDocumentButton(t *testing.T) {
	// 1. Long plan
	longPlan := strings.Repeat("Абвгд12345 ", 300) // ~3300 runes > maxInlinePlanRunes
	planRunes := []rune(longPlan)
	isLong := len(planRunes) > maxInlinePlanRunes
	if !isLong {
		t.Fatalf("expected longPlan to be > maxInlinePlanRunes (%d), got %d", maxInlinePlanRunes, len(planRunes))
	}

	var rows [][]ports.Button
	rows = append(rows, []ports.Button{
		{Text: "✅ Утвердить и начать", Action: "plan_approve", Payload: "42"},
		{Text: "❌ Отменить", Action: "plan_cancel", Payload: "42"},
	})

	if isLong {
		rows = append(rows, []ports.Button{{Text: "📄 Скачать план (.md)", Action: "plan_doc", Payload: "42"}})
	}
	planMenu := &ports.Keyboard{Rows: rows}

	if len(planMenu.Rows) != 2 {
		t.Fatalf("expected 2 rows in long plan menu (actions + doc button), got %d", len(planMenu.Rows))
	}

	foundDocBtn := false
	for _, row := range planMenu.Rows {
		for _, btn := range row {
			if btn.Action == "plan_doc" {
				foundDocBtn = true
			}
		}
	}
	if !foundDocBtn {
		t.Errorf("expected plan_doc button in planMenu for long plan")
	}

	// 2. Document file creation
	docName := fmt.Sprintf("plan_task_%d.md", 42)
	doc := ports.Document{
		FileName: docName,
		MIME:     "text/markdown",
		Caption:  fmt.Sprintf("📄 Полный план реализации задачи #%d (%s)", 42, "test-proj"),
		Content:  []byte(longPlan),
	}

	if doc.FileName != "plan_task_42.md" {
		t.Errorf("expected doc.FileName to be plan_task_42.md, got %s", doc.FileName)
	}
	if doc.MIME != "text/markdown" {
		t.Errorf("expected MIME text/markdown, got %s", doc.MIME)
	}
}

func TestPlanSummaryFormattingForTelegram(t *testing.T) {
	plan := "# Архитектурный план\n\n## 1. Анализ проблемы\nЗдесь анализ...\n\n## 2. Решение\nЗдесь решение..."
	summary := utils.ExtractPlanSummary(plan, 50)
	htmlText := utils.MarkdownToTelegramHTML(summary)

	if !strings.Contains(htmlText, "<b>Архитектурный план</b>") {
		t.Errorf("expected HTML to contain formatted header, got: %s", htmlText)
	}
}

func TestSyncLegacySessionSavesToSQLite(t *testing.T) {
	s, err := storage.NewSQLiteStorage(":memory:")
	if err != nil {
		t.Fatalf("failed to create sqlite: %v", err)
	}
	defer s.Close()

	domain.GlobalTaskManager.InitWithStorage(s)

	task := domain.GlobalTaskManager.CreateTaskWithPlan("test-sync-proj", "flash", "Build sqlite feature", testChatID, true)

	task.Lock()
	task.Status = domain.TaskStatusWaitingApproval
	task.Plan = "### My Detailed SQLite Plan\n- Step 1: Storage\n- Step 2: Domain\n- Step 3: Handlers"
	task.PlanApproved = false
	task.LastPRURL = "https://github.com/pull/99"
	task.Unlock()

	// Вызов syncLegacySession синхронизирует состояние задачи в SQLite
	syncLegacySession(task)

	rec, err := s.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask from sqlite failed: %v", err)
	}
	if rec == nil {
		t.Fatalf("task was not found in sqlite")
	}
	if rec.Status != string(domain.TaskStatusWaitingApproval) {
		t.Errorf("expected status %s, got %s", domain.TaskStatusWaitingApproval, rec.Status)
	}
	if rec.Plan != task.Plan {
		t.Errorf("expected plan %q, got %q", task.Plan, rec.Plan)
	}
	if rec.LastPRURL != "https://github.com/pull/99" {
		t.Errorf("expected LastPRURL https://github.com/pull/99, got %s", rec.LastPRURL)
	}
}

func TestHandlersSQLiteSettingsPersistence(t *testing.T) {
	s, err := storage.NewSQLiteStorage(":memory:")
	if err != nil {
		t.Fatalf("failed to create sqlite: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	_ = s.SetSetting(ctx, "current_project", "my-persisted-project")
	_ = s.SetSetting(ctx, "current_model", "sonnet")
	_ = s.SetSetting(ctx, "plan_mode", "true")

	p, _ := s.GetSetting(ctx, "current_project")
	m, _ := s.GetSetting(ctx, "current_model")
	pm, _ := s.GetSetting(ctx, "plan_mode")

	if p != "my-persisted-project" || m != "sonnet" || pm != "true" {
		t.Errorf("settings mismatch: p=%q, m=%q, pm=%q", p, m, pm)
	}
}

func TestTaskStepTimeoutAndErrorHandlers(t *testing.T) {
	memStore, err := storage.NewSQLiteStorage(":memory:")
	if err != nil {
		t.Fatalf("failed to create sqlite: %v", err)
	}
	defer memStore.Close()

	tm := domain.NewTaskManagerWithStorage(memStore)
	domain.GlobalTaskManager = tm

	task := tm.CreateTask("proj-timeout", "m", "Prompt", testChatID)
	task.Lock()
	task.ConversationID = "conv-timeout-123"
	task.Status = domain.TaskStatusRunning
	task.Unlock()
	tm.SaveTask(task)

	// 1. Проверяем перевод задачи в статус paused при таймауте
	handleTaskStepTimeout(nil, testChatID, task, "proj-timeout", task.ID, false)

	task.Lock()
	st := task.Status
	logs := append([]string(nil), task.RecentLogs...)
	task.Unlock()

	if st != domain.TaskStatusPaused {
		t.Errorf("expected task to be paused after timeout, got: %s", st)
	}
	if len(logs) == 0 || !strings.Contains(logs[len(logs)-1], "Превышен таймаут") {
		t.Errorf("expected timeout message in logs, got: %+v", logs)
	}

	// 2. Проверяем перевод задачи в статус failed при ошибке
	task2 := tm.CreateTask("proj-err", "m", "Prompt 2", testChatID)
	task2.Lock()
	task2.ConversationID = "conv-err-456"
	task2.Status = domain.TaskStatusRunning
	task2.Unlock()
	tm.SaveTask(task2)

	handleTaskStepError(nil, testChatID, task2, "proj-err", task2.ID, fmt.Errorf("exit status 127"))

	task2.Lock()
	st2 := task2.Status
	logs2 := append([]string(nil), task2.RecentLogs...)
	task2.Unlock()

	if st2 != domain.TaskStatusFailed {
		t.Errorf("expected task to be failed after error, got: %s", st2)
	}
	if len(logs2) == 0 || !strings.Contains(logs2[len(logs2)-1], "exit status 127") {
		t.Errorf("expected error details in logs, got: %+v", logs2)
	}
}

func TestPlanfileCommandLinkParsing(t *testing.T) {
	testCases := []struct {
		input  string
		wantID int
		wantOK bool
	}{
		{"/planfile_42", 42, true},
		{"/planfile_100@bot", 100, true},
		{"/plan_7", 7, true},
		{"/plan_15@my_tg_bot", 15, true},
		{"/planfile_abc", 0, false},
		{"/planfile", 0, false},
		{"hello", 0, false},
	}

	for _, tc := range testCases {
		isPlanCmd := strings.HasPrefix(tc.input, "/planfile_") || strings.HasPrefix(tc.input, "/plan_")
		if !isPlanCmd {
			if tc.wantOK {
				t.Errorf("input %s expected to be recognized as plan command", tc.input)
			}
			continue
		}

		rawID := strings.TrimPrefix(tc.input, "/planfile_")
		rawID = strings.TrimPrefix(rawID, "/plan_")
		if atIdx := strings.Index(rawID, "@"); atIdx != -1 {
			rawID = rawID[:atIdx]
		}
		id, err := strconv.Atoi(strings.TrimSpace(rawID))
		if tc.wantOK {
			if err != nil || id != tc.wantID {
				t.Errorf("input %s: got id=%d err=%v, want %d", tc.input, id, err, tc.wantID)
			}
		} else {
			if err == nil {
				t.Errorf("input %s: expected error, got id=%d", tc.input, id)
			}
		}
	}
}

func TestStartPlanPayloadParsing(t *testing.T) {
	testCases := []struct {
		payload string
		wantID  int
		wantOK  bool
	}{
		{"plan_42", 42, true},
		{"planfile_99", 99, true},
		{"other_payload", 0, false},
	}

	for _, tc := range testCases {
		isPlanPayload := strings.HasPrefix(tc.payload, "plan_") || strings.HasPrefix(tc.payload, "planfile_")
		if !isPlanPayload {
			if tc.wantOK {
				t.Errorf("payload %s expected to match", tc.payload)
			}
			continue
		}

		rawID := strings.TrimPrefix(tc.payload, "planfile_")
		rawID = strings.TrimPrefix(rawID, "plan_")
		id, err := strconv.Atoi(rawID)
		if tc.wantOK {
			if err != nil || id != tc.wantID {
				t.Errorf("payload %s: got id=%d, want %d", tc.payload, id, tc.wantID)
			}
		}
	}
}

func TestTaskCompletionMessageWithPlanAndPrompt(t *testing.T) {
	taskID := 55
	projectName := "test-proj"
	prURL := "https://github.com/org/repo/pull/55"
	initialPrompt := strings.Repeat("Сделай важную фичу в проекте. ", 20) // ~600 chars
	hasPlan := true
	statsSummary := "⚡ 1500 токенов"

	var compBldr strings.Builder
	if prURL != "" {
		compBldr.WriteString(fmt.Sprintf("🎉 <b>Задача #%d выполнена!</b>\n📁 Проект: <code>%s</code>\n🔗 <a href=\"%s\">Открыть Pull Request</a>\n", taskID, html.EscapeString(projectName), html.EscapeString(prURL)))
	} else {
		compBldr.WriteString(fmt.Sprintf("✅ <b>Задача #%d завершена!</b> (<code>%s</code>)\n", taskID, html.EscapeString(projectName)))
	}

	if initialPrompt != "" {
		compBldr.WriteString(fmt.Sprintf("📝 <b>Задача:</b> <i>«%s»</i>\n",
			html.EscapeString(utils.TruncateString(initialPrompt, 200))))
	}

	if hasPlan {
		compBldr.WriteString(fmt.Sprintf("📄 <b>План реализации:</b> /planfile_%d\n", taskID))
	}

	compBldr.WriteString("\n" + statsSummary)

	res := compBldr.String()

	if !strings.Contains(res, "Задача #55 выполнена!") {
		t.Errorf("expected header in completion message")
	}
	if !strings.Contains(res, "/planfile_55") {
		t.Errorf("expected /planfile_55 in completion message")
	}
	if strings.Contains(res, initialPrompt) {
		t.Errorf("expected initialPrompt to be truncated in completion message")
	}
	if !strings.Contains(res, "...") {
		t.Errorf("expected ellipsis in truncated prompt")
	}

	// Inline buttons
	var actButtons []ports.Button
	if prURL != "" {
		actButtons = append(actButtons, ports.Button{Text: "🔗 Открыть PR", URL: prURL})
	}
	if hasPlan {
		actButtons = append(actButtons, ports.Button{Text: "📄 Скачать план (.md)", Action: "plan_doc", Payload: strconv.Itoa(taskID)})
	}
	compMenu := &ports.Keyboard{Rows: [][]ports.Button{actButtons}}

	if len(compMenu.Rows) != 1 || len(compMenu.Rows[0]) != 2 {
		t.Fatalf("expected 1 row with 2 buttons in completion menu")
	}
	if !strings.Contains(compMenu.Rows[0][1].Text, "Скачать план") {
		t.Errorf("expected plan download button in completion menu")
	}
}

func TestSendPlanForApprovalSingleMessageFormatting(t *testing.T) {
	taskID := 12
	projectName := "plan-project"
	initialPrompt := strings.Repeat("Разработай сложный модуль. ", 15) // ~400 chars
	planText := strings.Repeat("1. Шаг архитектурного плана.\n", 60)   // ~1800 runes

	promptSnippet := utils.TruncateString(initialPrompt, 250)
	if len([]rune(promptSnippet)) > 250 {
		t.Errorf("expected prompt snippet to be <= 250 runes")
	}

	planRunes := []rune(planText)
	isLong := len(planRunes) > maxInlinePlanRunes
	if !isLong {
		t.Fatalf("expected plan to be long (> %d), got %d", maxInlinePlanRunes, len(planRunes))
	}

	summary := utils.ExtractPlanSummary(planText, maxInlinePlanRunes)
	summaryHTML := utils.MarkdownToTelegramHTML(summary)

	msgText := fmt.Sprintf(
		"📋 <b>План реализации задачи #%d</b> (<code>%s</code>):\n\n"+
			"📝 <b>Задача:</b> <i>«%s»</i>\n\n"+
			"%s\n\n📄 <i>Полный детальный план (%d знаков):</i> /planfile_%d (или кнопка ниже)",
		taskID, html.EscapeString(projectName),
		html.EscapeString(promptSnippet),
		summaryHTML, len(planRunes), taskID,
	)

	if !strings.Contains(msgText, "/planfile_12") {
		t.Errorf("expected plan message to contain /planfile_12 link")
	}
	if !strings.Contains(msgText, "Задача:</b> <i>«") {
		t.Errorf("expected plan message to contain task section")
	}
	if strings.Contains(msgText, initialPrompt) {
		t.Errorf("expected initialPrompt to be truncated in plan message")
	}
	if len([]rune(msgText)) > 3000 {
		t.Errorf("expected plan message to comfortably fit in one Telegram message (<3000 runes), got %d", len([]rune(msgText)))
	}
}

func TestEvaluateStepCompletion_PlanningImmunity(t *testing.T) {
	// 1. Text plan containing questions, question marks, and options should NOT trigger waiting_input
	planWithQuestions := `# План реализации
1. Анализ проблемы:
   - Стоит ли использовать redis или in-memory cache?
   - Какой таймаут выбрать?
2. Архитектура:
   - Вариант 1: SQLite
   - Вариант 2: Postgres
Что вы думаете по поводу этого плана? Подтвердите выбор.`

	outcome, isQ, qText, qOpts := evaluateStepCompletion(true, false, "", nil, "", planWithQuestions)
	if outcome != StepOutcomeSuccess {
		t.Errorf("expected StepOutcomeSuccess for plan with questions, got %v", outcome)
	}
	if isQ {
		t.Errorf("expected isQuestion=false for plan text with question marks, got true")
	}
	if qText != "" || qOpts != nil {
		t.Errorf("expected empty question text/opts, got text=%q, opts=%v", qText, qOpts)
	}

	// 2. Planning phase with explicit ask_question tool call should trigger waiting_input
	toolQText := "Выберите базовую ветку для фичи"
	toolOpts := []string{"main", "develop"}
	outcome2, isQ2, qText2, qOpts2 := evaluateStepCompletion(true, true, toolQText, toolOpts, "", "thinking...")
	if outcome2 != StepOutcomeWaitingInput {
		t.Errorf("expected StepOutcomeWaitingInput for ask_question tool call during planning, got %v", outcome2)
	}
	if !isQ2 {
		t.Errorf("expected isQuestion=true for ask_question tool call")
	}
	if qText2 != toolQText {
		t.Errorf("expected qText=%q, got %q", toolQText, qText2)
	}
	if len(qOpts2) != 2 || qOpts2[0] != "main" || qOpts2[1] != "develop" {
		t.Errorf("expected qOpts %v, got %v", toolOpts, qOpts2)
	}
}

func TestEvaluateStepCompletion_ExecutionWithPR(t *testing.T) {
	// Even if output text ends with a question mark, having a PR URL guarantees StepOutcomeSuccess
	respWithQuestion := "Задача выполнена, PR создан. Хотите внести дополнительные изменения?"
	prURL := "https://github.com/org/repo/pull/123"

	outcome, isQ, _, _ := evaluateStepCompletion(false, false, "", nil, prURL, respWithQuestion)
	if outcome != StepOutcomeSuccess {
		t.Errorf("expected StepOutcomeSuccess when PR URL is present, got %v", outcome)
	}
	if isQ {
		t.Errorf("expected isQuestion=false when PR URL is present, got true")
	}
}

func TestEvaluateStepCompletion_ExecutionQuestions(t *testing.T) {
	// 1. Ask question tool call without PR
	toolQ := "Какой порт использовать для сервиса?"
	toolOpts := []string{"8080", "3000"}
	outcome1, isQ1, qText1, qOpts1 := evaluateStepCompletion(false, true, toolQ, toolOpts, "", "")
	if outcome1 != StepOutcomeWaitingInput || !isQ1 || qText1 != toolQ || len(qOpts1) != 2 {
		t.Errorf("expected StepOutcomeWaitingInput with tool question, got outcome=%v isQ=%v text=%q opts=%v",
			outcome1, isQ1, qText1, qOpts1)
	}

	// 2. Final response ending with question without PR
	questionResp := "Я реализовал логику валидации. Нужно ли также добавить интеграционные тесты?"
	outcome2, isQ2, qText2, _ := evaluateStepCompletion(false, false, "", nil, "", questionResp)
	if outcome2 != StepOutcomeWaitingInput || !isQ2 {
		t.Errorf("expected StepOutcomeWaitingInput for final response question, got outcome=%v isQ=%v", outcome2, isQ2)
	}
	if !strings.Contains(qText2, "Нужно ли также добавить интеграционные тесты?") {
		t.Errorf("expected question text to contain question sentence, got %q", qText2)
	}

	// 3. Normal completion without question without PR
	normalResp := "Все изменения успешно внесены и скомпилированы. Код готов к ревью."
	outcome3, isQ3, _, _ := evaluateStepCompletion(false, false, "", nil, "", normalResp)
	if outcome3 != StepOutcomeSuccess || isQ3 {
		t.Errorf("expected StepOutcomeSuccess for normal completion, got outcome=%v isQ=%v", outcome3, isQ3)
	}
}

func TestTaskWaitingInputResumeAndDeliver(t *testing.T) {
	tm := domain.NewTaskManager()
	task := tm.CreateTask("test-proj", "flash", "Test question answer flow", testChatID)

	task.Lock()
	task.Status = domain.TaskStatusWaitingInput
	task.LastQuestion = "Какой цвет выбрать?"
	task.QuestionOptions = []string{"Красный", "Синий"}
	task.Unlock()

	// 1. DeliverAnswer with active Stdin
	pr, pw := io.Pipe()
	task.Lock()
	task.Stdin = pw
	task.Unlock()

	readDone := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := pr.Read(buf)
		readDone <- string(buf[:n])
	}()

	task.DeliverAnswer("Красный")
	gotLine := <-readDone
	if gotLine != "Красный\n" {
		t.Errorf("expected 'Красный\\n' written to stdin, got %q", gotLine)
	}

	// Check that AnswerChan also received it
	select {
	case ans := <-task.AnswerChan:
		if ans != "Красный" {
			t.Errorf("expected 'Красный' on AnswerChan, got %q", ans)
		}
	default:
		t.Errorf("expected answer on AnswerChan")
	}

	// 2. ResumeTask when Cmd is nil
	task.Lock()
	task.Cmd = nil
	task.Status = domain.TaskStatusWaitingInput
	task.Unlock()

	resumedTask, err := tm.ResumeTask(task.ID, "Синий")
	if err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	resumedTask.Lock()
	st := resumedTask.Status
	prompt := resumedTask.CurrentPrompt
	resumedTask.Unlock()

	if st != domain.TaskStatusRunning {
		t.Errorf("expected status TaskStatusRunning after resume, got %s", st)
	}
	if prompt != "Синий" {
		t.Errorf("expected CurrentPrompt 'Синий', got %q", prompt)
	}
}

func TestIsLikelyErrorMessage(t *testing.T) {
	cases := []struct {
		text     string
		expected bool
	}{
		{
			text:     "error: Eligibility check failed: Your current account is not eligible for Antigravity, because it is not currently available in your location.",
			expected: true,
		},
		{
			text:     "fatal: unable to access repository: operation not permitted",
			expected: true,
		},
		{
			text:     "error: invalid model selection (--model \"foo\"): not recognized",
			expected: true,
		},
		{
			text:     "panic: runtime error: invalid memory address or nil pointer dereference",
			expected: true,
		},
		{
			text:     "# План реализации\n1. Добавить обработку error в сетевом клиенте.\n2. Архитектура: клиент-сервер.",
			expected: false,
		},
		{
			text:     "1. Исследование кода.\n2. Доработка методов для логирования fatal errors.\n3. План тестирования.",
			expected: false,
		},
		{
			text:     "",
			expected: false,
		},
	}

	for _, c := range cases {
		got := isLikelyErrorMessage(c.text)
		if got != c.expected {
			t.Errorf("isLikelyErrorMessage(%q) = %v, expected %v", c.text, got, c.expected)
		}
	}
}

func TestExtractStepErrorMessage(t *testing.T) {
	tm := domain.NewTaskManager()
	task := tm.CreateTask("test-proj", "flash", "Test extract error", testChatID)

	// 1. При наличии resultError возвращается именно он
	got1 := extractStepErrorMessage(task, "result error message from agy", errors.New("exit status 1"))
	if got1 != "result error message from agy" {
		t.Errorf("expected result error, got %q", got1)
	}

	// 2. При отсутствии resultError, поиск в RecentLogs
	task.Lock()
	task.RecentLogs = []string{
		"⚡ git status",
		"error: Eligibility check failed: Your current account is not eligible",
	}
	task.Unlock()
	got2 := extractStepErrorMessage(task, "", errors.New("exit status 1"))
	if got2 != "error: Eligibility check failed: Your current account is not eligible" {
		t.Errorf("expected recent log error, got %q", got2)
	}

	// 3. При отсутствии в RecentLogs, поиск в FullOutput
	task.Lock()
	task.RecentLogs = []string{"⚡ ls -la"}
	task.FullOutput.Reset()
	task.FullOutput.WriteString("some normal log\nerror: invalid model selection: model xyz\n")
	task.Unlock()
	got3 := extractStepErrorMessage(task, "", errors.New("exit status 1"))
	if got3 != "error: invalid model selection: model xyz" {
		t.Errorf("expected fullOutput error, got %q", got3)
	}

	// 4. Если ничего не найдено, возвращается waitErr
	task.Lock()
	task.RecentLogs = nil
	task.FullOutput.Reset()
	task.FullOutput.WriteString("just some text\nno error markers here\n")
	task.Unlock()
	got4 := extractStepErrorMessage(task, "", errors.New("process killed unexpectedly"))
	if got4 != "process killed unexpectedly" {
		t.Errorf("expected waitErr error, got %q", got4)
	}
}

func TestExecuteStepErrorEvaluation(t *testing.T) {
	// Эмуляция завершения executeStepForTask при ошибках
	tm := domain.NewTaskManager()
	task := tm.CreateTask("test-proj", "flash", "Task with error", testChatID)

	// Случай: hasResult = true, но status = "ERROR" и waitErr != nil
	resultStatus := "ERROR"
	resultError := "error: quota exceeded"
	hasResult := true
	waitErr := errors.New("exit status 1")

	hasError := (hasResult && (strings.EqualFold(resultStatus, "ERROR") || strings.TrimSpace(resultError) != "")) || waitErr != nil
	if !hasError {
		t.Fatalf("expected hasError to be true when resultStatus is ERROR")
	}

	errText := extractStepErrorMessage(task, resultError, waitErr)
	res := StepResult{
		Outcome:      StepOutcomeError,
		Error:        errors.New(errText),
		HasResult:    hasResult,
		ResultStatus: resultStatus,
	}

	if res.Outcome != StepOutcomeError {
		t.Errorf("expected StepOutcomeError, got %v", res.Outcome)
	}
	if res.Error.Error() != "error: quota exceeded" {
		t.Errorf("expected 'error: quota exceeded', got %q", res.Error.Error())
	}
}

func TestIsAgyPrintTimeoutLine(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		expected bool
	}{
		{
			name:     "Real agy CLI timeout banner with partial output",
			line:     "[agy] print timeout after 30m0s with turn in progress; returning partial output",
			expected: true,
		},
		{
			name:     "Real agy CLI timeout banner short",
			line:     "[agy] print timeout after 5m0s",
			expected: true,
		},
		{
			name:     "Real Print mode CLI prefix",
			line:     "Print mode: print timeout after 15m with turn in progress after 20 polls (printed=10)",
			expected: true,
		},
		{
			name:     "Stream JSON event containing print timeout after in agent response text delta",
			line:     `{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"if strings.Contains(cleanLine, \"print timeout after\") {"}}`,
			expected: false,
		},
		{
			name:     "Stream JSON event containing print timeout after in tool call view_file content",
			line:     `{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"DONE","step_type":"tool","tool_name":"view_file","tool_info":{"name":"view_file","parameters":{"path":"main.go"},"content":"check print timeout after"}}`,
			expected: false,
		},
		{
			name:     "Stream JSON result event",
			line:     `{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"All done"}}`,
			expected: false,
		},
		{
			name:     "Git diff in plain output containing print timeout after",
			line:     `+			if strings.Contains(cleanLine, "print timeout after") {`,
			expected: false,
		},
		{
			name:     "User text discussing timeout in log",
			line:     `проверь, пожалуйста, print timeout after выполнение`,
			expected: false,
		},
		{
			name:     "Empty line",
			line:     "",
			expected: false,
		},
		{
			name:     "Whitespace only",
			line:     "   \t\n  ",
			expected: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isAgyPrintTimeoutLine(tc.line)
			if got != tc.expected {
				t.Errorf("isAgyPrintTimeoutLine(%q) = %v, expected %v", tc.line, got, tc.expected)
			}
		})
	}
}

func TestStepTimeoutSimulation(t *testing.T) {
	// 1. Проверяем, что стрим с JSON-событиями, содержащими "print timeout after", НЕ приводит к stepTimedOut = true
	jsonStreamWithTimeoutCode := []string{
		`{"event":"init","conversation_id":"conv-test-1","init":{"tools":["view_file"]}}`,
		`{"event":"step_update","step_update":{"step_index":1,"step_type":"agent_response","text_delta":"Examining main.go for print timeout after logic"}}`,
		`{"event":"result","result":{"conversation_id":"conv-test-1","status":"SUCCESS","response":"# План реализации\n1. Шаг"}}`,
	}

	var stepTimedOut bool
	for _, rawLine := range jsonStreamWithTimeoutCode {
		cleanLine := utils.AnsiRegex.ReplaceAllString(rawLine, "")
		cleanLine = strings.TrimSpace(cleanLine)
		if cleanLine == "" {
			continue
		}

		evt, err := domain.ParseStreamEvent(cleanLine)
		if err == nil && evt != nil {
			continue
		}

		if isAgyPrintTimeoutLine(cleanLine) {
			stepTimedOut = true
		}
	}

	if stepTimedOut {
		t.Fatalf("expected stepTimedOut to be FALSE when stream JSON contains 'print timeout after'")
	}

	// 2. Проверяем, что реальный терминальный баннер agy о таймауте приводит к stepTimedOut = true
	terminalOutputWithTimeout := []string{
		`{"event":"init","conversation_id":"conv-test-2","init":{"tools":["view_file"]}}`,
		`[agy] print timeout after 30m0s with turn in progress; returning partial output`,
		`{"event":"result","result":{"conversation_id":"conv-test-2","status":"SUCCESS","response":""}}`,
	}

	var realTimedOut bool
	for _, rawLine := range terminalOutputWithTimeout {
		cleanLine := utils.AnsiRegex.ReplaceAllString(rawLine, "")
		cleanLine = strings.TrimSpace(cleanLine)
		if cleanLine == "" {
			continue
		}

		evt, err := domain.ParseStreamEvent(cleanLine)
		if err == nil && evt != nil {
			continue
		}

		if isAgyPrintTimeoutLine(cleanLine) {
			realTimedOut = true
		}
	}

	if !realTimedOut {
		t.Fatalf("expected realTimedOut to be TRUE when terminal output contains [agy] print timeout banner")
	}
}

type mockTransport struct {
	*mock.Messenger
	commands  map[string]ports.Handler
	callbacks map[string]ports.Handler
	textH     ports.Handler
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		Messenger: mock.New(),
		commands:  make(map[string]ports.Handler),
		callbacks: make(map[string]ports.Handler),
	}
}

func (m *mockTransport) OnCommand(name string, h ports.Handler) {
	m.commands[name] = h
}
func (m *mockTransport) OnText(h ports.Handler) {
	m.textH = h
}
func (m *mockTransport) OnCallback(action string, h ports.Handler) {
	m.callbacks[action] = h
}
func (m *mockTransport) Use(mw func(ports.Handler) ports.Handler) {}
func (m *mockTransport) Start(ctx context.Context) error         { return nil }
func (m *mockTransport) Stop()                                   {}

func TestHasAgentConflict(t *testing.T) {
	// 1. nil task -> false
	if HasAgentConflict(nil, "agy") {
		t.Errorf("expected false for nil task")
	}

	// 2. Empty ConversationID -> false, even if agents differ
	t1 := &domain.TaskSession{Agent: "claude", ConversationID: ""}
	if HasAgentConflict(t1, "agy") {
		t.Errorf("expected false when ConversationID is empty")
	}

	// 3. ConversationID present, same agent -> false (case insensitive)
	t2 := &domain.TaskSession{Agent: "claude", ConversationID: "conv-1"}
	if HasAgentConflict(t2, "claude") {
		t.Errorf("expected false when agents match")
	}
	if HasAgentConflict(t2, "Claude") {
		t.Errorf("expected false for case-insensitive match")
	}

	// 4. ConversationID present, different agent -> true
	if !HasAgentConflict(t2, "agy") {
		t.Errorf("expected true when agent is claude and active is agy")
	}

	// 5. Empty agent defaults to agy
	t3 := &domain.TaskSession{Agent: "", ConversationID: "conv-2"}
	if HasAgentConflict(t3, "agy") {
		t.Errorf("expected false for default agy vs agy")
	}
	if !HasAgentConflict(t3, "claude") {
		t.Errorf("expected true for default agy vs claude")
	}
}

func TestSwitchActiveAgent(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	st, err := storage.NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite storage: %v", err)
	}
	defer st.Close()
	domain.GlobalTaskManager.InitWithStorage(st)

	config.ProjectState.Lock()
	config.ProjectState.CurrentModel = "gemini-3.1-pro-high"
	config.ProjectState.Unlock()

	// Switch to claude
	msg, err := SwitchActiveAgent("claude")
	if err != nil {
		t.Fatalf("unexpected error switching to claude: %v", err)
	}
	if ActiveAgentName != "claude" {
		t.Fatalf("expected ActiveAgentName to be claude, got %s", ActiveAgentName)
	}
	if !strings.Contains(msg, "claude") {
		t.Errorf("expected message to mention claude: %s", msg)
	}
	config.ProjectState.RLock()
	curModel := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()
	if curModel != "sonnet" {
		t.Errorf("expected model to switch to sonnet for claude, got %s", curModel)
	}
	savedAgent, _ := st.GetSetting(context.Background(), "current_agent")
	if savedAgent != "claude" {
		t.Errorf("expected current_agent in db to be claude, got %s", savedAgent)
	}

	// Switch to agy
	msg, err = SwitchActiveAgent("agy")
	if err != nil {
		t.Fatalf("unexpected error switching to agy: %v", err)
	}
	if ActiveAgentName != "agy" {
		t.Fatalf("expected ActiveAgentName to be agy, got %s", ActiveAgentName)
	}
	savedAgent, _ = st.GetSetting(context.Background(), "current_agent")
	if savedAgent != "agy" {
		t.Errorf("expected current_agent in db to be agy, got %s", savedAgent)
	}

	// Switch to invalid
	_, err = SwitchActiveAgent("unknown_bot")
	if err == nil {
		t.Fatalf("expected error switching to unknown agent")
	}
}

func TestBuildAgentConflictMarkup(t *testing.T) {
	kb := buildAgentConflictMarkup(42, "claude")
	if kb == nil || len(kb.Rows) != 1 || len(kb.Rows[0]) != 2 {
		t.Fatalf("expected 1 row with 2 buttons, got %v", kb)
	}
	btnRestart := kb.Rows[0][0]
	if btnRestart.Action != "task_agent_restart" || btnRestart.Payload != "42" || !strings.Contains(btnRestart.Text, "Начать заново") {
		t.Errorf("unexpected restart button: %+v", btnRestart)
	}
	btnSwitch := kb.Rows[0][1]
	if btnSwitch.Action != "task_agent_switch" || btnSwitch.Payload != "42" || !strings.Contains(btnSwitch.Text, "claude") {
		t.Errorf("unexpected switch button: %+v", btnSwitch)
	}
}

func TestSendAgentConflictDialog(t *testing.T) {
	task := &domain.TaskSession{
		ID:             99,
		Agent:          "claude",
		ConversationID: "conv-claude-99",
	}

	m := mock.New()
	sess := &mock.Session{
		M:      m,
		ChatID: testChatID,
	}

	ActiveAgentName = "agy"
	err := sendAgentConflictDialog(sess, task)
	if err != nil {
		t.Fatalf("sendAgentConflictDialog failed: %v", err)
	}

	last := m.LastSent()
	if last == nil {
		t.Fatalf("expected message to be sent")
	}
	if !strings.Contains(last.Text, "была начата агентом claude") {
		t.Errorf("expected message to mention claude: %s", last.Text)
	}
	if !strings.Contains(last.Text, "conv-claude-99") {
		t.Errorf("expected message to mention session id: %s", last.Text)
	}
	if !strings.Contains(last.Text, "Текущий активный агент бота: <b>agy</b>") {
		t.Errorf("expected message to mention active agent agy: %s", last.Text)
	}
	if last.Opts == nil || last.Opts.Keyboard == nil {
		t.Fatalf("expected markup with buttons")
	}
}

func TestSendAgentConflictDialogWithMessenger(t *testing.T) {
	task := &domain.TaskSession{
		ID:             100,
		Agent:          "agy",
		ConversationID: "conv-agy-100",
	}

	m := mock.New()
	ActiveAgentName = "claude"
	err := sendAgentConflictDialogWithMessenger(m, testChatID, task)
	if err != nil {
		t.Fatalf("sendAgentConflictDialogWithMessenger failed: %v", err)
	}

	last := m.LastSent()
	if last == nil {
		t.Fatalf("expected message to be sent")
	}
	if !strings.Contains(last.Text, "была начата агентом agy") {
		t.Errorf("expected message to mention agy: %s", last.Text)
	}
	if !strings.Contains(last.Text, "conv-agy-100") {
		t.Errorf("expected message to mention session id: %s", last.Text)
	}
	if !strings.Contains(last.Text, "Текущий активный агент бота: <b>claude</b>") {
		t.Errorf("expected message to mention active agent claude: %s", last.Text)
	}
}

func setupTestApp(t *testing.T) *mockTransport {
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "testproj")
	_ = os.MkdirAll(projDir, 0755)

	t.Setenv("TELEGRAM_ADMIN_ID", "12345")
	t.Setenv("PROJECTS_ROOT", tmpDir)
	t.Setenv("QUESTION_TIMEOUT", "5m")
	t.Setenv("BOT_SERVICE_NAME", "bro-bot.service")
	t.Setenv("BOT_DIR", tmpDir)
	t.Setenv("SQLITE_DB_PATH", filepath.Join(tmpDir, "bot.db"))
	t.Setenv("DEFAULT_MODEL", "gemini-3.1-pro-high")

	mt := newMockTransport()
	Start(mt)
	return mt
}

func TestResumeAgentConflictDialog(t *testing.T) {
	mt := setupTestApp(t)

	_, _ = SwitchActiveAgent("agy")

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "model", "claude", "initial prompt", testChatID, false)
	domain.GlobalTaskManager.SetTaskConversationID(task.ID, "session-uuid-1234")
	task.Lock()
	task.Status = domain.TaskStatusPaused
	task.Unlock()

	resumeHandler, ok := mt.commands["resume"]
	if !ok {
		t.Fatalf("resume command handler not found")
	}

	sess := &mock.Session{
		M:       mt.Messenger,
		ChatID:  testChatID,
		ArgsVal: []string{strconv.Itoa(task.ID), "answer text to resume"},
	}

	err := resumeHandler(sess)
	if err != nil {
		t.Fatalf("resume handler failed: %v", err)
	}

	last := mt.LastSent()
	if last == nil {
		t.Fatalf("expected conflict dialog message to be sent")
	}
	if !strings.Contains(last.Text, "была начата агентом claude") {
		t.Errorf("expected message to mention agent claude, got: %s", last.Text)
	}
	if !strings.Contains(last.Text, "session-uuid-1234") {
		t.Errorf("expected message to mention session uuid, got: %s", last.Text)
	}
	if last.Opts == nil || last.Opts.Keyboard == nil {
		t.Fatalf("expected conflict keyboard in dialog")
	}

	// Verify task.CurrentPrompt was preserved
	task.Lock()
	curPrompt := task.CurrentPrompt
	task.Unlock()
	if curPrompt != "answer text to resume" {
		t.Errorf("expected answer to be preserved in CurrentPrompt, got: %s", curPrompt)
	}
}

func TestTaskAgentRestartCallback(t *testing.T) {
	mt := setupTestApp(t)

	_, _ = SwitchActiveAgent("agy")

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "model", "claude", "initial prompt", testChatID, false)
	domain.GlobalTaskManager.SetTaskConversationID(task.ID, "session-uuid-9999")
	task.Lock()
	task.Status = domain.TaskStatusPaused
	task.CurrentPrompt = "new instructions"
	task.Unlock()

	restartCb, ok := mt.callbacks["task_agent_restart"]
	if !ok {
		t.Fatalf("task_agent_restart callback not registered")
	}

	sess := &mock.Session{
		M:      mt.Messenger,
		ChatID: testChatID,
		CB: &ports.CallbackQuery{
			ID:          "cb1",
			Action:      "task_agent_restart",
			Payload:     strconv.Itoa(task.ID),
			MessageText: "⚠️ Задача #1 была начата...",
		},
	}

	err := restartCb(sess)
	if err != nil {
		t.Fatalf("task_agent_restart failed: %v", err)
	}

	task.Lock()
	convID := task.ConversationID
	agent := task.Agent
	task.Unlock()

	if convID != "" {
		t.Errorf("expected ConversationID to be cleared, got: %s", convID)
	}
	if agent != "agy" {
		t.Errorf("expected Agent to be updated to agy, got: %s", agent)
	}
	if len(sess.Edits) == 0 || !strings.Contains(sess.Edits[0], "Начать заново с агентом agy") {
		t.Errorf("expected callback message to be edited with choice, got: %v", sess.Edits)
	}
}

func TestTaskAgentSwitchCallback(t *testing.T) {
	mt := setupTestApp(t)

	_, _ = SwitchActiveAgent("agy")

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "model", "claude", "initial prompt", testChatID, false)
	domain.GlobalTaskManager.SetTaskConversationID(task.ID, "session-uuid-8888")
	task.Lock()
	task.Status = domain.TaskStatusPaused
	task.CurrentPrompt = "continue step"
	task.Unlock()

	switchCb, ok := mt.callbacks["task_agent_switch"]
	if !ok {
		t.Fatalf("task_agent_switch callback not registered")
	}

	sess := &mock.Session{
		M:      mt.Messenger,
		ChatID: testChatID,
		CB: &ports.CallbackQuery{
			ID:          "cb2",
			Action:      "task_agent_switch",
			Payload:     strconv.Itoa(task.ID),
			MessageText: "⚠️ Задача #1 была начата...",
		},
	}

	err := switchCb(sess)
	if err != nil {
		t.Fatalf("task_agent_switch failed: %v", err)
	}

	if ActiveAgentName != "claude" {
		t.Errorf("expected ActiveAgentName to be claude after switch, got: %s", ActiveAgentName)
	}
	if len(sess.Edits) == 0 || !strings.Contains(sess.Edits[0], "Переключиться на claude") {
		t.Errorf("expected callback message to be edited with choice, got: %v", sess.Edits)
	}
}
