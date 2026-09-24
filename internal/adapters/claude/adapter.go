package claude

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"bro-bot/internal/adapters/cliproc"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"

	"github.com/creack/pty"
)

// ClaudeAdapter запускает CLI Claude Code. HTTPClient и BaseURL нужны только для
// списка моделей: у CLI нет команды, которая его выдаёт, поэтому при наличии ключа
// список берётся из /v1/models тем же кодом, что и в api-режиме.
type ClaudeAdapter struct {
	HTTPClient *http.Client
	BaseURL    string
}

func NewClaudeAdapter() *ClaudeAdapter {
	return &ClaudeAdapter{BaseURL: defaultClaudeBaseURL}
}

func (a *ClaudeAdapter) ExecutionMode() string {
	return "cli"
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

func buildClaudeArgs(convID, modelName, prompt, systemPrompt string) []string {
	args := []string{
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
	}
	if convID != "" {
		// В Claude для возобновления существующей сессии используется --resume (-r)
		args = append(args, "--resume", convID)
	} else if strings.TrimSpace(systemPrompt) != "" {
		// На первом ходе новой сессии передаём системный промпт через --append-system-prompt.
		// Claude сохраняет его в снэпшоте сессии (--system-prompt-snapshot on), поэтому при
		// последующих вызовах с --resume повторная передача не требуется.
		args = append(args, "--append-system-prompt", strings.TrimSpace(systemPrompt))
	}
	claudeModel := resolveClaudeModel(modelName)
	if claudeModel != "" {
		args = append(args, "--model", claudeModel)
	}
	args = append(args, "-p", prompt)
	return args
}

// ExecuteTask запускает CLI-агента claude.
// История диалога восстанавливается Claude по флагу --resume. На новой сессии системный
// промпт передается через --append-system-prompt.
func (a *ClaudeAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	cmdArgs := buildClaudeArgs(args.ConversationID, args.ModelName, args.Prompt, args.SystemPrompt)
	cmd := exec.CommandContext(ctx, "claude", cmdArgs...)
	cmd.Dir = args.WorkDir
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"NO_COLOR=1",
		"CI=true",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("claude-cli: start PTY: %w", err)
	}

	return &ClaudeProcess{
		cmd:    cmd,
		ptmx:   ptmx,
		stdout: cliproc.NewPTYReader(ptmx),
	}, nil
}

// claudeCLIAliases — псевдонимы, которые принимает флаг --model CLI («alias for the
// latest model»). Они не протухают по построению: CLI сам сопоставляет их с актуальной
// версией. Это запасной список на случай, когда ключа API нет и живой список недоступен.
const claudeCLIAliases = "sonnet Claude Sonnet (current version)\n" +
	"opus Claude Opus (current version)\n" +
	"fable Claude Fable (current version)\n"

// GetModels возвращает список моделей для cli-режима. Раньше здесь был зашитый список
// конкретных версий, который устаревал так же, как устарели claude-3-*: теперь при
// наличии ключа список живой (/v1/models), а без ключа — только псевдонимы CLI.
func (a *ClaudeAdapter) GetModels(ctx context.Context) ([]byte, error) {
	if apiKey := apiKeyFromEnv(); apiKey != "" {
		baseURL := a.BaseURL
		if baseURL == "" {
			baseURL = defaultClaudeBaseURL
		}
		available, err := listClaudeModels(ctx, a.HTTPClient, baseURL, apiKey, false)
		if err == nil && len(available) > 0 {
			return []byte(formatClaudeModelsList(available)), nil
		}
	}
	return []byte(claudeCLIAliases), nil
}

func (a *ClaudeAdapter) GetQuota(ctx context.Context, lang string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "claude", "--dangerously-skip-permissions", "--output-format", "json", "-p", "/usage").CombinedOutput()
	if err != nil {
		return out, err
	}
	cleanOut := utils.AnsiRegex.ReplaceAllString(string(out), "")
	if parsed, ok := parseClaudeCLIQuota([]byte(cleanOut), lang, time.Now()); ok {
		return parsed, nil
	}
	return out, nil
}

func (a *ClaudeAdapter) GetQuotaText(ctx context.Context, _ string) ([]byte, error) {
	return exec.CommandContext(ctx, "claude", "--dangerously-skip-permissions", "-p", "/usage").CombinedOutput()
}

func (a *ClaudeAdapter) GetCredits(ctx context.Context) ([]byte, error) {
	// Заглушка
	return []byte(`{}`), nil
}

// ClaudeProcess реализует интерфейс AgentProcess для PTY-ориентированного процесса claude
type ClaudeProcess struct {
	cmd    *exec.Cmd
	ptmx   *os.File
	stdout io.Reader
}

func (p *ClaudeProcess) Stdout() io.Reader {
	if p.stdout != nil {
		return p.stdout
	}
	if p.ptmx != nil {
		return cliproc.NewPTYReader(p.ptmx)
	}
	return nil
}

func (p *ClaudeProcess) Stdin() io.WriteCloser {
	return p.ptmx
}

func (p *ClaudeProcess) Wait() error {
	return p.cmd.Wait()
}

// Kill останавливает агента вместе с дочерними процессами (git, node и т. п.).
func (p *ClaudeProcess) Kill() error {
	return cliproc.KillGroup(p.cmd)
}

func (p *ClaudeProcess) Close() error {
	return p.ptmx.Close()
}

func (p *ClaudeProcess) PID() int {
	return cliproc.PID(p.cmd)
}
