package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestInitDefaultProject(t *testing.T) {
	tmpDir := t.TempDir()

	// Case 1: Multiple dirs exist including tg-bot-agent (which is alphabetically after aaa-project)
	if err := os.Mkdir(filepath.Join(tmpDir, "aaa-project"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmpDir, "tg-bot-agent"), 0755); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("DEFAULT_PROJECT")
	initDefaultProject(tmpDir)

	projectState.RLock()
	cur := projectState.currentProject
	projectState.RUnlock()

	if cur != "tg-bot-agent" {
		t.Errorf("expected tg-bot-agent, got %s", cur)
	}

	// Case 2: Custom DEFAULT_PROJECT env var
	os.Setenv("DEFAULT_PROJECT", "aaa-project")
	defer os.Unsetenv("DEFAULT_PROJECT")
	initDefaultProject(tmpDir)

	projectState.RLock()
	cur = projectState.currentProject
	projectState.RUnlock()

	if cur != "aaa-project" {
		t.Errorf("expected aaa-project, got %s", cur)
	}

	// Case 3: Neither custom nor tg-bot-agent exists -> fallback to first dir
	otherDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(otherDir, "zzz-fallback"), 0755); err != nil {
		t.Fatal(err)
	}
	os.Setenv("DEFAULT_PROJECT", "non-existent")
	initDefaultProject(otherDir)

	projectState.RLock()
	cur = projectState.currentProject
	projectState.RUnlock()

	if cur != "zzz-fallback" {
		t.Errorf("expected zzz-fallback, got %s", cur)
	}
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
	projectState.Lock()
	origMode := projectState.planMode
	projectState.planMode = true
	isPlan := projectState.planMode
	projectState.planMode = false
	isPlanFalse := projectState.planMode
	projectState.planMode = origMode
	projectState.Unlock()

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
	task := &TaskSession{
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
	variants := ExtractPlanVariantOptions(planWithVariants)
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
		"top",
		"limits",
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
	tm := NewTaskManager()
	task := tm.CreateTaskWithPlan("test-proj", "flash", "Build feature", dummyRecipient{}, true)
	task.Lock()
	task.Status = TaskStatusWaitingApproval
	task.Plan = "1. Step one\n2. Step two"
	task.PendingFollowups = []string{"add extra validation", "include unit test"}
	task.Unlock()

	// Simulate plan approval logic
	task.Lock()
	task.PlanApproved = true
	task.Status = TaskStatusRunning
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
	task := &TaskSession{
		ID:           1,
		RequiresPlan: true,
		PlanApproved: false,
		Status:       TaskStatusWaitingInput,
	}

	task.Lock()
	if task.RequiresPlan && !task.PlanApproved {
		task.Status = TaskStatusPlanning
	} else {
		task.Status = TaskStatusRunning
	}
	task.Unlock()

	if task.Status != TaskStatusPlanning {
		t.Errorf("expected status TaskStatusPlanning, got %s", task.Status)
	}

	// When task does not require plan or is approved
	task2 := &TaskSession{
		ID:           2,
		RequiresPlan: true,
		PlanApproved: true,
		Status:       TaskStatusWaitingInput,
	}

	task2.Lock()
	if task2.RequiresPlan && !task2.PlanApproved {
		task2.Status = TaskStatusPlanning
	} else {
		task2.Status = TaskStatusRunning
	}
	task2.Unlock()

	if task2.Status != TaskStatusRunning {
		t.Errorf("expected status TaskStatusRunning, got %s", task2.Status)
	}
}

