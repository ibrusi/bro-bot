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
