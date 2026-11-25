package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"
	"bro-bot/internal/tools"
)

type fakeBotHandler struct {
	calls []string
}

func (f *fakeBotHandler) CallBotTool(_ context.Context, name string, args map[string]interface{}) ports.BotToolResult {
	f.calls = append(f.calls, name)
	return ports.BotToolResult{Text: "called:" + name}
}

func TestToolServerHTTPFlowAndSecurity(t *testing.T) {
	handler := &fakeBotHandler{}
	srv, err := StartToolServer(handler, "secret-tok-123")
	if err != nil {
		t.Fatalf("StartToolServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	cfg := srv.ServerConfig()
	if cfg.Type != "http" || cfg.Name != "bro_bot" || len(cfg.Headers) != 1 || cfg.Headers[0].Value != "Bearer secret-tok-123" {
		t.Fatalf("unexpected ServerConfig: %+v", cfg)
	}

	client := &http.Client{}
	post := func(auth, body string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL(), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Без токена и с чужим токеном — 401
	for _, badAuth := range []string{"", "Bearer wrong"} {
		resp := post(badAuth, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("auth=%q: status = %d, want 401", badAuth, resp.StatusCode)
		}
	}

	// GET — 405
	getReq, _ := http.NewRequest(http.MethodGet, srv.URL(), nil)
	getReq.Header.Set("Authorization", "Bearer secret-tok-123")
	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", getResp.StatusCode)
	}

	// server/discover -> 200
	discResp := post("Bearer secret-tok-123", `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`)
	discResp.Body.Close()
	if discResp.StatusCode != http.StatusOK {
		t.Fatalf("server/discover status = %d, want 200", discResp.StatusCode)
	}

	// notification -> 202
	noteResp := post("Bearer secret-tok-123", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	noteResp.Body.Close()
	if noteResp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification status = %d, want 202", noteResp.StatusCode)
	}

	// tools/list -> все 8 инструментов бота
	listResp := post("Bearer secret-tok-123", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	listBody, _ := io.ReadAll(listResp.Body)
	listResp.Body.Close()
	for _, want := range []string{
		mcp.ToolSendMessage, mcp.ToolAskUser, mcp.ToolReportProgress,
		mcp.ToolSendFile, mcp.ToolGetTaskContext, mcp.ToolListTasks, mcp.ToolListProjects, mcp.ToolSQLiteQuery,
	} {
		if !strings.Contains(string(listBody), want) {
			t.Errorf("tools/list missing %q: %s", want, listBody)
		}
	}

	// tools/call -> попадает в handler
	callResp := post("Bearer secret-tok-123", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}`)
	callBody, _ := io.ReadAll(callResp.Body)
	callResp.Body.Close()
	if !strings.Contains(string(callBody), "called:list_projects") {
		t.Fatalf("unexpected tools/call body: %s", callBody)
	}

	// DELETE без токена -> 401
	delUnauth, _ := http.NewRequest(http.MethodDelete, srv.URL(), nil)
	delUnauthResp, err := client.Do(delUnauth)
	if err != nil {
		t.Fatal(err)
	}
	delUnauthResp.Body.Close()
	if delUnauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth DELETE status = %d, want 401", delUnauthResp.StatusCode)
	}

	// DELETE с токеном -> 204 No Content
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL(), nil)
	delReq.Header.Set("Authorization", "Bearer secret-tok-123")
	delResp, err := client.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", delResp.StatusCode)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestChoosePermissionAgyAndGrokReadOnlyMatrix(t *testing.T) {
	opts := []PermOption{
		{OptionID: "allow_once", Kind: "allow_once"},
		{OptionID: "allow_always", Kind: "allow_always"},
		{OptionID: "reject_once", Kind: "reject_once"},
	}

	cases := []struct {
		name     string
		readOnly bool
		call     ToolCallPermission
		want     string
	}{
		{
			name:     "agy call_mcp_tool bro_bot in ReadOnly allowed",
			readOnly: true,
			call: ToolCallPermission{
				Kind:     "execute",
				Name:     "call_mcp_tool",
				Title:    "MCP Tool list_projects",
				RawInput: map[string]any{"ServerName": "bro_bot", "ToolName": "list_projects"},
			},
			want: "allow_once",
		},
		{
			name:     "agy call_mcp_tool foreign server in ReadOnly rejected",
			readOnly: true,
			call: ToolCallPermission{
				Kind:     "execute",
				Name:     "call_mcp_tool",
				Title:    "MCP Tool send_message",
				RawInput: map[string]any{"ServerName": "slack", "ToolName": "send_message"},
			},
			want: "reject_once",
		},
		{
			name:     "agy ask_question in ReadOnly allowed",
			readOnly: true,
			call:     ToolCallPermission{Kind: "other", Name: "ask_question", Title: "Run ask_question"},
			want:     "allow_once",
		},
		{
			name:     "agy safe read-only run_command in ReadOnly allowed",
			readOnly: true,
			call: ToolCallPermission{
				Kind:     "execute",
				Name:     "run_command",
				Title:    "git status -sb && ls -la",
				RawInput: map[string]any{"CommandLine": "git status -sb && ls -la"},
			},
			want: "allow_once",
		},
		{
			name:     "agy mutating run_command in ReadOnly rejected",
			readOnly: true,
			call:     ToolCallPermission{Kind: "execute", Name: "run_command", Title: "echo hi > out.txt"},
			want:     "reject_once",
		},
		{
			name:     "grok search_tool in ReadOnly allowed",
			readOnly: true,
			call:     ToolCallPermission{Kind: "other", Title: `Search tools: "bro_bot"`, RawInput: map[string]any{"query": "bro_bot"}},
			want:     "allow_once",
		},
		{
			name:     "grok use_tool bro_bot in ReadOnly allowed",
			readOnly: true,
			call:     ToolCallPermission{Kind: "other", Title: "use_tool", RawInput: map[string]any{"tool_name": "bro_bot__get_task_context"}},
			want:     "allow_once",
		},
		{
			name:     "grok use_tool foreign server in ReadOnly rejected",
			readOnly: true,
			call:     ToolCallPermission{Kind: "other", Title: "use_tool", RawInput: map[string]any{"tool_name": "github__create_issue"}},
			want:     "reject_once",
		},
		{
			name:     "grok safe read-only bash in ReadOnly allowed",
			readOnly: true,
			call:     ToolCallPermission{Kind: "execute", Title: "bash", RawInput: map[string]any{"command": "git status -sb"}},
			want:     "allow_once",
		},
		{
			name:     "grok bash in ReadOnly rejected",
			readOnly: true,
			call:     ToolCallPermission{Kind: "execute", Title: "bash"},
			want:     "reject_once",
		},
		{
			name:     "agy call_mcp_tool sqlite_query in ReadOnly allowed",
			readOnly: true,
			call: ToolCallPermission{
				Kind:     "execute",
				Name:     "call_mcp_tool",
				Title:    "MCP Tool sqlite_query",
				RawInput: map[string]any{"ServerName": "bro_bot", "ToolName": "sqlite_query"},
			},
			want: "allow_once",
		},
		{
			name:     "agy safe read-only python3 run_command in ReadOnly allowed",
			readOnly: true,
			call: ToolCallPermission{
				Kind:     "execute",
				Name:     "run_command",
				Title:    "python3 -c 'print(1)'",
				RawInput: map[string]any{"CommandLine": "python3 -c 'print(1)'"},
			},
			want: "allow_once",
		},
		{
			name:     "agy safe read-only sqlite3 and curl in ReadOnly allowed",
			readOnly: true,
			call: ToolCallPermission{
				Kind:     "execute",
				Name:     "run_command",
				Title:    "curl",
				RawInput: map[string]any{"CommandLine": `curl -fsSL "https://api.example.com/candles" | python3 -`},
			},
			want: "allow_once",
		},
		{
			name:     "agy mutating deploy script and curl POST in ReadOnly rejected",
			readOnly: true,
			call: ToolCallPermission{
				Kind:     "execute",
				Name:     "run_command",
				Title:    "./scripts/deploy-release.sh",
				RawInput: map[string]any{"CommandLine": "./scripts/deploy-release.sh"},
			},
			want: "reject_once",
		},
		{
			name:     "agy curl POST in ReadOnly rejected",
			readOnly: true,
			call: ToolCallPermission{
				Kind:     "execute",
				Name:     "run_command",
				Title:    "curl POST",
				RawInput: map[string]any{"CommandLine": `curl -X POST "https://api.example.com/order"`},
			},
			want: "reject_once",
		},
		{
			name:     "task mode allows write/execute always",
			readOnly: false,
			call:     ToolCallPermission{Kind: "execute", Name: "run_command", Title: "go test ./..."},
			want:     "allow_always",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ChoosePermission(tc.readOnly, tc.call, opts)
			if !ok || got != tc.want {
				t.Fatalf("ChoosePermission = (%q, %v), want (%q, true)", got, ok, tc.want)
			}
		})
	}
}

func TestTranslateUpdateNormalizesAgyMCPToolAndSummary(t *testing.T) {
	raw := []byte(`{"update":{"sessionUpdate":"tool_call","toolCallId":"c1","name":"call_mcp_tool","title":"MCP Tool list_projects","kind":"execute","status":"in_progress","rawInput":{"ServerName":"bro_bot","ToolName":"list_projects"}}}`)
	evt, ok := TranslateUpdate("s1", raw, 1)
	if !ok || evt.StepUpdate == nil {
		t.Fatalf("expected event, got ok=%v", ok)
	}
	if evt.StepUpdate.ToolName != "mcp__bro_bot__list_projects" {
		t.Errorf("ToolName = %q, want mcp__bro_bot__list_projects", evt.StepUpdate.ToolName)
	}

	rawArgs := []byte(`{"update":{"sessionUpdate":"tool_call","toolCallId":"c2","name":"run_command","title":"go test","kind":"execute","status":"in_progress","args":{"CommandLine":"go test","toolSummary":"Run unit tests"}}}`)
	evt, ok = TranslateUpdate("s1", rawArgs, 2)
	if !ok || evt.StepUpdate.ToolInfo == nil || evt.StepUpdate.ToolInfo.Parameters["toolSummary"] != "Run unit tests" {
		t.Fatalf("expected toolSummary from args, got %+v", evt.StepUpdate)
	}
}

func TestParseUsageSupportsAgyAndGrokShapes(t *testing.T) {
	agyRaw := json.RawMessage(`{"stopReason":"end_turn","usage":{"inputTokens":120,"outputTokens":30,"cachedReadTokens":10,"thoughtTokens":5,"totalTokens":165}}`)
	u1 := ParseUsage(agyRaw)
	if u1 == nil || u1.InputTokens != 120 || u1.OutputTokens != 30 || u1.CacheReadTokens != 10 || u1.ThinkingTokens != 5 || u1.TotalTokens != 165 {
		t.Fatalf("unexpected agy usage: %+v", u1)
	}

	grokRaw := json.RawMessage(`{"stopReason":"end_turn","_meta":{"usage":{"inputTokens":200,"outputTokens":45,"cachedReadTokens":50,"reasoningTokens":15,"totalTokens":245}}}`)
	u2 := ParseUsage(grokRaw)
	if u2 == nil || u2.InputTokens != 200 || u2.OutputTokens != 45 || u2.CacheReadTokens != 50 || u2.ThinkingTokens != 15 || u2.TotalTokens != 245 {
		t.Fatalf("unexpected grok usage: %+v", u2)
	}

	if got := ParseUsage(json.RawMessage(`{"stopReason":"end_turn"}`)); got != nil {
		t.Fatalf("expected nil for empty usage, got %+v", got)
	}
}

func TestConnRoundTripAndEncodeEvent(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	var out bytes.Buffer
	conn := NewConn(pr, &out)
	if err := conn.Notify("session/cancel", map[string]string{"sessionId": "s1"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !strings.Contains(out.String(), `"method":"session/cancel"`) {
		t.Errorf("unexpected notify JSON: %s", out.String())
	}

	data, err := EncodeEvent(StreamEvent{Event: "init", ConversationID: "s1"})
	if err != nil || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("EncodeEvent: err=%v data=%q", err, data)
	}
}

func TestPermissionsBotPrefixesForeignServersSubagentAndFallback(t *testing.T) {
	opts := []PermOption{
		{OptionID: "allow_once", Kind: "allow_once"},
		{OptionID: "allow_always", Kind: "allow_always"},
		{OptionID: "reject_once", Kind: "reject_once"},
	}

	cases := []struct {
		name     string
		readOnly bool
		call     ToolCallPermission
		opts     []PermOption
		wantID   string
		wantOK   bool
	}{
		{
			name:     "mcp_bro_bot_ask_user single-underscore allowed",
			readOnly: true,
			call:     ToolCallPermission{Kind: "other", Name: "mcp_bro_bot_ask_user", Title: "ask_user"},
			opts:     opts,
			wantID:   "allow_once",
			wantOK:   true,
		},
		{
			name:     "bare bot tool list_projects allowed",
			readOnly: true,
			call:     ToolCallPermission{Kind: "other", Name: "list_projects"},
			opts:     opts,
			wantID:   "allow_once",
			wantOK:   true,
		},
		{
			name:     "bro_bot unknown destructive suffix rejected",
			readOnly: true,
			call:     ToolCallPermission{Kind: "other", Name: "mcp_bro_bot_rm_rf", Title: "bro_bot_rm_rf"},
			opts:     opts,
			wantID:   "reject_once",
			wantOK:   true,
		},
		{
			name:     "foreign server in nested _meta rejected even with ask_user name",
			readOnly: true,
			call: ToolCallPermission{
				Kind: "other",
				Name: "ask_user",
				Meta: map[string]any{"mcp": map[string]any{"server_name": "slack"}},
			},
			opts:   opts,
			wantID: "reject_once",
			wantOK: true,
		},
		{
			name:     "foreign slash server title send_file rejected",
			readOnly: true,
			call:     ToolCallPermission{Kind: "other", Name: "slack: send_file"},
			opts:     opts,
			wantID:   "reject_once",
			wantOK:   true,
		},
		{
			name:     "invoke_subagent research allowed in ReadOnly",
			readOnly: true,
			call:     ToolCallPermission{Kind: "other", Name: "invoke_subagent", RawInput: map[string]any{"TypeName": "research"}},
			opts:     opts,
			wantID:   "allow_once",
			wantOK:   true,
		},
		{
			name:     "invoke_subagent self rejected in ReadOnly",
			readOnly: true,
			call:     ToolCallPermission{Kind: "other", Name: "invoke_subagent", RawInput: map[string]any{"TypeName": "self"}},
			opts:     opts,
			wantID:   "reject_once",
			wantOK:   true,
		},
		{
			name:     "safe builtin read_file with empty kind allowed",
			readOnly: true,
			call:     ToolCallPermission{Name: "read_file"},
			opts:     opts,
			wantID:   "allow_once",
			wantOK:   true,
		},
		{
			name:     "ChoosePermission deny fallback without reject_once kind",
			readOnly: true,
			call:     ToolCallPermission{Kind: "execute", Name: "run_command"},
			opts:     []PermOption{{OptionID: "allow", Kind: "allow_once"}, {OptionID: "deny_btn", Kind: "deny_custom"}},
			wantID:   "deny_btn",
			wantOK:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ChoosePermission(tc.readOnly, tc.call, tc.opts)
			if ok != tc.wantOK || got != tc.wantID {
				t.Fatalf("ChoosePermission = (%q, %v), want (%q, %v)", got, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

func TestToolCallRegistryAndTranslateUsageAndActiveUpdate(t *testing.T) {
	var reg ToolCallRegistry
	first := []byte(`{"update":{"sessionUpdate":"tool_call","toolCallId":"q-1","name":"mcp_bro_bot_ask_user","kind":"other","status":"pending"}}`)
	reg.Observe(first)
	second := []byte(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"q-1","status":"in_progress","rawInput":{"question":"Pick option?","options":["A","B"]}}}`)
	reg.Observe(second)

	evt, ok := TranslateUpdate("s1", second, 2)
	if !ok || evt.StepUpdate == nil {
		t.Fatalf("expected ACTIVE tool_call_update with rawInput to be preserved, got ok=%v", ok)
	}
	reg.EnrichStepUpdate(evt.StepUpdate)
	if evt.StepUpdate.ToolCallID != "q-1" {
		t.Errorf("ToolCallID = %q, want q-1", evt.StepUpdate.ToolCallID)
	}
	if evt.StepUpdate.ToolName != "mcp__bro_bot__ask_user" {
		t.Errorf("ToolName = %q, want mcp__bro_bot__ask_user", evt.StepUpdate.ToolName)
	}
	if len(evt.StepUpdate.ToolInfo.Parameters) == 0 {
		t.Errorf("expected parameters preserved")
	}

	enrichedPerm := reg.Enrich(ToolCallPermission{ToolCallID: "q-1", Kind: "other"})
	if !IsBotOrInteractiveTool(enrichedPerm) {
		t.Errorf("expected enriched permission from ToolCallRegistry to allow bro_bot ask_user")
	}

	usageEvt, ok := TranslateUpdate("s1", []byte(`{"update":{"sessionUpdate":"usage_update","used":4200,"size":200000,"usage":{"inputTokens":3000,"outputTokens":200,"cacheReadTokens":1000}}}`), 3)
	if !ok || usageEvt.StepUpdate == nil || usageEvt.StepUpdate.Usage == nil {
		t.Fatalf("expected usage_update event, got ok=%v", ok)
	}
	u := usageEvt.StepUpdate.Usage
	if u.InputTokens != 3000 || u.OutputTokens != 200 || u.CacheReadTokens != 1000 || u.TotalTokens != 3200 {
		t.Errorf("unexpected usage_update translation: %+v", u)
	}

	usedOnly, ok := TranslateUpdate("s1", []byte(`{"update":{"sessionUpdate":"usage_update","used":1234,"size":100000}}`), 4)
	if !ok || usedOnly.StepUpdate == nil || usedOnly.StepUpdate.Usage == nil || usedOnly.StepUpdate.Usage.TotalTokens != 1234 {
		t.Fatalf("expected used fallback usage, got %+v ok=%v", usedOnly, ok)
	}
}

type fakeACPPermHandler struct {
	decision ports.PermissionDecision
	reason   string
	calls    []ports.PermissionRequest
}

func (f *fakeACPPermHandler) DecidePermission(_ context.Context, req ports.PermissionRequest) (ports.PermissionDecision, string) {
	f.calls = append(f.calls, req)
	return f.decision, f.reason
}

func TestResolvePermissionControlledModes(t *testing.T) {
	base := t.TempDir()
	scriptsDir := filepath.Join(base, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPy := filepath.Join(scriptsDir, "load.py")
	opts := []PermOption{
		{OptionID: "allow_always_id", Kind: "allow_always"},
		{OptionID: "allow_once_id", Kind: "allow_once"},
		{OptionID: "reject_once_id", Kind: "reject_once"},
	}

	// 1. Strict deny (handler == nil): blocks SCRIPTS_DIR exec and python3 writing /etc,
	// while allowing read-only diagnostics and analytical python3 reading SCRIPTS_DIR.
	id, ok, allowed, reason := ResolvePermission(context.Background(), true, scriptsDir, base, nil, ToolCallPermission{
		ToolName: "run_command",
		Kind:     "execute",
		RawInput: map[string]any{"command": "python3 " + scriptPy},
	}, opts)
	if !ok || allowed || id != "reject_once_id" || reason == "" {
		t.Fatalf("deny mode SCRIPTS_DIR exec = (%q, %v, %v, %q), want rejected", id, ok, allowed, reason)
	}

	id, ok, allowed, reason = ResolvePermission(context.Background(), true, scriptsDir, base, nil, ToolCallPermission{
		ToolName: "run_command",
		Kind:     "execute",
		RawInput: map[string]any{"command": `python3 -c "open('/etc/cron.d/evil', 'w').write('x')"`},
	}, opts)
	if !ok || allowed || id != "reject_once_id" || reason == "" {
		t.Fatalf("deny mode python3 writing /etc = (%q, %v, %v, %q), want blocked", id, ok, allowed, reason)
	}

	id, ok, allowed, _ = ResolvePermission(context.Background(), true, scriptsDir, base, nil, ToolCallPermission{
		ToolName: "run_command",
		Kind:     "execute",
		RawInput: map[string]any{"command": `python3 -c "print(open('` + scriptPy + `').read())"`},
	}, opts)
	if !ok || !allowed || id != "allow_once_id" {
		t.Fatalf("deny mode analytical python3 read SCRIPTS_DIR = (%q, %v, %v), want allow_once", id, ok, allowed)
	}

	for _, cmd := range []string{"systemctl status bro-bot", "cat /etc/os-release"} {
		id, ok, allowed, _ = ResolvePermission(context.Background(), true, scriptsDir, base, nil, ToolCallPermission{
			ToolName: "run_command",
			Kind:     "execute",
			RawInput: map[string]any{"command": cmd},
		}, opts)
		if !ok || !allowed || id != "allow_once_id" {
			t.Fatalf("deny mode %q = (%q, %v, %v), want allow_once", cmd, id, ok, allowed)
		}
	}

	// 2. Handler returns PermissionAllowAlways -> ACP MUST STILL receive allow_once (never allow_always).
	hAllow := &fakeACPPermHandler{decision: ports.PermissionAllowAlways}
	id, ok, allowed, _ = ResolvePermission(context.Background(), true, scriptsDir, base, hAllow, ToolCallPermission{
		ToolName: "write_file",
		Kind:     "edit",
		Locations: []struct {
			Path string `json:"path"`
		}{{Path: "/tmp/moex/data.csv"}},
	}, opts)
	if !ok || !allowed || id != "allow_once_id" {
		t.Fatalf("interactive PermissionAllowAlways = (%q, %v, %v), want allow_once_id", id, ok, allowed)
	}
	if len(hAllow.calls) != 1 || len(hAllow.calls[0].Paths) != 1 || hAllow.calls[0].Paths[0] != "/tmp/moex/data.csv" {
		t.Fatalf("expected Locations path forwarded to PermissionHandler, got %+v", hAllow.calls)
	}

	// 3. Handler returns PermissionDeny -> reject_once and non-empty reason.
	hDeny := &fakeACPPermHandler{decision: ports.PermissionDeny, reason: "user rejected"}
	id, ok, allowed, reason = ResolvePermission(context.Background(), true, scriptsDir, base, hDeny, ToolCallPermission{
		ToolName: "run_command",
		Kind:     "execute",
		RawInput: map[string]any{"command": "bash " + filepath.Join(scriptsDir, "run.sh")},
	}, opts)
	if !ok || allowed || id != "reject_once_id" || reason != "user rejected" {
		t.Fatalf("interactive PermissionDeny = (%q, %v, %v, %q)", id, ok, allowed, reason)
	}

	// 4. DeniedPermission formatting with reason & ControlledDeniedRecoveryPrompt.
	d := NewDeniedPermissionWithReason(ToolCallPermission{ToolName: "write_file", Title: "/etc/hosts"}, tools.CheckWriteGuardrail(ports.PermissionRequest{
		ToolName: "write_file",
		Kind:     "edit",
		Paths:    []string{"/etc/hosts"},
	}, nil).Reason)
	formatted := FormatDeniedPermissions([]DeniedPermission{d})
	if !strings.Contains(formatted, "/etc/hosts") || !strings.Contains(formatted, "forbidden") {
		t.Fatalf("unexpected FormatDeniedPermissions: %q", formatted)
	}
	if p := ControlledDeniedRecoveryPrompt(formatted, true); !strings.Contains(p, "user decision") {
		t.Fatalf("unexpected ControlledDeniedRecoveryPrompt(true): %q", p)
	}
	if p := ControlledDeniedRecoveryPrompt(formatted, false); !strings.Contains(p, "strictly read-only") {
		t.Fatalf("unexpected ControlledDeniedRecoveryPrompt(false): %q", p)
	}
}
