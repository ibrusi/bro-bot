package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeResolvePath(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewExecutor(tempDir)

	// Valid relative path
	p, err := executor.SafeResolvePath("foo/bar.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(tempDir, "foo/bar.go")
	if p != expected {
		t.Errorf("got %q, want %q", p, expected)
	}

	// Valid absolute path within workspace
	p2, err := executor.SafeResolvePath(expected)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p2 != expected {
		t.Errorf("got %q, want %q", p2, expected)
	}

	// Path traversal attempt (relative)
	_, err = executor.SafeResolvePath("../outside.txt")
	if !errors.Is(err, ErrPathEscapesWorkDir) {
		t.Errorf("expected ErrPathEscapesWorkDir, got %v", err)
	}

	// Path traversal attempt (subfolder relative ../..)
	_, err = executor.SafeResolvePath("foo/../../outside.txt")
	if !errors.Is(err, ErrPathEscapesWorkDir) {
		t.Errorf("expected ErrPathEscapesWorkDir, got %v", err)
	}

	// Absolute path outside workspace
	_, err = executor.SafeResolvePath("/etc/passwd")
	if !errors.Is(err, ErrPathEscapesWorkDir) {
		t.Errorf("expected ErrPathEscapesWorkDir, got %v", err)
	}

	// Empty workspace
	emptyExec := NewExecutor("")
	_, err = emptyExec.SafeResolvePath("test.txt")
	if !errors.Is(err, ErrWorkspaceNotSet) {
		t.Errorf("expected ErrWorkspaceNotSet, got %v", err)
	}
}

func TestReadFileAndWriteFile(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewExecutor(tempDir)

	// Write file in subfolder
	res, err := executor.WriteFile("sub/hello.txt", "line 1\nline 2\nline 3\nline 4\n")
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if !strings.Contains(res, "Successfully wrote") {
		t.Errorf("unexpected write response: %s", res)
	}

	// Read entire file
	content, err := executor.ReadFile("sub/hello.txt", 0, 0)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if content != "line 1\nline 2\nline 3\nline 4\n" {
		t.Errorf("content mismatch: %q", content)
	}

	// Read lines 2 to 3
	slice, err := executor.ReadFile("sub/hello.txt", 2, 3)
	if err != nil {
		t.Fatalf("ReadFile slice failed: %v", err)
	}
	if !strings.Contains(slice, "2 | line 2") || !strings.Contains(slice, "3 | line 3") {
		t.Errorf("slice missing lines: %s", slice)
	}
	if strings.Contains(slice, "1 | line 1") || strings.Contains(slice, "4 | line 4") {
		t.Errorf("slice contains unexpected lines: %s", slice)
	}

	// Read non-existent file
	_, err = executor.ReadFile("nonexistent.txt", 0, 0)
	if !errors.Is(err, ErrFileNotFound) {
		t.Errorf("expected ErrFileNotFound, got %v", err)
	}

	// Binary file detection
	binPath := filepath.Join(tempDir, "bin.dat")
	_ = os.WriteFile(binPath, []byte{0x00, 0x01, 0x02, 0xff}, 0644)
	binRes, err := executor.ReadFile("bin.dat", 0, 0)
	if err != nil {
		t.Fatalf("reading binary file failed: %v", err)
	}
	if !strings.Contains(binRes, "Binary file") {
		t.Errorf("expected binary notice, got %s", binRes)
	}
}

func TestEditFile(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewExecutor(tempDir)

	_, err := executor.WriteFile("code.go", "package main\n\nfunc main() {\n\tprintln(\"old\")\n}\n")
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Successful edit
	res, err := executor.EditFile("code.go", "println(\"old\")", "println(\"new\")")
	if err != nil {
		t.Fatalf("EditFile failed: %v", err)
	}
	if !strings.Contains(res, "Successfully updated") {
		t.Errorf("unexpected edit result: %s", res)
	}

	content, _ := executor.ReadFile("code.go", 0, 0)
	if !strings.Contains(content, "println(\"new\")") {
		t.Errorf("file was not updated: %s", content)
	}

	// Target not found
	_, err = executor.EditFile("code.go", "missing_string", "replacement")
	if !errors.Is(err, ErrTargetStringNotFound) {
		t.Errorf("expected ErrTargetStringNotFound, got %v", err)
	}

	// Empty target
	_, err = executor.EditFile("code.go", "", "replacement")
	if err == nil {
		t.Errorf("expected error for empty old_string")
	}
}

func TestListDir(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewExecutor(tempDir)

	_ = os.Mkdir(filepath.Join(tempDir, "dir_a"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "file_b.txt"), []byte("abc"), 0644)
	_ = os.Mkdir(filepath.Join(tempDir, ".git"), 0755)

	out, err := executor.ListDir(".")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}

	if !strings.Contains(out, "[DIR]  dir_a/") {
		t.Errorf("missing dir_a in listing: %s", out)
	}
	if !strings.Contains(out, "[FILE] file_b.txt (3 bytes)") {
		t.Errorf("missing file_b.txt in listing: %s", out)
	}
	if !strings.Contains(out, "[DIR]  .git/ (git repository)") {
		t.Errorf("missing git notice in listing: %s", out)
	}
}

