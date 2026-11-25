package grok

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bro-bot/internal/adapters/acp"
	"bro-bot/internal/effort"
	"bro-bot/internal/ports"
)

func TestParseGrokModelsOutput(t *testing.T) {
	raw := "You are logged in.\n\nDefault model: grok-4.7\n\nAvailable models:\n  * grok-4.7 (default)\n  - grok-4.7-build-fast\n  - grok-4.6\n"
	got := parseGrokModelsOutput(raw)
	for _, id := range []string{"grok-4.7 ", "grok-4.7-build-fast", "grok-4.6"} {
		if !strings.Contains(got, id) {
			t.Errorf("в каталоге нет %q:\n%s", id, got)
		}
	}
	if strings.Contains(got, "(default)") {
		t.Errorf("пометка default не должна попадать в имя:\n%s", got)
	}
}

func TestChoosePermission(t *testing.T) {
	opts := []acp.PermOption{
		{OptionID: "yes", Kind: "allow_once"},
		{OptionID: "always", Kind: "allow_always"},
		{OptionID: "no", Kind: "reject_once"},
	}
	if id, ok := acp.ChoosePermission(false, acp.ToolCallPermission{Kind: "execute"}, opts); !ok || id != "always" {
		t.Errorf("задача должна разрешать всегда, получили %q ok=%v", id, ok)
	}
	if id, ok := acp.ChoosePermission(true, acp.ToolCallPermission{Kind: "execute"}, opts); !ok || id != "no" {
		t.Errorf("диалог должен отклонять команду, получили %q ok=%v", id, ok)
	}
	if id, ok := acp.ChoosePermission(true, acp.ToolCallPermission{Kind: "read"}, opts); !ok || id != "yes" {
		t.Errorf("диалог должен разрешать чтение один раз, получили %q ok=%v", id, ok)
	}
}

func TestTranslateUpdateTextAndTool(t *testing.T) {
	text := []byte(`{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hi"}}}`)
	evt, ok := acp.TranslateUpdate("s1", text, 1)
	if !ok || evt.StepUpdate == nil || evt.StepUpdate.TextDelta != "Hi" || evt.StepUpdate.StepType != "agent_response" {
		t.Fatalf("текст: %+v ok=%v", evt, ok)
	}

	tool := []byte(`{"update":{"sessionUpdate":"tool_call","toolCallId":"t","title":"read_file","kind":"read","status":"in_progress","rawInput":{"path":"main.go"}}}`)
	evt, ok = acp.TranslateUpdate("s1", tool, 2)
	if !ok || evt.StepUpdate.ToolName != "read_file" || evt.StepUpdate.State != "ACTIVE" {
		t.Fatalf("инструмент: %+v", evt.StepUpdate)
	}

	thought := []byte(`{"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"secret"}}}`)
	if _, ok := acp.TranslateUpdate("s1", thought, 3); ok {
		t.Fatal("мысли не должны попадать в ответ")
	}
}

func TestGrokEffortMapsMaxToXHigh(t *testing.T) {
	if _, ok := grokEffort(effort.Auto); ok {
		t.Fatal("auto не передаётся")
	}
	got, ok := grokEffort(effort.Max)
	if !ok || got != "xhigh" {
		t.Fatalf("max = %q ok=%v", got, ok)
	}
}

func TestRejectsForeignModels(t *testing.T) {
	if !rejectsModel("gemini-3.1-pro-high") || !rejectsModel("claude-sonnet-5-5") {
		t.Fatal("чужие модели должны отклоняться")
	}
	if rejectsModel("grok-4.7") || rejectsModel("grok-4.6") {
		t.Fatal("модели grok должны приниматься")
	}
}

type fakeGrokBotTools struct {
	calls []string
}

func (f *fakeGrokBotTools) CallBotTool(_ context.Context, name string, _ map[string]interface{}) ports.BotToolResult {
	f.calls = append(f.calls, name)
	return ports.BotToolResult{Text: `{"ok":true}`}
}

