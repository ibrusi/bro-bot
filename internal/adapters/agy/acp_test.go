package agy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bro-bot/internal/effort"
	"bro-bot/internal/ports"
)

type fakeAgyBotTools struct {
	calls []string
}

func (f *fakeAgyBotTools) CallBotTool(_ context.Context, name string, _ map[string]interface{}) ports.BotToolResult {
	f.calls = append(f.calls, name)
	return ports.BotToolResult{Text: `{"active_project":"bro-bot"}`}
}

func TestAgyACPSpeaksProtocolAndServesHTTPBotTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyCfg := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(legacyCfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyCfg, []byte(`{"mcpServers":{"bro_bot":{"command":"/old"},"keep":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("LOCAL_RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	permFile := filepath.Join(dir, "perm")
	metaFile := filepath.Join(dir, "meta")
	script := filepath.Join(dir, "agy")
	if err := os.WriteFile(script, []byte(fakeAgyACPScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERM_FILE", permFile)
	t.Setenv("META_FILE", metaFile)
	prev := agyBin
	agyBin = script
	t.Cleanup(func() { agyBin = prev })

	botTools := &fakeAgyBotTools{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	proc, err := NewAgyACPAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
		Prompt:          "Check project",
		WorkDir:         dir,
		ModelName:       "flash",
		Effort:          effort.Low,
		ReadOnly:        true,
		SystemPrompt:    "DUPLICATE_RULES_SHOULD_SKIP",
		BotInstructions: "BOT_INSTRUCTIONS_MARKER",
		ChannelToken:    "tok-agy-http-1",
		BotTools:        botTools,
	})
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
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
	for _, want := range []string{
		`"conversation_id":"agy-sess-1"`,
		`"tool_name":"mcp__bro_bot__list_projects"`,
		`"tool_name":"ask_question"`,
		`"text_delta":"Agy ACP reply"`,
		`"input_tokens":100`,
		`"thinking_tokens":20`,
		`"status":"SUCCESS"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %s:\n%s", want, text)
		}
	}

	if len(botTools.calls) != 1 || botTools.calls[0] != "list_projects" {
		t.Errorf("expected HTTP tools/call list_projects in-process, got %v", botTools.calls)
	}

	permData, err := os.ReadFile(permFile)
	if err != nil {
		t.Fatal(err)
	}
	// call_mcp_tool(bro_bot) разрешён (allow), run_command в ReadOnly отклонён (deny)
	if got := strings.TrimSpace(string(permData)); got != "allow\ndeny" {
		t.Errorf("permissions = %q, want %q", got, "allow\ndeny")
	}

	metaData, err := os.ReadFile(metaFile)
	if err != nil {
		t.Fatal(err)
	}
	metaStr := string(metaData)
	if !strings.Contains(metaStr, "BOT_INSTRUCTIONS_MARKER") || strings.Contains(metaStr, "DUPLICATE_RULES_SHOULD_SKIP") {
		t.Errorf("unexpected systemPrompt append: %s", metaStr)
	}
	if !strings.Contains(metaStr, "model=gemini-3.8-flash-low") {
		t.Errorf("expected set_model gemini-3.8-flash-low, got: %s", metaStr)
	}
	if !strings.Contains(metaStr, "delete_status=204") {
		t.Errorf("expected DELETE /mcp -> 204, got: %s", metaStr)
	}

	// Legacy bro_bot должен быть удалён, а foreign server keep — сохранён.
	cfgBytes, err := os.ReadFile(legacyCfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfgBytes), "bro_bot") || !strings.Contains(string(cfgBytes), "keep") {
		t.Errorf("legacy mcp_config.json cleanup wrong: %s", cfgBytes)
	}
}

