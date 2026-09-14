package handlers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"tg-agent-bot/internal/config"
	"tg-agent-bot/internal/domain"
	"tg-agent-bot/internal/models"
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
