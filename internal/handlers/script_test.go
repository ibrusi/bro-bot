package handlers

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/ports"
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
		"agy-patch.sh": 0o755,
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
	for _, want := range []string{"agy-patch.sh", "report.py"} {
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
	waitFor(t, "вывод скрипта", func() bool { return strings.Contains(lastText(t, mt), "ПРИВЕТ") })
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

// useFastScriptProgress ускоряет обновление сообщения запуска на время теста.
func useFastScriptProgress(t *testing.T) {
	t.Helper()
	prev := scriptProgressInterval
	scriptProgressInterval = 20 * time.Millisecond
	t.Cleanup(func() { scriptProgressInterval = prev })
}

// scriptEdits возвращает правки сообщения запуска; final — без клавиатуры.
func scriptEdits(mt *mockTransport) (progress, final []string) {
	for _, e := range mt.EditedMessages() {
		if e.Opts != nil && e.Opts.Keyboard != nil {
			progress = append(progress, e.Text)
		} else {
			final = append(final, e.Text)
		}
	}
	return progress, final
}

// stopButton достаёт кнопку остановки из сообщения о запуске скрипта.
func stopButton(t *testing.T, mt *mockTransport) ports.Button {
	t.Helper()
	sent := mt.LastSent()
	if sent == nil || sent.Opts == nil || sent.Opts.Keyboard == nil {
		t.Fatalf("сообщение о запуске без кнопки: %+v", sent)
	}
	button := sent.Opts.Keyboard.Rows[0][0]
	if button.Action != "script_stop" {
		t.Fatalf("ожидали кнопку script_stop, получили %+v", button)
	}
	return button
}

// TestScriptPassesArgsAndReportsResult — аргументы доходят до скрипта, а итог
// заменяет сообщение о запуске и убирает кнопку остановки.
func TestScriptPassesArgsAndReportsResult(t *testing.T) {
	mt := setupTestApp(t)

	dir := t.TempDir()
	script := "#!/bin/sh\necho \"args: $1|$2\"\n"
	if err := os.WriteFile(filepath.Join(dir, "echo.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	useScriptsDir(t, dir)

	session := adminSession(mt, &mock.Session{ArgsVal: []string{"echo.sh", "patch", "<cli>"}})
	if err := mt.commands["script"](session); err != nil {
		t.Fatalf("/script вернула ошибку: %v", err)
	}
	stopButton(t, mt)

	waitFor(t, "итог скрипта", func() bool {
		_, final := scriptEdits(mt)
		return len(final) > 0
	})
	_, final := scriptEdits(mt)
	got := final[len(final)-1]
	if !strings.Contains(got, "args: patch|&lt;cli&gt;") {
		t.Errorf("аргументы не дошли или вывод не экранирован: %s", got)
	}
	if !strings.Contains(got, "finished") {
		t.Errorf("ожидали сообщение о завершении: %s", got)
	}
}

// TestScriptProgressUpdatesOutput — пока скрипт работает, сообщение обновляется
// свежим выводом и сохраняет кнопку остановки.
func TestScriptProgressUpdatesOutput(t *testing.T) {
	mt := setupTestApp(t)
	useFastScriptProgress(t)

	dir := t.TempDir()
	script := "#!/bin/sh\necho first\nsleep 0.3\necho second\nsleep 0.3\n"
	if err := os.WriteFile(filepath.Join(dir, "slow.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	useScriptsDir(t, dir)

	if err := mt.commands["script"](adminSession(mt, &mock.Session{ArgsVal: []string{"slow.sh"}})); err != nil {
		t.Fatalf("/script вернула ошибку: %v", err)
	}

	waitFor(t, "промежуточный вывод", func() bool {
		progress, _ := scriptEdits(mt)
		for _, text := range progress {
			if strings.Contains(text, "first") && !strings.Contains(text, "second") {
				return true
			}
		}
		return false
	})
	waitFor(t, "итог скрипта", func() bool {
		_, final := scriptEdits(mt)
		return len(final) > 0 && strings.Contains(final[len(final)-1], "second")
	})
}

// TestScriptStopButton — кнопка останавливает скрипт, повторное нажатие сообщает,
// что он уже завершён.
func TestScriptStopButton(t *testing.T) {
	mt := setupTestApp(t)

	dir := t.TempDir()
	script := "#!/bin/sh\necho started\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "long.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	useScriptsDir(t, dir)

	if err := mt.commands["script"](adminSession(mt, &mock.Session{ArgsVal: []string{"long.sh"}})); err != nil {
		t.Fatalf("/script вернула ошибку: %v", err)
	}
	button := stopButton(t, mt)

	press := func() *mock.Session {
		cb := &mock.Session{CB: &ports.CallbackQuery{ID: "cb", Action: button.Action, Payload: button.Payload}}
		if err := mt.callbacks["script_stop"](adminSession(mt, cb)); err != nil {
			t.Fatalf("кнопка остановки вернула ошибку: %v", err)
		}
		return cb
	}

	if cb := press(); len(cb.Responses) == 0 || !strings.Contains(cb.Responses[0], "Stopping") {
		t.Errorf("ожидали уведомление об остановке, получили %v", cb.Responses)
	}
	waitFor(t, "сообщение об остановке", func() bool {
		_, final := scriptEdits(mt)
		return len(final) > 0 && strings.Contains(final[len(final)-1], "stopped")
	})

	if cb := press(); len(cb.Responses) == 0 || !strings.Contains(cb.Responses[0], "already finished") {
		t.Errorf("ожидали уведомление о завершённом скрипте, получили %v", cb.Responses)
	}
}

// TestScriptStopButtonBadPayload — мусор в payload не роняет обработчик.
func TestScriptStopButtonBadPayload(t *testing.T) {
	mt := setupTestApp(t)
	for _, payload := range []string{"", "abc", strconv.Itoa(1 << 30)} {
		cb := &mock.Session{CB: &ports.CallbackQuery{ID: "cb", Action: "script_stop", Payload: payload}}
		if err := mt.callbacks["script_stop"](adminSession(mt, cb)); err != nil {
			t.Fatalf("payload %q: %v", payload, err)
		}
	}
}
