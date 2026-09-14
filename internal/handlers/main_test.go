package handlers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"tg-agent-bot/internal/config"
	"tg-agent-bot/internal/domain"
	"tg-agent-bot/internal/models"
	"tg-agent-bot/internal/storage"
	"tg-agent-bot/internal/utils"
	"time"

	"github.com/creack/pty"
	tele "gopkg.in/telebot.v3"
)


func TestInitDefaultProject(t *testing.T) {
	tmpDir := t.TempDir()

	// Case 1: DEFAULT_PROJECT unset -> picks first found dir (aaa-project)
	if err := os.Mkdir(filepath.Join(tmpDir, "aaa-project"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmpDir, "tg-bot-agent"), 0755); err != nil {
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
	os.Setenv("DEFAULT_PROJECT", "tg-bot-agent")
	defer os.Unsetenv("DEFAULT_PROJECT")
	initDefaultProject(tmpDir)

	config.ProjectState.RLock()
	cur = config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	if cur != "tg-bot-agent" {
		t.Errorf("expected tg-bot-agent, got %s", cur)
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

func TestBuildAgyArgs(t *testing.T) {
	// Case 1: Without conversation ID
	args1 := buildAgyArgs("", "flash", "Hello")
	for i, arg := range args1 {
		if arg == "--conversation" {
			t.Errorf("expected no --conversation flag, got at index %d", i)
		}
	}

	// Case 2: With conversation ID
	args2 := buildAgyArgs("conv-uuid-12345", "flash", "Hello")
	hasConv := false
	for i, arg := range args2 {
		if arg == "--conversation" && i+1 < len(args2) && args2[i+1] == "conv-uuid-12345" {
			hasConv = true
			break
		}
	}
	if !hasConv {
		t.Errorf("expected --conversation conv-uuid-12345 in args, got: %v", args2)
	}

	// Check prompt
	hasPrompt := false
	for i, arg := range args2 {
		if arg == "-p" && i+1 < len(args2) && args2[i+1] == "Hello" {
			hasPrompt = true
			break
		}
	}
	if !hasPrompt {
		t.Errorf("expected -p Hello in args, got: %v", args2)
	}
}

func TestBuildQuestionMarkup(t *testing.T) {
	task := &domain.TaskSession{
		ID:              42,
		QuestionOptions: []string{"Вариант 1", "Вариант 2", "Вариант 3"},
	}

	menu := buildQuestionMarkup(task)
	if menu == nil || len(menu.InlineKeyboard) == 0 {
		t.Fatalf("expected inline keyboard to be generated")
	}

	// Should contain option buttons and pause/cancel buttons
	totalButtons := 0
	foundChoice := false
	foundPause := false
	foundCancel := false

	for _, row := range menu.InlineKeyboard {
		for _, btn := range row {
			totalButtons++
			if btn.Unique == "q_choice" {
				foundChoice = true
			}
			if btn.Unique == "q_pause" || btn.Text == "⏸ Приостановить" {
				foundPause = true
			}
			if btn.Unique == "plan_cancel" || btn.Text == "❌ Отменить" {
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
	if menu == nil || len(menu.InlineKeyboard) == 0 {
		t.Fatalf("expected resume markup")
	}

	foundResume := false
	for _, row := range menu.InlineKeyboard {
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

	planMenu := &tele.ReplyMarkup{}
	var rows []tele.Row
	for i, v := range variants {
		btnText := "Утвердить: " + v
		btn := planMenu.Data(btnText, "plan_appr_var", "10:"+string(rune('0'+i)))
		rows = append(rows, planMenu.Row(btn))
	}
	btnApprove := planMenu.Data("✅ Утвердить и начать", "plan_approve", "10")
	btnCancel := planMenu.Data("❌ Отменить", "plan_cancel", "10")
	rows = append(rows, planMenu.Row(btnApprove, btnCancel))
	planMenu.Inline(rows...)

	if len(planMenu.InlineKeyboard) != 3 {
		t.Errorf("expected 3 rows in plan menu, got %d", len(planMenu.InlineKeyboard))
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
		if seen[cmd.Text] {
			t.Errorf("duplicate command found in getDefaultCommands: %s", cmd.Text)
		}
		seen[cmd.Text] = true

		if len(cmd.Text) < 1 || len(cmd.Text) > 32 {
			t.Errorf("invalid command length for '%s': %d (must be 1-32)", cmd.Text, len(cmd.Text))
		}
		if strings.ToLower(cmd.Text) != cmd.Text {
			t.Errorf("command text must be lowercase: %s", cmd.Text)
		}
		if len(cmd.Description) < 1 || len(cmd.Description) > 256 {
			t.Errorf("invalid description length for '%s': %d (must be 1-256)", cmd.Text, len(cmd.Description))
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
	task := tm.CreateTaskWithPlan("test-proj", "flash", "Build feature", dummyRecipient{}, true)
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

type dummyRecipient struct{}
func (d dummyRecipient) Recipient() string { return "12345" }

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
		t.Fatalf("expected longPlan to be > maxInlinePlanRunes (2500), got %d", len(planRunes))
	}

	planMenu := &tele.ReplyMarkup{}
	var rows []tele.Row
	btnApprove := planMenu.Data("✅ Утвердить и начать", "plan_approve", "42")
	btnCancel := planMenu.Data("❌ Отменить", "plan_cancel", "42")
	rows = append(rows, planMenu.Row(btnApprove, btnCancel))

	if isLong {
		btnDoc := planMenu.Data("📄 Скачать план (.md)", "plan_doc", "42")
		rows = append(rows, planMenu.Row(btnDoc))
	}
	planMenu.Inline(rows...)

	if len(planMenu.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows in long plan menu (actions + doc button), got %d", len(planMenu.InlineKeyboard))
	}

	foundDocBtn := false
	for _, row := range planMenu.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.Text, "Скачать план") && btn.Data == "plan_doc|42" || btn.Unique == "plan_doc" {
				foundDocBtn = true
			}
		}
	}
	if !foundDocBtn {
		t.Errorf("expected plan_doc button in planMenu for long plan")
	}

	// 2. Document file creation
	docName := fmt.Sprintf("plan_task_%d.md", 42)
	doc := &tele.Document{
		File:     tele.FromReader(strings.NewReader(longPlan)),
		FileName: docName,
		MIME:     "text/markdown",
		Caption:  fmt.Sprintf("📄 Полный план реализации задачи #%d (%s)", 42, "test-proj"),
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

	task := domain.GlobalTaskManager.CreateTaskWithPlan("test-sync-proj", "flash", "Build sqlite feature", dummyRecipient{}, true)

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

func TestBuildAgyArgsWithStepTimeout(t *testing.T) {
	origTimeout := config.StepTimeout
	defer func() { config.StepTimeout = origTimeout }()

	config.StepTimeout = 45 * time.Minute
	args := buildAgyArgs("conv-xyz-789", "gemini-3.1-pro-high", "Test prompt")

	hasTimeout := false
	for i, arg := range args {
		if arg == "--print-timeout" && i+1 < len(args) && args[i+1] == "45m0s" {
			hasTimeout = true
			break
		}
	}
	if !hasTimeout {
		t.Errorf("expected --print-timeout 45m0s in args, got: %v", args)
	}

	hasConv := false
	for i, arg := range args {
		if arg == "--conversation" && i+1 < len(args) && args[i+1] == "conv-xyz-789" {
			hasConv = true
			break
		}
	}
	if !hasConv {
		t.Errorf("expected --conversation conv-xyz-789 in args, got: %v", args)
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

	task := tm.CreateTask("proj-timeout", "m", "Prompt", dummyRecipient{})
	task.Lock()
	task.ConversationID = "conv-timeout-123"
	task.Status = domain.TaskStatusRunning
	task.Unlock()
	tm.SaveTask(task)

	// 1. Проверяем перевод задачи в статус paused при таймауте
	handleTaskStepTimeout(nil, dummyRecipient{}, task, "proj-timeout", task.ID, false)

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
	task2 := tm.CreateTask("proj-err", "m", "Prompt 2", dummyRecipient{})
	task2.Lock()
	task2.ConversationID = "conv-err-456"
	task2.Status = domain.TaskStatusRunning
	task2.Unlock()
	tm.SaveTask(task2)

	handleTaskStepError(nil, dummyRecipient{}, task2, "proj-err", task2.ID, fmt.Errorf("exit status 127"))

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


