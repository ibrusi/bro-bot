package agy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"bro-bot/internal/adapters/acp"
	"bro-bot/internal/adapters/cliproc"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
)

// AgyACPAdapter ведёт один ход Google Antigravity CLI по протоколу ACP (`agy acp`),
// предоставляя инструменты бота через эфемерный in-process HTTP MCP-сервер.
type AgyACPAdapter struct {
	cli             *agyCLI
	cleanLegacyOnce sync.Once
}

// NewAgyACPAdapter создаёт адаптер режима acp для agy.
func NewAgyACPAdapter() *AgyACPAdapter {
	return &AgyACPAdapter{
		cli: newAgyCLI(),
	}
}

func (a *AgyACPAdapter) ExecutionMode() string { return "acp" }

func (a *AgyACPAdapter) SupportsEffort() bool { return true }

func (a *AgyACPAdapter) GetModels(ctx context.Context) ([]byte, error) {
	return a.cli.GetModels(ctx)
}

func (a *AgyACPAdapter) GetQuota(ctx context.Context, lang string) ([]byte, error) {
	return a.cli.GetQuota(ctx, lang)
}

func (a *AgyACPAdapter) GetQuotaText(ctx context.Context, lang string) ([]byte, error) {
	return a.cli.GetQuotaText(ctx, lang)
}

func (a *AgyACPAdapter) GetCredits(ctx context.Context) ([]byte, error) {
	return a.cli.GetCredits(ctx)
}

type acpTurnConfig struct {
	WorkDir         string
	Prompt          string
	SystemPrompt    string
	BotInstructions string
	ChannelToken    string
	ConversationID  string
	ModelID         string
	Effort          string
	ReadOnly        bool
	MCPServer       acp.MCPServer
}

// ExecuteTask запускает один ход agy по протоколу ACP.
// Сессия хранится на стороне agy по ConversationID и продолжается через session/resume.
func (a *AgyACPAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	if _, err := exec.LookPath(agyBin); err != nil && agyBin == "agy" {
		return nil, fmt.Errorf("agy-acp: the agy CLI is not installed")
	}
	workDir, err := filepath.Abs(args.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("agy-acp: working directory: %w", err)
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return nil, errors.New("agy-acp: empty prompt")
	}

	// Однократно удаляем устаревшую запись bro_bot из глобальных mcp_config.json,
	// чтобы она не перебивала встроенный in-process HTTP-эндпоинт сессии agy acp.
	a.cleanLegacyOnce.Do(func() {
		if rmErr := mcp.RemoveAgyMCPConfig(); rmErr != nil {
			log.Printf("warning: agy-acp: cannot clean legacy MCP config: %v", rmErr)
		}
	})

	var toolSrv *acp.ToolServer
	var mcpServer acp.MCPServer
	if args.BotTools != nil {
		ts, srvErr := acp.StartToolServer(args.BotTools, args.ChannelToken)
		if srvErr != nil {
			return nil, fmt.Errorf("agy-acp: tool server: %w", srvErr)
		}
		toolSrv = ts
		mcpServer = ts.ServerConfig()
	} else {
		mcpServer = acp.BroBotStdioServer(args.ChannelToken)
	}

	// Закрываем ToolServer на любой ветке ошибки до передачи владения в proc.run.
	started := false
	defer func() {
		if !started && toolSrv != nil {
			_ = toolSrv.Close()
		}
	}()

	cmdArgs := []string{"acp"}
	if !args.ReadOnly {
		cmdArgs = append(cmdArgs, "--dangerously-skip-permissions")
	}

	cmd := exec.CommandContext(ctx, agyBin, cmdArgs...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1", "CI=true")
	if tok := strings.TrimSpace(args.ChannelToken); tok != "" {
		cmd.Env = append(cmd.Env, mcp.ChannelTokenEnv+"="+tok)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return cliproc.KillGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("agy-acp: stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("agy-acp: stderr: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("agy-acp: stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("agy-acp: start: %w", err)
	}
	started = true

	modelID, effortVal := resolveAgyModelTarget(args.ModelName, args.Effort)
	eventR, eventW := io.Pipe()
	proc := &acpProcess{cmd: cmd, stdout: eventR, stdin: stdin}
	go drainACPSterr(stderr)
	go proc.run(ctx, stdin, stdout, eventW, toolSrv, acpTurnConfig{
		WorkDir:         workDir,
		Prompt:          args.Prompt,
		SystemPrompt:    args.SystemPrompt,
		BotInstructions: args.BotInstructions,
		ChannelToken:    args.ChannelToken,
		ConversationID:  args.ConversationID,
		ModelID:         modelID,
		Effort:          effortVal,
		ReadOnly:        args.ReadOnly,
		MCPServer:       mcpServer,
	})
	return proc, nil
}

func drainACPSterr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			if len(line) > 500 {
				line = line[:500] + "…"
			}
			log.Printf("agy-acp: %s", line)
		}
	}
}

