package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
)

// TestUseRejectsPathTraversal — "/use ../../.." не должен делать рабочим каталогом
// агента произвольную директорию хоста.
func TestUseRejectsPathTraversal(t *testing.T) {
	mt := setupTestApp(t)

	handler, ok := mt.commands["use"]
	if !ok {
		t.Fatal("команда /use не зарегистрирована")
	}

	config.ProjectState.RLock()
	before := config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	for _, target := range []string{"../../..", "../../../etc", "/etc", "..", "-rf"} {
		if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{target}})); err != nil {
			t.Fatalf("/use %q вернула ошибку: %v", target, err)
		}

		config.ProjectState.RLock()
		after := config.ProjectState.CurrentProject
		config.ProjectState.RUnlock()

		if after != before {
			t.Fatalf("/use %q сменила активный проект на %q", target, after)
		}
	}

	texts := mt.AllTexts()
	if len(texts) == 0 {
		t.Fatal("/use должна была ответить отказом")
	}
	if !strings.Contains(texts[len(texts)-1], "❌") {
		t.Errorf("ожидали отказ, получили: %s", texts[len(texts)-1])
	}
}

// TestUseSwitchesToRealProject — обычный сценарий не сломан.
func TestUseSwitchesToRealProject(t *testing.T) {
	mt := setupTestApp(t)

	handler := mt.commands["use"]
	if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{"testproj"}})); err != nil {
		t.Fatalf("/use testproj вернула ошибку: %v", err)
	}

	config.ProjectState.RLock()
	current := config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	if current != "testproj" {
		t.Errorf("активный проект = %q, ожидали testproj", current)
	}
}

// TestScriptRejectsSiblingDirectory — проверка префикса без разделителя пропускала
// каталог-сосед: при SCRIPTS_DIR=<tmp>/scripts имя "../scripts-evil/payload.sh"
// давало "<tmp>/scripts-evil/payload.sh", и строка проходила HasPrefix.
func TestScriptRejectsSiblingDirectory(t *testing.T) {
	mt := setupTestApp(t)

	root := t.TempDir()
	scriptsDir := filepath.Join(root, "scripts")
	evilDir := filepath.Join(root, "scripts-evil")
	for _, dir := range []string{scriptsDir, evilDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	marker := filepath.Join(root, "executed.txt")
	payload := filepath.Join(evilDir, "payload.sh")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(payload, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	prev := config.ScriptsDir
	config.ScriptsDir = scriptsDir
	t.Cleanup(func() { config.ScriptsDir = prev })

	handler, ok := mt.commands["script"]
	if !ok {
		t.Fatal("команда /script не зарегистрирована")
	}

	for _, name := range []string{"../scripts-evil/payload.sh", "../../etc/passwd", "/bin/sh", "-rf"} {
		if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{name}})); err != nil {
			t.Fatalf("/script %q вернула ошибку: %v", name, err)
		}
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("скрипт из соседнего каталога был выполнен")
	}

	texts := mt.AllTexts()
	if len(texts) == 0 || !strings.Contains(texts[len(texts)-1], "Invalid script name") {
		t.Errorf("ожидали отказ, получили: %v", texts)
	}
}

// TestScriptRunsAllowedScript — обычный сценарий не сломан, включая вложенный путь.
func TestScriptRunsAllowedScript(t *testing.T) {
	mt := setupTestApp(t)

	scriptsDir := filepath.Join(t.TempDir(), "scripts")
	if err := os.MkdirAll(filepath.Join(scriptsDir, "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scriptsDir, "deploy", "hello.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ПРИВЕТ\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	prev := config.ScriptsDir
	config.ScriptsDir = scriptsDir
	t.Cleanup(func() { config.ScriptsDir = prev })

	handler := mt.commands["script"]
	if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{"deploy/hello.sh"}})); err != nil {
		t.Fatalf("/script вернула ошибку: %v", err)
	}

	texts := mt.AllTexts()
	if len(texts) == 0 || !strings.Contains(texts[len(texts)-1], "ПРИВЕТ") {
		t.Errorf("ожидали вывод скрипта, получили: %v", texts)
	}
}
