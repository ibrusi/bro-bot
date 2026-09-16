package claude

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"bro-bot/internal/ports"

	"github.com/creack/pty"
)

type ClaudeAdapter struct{}

func NewClaudeAdapter() *ClaudeAdapter {
	return &ClaudeAdapter{}
}

func buildClaudeArgs(convID, modelName, prompt string) []string {
	args := []string{
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
	}
	if convID != "" {
		// В Claude используется --session-id (в зависимости от версии это может быть -r)
		args = append(args, "--session-id", convID)
	}
	if modelName != "" {
		args = append(args, "--model", modelName)
	}
	args = append(args, "-p", prompt)
	return args
}

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
	// Claude CLI может не поддерживать команду models в формате json.
	// Возвращаем пустой массив, чтобы не сломать парсинг.
	return []byte(`[]`), nil
}

func (a *ClaudeAdapter) GetQuota(ctx context.Context) ([]byte, error) {
	// Заглушка
	return []byte(`{}`), nil
}

func (a *ClaudeAdapter) GetQuotaText(ctx context.Context) ([]byte, error) {
	// Заглушка
	return []byte("Claude CLI не поддерживает проверку квот напрямую."), nil
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