func TestAgyACPResumesAndFallsBackToNewSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	script := filepath.Join(dir, "agy")
	if err := os.WriteFile(script, []byte(fakeAgyACPScript), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := agyBin
	agyBin = script
	t.Cleanup(func() { agyBin = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := NewAgyACPAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
		Prompt:         "Continue",
		WorkDir:        dir,
		ConversationID: "missing-conv",
		ModelName:      "orchestrator",
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
	if !strings.Contains(string(out), `"status":"SUCCESS"`) || !strings.Contains(string(out), `"conversation_id":"agy-sess-1"`) {
		t.Fatalf("expected fallback to session/new on missing resume:\n%s", out)
	}
}

func TestAgyACPCleansToolServerWhenStartFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := agyBin
	agyBin = filepath.Join(t.TempDir(), "does-not-exist-agy")
	t.Cleanup(func() { agyBin = prev })

	_, err := NewAgyACPAdapter().ExecuteTask(context.Background(), ports.ExecuteArgs{
		Prompt:   "hi",
		WorkDir:  t.TempDir(),
		BotTools: &fakeAgyBotTools{},
	})
	if err == nil {
		t.Fatal("expected error when agy binary cannot start")
	}
}

func TestAgyACPEffortEmptyReadOnlyGuardAndLingeringExit(t *testing.T) {
	t.Run("sets thought_level alongside set_model and finishes even when process lingers", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		metaFile := filepath.Join(dir, "meta")
		t.Setenv("META_FILE", metaFile)

		lingeringScript := filepath.Join(dir, "agy-linger")
		scriptBody := `#!/usr/bin/env python3
import json, os, sys, time
meta_file = os.environ["META_FILE"]
notes = []
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
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1}})
    elif m == "session/new":
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "s-effort"}})
    elif m == "session/set_model":
        notes.append("model=" + str(p.get("modelId", "")))
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
    elif m == "session/set_config_option":
        notes.append(str(p.get("configId", "")) + "=" + str(p.get("value", "")))
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
    elif m == "session/prompt":
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "s-effort", "update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "Plan OK"}}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
    elif m == "session/close":
        open(meta_file, "w").write("\n".join(notes))
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
        # Эмулируем зависание CLI после session/close, но закрываем поток при закрытии stdin
        time.sleep(10)
        break
`
		if err := os.WriteFile(lingeringScript, []byte(scriptBody), 0o755); err != nil {
			t.Fatal(err)
		}
		prev := agyBin
		agyBin = lingeringScript
		t.Cleanup(func() { agyBin = prev })

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		proc, err := NewAgyACPAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
			Prompt:    "Make plan",
			WorkDir:   dir,
			ModelName: "custom-model",
			Effort:    effort.High,
			ReadOnly:  true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer proc.Close()
		out, _ := io.ReadAll(proc.Stdout())
		if waitErr := proc.Wait(); waitErr != nil {
			t.Fatalf("expected Wait() to kill lingering successful process and return nil, got %v", waitErr)
		}
		if !strings.Contains(string(out), `"status":"SUCCESS"`) {
			t.Fatalf("expected SUCCESS result, got %s", out)
		}
		metaBytes, _ := os.ReadFile(metaFile)
		metaStr := string(metaBytes)
		if !strings.Contains(metaStr, "model=custom-model") || !strings.Contains(metaStr, "thought_level=high") {
			t.Errorf("expected both model and thought_level set, got:\n%s", metaStr)
		}
	})

	t.Run("ReadOnly returns ERROR if only read tool ran and final answer is empty", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		emptyScript := filepath.Join(dir, "agy-empty")
		scriptBody := `#!/usr/bin/env python3
import json, sys
def send(o):
    sys.stdout.write(json.dumps(o) + "\n")
    sys.stdout.flush()
while True:
    line = sys.stdin.readline()
    if not line:
        break
    msg = json.loads(line)
    m, mid = msg.get("method"), msg.get("id")
    if m == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1}})
    elif m == "session/new":
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "s-empty"}})
    elif m == "session/prompt":
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "s-empty", "update": {"sessionUpdate": "tool_call", "toolCallId": "r1", "name": "read_file", "kind": "read", "status": "completed"}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
    elif m == "session/close":
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
        break
`
		if err := os.WriteFile(emptyScript, []byte(scriptBody), 0o755); err != nil {
			t.Fatal(err)
		}
		prev := agyBin
		agyBin = emptyScript
		t.Cleanup(func() { agyBin = prev })

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		proc, err := NewAgyACPAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
			Prompt:   "Plan please",
			WorkDir:  dir,
			ReadOnly: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer proc.Close()
		out, _ := io.ReadAll(proc.Stdout())
		_ = proc.Wait()
		if !strings.Contains(string(out), `"status":"ERROR"`) {
			t.Fatalf("expected ReadOnly empty answer after read_file to fail as ERROR, got:\n%s", out)
		}
		if !strings.Contains(string(out), `"agy returned an empty answer"`) {
			t.Fatalf("expected default empty answer error when nothing was denied, got:\n%s", out)
		}
	})
}

