package handlers

import (
	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/storage"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type approvalSignal struct {
	ref  ports.MessageRef
	sent mock.SentMessage
}

type signalMessenger struct {
	*mock.Messenger
	sentCh chan approvalSignal
}

func newSignalMessenger() *signalMessenger {
	return &signalMessenger{
		Messenger: mock.New(),
		sentCh:    make(chan approvalSignal, 32),
	}
}

func (m *signalMessenger) Send(ctx context.Context, chat ports.ChatID, text string, opts *ports.SendOptions) (ports.MessageRef, error) {
	ref, err := m.Messenger.Send(ctx, chat, text, opts)
	if err == nil {
		m.sentCh <- approvalSignal{
			ref:  ref,
			sent: mock.SentMessage{Chat: chat, Text: text, Opts: opts},
		}
	}
	return ref, err
}

func (m *signalMessenger) waitCard(t *testing.T, timeout time.Duration) approvalSignal {
	t.Helper()
	select {
	case sig := <-m.sentCh:
		return sig
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for confirmation card")
		return approvalSignal{}
	}
}

func findActionPayload(kb *ports.Keyboard, action string) (string, bool) {
	if kb == nil {
		return "", false
	}
	for _, row := range kb.Rows {
		for _, btn := range row {
			if btn.Action == action {
				return btn.Payload, true
			}
		}
	}
	return "", false
}

func setupWriteTestEnv(t *testing.T) (*storage.SQLiteStorage, *mock.Messenger, func()) {
	t.Helper()
	st, err := storage.NewSQLiteStorage(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStorage: %v", err)
	}
	prevTM := domain.GlobalTaskManager
	domain.GlobalTaskManager = domain.NewTaskManagerWithStorage(st)

	prevChatWrite := config.ProjectState.GetChatWriteMode()
	prevPlanWrite := config.ProjectState.GetPlanWriteMode()
	prevPlanMode := func() bool {
		config.ProjectState.RLock()
		defer config.ProjectState.RUnlock()
		return config.ProjectState.PlanMode
	}()
	prevProj := func() string {
		config.ProjectState.RLock()
		defer config.ProjectState.RUnlock()
		return config.ProjectState.CurrentProject
	}()
	prevAgent := ActiveAgentName()
	prevMode := config.ProjectState.GetExecutionMode()

	config.ProjectState.SetChatWriteMode(domain.WriteModeAsk)
	config.ProjectState.SetPlanWriteMode(domain.WriteModeDeny)
	config.ProjectState.Lock()
	config.ProjectState.CurrentProject = "demo-proj"
	config.ProjectState.Unlock()

	msg := mock.New()
	return st, msg, func() {
		config.ProjectState.SetChatWriteMode(prevChatWrite)
		config.ProjectState.SetPlanWriteMode(prevPlanWrite)
		config.ProjectState.Lock()
		config.ProjectState.PlanMode = prevPlanMode
		config.ProjectState.CurrentProject = prevProj
		config.ProjectState.Unlock()
		config.ProjectState.SetExecutionMode(prevMode)
		_ = prevAgent
		domain.GlobalTaskManager = prevTM
		_ = st.Close()
	}
}

