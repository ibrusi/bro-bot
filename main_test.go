package main

import (
	"os"
	"path/filepath"
	"testing"
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
