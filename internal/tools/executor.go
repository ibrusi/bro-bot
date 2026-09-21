package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// maxReadFileBytes — потолок размера файла при чтении (1 МБ).
	maxReadFileBytes = 1024 * 1024
	// maxOutputBytes — потолок вывода команды или содержимого файла для ответа модели (64 КБ).
	maxOutputBytes = 64 * 1024
	// defaultCommandTimeout — таймаут выполнения консольной команды.
	defaultCommandTimeout = 60 * time.Second
)

// Common error definitions for tools.
var (
	ErrReadOnly            = errors.New("operation is not permitted in read-only mode")
	ErrWorkspaceNotSet     = errors.New("workspace directory is not specified")
	ErrPathEscapesWorkDir  = errors.New("path escapes workspace directory")
	ErrFileNotFound        = errors.New("file not found")
	ErrTargetStringNotFound = errors.New("target string not found in file")
)

// Executor выполняет файловые операции и команды внутри рабочей директории проекта.
type Executor struct {
	WorkDir    string
	UseSandbox bool
}

// NewExecutor создаёт новый исполнитель инструментов для указанной директории.
func NewExecutor(workDir string) *Executor {
	return &Executor{WorkDir: workDir}
}

// NewExecutorWithSandbox создаёт новый исполнитель инструментов с явным указанием режима песочницы.
func NewExecutorWithSandbox(workDir string, useSandbox bool) *Executor {
	return &Executor{WorkDir: workDir, UseSandbox: useSandbox}
}

var lookPath = exec.LookPath

func hasBwrap() bool {
	_, err := lookPath("bwrap")
	return err == nil
}

// SafeResolvePath проверяет и возвращает абсолютный путь к файлу,
// гарантируя, что он находится внутри WorkDir (защита от path traversal).
func (e *Executor) SafeResolvePath(targetPath string) (string, error) {
	if e.WorkDir == "" {
		return "", ErrWorkspaceNotSet
	}

	absWorkDir, err := filepath.Abs(e.WorkDir)
	if err != nil {
		return "", fmt.Errorf("invalid workspace directory %q: %w", e.WorkDir, err)
	}

	var fullPath string
	if filepath.IsAbs(targetPath) {
		fullPath = filepath.Clean(targetPath)
	} else {
		fullPath = filepath.Clean(filepath.Join(absWorkDir, targetPath))
	}

	rel, err := filepath.Rel(absWorkDir, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("%w: %q is outside %q", ErrPathEscapesWorkDir, targetPath, absWorkDir)
	}

	return fullPath, nil
}

// ReadFile читает содержимое файла. Поддерживает диапазон строк [startLine, endLine] (1-indexed).
func (e *Executor) ReadFile(path string, startLine, endLine int) (string, error) {
	fullPath, err := e.SafeResolvePath(path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrFileNotFound, path)
		}
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory: %s", path)
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Проверка на бинарный файл по первым 512 байтам
	head := make([]byte, 512)
	n, _ := f.Read(head)
	if n > 0 && isBinary(head[:n]) {
		return fmt.Sprintf("[Binary file: %s (%d bytes)]", path, info.Size()), nil
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	data, err := io.ReadAll(io.LimitReader(f, maxReadFileBytes+1))
	if err != nil {
		return "", err
	}

	truncated := false
	if len(data) > maxReadFileBytes {
		data = data[:maxReadFileBytes]
		truncated = true
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	if startLine > 0 || endLine > 0 {
		if startLine < 1 {
			startLine = 1
		}
		if endLine < 1 || endLine > len(lines) {
			endLine = len(lines)
		}
		if startLine > len(lines) {
			return "", fmt.Errorf("start_line %d exceeds total lines %d", startLine, len(lines))
		}
		if startLine > endLine {
			return "", fmt.Errorf("start_line %d is greater than end_line %d", startLine, endLine)
		}

		var bldr strings.Builder
		for i := startLine - 1; i < endLine; i++ {
			bldr.WriteString(fmt.Sprintf("%4d | %s\n", i+1, lines[i]))
		}
		res := bldr.String()
		if truncated {
			res += "\n... [truncated at 1MB]"
		}
		return res, nil
	}

	if truncated {
		content += "\n... [truncated at 1MB]"
	}
	return content, nil
}

// WriteFile создаёт или перезаписывает файл указанным содержимым, автоматически создавая директории.
func (e *Executor) WriteFile(path, content string) (string, error) {
	fullPath, err := e.SafeResolvePath(path)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create directory %q: %w", dir, err)
	}

	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write file %q: %w", fullPath, err)
	}

	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path), nil
}

// EditFile заменяет oldString на newString в файле.
func (e *Executor) EditFile(path, oldString, newString string) (string, error) {
	if oldString == "" {
		return "", errors.New("old_string cannot be empty")
	}

	fullPath, err := e.SafeResolvePath(path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrFileNotFound, path)
		}
		return "", err
	}

	content := string(data)
	count := strings.Count(content, oldString)
	if count == 0 {
		return "", fmt.Errorf("%w in %s", ErrTargetStringNotFound, path)
	}

	// Заменяем вхождение
	newContent := strings.Replace(content, oldString, newString, 1)
	if err := os.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("write file %q: %w", fullPath, err)
	}

	if count > 1 {
		return fmt.Sprintf("Successfully replaced first occurrence of %q in %s (%d occurrences found)", oldString, path, count), nil
	}
	return fmt.Sprintf("Successfully updated %s", path), nil
}

