package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
)

// TestUseRejectsPathTraversal — "/project ../../.." не должен делать рабочим каталогом
// агента произвольную директорию хоста.
func TestUseRejectsPathTraversal(t *testing.T) {
	mt := setupTestApp(t)

	if _, ok := mt.commands["use"]; ok {
		t.Fatal("команда /use не должна быть зарегистрирована после объединения в /project")
	}
	if _, ok := mt.commands["projects"]; ok {
		t.Fatal("команда /projects не должна быть зарегистрирована после объединения в /project")
	}

	handler, ok := mt.commands["project"]
	if !ok {
		t.Fatal("команда /project не зарегистрирована")
	}

	config.ProjectState.RLock()
	before := config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	for _, target := range []string{"../../..", "../../../etc", "/etc", "..", "-rf"} {
		if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{target}})); err != nil {
			t.Fatalf("/project %q вернула ошибку: %v", target, err)
		}

		config.ProjectState.RLock()
		after := config.ProjectState.CurrentProject
		config.ProjectState.RUnlock()

		if after != before {
			t.Fatalf("/project %q сменила активный проект на %q", target, after)
		}
	}

	texts := mt.AllTexts()
	if len(texts) == 0 {
		t.Fatal("/project должна была ответить отказом")
	}
	if !strings.Contains(texts[len(texts)-1], "❌") {
		t.Errorf("ожидали отказ, получили: %s", texts[len(texts)-1])
	}
}

// TestUseSwitchesToRealProject — обычный сценарий переключения проекта не сломан.
func TestUseSwitchesToRealProject(t *testing.T) {
	mt := setupTestApp(t)

	handler := mt.commands["project"]
	if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{"testproj"}})); err != nil {
		t.Fatalf("/project testproj вернула ошибку: %v", err)
	}

	config.ProjectState.RLock()
	current := config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	if current != "testproj" {
		t.Errorf("активный проект = %q, ожидали testproj", current)
	}
}

// TestProjectListsProjectsWithoutArgs — "/project" без аргументов показывает список доступных проектов.
func TestProjectListsProjectsWithoutArgs(t *testing.T) {
	mt := setupTestApp(t)

	if err := os.MkdirAll(filepath.Join(config.ProjectsRoot, "testproj", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(config.ProjectsRoot, "otherproj", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	handler, ok := mt.commands["project"]
	if !ok {
		t.Fatal("команда /project не зарегистрирована")
	}

	if err := handler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/project без аргументов вернула ошибку: %v", err)
	}

	texts := mt.AllTexts()
	if len(texts) == 0 {
		t.Fatal("/project без аргументов должна была отправить список проектов")
	}
	last := texts[len(texts)-1]
	if !strings.Contains(last, "testproj") {
		t.Errorf("ожидали активный проект testproj в списке, получили: %s", last)
	}
	if !strings.Contains(last, "/project otherproj") {
		t.Errorf("ожидали подсказку /project otherproj в списке, получили: %s", last)
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

	// Скрипт работает в фоне: итог приходит правкой сообщения о запуске.
	waitFor(t, "вывод скрипта", func() bool {
		texts := mt.AllTexts()
		return len(texts) > 0 && strings.Contains(texts[len(texts)-1], "ПРИВЕТ")
	})
}
