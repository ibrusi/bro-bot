package agy

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"bro-bot/internal/models"
	"bro-bot/internal/ports"

	"github.com/creack/pty"
)

type AgyAdapter struct{}

func NewAgyAdapter() *AgyAdapter {
	return &AgyAdapter{}
}

func buildAgyArgs(convID, modelName, prompt string) []string {
	args := []string{
		"--dangerously-skip-permissions",
		"--print-timeout", "30m", // Hardcoded fallback or we can pass it
		"--output-format", "stream-json",
	}
	if convID != "" {
		args = append(args, "--conversation", convID)
	}
	if modelName != "" {
		args = append(args, models.BuildAgyModelArgs(modelName)...)
	}
	args = append(args, "-p", prompt)
	return args
}

func (a *AgyAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	cmdArgs := buildAgyArgs(args.ConversationID, args.ModelName, args.Prompt)
	cmd := exec.CommandContext(ctx, "agy", cmdArgs...)
	cmd.Dir = args.WorkDir
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"NO_COLOR=1",
		"CI=true",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("ошибка запуска PTY: %w", err)
	}

	return &AgyProcess{
		cmd:  cmd,
		ptmx: ptmx,
	}, nil
}

func (a *AgyAdapter) GetModels(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "agy", "models", "--output-format", "json").CombinedOutput()
}

func (a *AgyAdapter) GetQuota(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "agy", "-p", "/quota", "--output-format", "json").CombinedOutput()
}

func (a *AgyAdapter) GetQuotaText(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "agy", "-p", "/quota").CombinedOutput()
}

func (a *AgyAdapter) GetCredits(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "agy", "-p", "/credits", "--output-format", "json").CombinedOutput()
}

// AgyProcess реализует интерфейс AgentProcess для PTY-ориентированного процесса agy
type AgyProcess struct {
	cmd  *exec.Cmd
	ptmx *os.File
}

func (p *AgyProcess) Stdout() io.Reader {
	return p.ptmx
}

func (p *AgyProcess) Stdin() io.WriteCloser {
	return p.ptmx
}

func (p *AgyProcess) Wait() error {
	return p.cmd.Wait()
}

func (p *AgyProcess) Kill() error {
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

func (p *AgyProcess) Close() error {
	return p.ptmx.Close()
}

func (p *AgyProcess) GetCmd() *exec.Cmd {
	return p.cmd
}
