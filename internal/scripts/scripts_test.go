package scripts

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestListReturnsOnlyRunnableScripts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "deploy.sh"), "echo ok\n", 0o644)
	writeFile(t, filepath.Join(dir, "report.py"), "print('ok')\n", 0o644)
	writeFile(t, filepath.Join(dir, "tool"), "#!/bin/sh\necho ok\n", 0o755)
	writeFile(t, filepath.Join(dir, "nested", "hello.sh"), "echo ok\n", 0o644)
	writeFile(t, filepath.Join(dir, "README.md"), "docs\n", 0o644)
	writeFile(t, filepath.Join(dir, ".hidden.sh"), "echo ok\n", 0o755)
	writeFile(t, filepath.Join(dir, ".git", "hook.sh"), "echo ok\n", 0o755)
	writeFile(t, filepath.Join(dir, "with space.sh"), "echo ok\n", 0o755)

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"deploy.sh", "nested/hello.sh", "report.py", "tool"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestListMissingDir(t *testing.T) {
	if _, err := List(filepath.Join(t.TempDir(), "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ожидали os.ErrNotExist, получили %v", err)
	}
}

func TestPrepareRunsExecutableDirectlyWithArgs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool")
	writeFile(t, path, "#!/bin/sh\npwd\necho \"$1|$2\"\n", 0o755)

	script, err := Prepare(path, []string{"a b", "--flag"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if want := []string{path, "a b", "--flag"}; !reflect.DeepEqual(script.cmd.Args, want) {
		t.Errorf("Args = %v, want %v", script.cmd.Args, want)
	}

	var out bytes.Buffer
	if err := script.Run(context.Background(), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := dir + "\na b|--flag\n"; out.String() != want {
		t.Errorf("вывод = %q, want %q (рабочий каталог и аргументы)", out.String(), want)
	}
}

func TestPrepareUsesInterpreterByExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.SH")
	writeFile(t, path, "echo \"ПРИВЕТ $1\" >&2\n", 0o644)

	script, err := Prepare(path, []string{"мир"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if want := []string{"bash", path, "мир"}; !reflect.DeepEqual(script.cmd.Args, want) {
		t.Errorf("Args = %v, want %v", script.cmd.Args, want)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash недоступен")
	}
	var out bytes.Buffer
	if err := script.Run(context.Background(), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "ПРИВЕТ мир") {
		t.Errorf("stderr не попал в вывод: %q", out.String())
	}
}

func TestPrepareRejectsNotRunnable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "notes.txt"), "text\n", 0o644)

	for _, path := range []string{filepath.Join(dir, "notes.txt"), dir} {
		if _, err := Prepare(path, nil); !errors.Is(err, ErrNotRunnable) {
			t.Errorf("Prepare(%q): ожидали ErrNotRunnable, получили %v", path, err)
		}
	}
}

func TestRunReturnsExitError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fail.sh")
	writeFile(t, path, "#!/bin/sh\necho boom\nexit 3\n", 0o755)

	script, err := Prepare(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var exitErr *exec.ExitError
	if err := script.Run(context.Background(), &out); !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Errorf("ожидали код выхода 3, получили %v", err)
	}
	if !strings.Contains(out.String(), "boom") {
		t.Errorf("вывод = %q", out.String())
	}
}

// TestRunCancelStopsWholeGroup — отмена останавливает и сам скрипт, и его детей.
func TestRunCancelStopsWholeGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "survived")
	path := filepath.Join(dir, "long.sh")
	// Дочерний процесс создал бы marker, если бы пережил остановку скрипта.
	writeFile(t, path, "#!/bin/sh\n(sleep 1; touch "+marker+") &\necho started\nsleep 30\n", 0o755)

	script, err := Prepare(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := NewTailBuffer(1024)

	result := make(chan error, 1)
	go func() { result <- script.Run(ctx, out) }()

	deadline := time.Now().Add(3 * time.Second)
	for got, _ := out.Snapshot(); !strings.Contains(got, "started"); got, _ = out.Snapshot() {
		if time.Now().After(deadline) {
			t.Fatal("скрипт не запустился")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run после отмены = %v, ожидали context.Canceled", err)
		}
	case <-time.After(killGrace + 3*time.Second):
		t.Fatal("Run не завершился после отмены")
	}

	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("дочерний процесс пережил остановку скрипта")
	}
}

func TestExtensionsSorted(t *testing.T) {
	exts := Extensions()
	if len(exts) != len(interpreters) {
		t.Fatalf("Extensions вернула %d из %d", len(exts), len(interpreters))
	}
	for i := 1; i < len(exts); i++ {
		if exts[i-1] >= exts[i] {
			t.Fatalf("список не отсортирован: %v", exts)
		}
	}
}
