package claude

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"bro-bot/internal/ports"

	"github.com/creack/pty"
)

type ClaudeAdapter struct{}

func NewClaudeAdapter() *ClaudeAdapter {
	return &ClaudeAdapter{}
}

func resolveClaudeModel(modelName string) string {
	m := strings.ToLower(strings.TrimSpace(modelName))
	if m == "" {
		return "sonnet"
	}
	// Если передана модель другого семейства (например, gemini или gpt),
	// переключаемся на безопасный дефолт sonnet во избежание ошибки 404 unrecognized_model
	if strings.Contains(m, "gemini") || strings.Contains(m, "gpt") || strings.Contains(m, "120b") {
		return "sonnet"
	}
	return modelName
}

func buildClaudeArgs(convID, modelName, prompt string) []string {
	args := []string{
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
	}
	if convID != "" {
		// В Claude для возобновления существующей сессии используется --resume (-r)
		args = append(args, "--resume", convID)
	}
	claudeModel := resolveClaudeModel(modelName)
	if claudeModel != "" {
		args = append(args, "--model", claudeModel)
	}
	args = append(args, "-p", prompt)
	return args
}

// ExecuteTask запускает CLI-агента claude.
// Поля args.History и args.SystemPrompt намеренно игнорируются: историю диалога claude хранит сам
// и восстанавливает по флагу --resume, а преамбула подмешивается в текст промпта.
func (a *ClaudeAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	cmdArgs := buildClaudeArgs(args.ConversationID, args.ModelName, args.Prompt)
	cmd := exec.CommandContext(ctx, "claude", cmdArgs...)
	cmd.Dir = args.WorkDir
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"NO_COLOR=1",
		"CI=true",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("ошибка запуска PTY (Claude): %w", err)
	}

	return &ClaudeProcess{
		cmd:  cmd,
		ptmx: ptmx,
	}, nil
}

func (a *ClaudeAdapter) GetModels(ctx context.Context) ([]byte, error) {
	// Возвращаем список поддерживаемых моделей Claude в формате, совместимом с parseAgyModelsOutput
	modelsText := "claude-sonnet-5 Claude Sonnet 5 (Hybrid Reasoning)\n" +
		"claude-sonnet-4-6 Claude Sonnet 4.6 (Thinking)\n" +
		"claude-opus-4-6-thinking Claude Opus 4.6 (Thinking)\n" +
		"claude-haiku-4-5 Claude Haiku 4.5 (Fast & Lightweight)\n"
	return []byte(modelsText), nil
}

func (a *ClaudeAdapter) GetQuota(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "claude", "--dangerously-skip-permissions", "--output-format", "json", "-p", "/usage").CombinedOutput()
}

func (a *ClaudeAdapter) GetQuotaText(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "claude", "--dangerously-skip-permissions", "-p", "/usage").CombinedOutput()
}

func (a *ClaudeAdapter) GetCredits(ctx context.Context) ([]byte, error) {
	// Заглушка
	return []byte(`{}`), nil
}

// ClaudeProcess реализует интерфейс AgentProcess для PTY-ориентированного процесса claude
type ClaudeProcess struct {
	cmd  *exec.Cmd
	ptmx *os.File
}

func (p *ClaudeProcess) Stdout() io.Reader {
	return p.ptmx
}

func (p *ClaudeProcess) Stdin() io.WriteCloser {
	return p.ptmx
}

func (p *ClaudeProcess) Wait() error {
	return p.cmd.Wait()
}

func (p *ClaudeProcess) Kill() error {
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

func (p *ClaudeProcess) Close() error {
	return p.ptmx.Close()
}

func (p *ClaudeProcess) GetCmd() *exec.Cmd {
	return p.cmd
}
