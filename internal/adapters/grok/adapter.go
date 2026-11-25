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

	"bro-bot/internal/adapters/acp"
	"bro-bot/internal/adapters/cliproc"
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

	var toolSrv *acp.ToolServer
	var mcpServer acp.MCPServer
	if args.BotTools != nil {
		ts, srvErr := acp.StartToolServer(args.BotTools, args.ChannelToken)
		if srvErr != nil {
			return nil, fmt.Errorf("grok-acp: tool server: %w", srvErr)
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
	if tok := strings.TrimSpace(args.ChannelToken); tok != "" {
		cmd.Env = append(cmd.Env, mcp.ChannelTokenEnv+"="+tok)
	}
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
	started = true

	eventR, eventW := io.Pipe()
	proc := &process{cmd: cmd, stdout: eventR, stdin: stdin, runDone: make(chan struct{}), readDone: make(chan struct{})}
	go drainStderr(stderr)
	go proc.run(ctx, stdin, stdout, eventW, toolSrv, turnConfig{
		WorkDir:         workDir,
		Prompt:          args.Prompt,
		SystemPrompt:    args.SystemPrompt,
		BotInstructions: args.BotInstructions,
		ChannelToken:    args.ChannelToken,
		ConversationID:  args.ConversationID,
		Model:           model,
		Effort:          effortID,
		HasEffort:       hasEffort,
		ReadOnly:        args.ReadOnly,
		Yolo:            yolo,
		Timeout:         args.Timeout,
		MCPServer:       mcpServer,
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
	WorkDir         string
	Prompt          string
	SystemPrompt    string
	BotInstructions string
	ChannelToken    string
	ConversationID  string
	Model           string
	Effort          string
	HasEffort       bool
	ReadOnly        bool
	Yolo            bool
	Timeout         time.Duration
	MCPServer       acp.MCPServer
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

// pickAuthMethod выбирает вход без браузера: сохранённый токен, иначе ключ API.
func pickAuthMethod(methods []acp.AuthMethod) (string, bool) {
	for _, m := range methods {
		if m.ID == "cached_token" {
			return m.ID, true
		}
	}
	for _, m := range methods {
		if strings.Contains(m.ID, "api_key") {
			return m.ID, true
		}
	}
	return "", false
}

func (p *process) run(ctx context.Context, stdin io.WriteCloser, stdout io.Reader, events *io.PipeWriter, toolSrv *acp.ToolServer, cfg turnConfig) {
	defer close(p.runDone)
	// Закрываем ToolServer после session/close, чтобы клиент успел прислать DELETE /mcp.
	defer func() {
		if toolSrv != nil {
			_ = toolSrv.Close()
		}
	}()
	defer stdin.Close()
	defer events.Close()

	conn := acp.NewConn(stdout, stdin)
	var sessionID string
	var stateMu sync.Mutex
	var step int
	var acceptUpdates atomic.Bool
	var answer strings.Builder
	var sawTool bool
	var sawInteractiveBotTool bool
	var toolsReg acp.ToolCallRegistry
	var usageMu sync.Mutex
	var lastUpdateUsage *acp.StreamUsage
	var permMu sync.Mutex
	var deniedPerms []acp.DeniedPermission

	conn.OnNotify = func(msg acp.Message) {
		if !acceptUpdates.Load() || msg.Method != "session/update" {
			return
		}
		toolsReg.Observe(msg.Params)
		stateMu.Lock()
		step++
		curStep := step
		stateMu.Unlock()
		evt, ok := acp.TranslateUpdate(sessionID, msg.Params, curStep)
		if !ok {
			return
		}
		if evt.StepUpdate != nil {
			if evt.StepUpdate.StepType == "tool" {
				toolsReg.EnrichStepUpdate(evt.StepUpdate)
			}
			stateMu.Lock()
			if evt.StepUpdate.StepType == "tool" {
				sawTool = true
				if acp.IsInteractiveTurnTool(evt.StepUpdate.ToolName) {
					sawInteractiveBotTool = true
				}
			}
			if evt.StepUpdate.TextDelta != "" {
				answer.WriteString(evt.StepUpdate.TextDelta)
			}
			stateMu.Unlock()
			if evt.StepUpdate.Usage != nil {
				usageMu.Lock()
				uCopy := *evt.StepUpdate.Usage
				lastUpdateUsage = &uCopy
				usageMu.Unlock()
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
		params.ToolCall = toolsReg.Enrich(params.ToolCall)
		allowed := !cfg.ReadOnly || acp.IsAllowedInReadOnly(params.ToolCall)
		optionID, ok := acp.ChoosePermission(cfg.ReadOnly, params.ToolCall, params.Options)
		if !ok || !allowed {
			log.Printf("grok-acp: permission readOnly=%v name=%q toolName=%q kind=%q title=%q -> ok=%v opt=%q",
				cfg.ReadOnly, params.ToolCall.Name, params.ToolCall.ToolName, params.ToolCall.Kind, params.ToolCall.Title, ok, optionID)
			if cfg.ReadOnly && !allowed {
				permMu.Lock()
				deniedPerms = append(deniedPerms, acp.NewDeniedPermission(params.ToolCall))
				permMu.Unlock()
			}
		}
		_ = conn.Reply(msg.ID, acp.PermissionResult(optionID, ok))
	}

	go func() {
		defer close(p.readDone)
		_ = conn.ReadLoop()
	}()

	started := time.Now()
	numTurns := 1
	finish := func(status, errText string, usage *acp.StreamUsage) {
		stateMu.Lock()
		answerStr := answer.String()
		usedTool := sawTool
		usedInteractive := sawInteractiveBotTool
		stateMu.Unlock()

		permMu.Lock()
		denied := append([]acp.DeniedPermission(nil), deniedPerms...)
		permMu.Unlock()

		if status == "SUCCESS" && strings.TrimSpace(answerStr) == "" {
			if (cfg.ReadOnly && !usedInteractive) || !usedTool {
				status = "ERROR"
				if errText == "" {
					if len(denied) > 0 {
						errText = "read-only mode blocked " + acp.FormatDeniedPermissions(denied)
					} else {
						errText = "grok returned an empty answer"
					}
				}
			}
		}
		if status == "SUCCESS" {
			p.finishedOK.Store(true)
		}
		p.writeEvent(events, acp.StreamEvent{
			Event:          "result",
			ConversationID: sessionID,
			Result: &acp.StreamResult{
				ConversationID:  sessionID,
				Status:          status,
				Error:           errText,
				Response:        answerStr,
				DurationSeconds: time.Since(started).Seconds(),
				NumTurns:        numTurns,
				Usage:           usage,
			},
		})
	}

	initRaw, err := conn.Call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "bro-bot", "title": "bro-bot", "version": "acp"},
	})
	if err != nil {
		finish("ERROR", fmt.Sprintf("initialize: %v", err), nil)
		return
	}
	var init acp.InitializeResult
	_ = json.Unmarshal(initRaw, &init)
	methodID, ok := pickAuthMethod(init.AuthMethods)
	if !ok {
		finish("ERROR", "grok is not signed in: run `grok login` on the host or set XAI_API_KEY", nil)
		return
	}
	if _, err := conn.Call(ctx, "authenticate", map[string]any{"methodId": methodID}); err != nil {
		finish("ERROR", fmt.Sprintf("authenticate: %v", err), nil)
		return
	}

	sessionID, fresh, err := openSession(ctx, conn, cfg)
	if err != nil {
		finish("ERROR", err.Error(), nil)
		return
	}
	p.writeEvent(events, acp.StreamEvent{Event: "init", ConversationID: sessionID})

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
	promptCtx := ctx
	var promptCancel context.CancelFunc
	if cfg.Timeout > 0 {
		promptCtx, promptCancel = context.WithTimeout(ctx, cfg.Timeout)
	}

	var usage *acp.StreamUsage
	var status, errText string
	numTurns = 0
	curPrompt := prompt
	for attempt := 0; attempt < 2; attempt++ {
		numTurns++
		acceptUpdates.Store(true)
		raw, err := conn.Call(promptCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt":    []map[string]string{{"type": "text", "text": curPrompt}},
		})
		acceptUpdates.Store(false)
		if err != nil {
			timedOutByCfg := promptCancel != nil && errors.Is(promptCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
			if promptCancel != nil {
				promptCancel()
			}
			if timedOutByCfg {
				_ = conn.Notify("session/cancel", map[string]any{"sessionId": sessionID})
				finish("ERROR", context.DeadlineExceeded.Error(), usage)
				return
			}
			if ctx.Err() != nil {
				return
			}
			finish("ERROR", err.Error(), usage)
			return
		}

		var cancelled bool
		status, errText, cancelled = acp.ParsePromptStopReason(raw)
		if cancelled && ctx.Err() != nil {
			if promptCancel != nil {
				promptCancel()
			}
			return
		}
		usage = acp.MergeUsage(usage, acp.ParseUsage(raw))

		if attempt > 0 || status != "SUCCESS" || !cfg.ReadOnly || ctx.Err() != nil || promptCtx.Err() != nil {
			break
		}
		stateMu.Lock()
		emptyAnswer := strings.TrimSpace(answer.String()) == ""
		interactive := sawInteractiveBotTool
		stateMu.Unlock()
		permMu.Lock()
		denied := append([]acp.DeniedPermission(nil), deniedPerms...)
		permMu.Unlock()
		if !emptyAnswer || interactive || len(denied) == 0 {
			break
		}
		deniedSummary := acp.FormatDeniedPermissions(denied)
		log.Printf("grok-acp: read-only permission denied (%s) ended turn with empty answer; sending recovery prompt", deniedSummary)
		curPrompt = acp.ReadOnlyRecoveryPrompt(deniedSummary)
	}
	if promptCancel != nil {
		promptCancel()
	}
	if usage == nil {
		usageMu.Lock()
		if lastUpdateUsage != nil {
			uCopy := *lastUpdateUsage
			usage = &uCopy
		}
		usageMu.Unlock()
	}
	finish(status, errText, usage)

	if ctx.Err() == nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = conn.Call(closeCtx, "session/close", map[string]any{"sessionId": sessionID})
		cancel()
	}
}

func mergeSystemRules(botInstr, sysPrompt string) string {
	var parts []string
	if b := strings.TrimSpace(botInstr); b != "" {
		parts = append(parts, b)
	}
	if s := strings.TrimSpace(sysPrompt); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func openSession(ctx context.Context, conn *acp.Conn, cfg turnConfig) (id string, fresh bool, err error) {
	servers := []acp.MCPServer{cfg.MCPServer}
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
		if _, callErr := conn.Call(ctx, "session/resume", params); callErr == nil {
			return cfg.ConversationID, false, nil
		} else {
			log.Printf("grok-acp: resume %s: %v", cfg.ConversationID, callErr)
		}
	}
	if rules := mergeSystemRules(cfg.BotInstructions, cfg.SystemPrompt); rules != "" {
		meta["rules"] = rules
	}
	params := map[string]any{
		"cwd":        cfg.WorkDir,
		"mcpServers": servers,
	}
	if len(meta) > 0 {
		params["_meta"] = meta
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

func setConfig(ctx context.Context, conn *acp.Conn, sessionID, configID, value string) error {
	_, err := conn.Call(ctx, "session/set_config_option", map[string]any{
		"sessionId": sessionID,
		"configId":  configID,
		"value":     map[string]string{"value": value},
	})
	if err == nil {
		return nil
	}
	_, err2 := conn.Call(ctx, "session/set_config_option", map[string]any{
		"sessionId": sessionID,
		"configId":  configID,
		"value":     value,
	})
	if err2 == nil {
		return nil
	}
	return err
}

func (p *process) writeEvent(w io.Writer, evt acp.StreamEvent) {
	data, err := acp.EncodeEvent(evt)
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
	cmd        *exec.Cmd
	stdout     io.Reader
	stdin      io.WriteCloser
	runDone    chan struct{}
	readDone   chan struct{}
	finishedOK atomic.Bool
	killOnce   sync.Once
}

func (p *process) Stdout() io.Reader { return p.stdout }

func (p *process) Stdin() io.WriteCloser { return nopCloser{} }

func (p *process) Wait() error {
	return acp.WaitProcess(p.cmd, p.runDone, p.readDone, &p.finishedOK, p.Kill)
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
