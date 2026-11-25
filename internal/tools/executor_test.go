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
		"TargetFile":         "test.txt",
		"TargetContent":      "world",
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

func TestGrepSearchFindFilesAndBotToolDefs(t *testing.T) {
	tempDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tempDir, "pkg"), 0755)
	_ = os.MkdirAll(filepath.Join(tempDir, ".git"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "pkg", "main.go"), []byte("package pkg\nfunc Hello() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "pkg", "notes.txt"), []byte("Hello notes\n"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, ".git", "HEAD"), []byte("Hello git\n"), 0644)

	executor := NewExecutor(tempDir)
	ctx := context.Background()

	grepOut, err := executor.Execute(ctx, "grep_search", map[string]interface{}{
		"pattern": "Hello",
		"glob":    "*.go",
	}, true)
	if err != nil {
		t.Fatalf("grep_search failed: %v", err)
	}
	if !strings.Contains(grepOut, "pkg/main.go:2:") || strings.Contains(grepOut, "notes.txt") || strings.Contains(grepOut, ".git") {
		t.Errorf("unexpected grep_search output:\n%s", grepOut)
	}

	findOut, err := executor.Execute(ctx, "find_files", map[string]interface{}{
		"pattern": "*.go",
	}, true)
	if err != nil {
		t.Fatalf("find_files failed: %v", err)
	}
	if !strings.Contains(findOut, "pkg/main.go") || strings.Contains(findOut, "notes.txt") || strings.Contains(findOut, ".git") {
		t.Errorf("unexpected find_files output:\n%s", findOut)
	}

	if !IsBotToolName("send_file") || !IsBotToolName("ask_user") || IsBotToolName("read_file") {
		t.Errorf("unexpected IsBotToolName checks")
	}

	claudeTools := ClaudeToolDefinitionsWithBot(true, true)
	foundSendFile := false
	for _, td := range claudeTools {
		if td["name"] == "send_file" {
			foundSendFile = true
		}
	}
	if !foundSendFile {
		t.Errorf("expected send_file in ClaudeToolDefinitionsWithBot")
	}

	geminiDecls := GeminiToolDeclarationsWithBot(true, true)
	foundAskUser := false
	for _, gtool := range geminiDecls {
		for _, fn := range gtool.FunctionDeclarations {
			if fn.Name == "ask_user" {
				foundAskUser = true
			}
		}
	}
	if !foundAskUser {
		t.Errorf("expected ask_user in GeminiToolDeclarationsWithBot")
	}
}

func TestExecutorExternalReadAndWriteControl(t *testing.T) {
	base := t.TempDir()
	workDir := filepath.Join(base, "repo")
	outsideDir := filepath.Join(base, "outside")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "note.txt")
	if err := os.WriteFile(outsideFile, []byte("external content"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Default executor blocks reading/writing outside workspace.
	strict := NewExecutor(workDir)
	if _, err := strict.Execute(ctx, "read_file", map[string]interface{}{"path": outsideFile}, true); err == nil {
		t.Fatalf("expected strict executor to reject outside read_file")
	}
	if _, err := strict.Execute(ctx, "list_dir", map[string]interface{}{"path": outsideDir}, true); err == nil {
		t.Fatalf("expected strict executor to reject outside list_dir")
	}
	if _, err := strict.Execute(ctx, "write_file", map[string]interface{}{"path": filepath.Join(outsideDir, "new.txt"), "content": "x"}, false); err == nil {
		t.Fatalf("expected strict executor to reject outside write_file")
	}

	// AllowExternalRead permits read_file/list_dir/grep_search/find_files outside workspace while blocking outside write.
	extReadOnly := NewExecutor(workDir)
	extReadOnly.AllowExternalRead = true

	got, err := extReadOnly.Execute(ctx, "read_file", map[string]interface{}{"path": outsideFile}, true)
	if err != nil || got != "external content" {
		t.Fatalf("AllowExternalRead read_file = (%q, %v), want %q", got, err, "external content")
	}
	dirOut, err := extReadOnly.Execute(ctx, "list_dir", map[string]interface{}{"path": outsideDir}, true)
	if err != nil || !strings.Contains(dirOut, "note.txt") {
		t.Fatalf("AllowExternalRead list_dir = (%q, %v), want note.txt", dirOut, err)
	}
	grepOut, err := extReadOnly.Execute(ctx, "grep_search", map[string]interface{}{"pattern": "external", "path": outsideDir}, true)
	if err != nil || !strings.Contains(grepOut, "external content") {
		t.Fatalf("AllowExternalRead grep_search = (%q, %v)", grepOut, err)
	}
	findOut, err := extReadOnly.Execute(ctx, "find_files", map[string]interface{}{"pattern": "*.txt", "path": outsideDir}, true)
	if err != nil || !strings.Contains(findOut, "note.txt") {
		t.Fatalf("AllowExternalRead find_files = (%q, %v)", findOut, err)
	}
	if _, err := extReadOnly.Execute(ctx, "write_file", map[string]interface{}{"path": filepath.Join(outsideDir, "blocked.txt"), "content": "x"}, false); err == nil {
		t.Fatalf("expected AllowExternalRead alone to still reject outside write_file")
	}

	// AllowExternalWrite permits outside writes only when readOnly is false.
	extWrite := NewExecutor(workDir)
	extWrite.AllowExternalRead = true
	extWrite.AllowExternalWrite = true

	targetOut := filepath.Join(outsideDir, "created.csv")
	if _, err := extWrite.Execute(ctx, "write_file", map[string]interface{}{"path": targetOut, "content": "a,b\n1,2\n"}, true); err == nil {
		t.Fatalf("expected readOnly=true to still block write_file")
	}
	if _, err := extWrite.Execute(ctx, "write_file", map[string]interface{}{"path": targetOut, "content": "a,b\n1,2\n"}, false); err != nil {
		t.Fatalf("expected AllowExternalWrite with readOnly=false to write %s: %v", targetOut, err)
	}
	if _, err := extWrite.Execute(ctx, "edit_file", map[string]interface{}{"path": targetOut, "old_string": "1,2", "new_string": "3,4"}, false); err != nil {
		t.Fatalf("expected AllowExternalWrite with readOnly=false to edit %s: %v", targetOut, err)
	}
	edited, err := os.ReadFile(targetOut)
	if err != nil || !strings.Contains(string(edited), "3,4") {
		t.Fatalf("unexpected edited outside file content: %q, err=%v", string(edited), err)
	}
}