func TestWriteModeCommandsCallbacksAndPersistence(t *testing.T) {
	st, msg, cleanup := setupWriteTestEnv(t)
	defer cleanup()

	if config.ProjectState.GetChatWriteMode() != domain.WriteModeAsk {
		t.Fatalf("default ChatWriteMode = %q, want ask", config.ProjectState.GetChatWriteMode())
	}
	if config.ProjectState.GetPlanWriteMode() != domain.WriteModeDeny {
		t.Fatalf("default PlanWriteMode = %q, want deny", config.ProjectState.GetPlanWriteMode())
	}

	// /chat write (no args) -> status & 3 buttons
	if err := handleChat(&mock.Session{M: msg, ChatID: testChatID, ArgsVal: []string{"write"}}); err != nil {
		t.Fatal(err)
	}
	last := msg.LastSent()
	if last == nil || last.Opts == nil || last.Opts.Keyboard == nil || len(last.Opts.Keyboard.Rows) == 0 {
		t.Fatalf("expected status message with write mode keyboard")
	}
	if _, ok := findActionPayload(last.Opts.Keyboard, "chat_write_sel"); !ok {
		t.Fatalf("expected chat_write_sel buttons in /chat write response")
	}

	// /chat write allow (independent from /plan)
	if err := handleChat(&mock.Session{M: msg, ChatID: testChatID, ArgsVal: []string{"write", "allow"}}); err != nil {
		t.Fatal(err)
	}
	if config.ProjectState.GetChatWriteMode() != domain.WriteModeAllow {
		t.Fatalf("ChatWriteMode = %q, want allow", config.ProjectState.GetChatWriteMode())
	}
	if config.ProjectState.GetPlanWriteMode() != domain.WriteModeDeny {
		t.Fatalf("PlanWriteMode changed unexpectedly to %q", config.ProjectState.GetPlanWriteMode())
	}

	// /plan write ask (aliases & independent)
	if err := handlePlan(&mock.Session{M: msg, ChatID: testChatID, ArgsVal: []string{"запись", "спрашивать"}}); err != nil {
		t.Fatal(err)
	}
	if config.ProjectState.GetPlanWriteMode() != domain.WriteModeAsk {
		t.Fatalf("PlanWriteMode = %q, want ask", config.ProjectState.GetPlanWriteMode())
	}
	if config.ProjectState.GetChatWriteMode() != domain.WriteModeAllow {
		t.Fatalf("ChatWriteMode changed unexpectedly to %q", config.ProjectState.GetChatWriteMode())
	}

	// Callbacks
	if err := onChatWriteSel(&mock.Session{
		M:      msg,
		ChatID: testChatID,
		CB:     &ports.CallbackQuery{ID: "cb1", Chat: testChatID, Action: "chat_write_sel", Payload: "deny"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := onPlanWriteSel(&mock.Session{
		M:      msg,
		ChatID: testChatID,
		CB:     &ports.CallbackQuery{ID: "cb2", Chat: testChatID, Action: "plan_write_sel", Payload: "allow"},
	}); err != nil {
		t.Fatal(err)
	}

	gotChat, _ := st.GetSetting(context.Background(), "chat_write_mode")
	gotPlan, _ := st.GetSetting(context.Background(), "plan_write_mode")
	if gotChat != domain.WriteModeDeny || gotPlan != domain.WriteModeAllow {
		t.Fatalf("SQLite settings mismatch: chat_write_mode=%q plan_write_mode=%q", gotChat, gotPlan)
	}
}

func TestWriteCommandCollisionProtection(t *testing.T) {
	_, msg, cleanup := setupWriteTestEnv(t)
	defer cleanup()

	mustBeSubcommand := [][]string{
		{"write"},
		{"write", "deny"},
		{"write", "ask"},
		{"write", "allow"},
		{"write", "on"},
		{"write", "off"},
		{"write", "ro"},
		{"write", "status"},
		{"запись", "разрешить"},
	}
	for _, args := range mustBeSubcommand {
		if !isWriteSubcommandArgs(args) {
			t.Errorf("isWriteSubcommandArgs(%v) = false, want true", args)
		}
	}

	mustBePrompt := [][]string{
		{"write", "tests"},
		{"write", "docs"},
		{"write", "a", "new", "endpoint"},
		{"write", "a", "python", "script"},
		{"write", "unit", "tests", "for", "handler"},
	}
	for _, args := range mustBePrompt {
		if isWriteSubcommandArgs(args) {
			t.Errorf("isWriteSubcommandArgs(%v) = true, want false (must not hijack user prompt)", args)
		}
	}

	// End-to-end check: `/plan write a new endpoint` must NOT change PlanWriteMode.
	config.ProjectState.SetPlanWriteMode(domain.WriteModeDeny)
	config.ProjectState.Lock()
	config.ProjectState.CurrentProject = "" // forces handleCreatePlanTask to reject early without launching agent
	config.ProjectState.Unlock()

	_ = handlePlan(&mock.Session{
		M:       msg,
		ChatID:  testChatID,
		TextVal: "/plan write a new endpoint",
		ArgsVal: []string{"write", "a", "new", "endpoint"},
	})
	if config.ProjectState.GetPlanWriteMode() != domain.WriteModeDeny {
		t.Fatalf("PlanWriteMode was modified by `/plan write a new endpoint`")
	}
	if last := msg.LastSent(); last != nil && strings.Contains(last.Text, "Usage:") {
		t.Fatalf("unexpected write usage message for task prompt: %q", last.Text)
	}
}

func TestRunningTaskWorkDirsNoSelfBlock(t *testing.T) {
	_, _, cleanup := setupWriteTestEnv(t)
	defer cleanup()

	root := t.TempDir()
	planDir := filepath.Join(root, "proj-plan")
	busyDir := filepath.Join(root, "proj-busy")
	doneDir := filepath.Join(root, "proj-done")
	freeDir := filepath.Join(root, "datasets")
	for _, d := range []string{planDir, busyDir, doneDir, freeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tPlan := domain.GlobalTaskManager.CreateTaskWithPlan("proj-plan", "m", "Plan task", testChatID, true)
	tPlan.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusPlanning })

	tBusy := domain.GlobalTaskManager.CreateTask("proj-busy", "m", "Running task", testChatID)
	tBusy.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusRunning })

	tDone := domain.GlobalTaskManager.CreateTask("proj-done", "m", "Done task", testChatID)
	tDone.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusCompleted })

	dirs := runningTaskWorkDirs(root, tPlan.Snapshot().ID)
	if len(dirs) != 1 || dirs[0] != busyDir {
		t.Fatalf("runningTaskWorkDirs(exclude=%d) = %v, want [%s]", tPlan.Snapshot().ID, dirs, busyDir)
	}

	prevPolicy := config.WritePolicySnapshot()
	config.SetWritePolicy(config.WritePolicyConfig{
		ScriptsDir:          filepath.Join(root, "scripts"),
		ChatWritePaths:      []string{freeDir},
		ChatApprovalTimeout: time.Second,
	})
	defer config.SetWritePolicy(prevPolicy)

	config.ProjectState.SetPlanWriteMode(domain.WriteModeAllow)
	ctrl := newWritePermissionController(nil, testChatID, writePermScopePlan, tPlan.Snapshot().ID, "proj-plan", root, i18n.Default)

	// Allowed outside busyDir (and not self-blocked by own planning task).
	dec, reason := ctrl.DecidePermission(context.Background(), ports.PermissionRequest{
		ToolName: "write_file",
		Kind:     "edit",
		WorkDir:  planDir,
		Paths:    []string{filepath.Join(freeDir, "candles.csv")},
	})
	if dec == ports.PermissionDeny {
		t.Fatalf("expected write to freeDir during planning to succeed, got deny: %q", reason)
	}

	// Blocked inside busyDir where another active task runs.
	dec, reason = ctrl.DecidePermission(context.Background(), ports.PermissionRequest{
		ToolName: "write_file",
		Kind:     "edit",
		WorkDir:  planDir,
		Paths:    []string{filepath.Join(busyDir, "main.go")},
	})
	if dec != ports.PermissionDeny || !strings.Contains(reason, "proj-busy") {
		t.Fatalf("expected busy project write blocked, got (%v, %q)", dec, reason)
	}
}

