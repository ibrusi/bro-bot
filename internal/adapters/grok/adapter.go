package grok

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

	"bro-bot/internal/adapters/cliproc"
	"bro-bot/internal/config"
	"bro-bot/internal/effort"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"
)

// grokBin — исполняемый файл CLI. Тесты подменяют его фальшивым агентом.
var grokBin = "grok"

// Adapter ведёт один ход Grok по ACP: поднимает `grok agent stdio`,
// открывает или продолжает сессию и отдаёт ответ потоком stream-json.
type Adapter struct{}

// NewAdapter создаёт адаптер режима acp.
func NewAdapter() *Adapter {
	return &Adapter{}
}

func (a *Adapter) ExecutionMode() string { return "acp" }

func (a *Adapter) SupportsEffort() bool { return true }

func (a *Adapter) GetModels(ctx context.Context) ([]byte, error) {
	return listModels(ctx)
}

func (a *Adapter) GetQuota(ctx context.Context, _ string) ([]byte, error) {
	return grokUsage(ctx)
}

func (a *Adapter) GetQuotaText(ctx context.Context, _ string) ([]byte, error) {
	return grokUsage(ctx)
}

func (a *Adapter) GetCredits(context.Context) ([]byte, error) {
	return []byte(`{}`), nil
}

func grokUsage(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, grokBin, "usage").CombinedOutput()
}

// ExecuteTask запускает один ход агента. Сессия Grok живёт по ConversationID:
// следующий ход открывает её через session/resume.
func (a *Adapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	if _, err := exec.LookPath(grokBin); err != nil && grokBin == "grok" {
		return nil, fmt.Errorf("grok-acp: the grok CLI is not installed")
	}
	workDir, err := filepath.Abs(args.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("grok-acp: working directory: %w", err)
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return nil, errors.New("grok-acp: empty prompt")
	}

	model := resolveModel(args.ModelName)
	effortID, hasEffort := grokEffort(args.Effort)
	yolo := yoloFromEnv() && !args.ReadOnly

	cmdArgs := []string{"agent"}
	if yolo {
		cmdArgs = append(cmdArgs, "--always-approve")
	}
	if model != "" {
		cmdArgs = append(cmdArgs, "--model", model)
	}
	if hasEffort {
		cmdArgs = append(cmdArgs, "--reasoning-effort", effortID)
	}
	cmdArgs = append(cmdArgs, "stdio")

	cmd := exec.CommandContext(ctx, grokBin, cmdArgs...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return cliproc.KillGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("grok-acp: stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("grok-acp: stderr: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("grok-acp: stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("grok-acp: start: %w", err)
	}

	eventR, eventW := io.Pipe()
	proc := &process{cmd: cmd, stdout: eventR, stdin: stdin}
	go drainStderr(stderr)
	go proc.run(ctx, stdin, stdout, eventW, turnConfig{
		WorkDir:        workDir,
		Prompt:         args.Prompt,
		SystemPrompt:   args.SystemPrompt,
		ConversationID: args.ConversationID,
		Model:          model,
		Effort:         effortID,
		HasEffort:      hasEffort,
		ReadOnly:       args.ReadOnly,
		Yolo:           yolo,
	})
	return proc, nil
}

func drainStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			log.Printf("grok-acp: %s", truncate(line, 500))
		}
	}
}

type turnConfig struct {
	WorkDir        string
	Prompt         string
	SystemPrompt   string
	ConversationID string
	Model          string
	Effort         string
	HasEffort      bool
	ReadOnly       bool
	Yolo           bool
}

type mcpEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type mcpServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []mcpEnv `json:"env"`
}

func broBotServer() mcpServer {
	args := []string{"mcp-serve"}
	if socket := strings.TrimSpace(config.MCPSocketPath); socket != "" {
		args = append(args, "--socket", socket)
	}
	return mcpServer{
		Name:    "bro_bot",
		Command: mcp.GetBotExecutable(),
		Args:    args,
		Env:     []mcpEnv{},
	}
}

func resolveModel(name string) string {
	if rejectsModel(name) || strings.TrimSpace(name) == "" {
		return defaultModel
	}
	return strings.TrimSpace(name)
}

func grokEffort(level effort.Level) (string, bool) {
	if level.IsAuto() {
		return "", false
	}
	// У Grok нет ступени max: ближайшая заявленная — xhigh.
	if level == effort.Max {
		return "xhigh", true
	}
	return level.String(), true
}