// ListDir возвращает список файлов и директорий по относительному пути.
func (e *Executor) ListDir(path string) (string, error) {
	if path == "" {
		path = "."
	}
	fullPath, err := e.SafeResolvePath(path)
	if err != nil {
		return "", err
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return "", err
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	var bldr strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			bldr.WriteString("[DIR]  .git/ (git repository)\n")
			continue
		}
		if entry.IsDir() {
			bldr.WriteString(fmt.Sprintf("[DIR]  %s/\n", name))
		} else {
			info, err := entry.Info()
			if err == nil {
				bldr.WriteString(fmt.Sprintf("[FILE] %s (%d bytes)\n", name, info.Size()))
			} else {
				bldr.WriteString(fmt.Sprintf("[FILE] %s\n", name))
			}
		}
	}

	res := bldr.String()
	if res == "" {
		return "[Empty directory]", nil
	}
	return strings.TrimRight(res, "\n"), nil
}

// RunCommand выполняет консольную команду внутри WorkDir.
// При включённом UseSandbox команда изолируется с помощью bubblewrap (bwrap).
func (e *Executor) RunCommand(ctx context.Context, command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", errors.New("command cannot be empty")
	}
	if e.WorkDir == "" {
		return "", ErrWorkspaceNotSet
	}

	absWorkDir, err := filepath.Abs(e.WorkDir)
	if err != nil {
		return "", fmt.Errorf("invalid workspace directory %q: %w", e.WorkDir, err)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, defaultCommandTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if e.UseSandbox && hasBwrap() {
		cmd = exec.CommandContext(cmdCtx, "bwrap",
			"--ro-bind", "/", "/",
			"--bind", absWorkDir, absWorkDir,
			"--bind", "/tmp", "/tmp",
			"--proc", "/proc",
			"--dev", "/dev",
			"--die-with-parent",
			"--",
			"bash", "-c", command,
		)
	} else {
		cmd = exec.CommandContext(cmdCtx, "bash", "-c", command)
	}
	cmd.Dir = e.WorkDir

	out, err := cmd.CombinedOutput()
	outputStr := string(out)

	if len(outputStr) > maxOutputBytes {
		outputStr = outputStr[:maxOutputBytes] + "\n... [output truncated]"
	}

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return outputStr, fmt.Errorf("command timed out after %v: %w", defaultCommandTimeout, err)
		}
		return outputStr, fmt.Errorf("command failed: %w\nOutput:\n%s", err, outputStr)
	}

	if strings.TrimSpace(outputStr) == "" {
		return "[Command completed with no output]", nil
	}
	return outputStr, nil
}

// Execute парсит имя инструмента и аргументы, проверяет права и вызывает соответствующий метод.
func (e *Executor) Execute(ctx context.Context, name string, args map[string]interface{}, readOnly bool) (string, error) {
	switch strings.ToLower(name) {
	case "read_file", "view_file", "read":
		path := extractString(args, "path", "file_path", "AbsolutePath", "target")
		if path == "" {
			return "", errors.New("missing required argument 'path'")
		}
		startLine := extractInt(args, "start_line", "StartLine")
		endLine := extractInt(args, "end_line", "EndLine")
		return e.ReadFile(path, startLine, endLine)

	case "list_dir", "list_directory", "list_files":
		path := extractString(args, "path", "dir_path", "directory")
		return e.ListDir(path)

	case "write_file", "write_to_file", "write":
		if readOnly {
			return "", ErrReadOnly
		}
		path := extractString(args, "path", "file_path", "TargetFile", "target")
		if path == "" {
			return "", errors.New("missing required argument 'path'")
		}
		content := extractString(args, "content", "CodeContent", "text")
		return e.WriteFile(path, content)

	case "edit_file", "replace_file_content", "edit":
		if readOnly {
			return "", ErrReadOnly
		}
		path := extractString(args, "path", "file_path", "TargetFile", "target")
		if path == "" {
			return "", errors.New("missing required argument 'path'")
		}
		oldStr := extractString(args, "old_string", "target_content", "TargetContent")
		newStr := extractString(args, "new_string", "replacement_content", "ReplacementContent")
		return e.EditFile(path, oldStr, newStr)

	case "run_command", "bash", "execute_command":
		if readOnly {
			return "", ErrReadOnly
		}
		cmd := extractString(args, "command", "CommandLine", "cmd")
		if cmd == "" {
			return "", errors.New("missing required argument 'command'")
		}
		return e.RunCommand(ctx, cmd)

	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func extractString(m map[string]interface{}, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		if val, ok := m[k]; ok && val != nil {
			if s, ok := val.(string); ok {
				return s
			}
		}
	}
	return ""
}

func extractInt(m map[string]interface{}, keys ...string) int {
	if m == nil {
		return 0
	}
	for _, k := range keys {
		if val, ok := m[k]; ok && val != nil {
			switch v := val.(type) {
			case int:
				return v
			case int64:
				return int(v)
			case float64:
				return int(v)
			}
		}
	}
	return 0
}

func isBinary(data []byte) bool {
	if bytes.IndexByte(data, 0) != -1 {
		return true
	}
	return !utf8.Valid(data)
}