func (p *acpProcess) run(ctx context.Context, stdin io.WriteCloser, stdout io.Reader, events *io.PipeWriter, toolSrv *acp.ToolServer, cfg acpTurnConfig) {
	// Закрываем ToolServer после session/close, чтобы клиент успел прислать DELETE /mcp.
	defer func() {
		if toolSrv != nil {
			_ = toolSrv.Close()
		}
	}()
	defer events.Close()
	defer stdin.Close()

	conn := acp.NewConn(stdout, stdin)
	var sessionID string
	var step int
	var acceptUpdates atomic.Bool
	var answer strings.Builder
	var sawTool bool

	conn.OnNotify = func(msg acp.Message) {
		if !acceptUpdates.Load() || msg.Method != "session/update" {
			return
		}
		step++
		evt, ok := acp.TranslateUpdate(sessionID, msg.Params, step)
		if !ok {
			return
		}
		if evt.StepUpdate != nil {
			if evt.StepUpdate.TextDelta != "" {
				answer.WriteString(evt.StepUpdate.TextDelta)
			}
			if evt.StepUpdate.StepType == "tool" {
				sawTool = true
			}
		}
		p.writeEvent(events, evt)
	}
	conn.OnRequest = func(msg acp.Message) {
		if msg.Method != "session/request_permission" {
			_ = conn.ReplyError(msg.ID, -32601, "method not supported")
			return
		}
		var params acp.PermissionParams
		_ = json.Unmarshal(msg.Params, &params)
		optionID, ok := acp.ChoosePermission(cfg.ReadOnly, params.ToolCall, params.Options)
		_ = conn.Reply(msg.ID, acp.PermissionResult(optionID, ok))
	}

	readErr := make(chan error, 1)
	go func() { readErr <- conn.ReadLoop() }()

	started := time.Now()
	finish := func(status, errText string, usage *acp.StreamUsage) {
		if answer.Len() == 0 && !sawTool && status == "SUCCESS" {
			status = "ERROR"
			if errText == "" {
				errText = "agy returned an empty answer"
			}
		}
		p.writeEvent(events, acp.StreamEvent{
			Event:          "result",
			ConversationID: sessionID,
			Result: &acp.StreamResult{
				ConversationID:  sessionID,
				Status:          status,
				Error:           errText,
				Response:        answer.String(),
				DurationSeconds: time.Since(started).Seconds(),
				NumTurns:        1,
				Usage:           usage,
			},
		})
	}

	if _, err := conn.Call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "bro-bot", "title": "bro-bot", "version": "acp"},
	}); err != nil {
		finish("ERROR", fmt.Sprintf("initialize: %v", err), nil)
		return
	}

	var fresh bool
	var err error
	sessionID, fresh, err = openAgySession(ctx, conn, cfg)
	if err != nil {
		finish("ERROR", err.Error(), nil)
		return
	}
	p.writeEvent(events, acp.StreamEvent{Event: "init", ConversationID: sessionID})

	if cfg.ModelID != "" {
		if err := applyAgyModelAndEffort(ctx, conn, sessionID, cfg.ModelID, cfg.Effort); err != nil {
			log.Printf("agy-acp: model %s (effort %q): %v", cfg.ModelID, cfg.Effort, err)
		}
	}

	prompt := cfg.Prompt
	if fresh && cfg.ConversationID != "" {
		prompt = "The previous Antigravity session could not be resumed. Continue from this message only.\n\n" + prompt
	}
	acceptUpdates.Store(true)
	raw, err := conn.Call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": prompt}},
	})
	acceptUpdates.Store(false)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		finish("ERROR", err.Error(), nil)
		return
	}

	var promptRes struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(raw, &promptRes)
	status, errText := "SUCCESS", ""
	switch promptRes.StopReason {
	case "", "end_turn", "end":
	case "cancelled":
		if ctx.Err() != nil {
			return
		}
		status, errText = "ERROR", "cancelled"
	default:
		status, errText = "ERROR", promptRes.StopReason
	}
	finish(status, errText, acp.ParseUsage(raw))

	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _ = conn.Call(closeCtx, "session/close", map[string]any{"sessionId": sessionID})
	cancel()
	_ = readErr
}

