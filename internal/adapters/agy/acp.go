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
	"bro-bot/internal/storage"
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
	WorkDir           string
	Prompt            string
	SystemPrompt      string
	BotInstructions   string
	ChannelToken      string
	ConversationID    string
	ModelID           string
	Effort            string
	ReadOnly          bool
	ScriptsDir        string
	PermissionHandler ports.PermissionHandler
	Timeout           time.Duration
	MCPServer         acp.MCPServer
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
	proc := &acpProcess{cmd: cmd, stdout: eventR, stdin: stdin, runDone: make(chan struct{}), readDone: make(chan struct{})}
	go drainACPSterr(stderr)
	go proc.run(ctx, stdin, stdout, eventW, toolSrv, acpTurnConfig{
		WorkDir:           workDir,
		Prompt:            args.Prompt,
		SystemPrompt:      args.SystemPrompt,
		BotInstructions:   args.BotInstructions,
		ChannelToken:      args.ChannelToken,
		ConversationID:    args.ConversationID,
		ModelID:           modelID,
		Effort:            effortVal,
		ReadOnly:          args.ReadOnly,
		ScriptsDir:        args.ScriptsDir,
		PermissionHandler: args.PermissionHandler,
		Timeout:           args.Timeout,
		MCPServer:         mcpServer,
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
		optionID, ok, allowed, denyReason := acp.ResolvePermission(ctx, cfg.ReadOnly, cfg.ScriptsDir, cfg.WorkDir, cfg.PermissionHandler, params.ToolCall, params.Options)
		if !ok || !allowed {
			log.Printf("agy-acp: permission readOnly=%v name=%q toolName=%q kind=%q title=%q reason=%q -> ok=%v opt=%q",
				cfg.ReadOnly, params.ToolCall.Name, params.ToolCall.ToolName, params.ToolCall.Kind, params.ToolCall.Title, denyReason, ok, optionID)
			if cfg.ReadOnly && !allowed {
				permMu.Lock()
				deniedPerms = append(deniedPerms, acp.NewDeniedPermissionWithReason(params.ToolCall, denyReason))
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
						if cfg.PermissionHandler != nil {
							errText = "permission denied: " + acp.FormatDeniedPermissions(denied)
						} else {
							errText = "read-only mode blocked " + acp.FormatDeniedPermissions(denied)
						}
					} else {
						errText = "agy returned an empty answer"
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

	var baseUsage storage.ConversationUsageSummary
	var hasBaseUsage bool
	if !fresh && sessionID != "" {
		baseUsage, hasBaseUsage = storage.ExtractConversationUsage(sessionID)
	}

	if cfg.ModelID != "" || cfg.Effort != "" {
		if err := applyAgyModelAndEffort(ctx, conn, sessionID, cfg.ModelID, cfg.Effort); err != nil {
			log.Printf("agy-acp: model %s (effort %q): %v", cfg.ModelID, cfg.Effort, err)
		}
	}

	prompt := cfg.Prompt
	if fresh && cfg.ConversationID != "" {
		prompt = "The previous Antigravity session could not be resumed. Continue from this message only.\n\n" + prompt
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
		log.Printf("agy-acp: read-only permission denied (%s) ended turn with empty answer; sending recovery prompt", deniedSummary)
		curPrompt = acp.ControlledDeniedRecoveryPrompt(deniedSummary, cfg.PermissionHandler != nil)
	}
	if promptCancel != nil {
		promptCancel()
	}

	if usage == nil || (usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheReadTokens == 0) {
		if afterUsage, ok := storage.ExtractConversationUsage(sessionID); ok {
			dbUsage := diffAgyUsage(baseUsage, hasBaseUsage, afterUsage)
			if dbUsage != nil {
				usage = dbUsage
			}
		}
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

func diffAgyUsage(base storage.ConversationUsageSummary, hasBase bool, after storage.ConversationUsageSummary) *acp.StreamUsage {
	in := after.Total.InputTokens
	out := after.Total.OutputTokens
	think := after.Total.ThinkingTokens
	cache := after.Total.CacheReadTokens
	total := after.Total.TotalTokens
	if hasBase {
		if in >= base.Total.InputTokens {
			in -= base.Total.InputTokens
		}
		if out >= base.Total.OutputTokens {
			out -= base.Total.OutputTokens
		}
		if think >= base.Total.ThinkingTokens {
			think -= base.Total.ThinkingTokens
		}
		if cache >= base.Total.CacheReadTokens {
			cache -= base.Total.CacheReadTokens
		}
		if total >= base.Total.TotalTokens {
			total -= base.Total.TotalTokens
		}
	}
	if total == 0 && (in > 0 || out > 0 || think > 0) {
		total = in + out + think
	}
	if in == 0 && out == 0 && think == 0 && cache == 0 && total == 0 {
		return nil
	}
	return &acp.StreamUsage{
		InputTokens:     in,
		OutputTokens:    out,
		ThinkingTokens:  think,
		CacheReadTokens: cache,
		TotalTokens:     total,
	}
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
	if modelID != "" {
		if _, err := conn.Call(ctx, "session/set_model", map[string]any{
			"sessionId": sessionID,
			"modelId":   modelID,
		}); err != nil {
			if _, err2 := conn.Call(ctx, "session/set_config_option", map[string]any{
				"sessionId": sessionID,
				"configId":  "model",
				"value":     modelID,
			}); err2 != nil {
				return err2
			}
		}
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
	cmd        *exec.Cmd
	stdout     io.Reader
	stdin      io.WriteCloser
	runDone    chan struct{}
	readDone   chan struct{}
	finishedOK atomic.Bool
	killOnce   sync.Once
}

func (p *acpProcess) Stdout() io.Reader { return p.stdout }

func (p *acpProcess) Stdin() io.WriteCloser { return nopCloser{} }

func (p *acpProcess) Wait() error {
	return acp.WaitProcess(p.cmd, p.runDone, p.readDone, &p.finishedOK, p.Kill)
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
