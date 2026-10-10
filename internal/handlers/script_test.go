package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
)

// useScriptsDir подменяет SCRIPTS_DIR на время теста.
func useScriptsDir(t *testing.T, dir string) {
	t.Helper()
	prev := config.ScriptsDir
	config.ScriptsDir = dir
	t.Cleanup(func() { config.ScriptsDir = prev })
}

func lastText(t *testing.T, mt *mockTransport) string {
	t.Helper()
	texts := mt.AllTexts()
	if len(texts) == 0 {
		t.Fatal("бот ничего не ответил")
	}
	return texts[len(texts)-1]
}

// TestScriptWithoutNameListsScripts — /script без имени показывает, что можно запустить.
func TestScriptWithoutNameListsScripts(t *testing.T) {
	mt := setupTestApp(t)

	dir := t.TempDir()
	for name, mode := range map[string]os.FileMode{
		"agy-patch-cli.sh": 0o755,
		"report.py":        0o644,
		"notes.txt":        0o644,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("echo ok\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	useScriptsDir(t, dir)

	if err := mt.commands["script"](adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/script вернула ошибку: %v", err)
	}

	got := lastText(t, mt)
	for _, want := range []string{"agy-patch-cli.sh", "report.py"} {
		if !strings.Contains(got, want) {
			t.Errorf("в списке нет %s: %s", want, got)
		}
	}
	if strings.Contains(got, "notes.txt") {
		t.Errorf("в списке оказался незапускаемый файл: %s", got)
	}
}

// TestScriptListEmptyDir — пустой или ещё не созданный каталог не считается ошибкой.
func TestScriptListEmptyDir(t *testing.T) {
	mt := setupTestApp(t)
	useScriptsDir(t, filepath.Join(t.TempDir(), "absent"))

	if err := mt.commands["script"](adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/script вернула ошибку: %v", err)
	}
	if got := lastText(t, mt); !strings.Contains(got, "No scripts") {
		t.Errorf("ожидали сообщение о пустом каталоге, получили: %s", got)
	}
}

// TestScriptRunsNonExecutableByExtension — .sh без бита исполнения запускается через bash.
func TestScriptRunsNonExecutableByExtension(t *testing.T) {
	mt := setupTestApp(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.sh"), []byte("echo ПРИВЕТ\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useScriptsDir(t, dir)

	if err := mt.commands["script"](adminSession(mt, &mock.Session{ArgsVal: []string{"hello.sh"}})); err != nil {
		t.Fatalf("/script вернула ошибку: %v", err)
	}
	if got := lastText(t, mt); !strings.Contains(got, "ПРИВЕТ") {
		t.Errorf("ожидали вывод скрипта, получили: %s", got)
	}
}

// TestScriptRejectsNotRunnable — файл без бита исполнения и с неизвестным
// расширением не запускается, а пользователь узнаёт, как это исправить.
func TestScriptRejectsNotRunnable(t *testing.T) {
	mt := setupTestApp(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useScriptsDir(t, dir)

	if err := mt.commands["script"](adminSession(mt, &mock.Session{ArgsVal: []string{"notes.txt"}})); err != nil {
		t.Fatalf("/script вернула ошибку: %v", err)
	}
	if got := lastText(t, mt); !strings.Contains(got, "chmod +x") {
		t.Errorf("ожидали подсказку про chmod +x, получили: %s", got)
	}
}