func TestExecuteTaskSpeaksACP(t *testing.T) {
	dir := t.TempDir()
	permFile := filepath.Join(dir, "perm")
	script := filepath.Join(dir, "grok")
	if err := os.WriteFile(script, []byte(fakeGrokScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERM_FILE", permFile)
	prev := grokBin
	grokBin = script
	t.Cleanup(func() { grokBin = prev })

	botTools := &fakeGrokBotTools{}
	proc, err := NewAdapter().ExecuteTask(context.Background(), ports.ExecuteArgs{
		Prompt:       "Say hello",
		WorkDir:      dir,
		ModelName:    "gemini-3.1-pro-high",
		ReadOnly:     true,
		Effort:       effort.High,
		ChannelToken: "tok-grok-http",
		BotTools:     botTools,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()

	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	if waitErr := proc.Wait(); waitErr != nil {
		t.Fatalf("wait: %v\n%s", waitErr, out)
	}
	text := string(out)
	for _, want := range []string{
		`"conversation_id":"sess-1"`,
		`"text_delta":"Hello"`,
		`"status":"SUCCESS"`,
		`"input_tokens":321`,
		`"thinking_tokens":44`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %s:\n%s", want, text)
		}
	}
	if len(botTools.calls) != 1 || botTools.calls[0] != "list_projects" {
		t.Errorf("expected HTTP tools/call list_projects in-process, got %v", botTools.calls)
	}
	chosen, err := os.ReadFile(permFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(chosen)) != "yes\nyes\nno" {
		t.Fatalf("в диалоге search_tool и use_tool(bro_bot) разрешены, а bash отклонён; получили %q", chosen)
	}
}

func TestExecuteTaskAllowsToolsWhenNotReadOnly(t *testing.T) {
	dir := t.TempDir()
	permFile := filepath.Join(dir, "perm")
	script := filepath.Join(dir, "grok")
	if err := os.WriteFile(script, []byte(fakeGrokScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERM_FILE", permFile)
	prev := grokBin
	grokBin = script
	t.Cleanup(func() { grokBin = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := NewAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
		Prompt:         "Do the task",
		WorkDir:        dir,
		ConversationID: "missing-session",
		ModelName:      "grok-4.6",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	_ = proc.Wait()
	if !strings.Contains(string(out), "could not be resumed") && !strings.Contains(string(out), "Hello") {
		t.Fatalf("ожидали ответ после неудачного resume:\n%s", out)
	}
	chosen, err := os.ReadFile(permFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(chosen)) != "always\nalways\nalways" {
		t.Fatalf("задача должна разрешать инструменты, выбрано %q\n%s", chosen, out)
	}
}

func TestGrokACPCleansToolServerWhenStartFails(t *testing.T) {
	prev := grokBin
	grokBin = filepath.Join(t.TempDir(), "does-not-exist-grok")
	t.Cleanup(func() { grokBin = prev })

	_, err := NewAdapter().ExecuteTask(context.Background(), ports.ExecuteArgs{
		Prompt:   "hi",
		WorkDir:  t.TempDir(),
		BotTools: &fakeGrokBotTools{},
	})
	if err == nil {
		t.Fatal("expected error when grok binary cannot start")
	}
}

const fakeGrokScript = "#!/usr/bin/env python3\n" + `
import json, os, sys, urllib.request
perm = os.environ.get("PERM_FILE", "")
perms = []
mcp_srv = None

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def http_mcp(srv, method, params=None, msg_id=1):
    headers = {h["name"]: h["value"] for h in srv.get("headers", [])}
    headers["Content-Type"] = "application/json"
    body = json.dumps({"jsonrpc": "2.0", "id": msg_id, "method": method, "params": params or {}}).encode()
    req = urllib.request.Request(srv["url"], data=body, headers=headers, method="POST")
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.loads(resp.read().decode())

def http_delete(srv):
    headers = {h["name"]: h["value"] for h in srv.get("headers", [])}
    req = urllib.request.Request(srv["url"], headers=headers, method="DELETE")
    with urllib.request.urlopen(req, timeout=5) as resp:
        return resp.status

while True:
    line = sys.stdin.readline()
    if not line:
        break
    line = line.strip()
    if not line:
        continue
    msg = json.loads(line)
    method = msg.get("method")
    mid = msg.get("id")
    params = msg.get("params") or {}
    if method == "initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"authMethods":[{"id":"cached_token"}],"agentCapabilities":{"loadSession":True,"sessionCapabilities":{"resume":{}}}}})
    elif method == "session/resume":
        send({"jsonrpc":"2.0","id":mid,"error":{"code":-32002,"message":"unknown session"}})
    elif method == "session/new":
        srvs = params.get("mcpServers") or []
        if srvs and srvs[0].get("type") == "http":
            mcp_srv = srvs[0]
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"sess-1"}})
    elif method == "session/prompt":
        if mcp_srv:
            http_mcp(mcp_srv, "tools/call", {"name": "list_projects", "arguments": {}}, 1)
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello"}}}})
        for req_id, tc in [
            (7, {"kind":"other","title":"Search tools: \"bro_bot\"","rawInput":{"query":"bro_bot"}}),
            (8, {"kind":"other","title":"use_tool","rawInput":{"tool_name":"bro_bot__list_projects"}}),
            (9, {"kind":"execute","title":"bash"}),
        ]:
            send({"jsonrpc":"2.0","id":req_id,"method":"session/request_permission","params":{"toolCall":tc,"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"always","kind":"allow_always"},{"optionId":"no","kind":"reject_once"}]}})
            reply = json.loads(sys.stdin.readline())
            outcome = (reply.get("result") or {}).get("outcome") or {}
            perms.append(outcome.get("optionId") or "")
        send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn","_meta":{"usage":{"inputTokens":321,"outputTokens":55,"cachedReadTokens":12,"reasoningTokens":44,"totalTokens":376}}}})
    elif method == "session/close":
        if mcp_srv:
            assert http_delete(mcp_srv) == 204
        if perm:
            open(perm, "w").write("\n".join(perms))
        send({"jsonrpc":"2.0","id":mid,"result":{}})
        break
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"result":{}})
`

func TestPickAuthMethodPrefersCachedToken(t *testing.T) {
	id, ok := pickAuthMethod([]acp.AuthMethod{{ID: "grok.com"}, {ID: "cached_token"}})
	if !ok || id != "cached_token" {
		t.Fatalf("метод = %q ok=%v", id, ok)
	}
	if _, ok := pickAuthMethod([]acp.AuthMethod{{ID: "grok.com"}}); ok {
		t.Fatal("браузерный вход без токена не должен выбираться")
	}
}

func TestEncodeEventIsOneJSONLine(t *testing.T) {
	data, err := acp.EncodeEvent(acp.StreamEvent{Event: "init", ConversationID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("событие должно быть одной строкой: %q", data)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
}

func TestGrokBinaryLookPathUsedByTests(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the fake grok agent")
	}
}

func TestGrokPermissionsAllowBotMCPInReadOnlyAndPropagatesToken(t *testing.T) {
	opts := []acp.PermOption{
		{OptionID: "no", Kind: "reject_once"},
		{OptionID: "yes", Kind: "allow_once"},
		{OptionID: "always", Kind: "allow_always"},
	}
	if id, ok := acp.ChoosePermission(true, acp.ToolCallPermission{Kind: "other", Title: "mcp__bro_bot__ask_user"}, opts); !ok || id != "yes" {
		t.Errorf("expected bot MCP tool to be permitted in readOnly mode, got (%q, %v)", id, ok)
	}
	if id, ok := acp.ChoosePermission(true, acp.ToolCallPermission{Kind: "execute", Title: "bash"}, opts); !ok || id != "no" {
		t.Errorf("expected execute tool to be rejected in readOnly mode, got (%q, %v)", id, ok)
	}
	if id, ok := acp.ChoosePermission(true, acp.ToolCallPermission{Kind: "execute", Title: "bash", RawInput: map[string]any{"command": "git status -sb"}}, opts); !ok || id != "yes" {
		t.Errorf("expected safe read-only bash tool to be permitted in readOnly mode, got (%q, %v)", id, ok)
	}

	srv := acp.BroBotStdioServer("tok-abc")
	var hasTokenEnv, hasTokenArg bool
	for _, kv := range srv.Env {
		if kv.Value == "tok-abc" {
			hasTokenEnv = true
		}
	}
	for _, a := range srv.Args {
		if a == "tok-abc" {
			hasTokenArg = true
		}
	}
	if !hasTokenEnv || !hasTokenArg {
		t.Errorf("expected channel token in mcpServer Env and Args: %+v", srv)
	}
}

func TestGrokACPReadOnlyShellWhitelistAndRecovery(t *testing.T) {
	dir := t.TempDir()
	permFile := filepath.Join(dir, "perm")
	promptsFile := filepath.Join(dir, "prompts")
	script := filepath.Join(dir, "grok-ro")
	scriptBody := "#!/usr/bin/env python3\n" + `
import json, os, sys
perm = os.environ.get("PERM_FILE", "")
prompts_file = os.environ.get("PROMPTS_FILE", "")
perms = []
prompts = []
turn = 0
def send(o):
    sys.stdout.write(json.dumps(o) + "\n")
    sys.stdout.flush()
while True:
    line = sys.stdin.readline()
    if not line:
        break
    msg = json.loads(line)
    m, mid, p = msg.get("method"), msg.get("id"), msg.get("params") or {}
    if m == "initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"authMethods":[{"id":"cached_token"}],"agentCapabilities":{"loadSession":True,"sessionCapabilities":{"resume":{}}}}})
    elif m == "authenticate":
        send({"jsonrpc":"2.0","id":mid,"result":{}})
    elif m == "session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"grok-ro-1"}})
    elif m == "session/prompt":
        turn += 1
        prompts.append("".join(c.get("text", "") for c in (p.get("prompt") or [])))
        if turn == 1:
            for req_id, tc in [
                (10, {"kind":"execute","title":"bash","rawInput":{"command":"git status -sb"}}),
                (11, {"kind":"execute","title":"bash","rawInput":{"command":"rm -rf /tmp/x"}}),
            ]:
                send({"jsonrpc":"2.0","id":req_id,"method":"session/request_permission","params":{"toolCall":tc,"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}})
                reply = json.loads(sys.stdin.readline())
                perms.append(((reply.get("result") or {}).get("outcome") or {}).get("optionId", ""))
            send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
        else:
            send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"grok-ro-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Blocked rm in read-only mode."}}}})
            send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
    elif m == "session/close":
        if perm:
            open(perm, "w").write("\n".join(perms))
        if prompts_file:
            open(prompts_file, "w").write("\n---\n".join(prompts))
        send({"jsonrpc":"2.0","id":mid,"result":{}})
        break
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"result":{}})
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERM_FILE", permFile)
	t.Setenv("PROMPTS_FILE", promptsFile)
	prev := grokBin
	grokBin = script
	t.Cleanup(func() { grokBin = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := NewAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
		Prompt:   "Check status and remove file",
		WorkDir:  dir,
		ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	if waitErr := proc.Wait(); waitErr != nil {
		t.Fatalf("Wait: %v\n%s", waitErr, out)
	}
	text := string(out)
	if !strings.Contains(text, `"status":"SUCCESS"`) || !strings.Contains(text, "Blocked rm in read-only mode.") || !strings.Contains(text, `"num_turns":2`) {
		t.Fatalf("expected recovery SUCCESS with num_turns=2, got:\n%s", text)
	}
	permData, _ := os.ReadFile(permFile)
	if got := strings.TrimSpace(string(permData)); got != "yes\nno" {
		t.Errorf("permissions = %q, want yes\\nno", got)
	}
	pData, _ := os.ReadFile(promptsFile)
	if !strings.Contains(string(pData), "bash: rm -rf /tmp/x") {
		t.Errorf("expected recovery prompt to include actual blocked command, got:\n%s", pData)
	}
}

type fakeGrokPermHandler struct {
	decision ports.PermissionDecision
	reason   string
	calls    []ports.PermissionRequest
}

func (f *fakeGrokPermHandler) DecidePermission(_ context.Context, req ports.PermissionRequest) (ports.PermissionDecision, string) {
	f.calls = append(f.calls, req)
	return f.decision, f.reason
}

func TestGrokACPAdapterPermissionHandler(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XAI_API_KEY", "test-key")
	dir := t.TempDir()
	permFile := filepath.Join(dir, "perm")
	promptsFile := filepath.Join(dir, "prompts")
	t.Setenv("PERM_FILE", permFile)
	t.Setenv("PROMPTS_FILE", promptsFile)

	script := filepath.Join(dir, "grok-perm")
	scriptBody := `#!/usr/bin/env python3
import json, os, sys
perm_file = os.environ["PERM_FILE"]
prompts_file = os.environ["PROMPTS_FILE"]
perms = []
prompts = []
def send(o):
    sys.stdout.write(json.dumps(o) + "\n")
    sys.stdout.flush()
turn = 0
while True:
    line = sys.stdin.readline()
    if not line:
        break
    msg = json.loads(line)
    m, mid, p = msg.get("method"), msg.get("id"), msg.get("params") or {}
    if m == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1, "authMethods": [{"id": "cached_token"}], "agentCapabilities": {"loadSession": True, "sessionCapabilities": {"resume": {}}}}})
    elif m == "authenticate":
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
    elif m == "session/new":
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "g-perm"}})
    elif m == "session/prompt":
        turn += 1
        txt = "".join(c.get("text", "") for c in (p.get("prompt") or []))
        prompts.append(txt)
        if turn == 1:
            send({"jsonrpc": "2.0", "id": 10, "method": "session/request_permission", "params": {
                "toolCall": {"name": "write_file", "kind": "edit", "title": "/tmp/moex/candles.csv", "rawInput": {"path": "/tmp/moex/candles.csv"}},
                "options": [{"optionId": "always_id", "kind": "allow_always"}, {"optionId": "once_id", "kind": "allow_once"}, {"optionId": "deny_id", "kind": "reject_once"}]
            }})
            r1 = json.loads(sys.stdin.readline())
            perms.append(((r1.get("result") or {}).get("outcome") or {}).get("optionId", ""))

            send({"jsonrpc": "2.0", "id": 11, "method": "session/request_permission", "params": {
                "toolCall": {"name": "run_command", "kind": "execute", "title": "sudo reboot"},
                "options": [{"optionId": "once_id", "kind": "allow_once"}, {"optionId": "deny_id", "kind": "reject_once"}]
            }})
            r2 = json.loads(sys.stdin.readline())
            perms.append(((r2.get("result") or {}).get("outcome") or {}).get("optionId", ""))
            send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
        else:
            send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "g-perm", "update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "Recovered cleanly."}}}})
            send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
    elif m == "session/close":
        open(perm_file, "w").write("\n".join(perms))
        open(prompts_file, "w").write("\n---\n".join(prompts))
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
        break
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := grokBin
	grokBin = script
	t.Cleanup(func() { grokBin = prev })

	h := &fakeGrokPermHandler{decision: ports.PermissionAllowAlways}
	// First call returns allow_always (ACP must map to once_id); override to deny for second?
	// Use dynamic handler instead:
	_ = h
	callNum := 0
	dynHandler := &fakeGrokDynHandler{fn: func(_ context.Context, req ports.PermissionRequest) (ports.PermissionDecision, string) {
		callNum++
		if callNum == 1 {
			return ports.PermissionAllowAlways, ""
		}
		return ports.PermissionDeny, "sudo blocked"
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := NewAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
		Prompt:            "Save data",
		WorkDir:           dir,
		ReadOnly:          true,
		PermissionHandler: dynHandler,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	out, _ := io.ReadAll(proc.Stdout())
	if waitErr := proc.Wait(); waitErr != nil {
		t.Fatalf("Wait: %v\n%s", waitErr, out)
	}
	permData, _ := os.ReadFile(permFile)
	if got := strings.TrimSpace(string(permData)); got != "once_id\ndeny_id" {
		t.Fatalf("permissions = %q, want once_id\\ndeny_id", got)
	}
	pData, _ := os.ReadFile(promptsFile)
	if !strings.Contains(string(pData), "user decision") || !strings.Contains(string(pData), "sudo blocked") {
		t.Fatalf("expected controlled recovery prompt with denial reason, got:\n%s", pData)
	}
}

type fakeGrokDynHandler struct {
	fn func(context.Context, ports.PermissionRequest) (ports.PermissionDecision, string)
}

func (f *fakeGrokDynHandler) DecidePermission(ctx context.Context, req ports.PermissionRequest) (ports.PermissionDecision, string) {
	return f.fn(ctx, req)
}