func TestRunCommand(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewExecutor(tempDir)

	out, err := executor.RunCommand(context.Background(), "echo 'test command execution'")
	if err != nil {
		t.Fatalf("RunCommand failed: %v", err)
	}
	if !strings.Contains(out, "test command execution") {
		t.Errorf("unexpected output: %s", out)
	}

	// Failing command
	_, err = executor.RunCommand(context.Background(), "exit 42")
	if err == nil {
		t.Errorf("expected error for non-zero exit code")
	}
}

func TestExecuteDispatchAndReadOnly(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewExecutor(tempDir)
	ctx := context.Background()

	// In read-only mode, write should fail
	_, err := executor.Execute(ctx, "write_file", map[string]interface{}{
		"path":    "test.txt",
		"content": "hello",
	}, true)
	if !errors.Is(err, ErrReadOnly) {
		t.Errorf("expected ErrReadOnly for write_file in read-only mode, got %v", err)
	}

	// In read-only mode, edit should fail
	_, err = executor.Execute(ctx, "edit_file", map[string]interface{}{
		"path":       "test.txt",
		"old_string": "a",
		"new_string": "b",
	}, true)
	if !errors.Is(err, ErrReadOnly) {
		t.Errorf("expected ErrReadOnly for edit_file in read-only mode, got %v", err)
	}

	// In read-only mode, run_command should fail
	_, err = executor.Execute(ctx, "run_command", map[string]interface{}{
		"command": "ls",
	}, true)
	if !errors.Is(err, ErrReadOnly) {
		t.Errorf("expected ErrReadOnly for run_command in read-only mode, got %v", err)
	}

	// Write in non-read-only mode should succeed
	writeRes, err := executor.Execute(ctx, "write_file", map[string]interface{}{
		"path":    "test.txt",
		"content": "hello world",
	}, false)
	if err != nil {
		t.Fatalf("write_file in task mode failed: %v", err)
	}
	if !strings.Contains(writeRes, "Successfully wrote") {
		t.Errorf("unexpected write result: %s", writeRes)
	}

	// Read in read-only mode should succeed
	readRes, err := executor.Execute(ctx, "read_file", map[string]interface{}{
		"path": "test.txt",
	}, true)
	if err != nil {
		t.Fatalf("read_file failed: %v", err)
	}
	if readRes != "hello world" {
		t.Errorf("read mismatch: %q", readRes)
	}

	// Aliases test (e.g. view_file, replace_file_content)
	editRes, err := executor.Execute(ctx, "replace_file_content", map[string]interface{}{
		"TargetFile":     "test.txt",
		"TargetContent":  "world",
		"ReplacementContent": "bro",
	}, false)
	if err != nil {
		t.Fatalf("replace_file_content alias failed: %v", err)
	}
	if !strings.Contains(editRes, "Successfully updated") {
		t.Errorf("unexpected edit result: %s", editRes)
	}

	viewRes, err := executor.Execute(ctx, "view_file", map[string]interface{}{
		"AbsolutePath": "test.txt",
	}, true)
	if err != nil {
		t.Fatalf("view_file alias failed: %v", err)
	}
	if viewRes != "hello bro" {
		t.Errorf("alias read mismatch: %q", viewRes)
	}

	// Unknown tool
	_, err = executor.Execute(ctx, "unknown_tool", nil, false)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected unknown tool error, got %v", err)
	}
}
