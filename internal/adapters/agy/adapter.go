package agy

import (
	"bro-bot/internal/adapters/cliproc"
	"bro-bot/internal/models"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/creack/pty"
)

type AgyAdapter struct{}

func NewAgyAdapter() *AgyAdapter {
	return &AgyAdapter{}
}

func (a *AgyAdapter) ExecutionMode() string {
	return "cli"
}

func buildAgyArgs(convID, modelName, prompt, systemPrompt, workDir string) []string {
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

	finalPrompt := prompt
	// Если это новая сессия (convID == "") и передан systemPrompt:
	// agy CLI автоматически считывает AGENTS.md из workDir. Но если файла в workDir нет,
	// подмешиваем системный промпт в начало первого пользовательского запроса.
	if convID == "" && strings.TrimSpace(systemPrompt) != "" && !utils.HasLocalAgentsRules(workDir) {
		finalPrompt = fmt.Sprintf("ИНСТРУКЦИИ ПРОЕКТА (AGENTS.md):\n%s\n\n---\n\n%s", strings.TrimSpace(systemPrompt), prompt)
	}

	args = append(args, "-p", finalPrompt)
	return args
}

// ExecuteTask запускает CLI-агента agy.
// Историю диалога agy хранит сам и восстанавливает по флагу --conversation.
// На первой сессии инструкции передаются через правила проекта или преамбулу промпта.
func (a *AgyAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	cmdArgs := buildAgyArgs(args.ConversationID, args.ModelName, args.Prompt, args.SystemPrompt, args.WorkDir)
	cmd := exec.CommandContext(ctx, "agy", cmdArgs...)
	cmd.Dir = args.WorkDir
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"NO_COLOR=1",
		"CI=true",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("agy-cli: start PTY: %w", err)
	}

	return &AgyProcess{
		cmd:    cmd,
		ptmx:   ptmx,
		stdout: cliproc.NewPTYReader(ptmx),
	}, nil
}

func (a *AgyAdapter) GetModels(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "agy", "models")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

func (a *AgyAdapter) GetQuota(ctx context.Context, _ string) ([]byte, error) {
	return exec.CommandContext(ctx, "agy", "-p", "/quota", "--output-format", "json").CombinedOutput()
}

func (a *AgyAdapter) GetQuotaText(ctx context.Context, _ string) ([]byte, error) {
	return exec.CommandContext(ctx, "agy", "-p", "/quota").CombinedOutput()
}

func (a *AgyAdapter) GetCredits(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "agy", "-p", "/credits", "--output-format", "json").CombinedOutput()
}

// AgyProcess реализует интерфейс AgentProcess для PTY-ориентированного процесса agy
type AgyProcess struct {
	cmd    *exec.Cmd
	ptmx   *os.File
	stdout io.Reader
}

func (p *AgyProcess) Stdout() io.Reader {
	if p.stdout != nil {
		return p.stdout
	}
	if p.ptmx != nil {
		return cliproc.NewPTYReader(p.ptmx)
	}
	return nil
}

func (p *AgyProcess) Stdin() io.WriteCloser {
	return p.ptmx
}

func (p *AgyProcess) Wait() error {
	return p.cmd.Wait()
}

// Kill останавливает агента вместе с дочерними процессами (git, node и т. п.).
func (p *AgyProcess) Kill() error {
	return cliproc.KillGroup(p.cmd)
}

func (p *AgyProcess) Close() error {
	return p.ptmx.Close()
}

func (p *AgyProcess) PID() int {
	return cliproc.PID(p.cmd)
}
