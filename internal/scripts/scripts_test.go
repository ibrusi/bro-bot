package scripts

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

func TestCommandRunsExecutableDirectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool")
	writeFile(t, path, "#!/bin/sh\npwd\n", 0o755)

	cmd, err := Command(context.Background(), path)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Path != path || len(cmd.Args) != 1 {
		t.Errorf("исполняемый файл должен запускаться напрямую, получили %v", cmd.Args)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("запуск: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != dir {
		t.Errorf("рабочий каталог = %q, ожидали каталог скрипта %q", got, dir)
	}
}

func TestCommandUsesInterpreterByExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.SH")
	writeFile(t, path, "echo ПРИВЕТ\n", 0o644)

	cmd, err := Command(context.Background(), path)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if want := []string{"bash", path}; !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("Args = %v, want %v", cmd.Args, want)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash недоступен")
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("запуск: %v", err)
	}
	if !strings.Contains(string(out), "ПРИВЕТ") {
		t.Errorf("вывод = %q", out)
	}
}

func TestCommandRejectsNotRunnable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "notes.txt"), "text\n", 0o644)

	for _, path := range []string{filepath.Join(dir, "notes.txt"), dir} {
		if _, err := Command(context.Background(), path); !errors.Is(err, ErrNotRunnable) {
			t.Errorf("Command(%q): ожидали ErrNotRunnable, получили %v", path, err)
		}
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