func TestAgyACPReadOnlyShellWhitelistAndRecovery(t *testing.T) {
	t.Run("ReadOnly allows safe read-only run_command and returns answer", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		permFile := filepath.Join(dir, "perm")
		t.Setenv("PERM_FILE", permFile)

		safeScript := filepath.Join(dir, "agy-safe-cmd")
		scriptBody := `#!/usr/bin/env python3
import json, os, sys
perm_file = os.environ["PERM_FILE"]
perms = []
def send(o):
    sys.stdout.write(json.dumps(o) + "\n")
    sys.stdout.flush()
while True:
    line = sys.stdin.readline()
    if not line:
        break
    msg = json.loads(line)
    m, mid = msg.get("method"), msg.get("id")
    if m == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1}})
    elif m == "session/new":
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "s-safe"}})
    elif m == "session/prompt":
        send({"jsonrpc": "2.0", "id": 10, "method": "session/request_permission", "params": {
            "toolCall": {"name": "run_command", "kind": "execute", "title": "ls -la && git status -sb", "rawInput": {"CommandLine": "ls -la && git status -sb"}},
            "options": [{"optionId": "allow", "kind": "allow_once"}, {"optionId": "deny", "kind": "reject_once"}]
        }})
        r = json.loads(sys.stdin.readline())
        perms.append(((r.get("result") or {}).get("outcome") or {}).get("optionId", ""))
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "s-safe", "update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "Repo OK"}}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
    elif m == "session/close":
        open(perm_file, "w").write("\n".join(perms))
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
        break
`
		if err := os.WriteFile(safeScript, []byte(scriptBody), 0o755); err != nil {
			t.Fatal(err)
		}
		prev := agyBin
		agyBin = safeScript
		t.Cleanup(func() { agyBin = prev })

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		proc, err := NewAgyACPAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
			Prompt:   "Inspect repo",
			WorkDir:  dir,
			ReadOnly: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer proc.Close()
		out, _ := io.ReadAll(proc.Stdout())
		if waitErr := proc.Wait(); waitErr != nil {
			t.Fatalf("Wait: %v\n%s", waitErr, out)
		}
		if !strings.Contains(string(out), `"status":"SUCCESS"`) || !strings.Contains(string(out), "Repo OK") {
			t.Fatalf("expected SUCCESS with Repo OK, got:\n%s", out)
		}
		permData, _ := os.ReadFile(permFile)
		if got := strings.TrimSpace(string(permData)); got != "allow" {
			t.Errorf("permissions = %q, want allow", got)
		}
	})

	t.Run("ReadOnly allows sqlite_query MCP tool and analytical python3/sqlite3/curl commands", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		permFile := filepath.Join(dir, "perm")
		t.Setenv("PERM_FILE", permFile)

		analyticsScript := filepath.Join(dir, "agy-analytics")
		scriptBody := `#!/usr/bin/env python3
import json, os, sys, urllib.request
perm_file = os.environ["PERM_FILE"]
perms = []
mcp_srv = None
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
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1}})
    elif m == "session/new":
        srvs = p.get("mcpServers") or []
        if srvs and srvs[0].get("type") == "http":
            mcp_srv = srvs[0]
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "s-analytics"}})
    elif m == "session/prompt":
        calls = [
            (10, {"name": "call_mcp_tool", "kind": "execute", "title": "MCP Tool sqlite_query", "rawInput": {"ServerName": "bro_bot", "ToolName": "sqlite_query"}}),
            (11, {"name": "run_command", "kind": "execute", "title": "python3", "rawInput": {"CommandLine": "python3 -c 'print(1)'"}}),
            (12, {"name": "run_command", "kind": "execute", "title": "curl", "rawInput": {"CommandLine": "curl -fsSL \"https://api.example.com/candles\" -o /tmp/c.json"}}),
            (13, {"name": "run_command", "kind": "execute", "title": "sqlite3", "rawInput": {"CommandLine": "sqlite3 -readonly data/db/pair_edge.db \"SELECT count(*) FROM trades\""}}),
            (14, {"name": "run_command", "kind": "execute", "title": "deploy", "rawInput": {"CommandLine": "./scripts/deploy-release.sh"}}),
        ]
        for req_id, tc in calls:
            send({"jsonrpc": "2.0", "id": req_id, "method": "session/request_permission", "params": {
                "toolCall": tc,
                "options": [{"optionId": "allow", "kind": "allow_once"}, {"optionId": "deny", "kind": "reject_once"}]
            }})
            r = json.loads(sys.stdin.readline())
            perms.append(((r.get("result") or {}).get("outcome") or {}).get("optionId", ""))
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "s-analytics", "update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "Analytics OK"}}}})
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
    elif m == "session/close":
        open(perm_file, "w").write("\n".join(perms))
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
        break
`
		if err := os.WriteFile(analyticsScript, []byte(scriptBody), 0o755); err != nil {
			t.Fatal(err)
		}
		prev := agyBin
		agyBin = analyticsScript
		t.Cleanup(func() { agyBin = prev })

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		proc, err := NewAgyACPAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
			Prompt:   "Inspect SQLite and fetch candles",
			WorkDir:  dir,
			ReadOnly: true,
			BotTools: &fakeAgyBotTools{},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer proc.Close()
		out, _ := io.ReadAll(proc.Stdout())
		if waitErr := proc.Wait(); waitErr != nil {
			t.Fatalf("Wait: %v\n%s", waitErr, out)
		}
		if !strings.Contains(string(out), `"status":"SUCCESS"`) || !strings.Contains(string(out), "Analytics OK") {
			t.Fatalf("expected SUCCESS with Analytics OK, got:\n%s", out)
		}
		permData, _ := os.ReadFile(permFile)
		if got := strings.TrimSpace(string(permData)); got != "allow\nallow\nallow\nallow\ndeny" {
			t.Errorf("permissions = %q, want allow/allow/allow/allow/deny", got)
		}
	})

	t.Run("ReadOnly denied command triggering empty turn sends single recovery prompt and sums usage", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		promptsFile := filepath.Join(dir, "prompts")
		t.Setenv("PROMPTS_FILE", promptsFile)

		recScript := filepath.Join(dir, "agy-recover")
		scriptBody := `#!/usr/bin/env python3
import json, os, sys
prompts_file = os.environ["PROMPTS_FILE"]
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
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1}})
    elif m == "session/new":
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "s-rec"}})
    elif m == "session/prompt":
        turn += 1
        txt = "".join(c.get("text", "") for c in (p.get("prompt") or []))
        prompts.append(txt)
        if turn == 1:
            send({"jsonrpc": "2.0", "id": 10, "method": "session/request_permission", "params": {
                "toolCall": {"name": "run_command", "kind": "execute", "title": "rm -rf /"},
                "options": [{"optionId": "allow", "kind": "allow_once"}, {"optionId": "deny", "kind": "reject_once"}]
            }})
            r = json.loads(sys.stdin.readline())
            assert ((r.get("result") or {}).get("outcome") or {}).get("optionId") == "deny"
            send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn", "usage": {"inputTokens": 100, "outputTokens": 10, "totalTokens": 110}}})
        else:
            send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "s-rec", "update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "Cannot run rm in read-only mode."}}}})
            send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn", "usage": {"inputTokens": 50, "outputTokens": 20, "totalTokens": 70}}})
    elif m == "session/close":
        open(prompts_file, "w").write("\n---\n".join(prompts))
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
        break
`
		if err := os.WriteFile(recScript, []byte(scriptBody), 0o755); err != nil {
			t.Fatal(err)
		}
		prev := agyBin
		agyBin = recScript
		t.Cleanup(func() { agyBin = prev })

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		proc, err := NewAgyACPAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
			Prompt:   "Delete files",
			WorkDir:  dir,
			ReadOnly: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer proc.Close()
		out, _ := io.ReadAll(proc.Stdout())
		if waitErr := proc.Wait(); waitErr != nil {
			t.Fatalf("Wait: %v\n%s", waitErr, out)
		}
		text := string(out)
		for _, want := range []string{
			`"status":"SUCCESS"`,
			`Cannot run rm in read-only mode.`,
			`"num_turns":2`,
			`"input_tokens":150`,
			`"output_tokens":30`,
			`"total_tokens":180`,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("output missing %s:\n%s", want, text)
			}
		}
		pData, _ := os.ReadFile(promptsFile)
		if !strings.Contains(string(pData), "run_command: rm -rf /") {
			t.Errorf("expected recovery prompt to mention blocked run_command, got:\n%s", pData)
		}
	})

	t.Run("ReadOnly denied command with still-empty recovery returns descriptive blocked error", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		emptyRecScript := filepath.Join(dir, "agy-empty-rec")
		scriptBody := `#!/usr/bin/env python3
import json, sys
def send(o):
    sys.stdout.write(json.dumps(o) + "\n")
    sys.stdout.flush()
turn = 0
while True:
    line = sys.stdin.readline()
    if not line:
        break
    msg = json.loads(line)
    m, mid = msg.get("method"), msg.get("id")
    if m == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1}})
    elif m == "session/new":
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "s-empty-rec"}})
    elif m == "session/prompt":
        turn += 1
        if turn == 1:
            send({"jsonrpc": "2.0", "id": 10, "method": "session/request_permission", "params": {
                "toolCall": {"name": "run_command", "kind": "execute", "title": "rm -rf /"},
                "options": [{"optionId": "allow", "kind": "allow_once"}, {"optionId": "deny", "kind": "reject_once"}]
            }})
            _ = sys.stdin.readline()
        send({"jsonrpc": "2.0", "id": mid, "result": {"stopReason": "end_turn"}})
    elif m == "session/close":
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
        break
`
		if err := os.WriteFile(emptyRecScript, []byte(scriptBody), 0o755); err != nil {
			t.Fatal(err)
		}
		prev := agyBin
		agyBin = emptyRecScript
		t.Cleanup(func() { agyBin = prev })

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		proc, err := NewAgyACPAdapter().ExecuteTask(ctx, ports.ExecuteArgs{
			Prompt:   "Delete files",
			WorkDir:  dir,
			ReadOnly: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer proc.Close()
		out, _ := io.ReadAll(proc.Stdout())
		_ = proc.Wait()
		text := string(out)
		if !strings.Contains(text, `"status":"ERROR"`) {
			t.Fatalf("expected ERROR status, got:\n%s", text)
		}
		if !strings.Contains(text, "read-only mode blocked") || !strings.Contains(text, "run_command") {
			t.Errorf("expected descriptive blocked error, got:\n%s", text)
		}
		if strings.Contains(text, "agy returned an empty answer") {
			t.Errorf("did not expect generic empty answer error after denial:\n%s", text)
		}
	})
}