func TestWritePermissionControllerMatrix(t *testing.T) {
	_, _, cleanup := setupWriteTestEnv(t)
	defer cleanup()

	base := t.TempDir()
	workDir := filepath.Join(base, "repo")
	scriptsDir := filepath.Join(base, "scripts")
	autoDir := "/srv/test-moex-cache"
	otherDir := "/srv/test-custom-out"
	busyDir := filepath.Join(base, "busy-proj")
	for _, d := range []string{workDir, scriptsDir, busyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	busyTask := domain.GlobalTaskManager.CreateTask("busy-proj", "m", "Busy", testChatID)
	busyTask.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusRunning })

	prevPolicy := config.WritePolicySnapshot()
	config.SetWritePolicy(config.WritePolicyConfig{
		ScriptsDir:          scriptsDir,
		ChatWritePaths:      []string{autoDir},
		ChatApprovalTimeout: 2 * time.Second,
	})
	defer config.SetWritePolicy(prevPolicy)

	scriptPy := filepath.Join(scriptsDir, "load.py")

	// 1. Diagnostics allowed without buttons even in deny mode.
	config.ProjectState.SetChatWriteMode(domain.WriteModeDeny)
	denyCtrl := newWritePermissionController(newSignalMessenger(), testChatID, writePermScopeChat, 0, "repo", base, "ru")
	for _, cmd := range []string{
		"systemctl status bro-bot",
		"journalctl -u bro-bot -n 20",
		"cat /etc/os-release",
		`python3 -c "print(open('` + scriptPy + `').read())"`,
	} {
		dec, reason := denyCtrl.DecidePermission(context.Background(), ports.PermissionRequest{
			ToolName: "run_command", Kind: "execute", WorkDir: workDir, Command: cmd,
		})
		if dec != ports.PermissionAllowOnce {
			t.Fatalf("deny mode expected diagnostic %q allowed, got (%v, %q)", cmd, dec, reason)
		}
	}
	if dec, _ := denyCtrl.DecidePermission(context.Background(), ports.PermissionRequest{
		ToolName: "write_file", Kind: "edit", WorkDir: workDir, Paths: []string{scriptPy},
	}); dec != ports.PermissionDeny {
		t.Fatalf("deny mode must block writing SCRIPTS_DIR")
	}

	// 2. Hard guardrails blocked even in allow mode.
	config.ProjectState.SetChatWriteMode(domain.WriteModeAllow)
	allowMessenger := newSignalMessenger()
	allowCtrl := newWritePermissionController(allowMessenger, testChatID, writePermScopeChat, 0, "repo", base, "ru")

	for _, req := range []ports.PermissionRequest{
		{ToolName: "run_command", Kind: "execute", Command: "git commit -m wip"},
		{ToolName: "run_command", Kind: "execute", Command: "sudo id"},
		{ToolName: "run_command", Kind: "execute", Command: "systemctl restart bro-bot"},
		{ToolName: "run_command", Kind: "execute", Command: "rm -rf " + autoDir},
		{ToolName: "write_file", Kind: "edit", Paths: []string{"/etc/passwd"}},
		{ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(busyDir, "a.go")}},
		{ToolName: "mcp_github_issue", RawInput: map[string]any{"ServerName": "github"}},
		{ToolName: "invoke_subagent", RawInput: map[string]any{"TypeName": "self"}},
	} {
		if dec, _ := allowCtrl.DecidePermission(context.Background(), req); dec != ports.PermissionDeny {
			t.Fatalf("allow mode guardrail must block %+v, got %v", req, dec)
		}
	}

	// 3. In allow mode: SCRIPTS_DIR write is immediate, while SCRIPTS_DIR exec STILL asks 2 buttons!
	if dec, reason := allowCtrl.DecidePermission(context.Background(), ports.PermissionRequest{
		ToolName: "write_file", Kind: "edit", WorkDir: workDir, Paths: []string{scriptPy},
	}); dec != ports.PermissionAllowOnce {
		t.Fatalf("allow mode SCRIPTS_DIR write = (%v, %q), want allow_once", dec, reason)
	}

	execDone := make(chan ports.PermissionDecision, 1)
	go func() {
		dec, _ := allowCtrl.DecidePermission(context.Background(), ports.PermissionRequest{
			ToolName: "run_command", Kind: "execute", WorkDir: workDir, Command: "python3 " + scriptPy,
		})
		execDone <- dec
	}()
	card := allowMessenger.waitCard(t, time.Second)
	if _, hasTurn := findActionPayload(card.sent.Opts.Keyboard, "perm_turn"); hasTurn {
		t.Fatalf("SCRIPTS_DIR execution card must NOT include perm_turn button")
	}
	onceID, ok := findActionPayload(card.sent.Opts.Keyboard, "perm_once")
	if !ok {
		t.Fatalf("SCRIPTS_DIR execution card missing perm_once")
	}
	if err := onPermOnce(&mock.Session{
		M: allowMessenger.Messenger, ChatID: testChatID,
		CB: &ports.CallbackQuery{ID: "cb-once", Chat: testChatID, Action: "perm_once", Payload: onceID, Message: &card.ref},
	}); err != nil {
		t.Fatal(err)
	}
	if dec := <-execDone; dec != ports.PermissionAllowOnce {
		t.Fatalf("SCRIPTS_DIR exec decision = %v, want PermissionAllowOnce", dec)
	}

	// 4. In ask mode: CHAT_WRITE_PATHS auto-allowed, SCRIPTS_DIR write shows 2 buttons,
	// spoofed perm_turn is rejected, concurrent outside writes share perm_turn serialization.
	config.ProjectState.SetChatWriteMode(domain.WriteModeAsk)
	askMessenger := newSignalMessenger()
	askCtrl := newWritePermissionController(askMessenger, testChatID, writePermScopeChat, 0, "repo", base, "en")

	if dec, reason := askCtrl.DecidePermission(context.Background(), ports.PermissionRequest{
		ToolName: "run_command", Kind: "execute", WorkDir: workDir,
		Command: "curl -s https://example.com -o " + filepath.Join(autoDir, "moex.json"),
	}); dec != ports.PermissionAllowOnce {
		t.Fatalf("CHAT_WRITE_PATHS download failed auto-allow: (%v, %q)", dec, reason)
	}

	// SCRIPTS_DIR write in ask -> 2 buttons only, and spoofed perm_turn fails cleanly.
	swDone := make(chan ports.PermissionDecision, 1)
	go func() {
		dec, _ := askCtrl.DecidePermission(context.Background(), ports.PermissionRequest{
			ToolName: "write_file", Kind: "edit", WorkDir: workDir, Paths: []string{scriptPy},
		})
		swDone <- dec
	}()
	swCard := askMessenger.waitCard(t, time.Second)
	if _, hasTurn := findActionPayload(swCard.sent.Opts.Keyboard, "perm_turn"); hasTurn {
		t.Fatalf("ask mode SCRIPTS_DIR write must NOT expose perm_turn")
	}
	swID, _ := findActionPayload(swCard.sent.Opts.Keyboard, "perm_once")
	spoofSess := &mock.Session{
		M: askMessenger.Messenger, ChatID: testChatID,
		CB: &ports.CallbackQuery{ID: "cb-spoof", Chat: testChatID, Action: "perm_turn", Payload: swID, Message: &swCard.ref},
	}
	_ = onPermTurn(spoofSess)
	if len(spoofSess.Responses) == 0 || !strings.Contains(spoofSess.Responses[0], "one-time") {
		t.Fatalf("expected spoofed perm_turn rejected cleanly, got %v", spoofSess.Responses)
	}
	_ = onPermDeny(&mock.Session{
		M: askMessenger.Messenger, ChatID: testChatID,
		CB: &ports.CallbackQuery{ID: "cb-deny", Chat: testChatID, Action: "perm_deny", Payload: swID, Message: &swCard.ref},
	})
	if dec := <-swDone; dec != ports.PermissionDeny {
		t.Fatalf("expected denied SCRIPTS_DIR write, got %v", dec)
	}

	// Concurrent outside writes: press perm_turn on first -> second approves without a second card!
	turnCtx, turnCancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		turnCancel()
		wg.Wait()
	}()
	decs := make([]ports.PermissionDecision, 2)
	for i := 0; i < 2; i++ {
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			decs[idx], _ = askCtrl.DecidePermission(turnCtx, ports.PermissionRequest{
				ToolName: "write_file", Kind: "edit", WorkDir: workDir,
				Paths: []string{filepath.Join(otherDir, "out.csv")},
			})
		}()
	}
	turnCard := askMessenger.waitCard(t, time.Second)
	turnID, hasTurn := findActionPayload(turnCard.sent.Opts.Keyboard, "perm_turn")
	if !hasTurn {
		t.Fatalf("expected perm_turn on outside write card")
	}
	_ = onPermTurn(&mock.Session{
		M: askMessenger.Messenger, ChatID: testChatID,
		CB: &ports.CallbackQuery{ID: "cb-turn", Chat: testChatID, Action: "perm_turn", Payload: turnID, Message: &turnCard.ref},
	})
	wg.Wait()
	if decs[0] != ports.PermissionAllowAlways || decs[1] != ports.PermissionAllowAlways {
		t.Fatalf("expected both concurrent writes approved via allowTurn, got %v", decs)
	}
	select {
	case extra := <-askMessenger.sentCh:
		t.Fatalf("unexpected second confirmation card sent after perm_turn: %+v", extra)
	default:
	}

	// Even with perm_turn active: hard guardrails still block, and SCRIPTS_DIR exec STILL asks 2 buttons.
	if dec, _ := askCtrl.DecidePermission(context.Background(), ports.PermissionRequest{
		ToolName: "run_command", Kind: "execute", Command: "git commit -m wip",
	}); dec != ports.PermissionDeny {
		t.Fatalf("hard guardrail must block even after perm_turn, got %v", dec)
	}
	scExecAfterTurn := make(chan ports.PermissionDecision, 1)
	go func() {
		d, _ := askCtrl.DecidePermission(turnCtx, ports.PermissionRequest{
			ToolName: "run_command", Kind: "execute", WorkDir: workDir, Command: "python3 " + scriptPy,
		})
		scExecAfterTurn <- d
	}()
	scAfterCard := askMessenger.waitCard(t, time.Second)
	if _, hasTurnBtn := findActionPayload(scAfterCard.sent.Opts.Keyboard, "perm_turn"); hasTurnBtn {
		t.Fatalf("SCRIPTS_DIR exec after perm_turn must still omit perm_turn")
	}
	scAfterID, _ := findActionPayload(scAfterCard.sent.Opts.Keyboard, "perm_once")
	_ = onPermOnce(&mock.Session{
		M: askMessenger.Messenger, ChatID: testChatID,
		CB: &ports.CallbackQuery{ID: "cb-sc-after", Chat: testChatID, Action: "perm_once", Payload: scAfterID, Message: &scAfterCard.ref},
	})
	if d := <-scExecAfterTurn; d != ports.PermissionAllowOnce {
		t.Fatalf("expected SCRIPTS_DIR exec after perm_turn to approve once, got %v", d)
	}
	turnCancel()

	// 5. Timeout edits card and expires callback, context cancel unblocks cleanly.
	config.SetWritePolicy(config.WritePolicyConfig{
		ScriptsDir:          scriptsDir,
		ChatWritePaths:      []string{autoDir},
		ChatApprovalTimeout: 25 * time.Millisecond,
	})
	toMessenger := newSignalMessenger()
	toCtrl := newWritePermissionController(toMessenger, testChatID, writePermScopeChat, 0, "repo", base, "en")
	dec, reason := toCtrl.DecidePermission(context.Background(), ports.PermissionRequest{
		ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(otherDir, "timeout.csv")},
	})
	if dec != ports.PermissionDeny || !strings.Contains(reason, "timed out") {
		t.Fatalf("expected timeout denial, got (%v, %q)", dec, reason)
	}
	toCard := toMessenger.waitCard(t, time.Second)
	expiredID, _ := findActionPayload(toCard.sent.Opts.Keyboard, "perm_once")
	lateSess := &mock.Session{
		M: toMessenger.Messenger, ChatID: testChatID,
		CB: &ports.CallbackQuery{ID: "cb-late", Chat: testChatID, Action: "perm_once", Payload: expiredID, Message: &toCard.ref},
	}
	_ = onPermOnce(lateSess)
	if len(lateSess.Responses) == 0 || !strings.Contains(lateSess.Responses[0], "expired") {
		t.Fatalf("expected expired toast after timeout, got %v", lateSess.Responses)
	}
	if edited := toMessenger.LastEdited(); edited == nil || !strings.Contains(edited.Text, "timeout") {
		t.Fatalf("expected edited timeout card, got %+v", edited)
	}

	// Cancel context while waiting
	config.SetWritePolicy(config.WritePolicyConfig{
		ScriptsDir:          scriptsDir,
		ChatWritePaths:      []string{autoDir},
		ChatApprovalTimeout: 5 * time.Second,
	})
	cancelMessenger := newSignalMessenger()
	cancelCtrl := newWritePermissionController(cancelMessenger, testChatID, writePermScopeChat, 0, "repo", base, "en")
	ctx, cancel := context.WithCancel(context.Background())
	cancelDone := make(chan ports.PermissionDecision, 1)
	go func() {
		d, _ := cancelCtrl.DecidePermission(ctx, ports.PermissionRequest{
			ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(otherDir, "cancel.csv")},
		})
		cancelDone <- d
	}()
	_ = cancelMessenger.waitCard(t, time.Second)
	cancel()
	if d := <-cancelDone; d != ports.PermissionDeny {
		t.Fatalf("expected PermissionDeny on context cancel, got %v", d)
	}
	if edited := cancelMessenger.LastEdited(); edited == nil || !strings.Contains(edited.Text, "Cancelled") {
		t.Fatalf("expected edited cancelled card, got %+v", edited)
	}
}
