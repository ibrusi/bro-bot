package agy

import (
	"bro-bot/internal/adapters/cliproc"
	"bro-bot/internal/config"
	"bro-bot/internal/mcp"
	"bro-bot/internal/models"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
)

type AgyAdapter struct{}

func NewAgyAdapter() *AgyAdapter {
	return &AgyAdapter{}
}

func (a *AgyAdapter) ExecutionMode() string {
	return "mcp"
}

// printTimeoutArg форматирует ограничение времени для флага --print-timeout CLI agy.
func printTimeoutArg(d time.Duration) string {
	if d <= 0 {
		d = config.DefaultStepTimeout
	}
	return d.String()
}

// buildAgyArgs собирает аргументы CLI agy для режима mcp.
func buildAgyArgs(args ports.ExecuteArgs) []string {
	cmdArgs := []string{
		"--dangerously-skip-permissions",
		"--print-timeout", printTimeoutArg(args.Timeout),
		"--output-format", "stream-json",
	}
	if args.ConversationID != "" {
		cmdArgs = append(cmdArgs, "--conversation", args.ConversationID)
	}
	if modelName := strings.TrimSpace(args.ModelName); modelName != "" {
		if resolved, ok := modelAliases.Resolve(modelName); ok {
			modelName = resolved
		}
		cmdArgs = append(cmdArgs, models.BuildAgyModelArgs(modelName, args.Effort, models.KnownModelIDs())...)
	}

	finalPrompt := args.Prompt
	// На первом ходе новой сессии (ConversationID пуст) всегда передаём BotInstructions
	// (инструменты бота и правила вопросов), а SystemPrompt (AGENTS.md) — только если
	// в WorkDir нет локального AGENTS.md/GEMINI.md (иначе agy CLI подхватывает его сам).
	if args.ConversationID == "" {
		var preamble []string
		if botInstr := strings.TrimSpace(args.BotInstructions); botInstr != "" {
			preamble = append(preamble, botInstr)
		}
		if sys := strings.TrimSpace(args.SystemPrompt); sys != "" && !utils.HasLocalAgentsRules(args.WorkDir) {
			preamble = append(preamble, "ИНСТРУКЦИИ ПРОЕКТА (AGENTS.md):\n"+sys)
		}
		if len(preamble) > 0 {
			finalPrompt = fmt.Sprintf("<SYSTEM_PROMPT>\n%s\n</SYSTEM_PROMPT>\n\n%s",
				strings.Join(preamble, "\n\n---\n\n"), args.Prompt)
		}
	}

	cmdArgs = append(cmdArgs, "-p", finalPrompt)
	return cmdArgs
}

// ExecuteTask запускает CLI-агента agy.
// Историю диалога agy хранит сам и восстанавливает по флагу --conversation.
// На первой сессии инструкции передаются через правила проекта или преамбулу промпта.
func (a *AgyAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	cmdArgs := buildAgyArgs(args)
	cmd := exec.CommandContext(ctx, "agy", cmdArgs...)
	cmd.Dir = args.WorkDir
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"NO_COLOR=1",
		"CI=true",
	)
	if token := strings.TrimSpace(args.ChannelToken); token != "" {
		cmd.Env = append(cmd.Env, mcp.ChannelTokenEnv+"="+token)
	}

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