const fakeAgyACPScript = "#!/usr/bin/env python3\n" + `
import json, os, sys, urllib.request

perm_file = os.environ.get("PERM_FILE", "")
meta_file = os.environ.get("META_FILE", "")
perms = []
meta_notes = []

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def http_mcp(srv, method, params=None, msg_id=1):
    url = srv["url"]
    headers = {h["name"]: h["value"] for h in srv.get("headers", [])}
    headers["Content-Type"] = "application/json"
    body = json.dumps({"jsonrpc": "2.0", "id": msg_id, "method": method, "params": params or {}}).encode()
    req = urllib.request.Request(url, data=body, headers=headers, method="POST")
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.loads(resp.read().decode())

def http_delete(srv):
    headers = {h["name"]: h["value"] for h in srv.get("headers", [])}
    req = urllib.request.Request(srv["url"], headers=headers, method="DELETE")
    with urllib.request.urlopen(req, timeout=5) as resp:
        return resp.status

mcp_srv = None

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
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": 1, "agentCapabilities": {"loadSession": True}}})
    elif method == "session/resume":
        send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32002, "message": "session not found"}})
    elif method == "session/new":
        srvs = params.get("mcpServers") or []
        if srvs and srvs[0].get("type") == "http":
            mcp_srv = srvs[0]
        append = (((params.get("_meta") or {}).get("antigravity") or {}).get("systemPrompt") or {}).get("append", "")
        meta_notes.append("append=" + append)
        send({"jsonrpc": "2.0", "id": mid, "result": {"sessionId": "agy-sess-1"}})
    elif method == "session/set_model":
        meta_notes.append("model=" + str(params.get("modelId", "")))
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
    elif method == "session/prompt":
        if mcp_srv:
            http_mcp(mcp_srv, "server/discover", {}, 1)
            http_mcp(mcp_srv, "tools/call", {"name": "list_projects", "arguments": {}}, 2)
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "agy-sess-1", "update": {
            "sessionUpdate": "tool_call", "toolCallId": "tc-1", "name": "call_mcp_tool", "kind": "execute", "status": "in_progress",
            "rawInput": {"ServerName": "bro_bot", "ToolName": "list_projects"}
        }}})
        send({"jsonrpc": "2.0", "id": 10, "method": "session/request_permission", "params": {
            "toolCall": {"name": "call_mcp_tool", "kind": "execute", "title": "MCP Tool list_projects", "rawInput": {"ServerName": "bro_bot", "ToolName": "list_projects"}},
            "options": [{"optionId": "allow", "kind": "allow_once"}, {"optionId": "deny", "kind": "reject_once"}]
        }})
        r1 = json.loads(sys.stdin.readline())
        perms.append(((r1.get("result") or {}).get("outcome") or {}).get("optionId", ""))

        send({"jsonrpc": "2.0", "id": 11, "method": "session/request_permission", "params": {
            "toolCall": {"name": "run_command", "kind": "execute", "title": "rm -rf /"},
            "options": [{"optionId": "allow", "kind": "allow_once"}, {"optionId": "deny", "kind": "reject_once"}]
        }})
        r2 = json.loads(sys.stdin.readline())
        perms.append(((r2.get("result") or {}).get("outcome") or {}).get("optionId", ""))

        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "agy-sess-1", "update": {
            "sessionUpdate": "tool_call", "toolCallId": "tc-2", "name": "ask_question", "kind": "other", "status": "in_progress",
            "rawInput": {"questions": [{"question": "Continue?", "options": ["Yes", "No"]}]}
        }}})
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "agy-sess-1", "update": {
            "sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "Agy ACP reply"}
        }}})
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "stopReason": "end_turn",
            "usage": {"inputTokens": 100, "outputTokens": 25, "cachedReadTokens": 10, "thoughtTokens": 20, "totalTokens": 155}
        }})
    elif method == "session/close":
        if mcp_srv:
            status = http_delete(mcp_srv)
            meta_notes.append("delete_status=" + str(status))
        if perm_file:
            open(perm_file, "w").write("\n".join(perms))
        if meta_file:
            open(meta_file, "w").write("\n".join(meta_notes))
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
        break
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
`