func (p *process) run(ctx context.Context, stdin io.WriteCloser, stdout io.Reader, events *io.PipeWriter, cfg turnConfig) {
	defer events.Close()
	defer stdin.Close()

	conn := newRPC(stdout, stdin)
	var sessionID string
	var step int
	var acceptUpdates atomic.Bool
	var answer strings.Builder
	var sawTool bool

	conn.onNotify = func(msg rpcMessage) {
		if !acceptUpdates.Load() || msg.Method != "session/update" {
			return
		}
		step++
		evt, ok := translateUpdate(sessionID, msg.Params, step)
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
	conn.onRequest = func(msg rpcMessage) {
		if msg.Method != "session/request_permission" {
			_ = conn.writeJSON(struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Error   *rpcError       `json:"error"`
			}{"2.0", msg.ID, &rpcError{Code: -32601, Message: "method not supported"}})
			return
		}
		var params permissionParams
		_ = json.Unmarshal(msg.Params, &params)
		optionID, ok := choosePermission(cfg.ReadOnly, params.ToolCall.Kind, params.Options)
		_ = conn.reply(msg.ID, permissionResult(optionID, ok))
	}

	readErr := make(chan error, 1)
	go func() { readErr <- conn.readLoop() }()

	started := time.Now()
	finish := func(status, errText string) {
		if answer.Len() == 0 && !sawTool && status == "SUCCESS" {
			status = "ERROR"
			if errText == "" {
				errText = "grok returned an empty answer"
			}
		}
		p.writeEvent(events, streamEvent{
			Event:          "result",
			ConversationID: sessionID,
			Result: &streamResult{
				ConversationID:  sessionID,
				Status:          status,
				Error:           errText,
				Response:        answer.String(),
				DurationSeconds: time.Since(started).Seconds(),
				NumTurns:        1,
			},
		})
	}

	initRaw, err := conn.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "bro-bot", "title": "bro-bot", "version": "acp"},
	})
	if err != nil {
		finish("ERROR", fmt.Sprintf("initialize: %v", err))
		return
	}
	var init initializeResult
	_ = json.Unmarshal(initRaw, &init)
	methodID, ok := pickAuthMethod(init.AuthMethods)
	if !ok {
		finish("ERROR", "grok is not signed in: run `grok login` on the host or set XAI_API_KEY")
		return
	}
	if _, err := conn.call(ctx, "authenticate", map[string]any{"methodId": methodID}); err != nil {
		finish("ERROR", fmt.Sprintf("authenticate: %v", err))
		return
	}

	sessionID, fresh, err := openSession(ctx, conn, cfg)
	if err != nil {
		finish("ERROR", err.Error())
		return
	}
	p.writeEvent(events, streamEvent{Event: "init", ConversationID: sessionID})

	if cfg.Model != "" {
		if err := setConfig(ctx, conn, sessionID, "model", cfg.Model); err != nil {
			log.Printf("grok-acp: model %s: %v", cfg.Model, err)
		}
	}
	if cfg.HasEffort {
		if err := setConfig(ctx, conn, sessionID, "reasoning_effort", cfg.Effort); err != nil {
			log.Printf("grok-acp: effort %s: %v", cfg.Effort, err)
		}
	}

	prompt := cfg.Prompt
	if fresh && cfg.ConversationID != "" {
		prompt = "The previous Grok session could not be resumed. Continue from this message only.\n\n" + prompt
	}
	acceptUpdates.Store(true)
	raw, err := conn.call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": prompt}},
	})
	acceptUpdates.Store(false)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		finish("ERROR", err.Error())
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
	finish(status, errText)

	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _ = conn.call(closeCtx, "session/close", map[string]any{"sessionId": sessionID})
	cancel()
	_ = readErr
}

func openSession(ctx context.Context, conn *rpcConn, cfg turnConfig) (id string, fresh bool, err error) {
	servers := []mcpServer{broBotServer()}
	meta := map[string]any{}
	if cfg.Yolo {
		meta["yoloMode"] = true
	}
	if strings.TrimSpace(cfg.ConversationID) != "" {
		params := map[string]any{
			"sessionId":  cfg.ConversationID,
			"cwd":        cfg.WorkDir,
			"mcpServers": servers,
		}
		if len(meta) > 0 {
			params["_meta"] = meta
		}
		if _, callErr := conn.call(ctx, "session/resume", params); callErr == nil {
			return cfg.ConversationID, false, nil
		} else {
			log.Printf("grok-acp: resume %s: %v", cfg.ConversationID, callErr)
		}
	}
	if rules := strings.TrimSpace(cfg.SystemPrompt); rules != "" {
		meta["rules"] = rules
	}
	params := map[string]any{
		"cwd":        cfg.WorkDir,
		"mcpServers": servers,
	}
	if len(meta) > 0 {
		params["_meta"] = meta
	}
	raw, err := conn.call(ctx, "session/new", params)
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

func setConfig(ctx context.Context, conn *rpcConn, sessionID, configID, value string) error {
	_, err := conn.call(ctx, "session/set_config_option", map[string]any{
		"sessionId": sessionID,
		"configId":  configID,
		"value":     map[string]string{"value": value},
	})
	if err == nil {
		return nil
	}
	_, err2 := conn.call(ctx, "session/set_config_option", map[string]any{
		"sessionId": sessionID,
		"configId":  configID,
		"value":     value,
	})
	if err2 == nil {
		return nil
	}
	return err
}

func (p *process) writeEvent(w io.Writer, evt streamEvent) {
	data, err := encodeEvent(evt)
	if err != nil {
		return
	}
	_, _ = w.Write(data)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// process — ход ACP, видимый боту как обычный процесс агента.
// Stdin наружу не отдаётся: туда пишет только протокол, произвольный текст его ломает.
type process struct {
	cmd      *exec.Cmd
	stdout   io.Reader
	stdin    io.WriteCloser
	killOnce sync.Once
}

func (p *process) Stdout() io.Reader { return p.stdout }

func (p *process) Stdin() io.WriteCloser { return nopCloser{} }

func (p *process) Wait() error {
	if p.cmd == nil {
		return nil
	}
	return p.cmd.Wait()
}

func (p *process) Kill() error {
	var err error
	p.killOnce.Do(func() {
		err = cliproc.KillGroup(p.cmd)
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
	})
	return err
}

func (p *process) Close() error {
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	return nil
}

func (p *process) PID() int { return cliproc.PID(p.cmd) }

type nopCloser struct{}

func (nopCloser) Write(b []byte) (int, error) { return len(b), nil }
func (nopCloser) Close() error                { return nil }