func buildAgyAppendPrompt(cfg acpTurnConfig) string {
	var parts []string
	if botInstr := strings.TrimSpace(cfg.BotInstructions); botInstr != "" {
		parts = append(parts, botInstr)
	}
	if sys := strings.TrimSpace(cfg.SystemPrompt); sys != "" && !utils.HasLocalAgentsRules(cfg.WorkDir) {
		parts = append(parts, "ИНСТРУКЦИИ ПРОЕКТА (AGENTS.md):\n"+sys)
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func openAgySession(ctx context.Context, conn *acp.Conn, cfg acpTurnConfig) (id string, fresh bool, err error) {
	servers := []acp.MCPServer{cfg.MCPServer}
	if strings.TrimSpace(cfg.ConversationID) != "" {
		params := map[string]any{
			"sessionId":  cfg.ConversationID,
			"cwd":        cfg.WorkDir,
			"mcpServers": servers,
		}
		if _, callErr := conn.Call(ctx, "session/resume", params); callErr == nil {
			return cfg.ConversationID, false, nil
		} else {
			log.Printf("agy-acp: resume %s: %v", cfg.ConversationID, callErr)
		}
	}

	params := map[string]any{
		"cwd":        cfg.WorkDir,
		"mcpServers": servers,
	}
	if appendPrompt := buildAgyAppendPrompt(cfg); appendPrompt != "" {
		params["_meta"] = map[string]any{
			"antigravity": map[string]any{
				"systemPrompt": map[string]any{
					"append": appendPrompt,
				},
			},
		}
	}

	raw, err := conn.Call(ctx, "session/new", params)
	if err != nil {
		return "", false, fmt.Errorf("session/new: %w", err)
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.SessionID == "" {
		return "", false, errors.New("session/new did not return a session id")
	}
	return created.SessionID, true, nil
}

func applyAgyModelAndEffort(ctx context.Context, conn *acp.Conn, sessionID, modelID, effortVal string) error {
	if _, err := conn.Call(ctx, "session/set_model", map[string]any{
		"sessionId": sessionID,
		"modelId":   modelID,
	}); err == nil {
		return nil
	}
	if _, err := conn.Call(ctx, "session/set_config_option", map[string]any{
		"sessionId": sessionID,
		"configId":  "model",
		"value":     modelID,
	}); err != nil {
		return err
	}
	if effortVal != "" {
		thought := effortVal
		if strings.EqualFold(thought, "max") {
			thought = "xhigh"
		}
		_, _ = conn.Call(ctx, "session/set_config_option", map[string]any{
			"sessionId": sessionID,
			"configId":  "thought_level",
			"value":     thought,
		})
	}
	return nil
}

func (p *acpProcess) writeEvent(w io.Writer, evt acp.StreamEvent) {
	data, err := acp.EncodeEvent(evt)
	if err != nil {
		return
	}
	_, _ = w.Write(data)
}

// acpProcess инкапсулирует один ход agy acp как стандартный ports.AgentProcess.
type acpProcess struct {
	cmd      *exec.Cmd
	stdout   io.Reader
	stdin    io.WriteCloser
	killOnce sync.Once
}

func (p *acpProcess) Stdout() io.Reader { return p.stdout }

func (p *acpProcess) Stdin() io.WriteCloser { return nopCloser{} }

func (p *acpProcess) Wait() error {
	if p.cmd == nil {
		return nil
	}
	return p.cmd.Wait()
}

func (p *acpProcess) Kill() error {
	var err error
	p.killOnce.Do(func() {
		err = cliproc.KillGroup(p.cmd)
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
	})
	return err
}

func (p *acpProcess) Close() error {
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	return nil
}

func (p *acpProcess) PID() int { return cliproc.PID(p.cmd) }

type nopCloser struct{}

func (nopCloser) Write(b []byte) (int, error) { return len(b), nil }
func (nopCloser) Close() error                { return nil }
