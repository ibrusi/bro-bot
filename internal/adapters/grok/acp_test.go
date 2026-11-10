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
	opts := []permOption{
		{OptionID: "yes", Kind: "allow_once"},
		{OptionID: "always", Kind: "allow_always"},
		{OptionID: "no", Kind: "reject_once"},
	}
	if id, ok := choosePermission(false, "execute", opts); !ok || id != "always" {
		t.Errorf("задача должна разрешать всегда, получили %q ok=%v", id, ok)
	}
	if id, ok := choosePermission(true, "execute", opts); !ok || id != "no" {
		t.Errorf("диалог должен отклонять команду, получили %q ok=%v", id, ok)
	}
	if id, ok := choosePermission(true, "read", opts); !ok || id != "yes" {
		t.Errorf("диалог должен разрешать чтение один раз, получили %q ok=%v", id, ok)
	}
}

func TestTranslateUpdateTextAndTool(t *testing.T) {
	text := []byte(`{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hi"}}}`)
	evt, ok := translateUpdate("s1", text, 1)
	if !ok || evt.StepUpdate == nil || evt.StepUpdate.TextDelta != "Hi" || evt.StepUpdate.StepType != "agent_response" {
		t.Fatalf("текст: %+v ok=%v", evt, ok)
	}

	tool := []byte(`{"update":{"sessionUpdate":"tool_call","toolCallId":"t","title":"read_file","kind":"read","status":"in_progress","rawInput":{"path":"main.go"}}}`)
	evt, ok = translateUpdate("s1", tool, 2)
	if !ok || evt.StepUpdate.ToolName != "read_file" || evt.StepUpdate.State != "ACTIVE" {
		t.Fatalf("инструмент: %+v", evt.StepUpdate)
	}

	thought := []byte(`{"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"secret"}}}`)
	if _, ok := translateUpdate("s1", thought, 3); ok {
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

	proc, err := NewAdapter().ExecuteTask(context.Background(), ports.ExecuteArgs{
		Prompt:    "Say hello",
		WorkDir:   dir,
		ModelName: "gemini-3.1-pro-high",
		ReadOnly:  true,
		Effort:    effort.High,
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
	if !strings.Contains(text, `"conversation_id":"sess-1"`) {
		t.Fatalf("нет id сессии:\n%s", text)
	}
	if !strings.Contains(text, `"text_delta":"Hello"`) {
		t.Fatalf("нет текста ответа:\n%s", text)
	}
	if !strings.Contains(text, `"status":"SUCCESS"`) {
		t.Fatalf("ход не завершился успехом:\n%s", text)
	}
	chosen, err := os.ReadFile(permFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(chosen)) != "no" {
		t.Fatalf("в диалоге команда должна быть отклонена, выбрано %q", chosen)
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
	if strings.TrimSpace(string(chosen)) != "always" {
		t.Fatalf("задача должна разрешать инструмент, выбрано %q\n%s", chosen, out)
	}
}

const fakeGrokScript = "#!/usr/bin/env python3\n" + `
import json, os, sys
perm = os.environ.get("PERM_FILE", "")
def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()
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
    if method == "initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"authMethods":[{"id":"cached_token"}],"agentCapabilities":{"loadSession":True,"sessionCapabilities":{"resume":{}}}}})
    elif method == "session/resume":
        send({"jsonrpc":"2.0","id":mid,"error":{"code":-32002,"message":"unknown session"}})
    elif method == "session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":"sess-1"}})
    elif method == "session/prompt":
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello"}}}})
        send({"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"toolCall":{"kind":"execute","title":"bash"},"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"always","kind":"allow_always"},{"optionId":"no","kind":"reject_once"}]}})
        reply = json.loads(sys.stdin.readline())
        outcome = (reply.get("result") or {}).get("outcome") or {}
        if perm:
            open(perm, "w").write(outcome.get("optionId") or "")
        send({"jsonrpc":"2.0","id":mid,"result":{"stopReason":"end_turn"}})
    elif method == "session/close":
        send({"jsonrpc":"2.0","id":mid,"result":{}})
        break
    elif mid is not None:
        send({"jsonrpc":"2.0","id":mid,"result":{}})
`

func TestPickAuthMethodPrefersCachedToken(t *testing.T) {
	id, ok := pickAuthMethod([]authMethod{{ID: "grok.com"}, {ID: "cached_token"}})
	if !ok || id != "cached_token" {
		t.Fatalf("метод = %q ok=%v", id, ok)
	}
	if _, ok := pickAuthMethod([]authMethod{{ID: "grok.com"}}); ok {
		t.Fatal("браузерный вход без токена не должен выбираться")
	}
}

func TestEncodeEventIsOneJSONLine(t *testing.T) {
	data, err := encodeEvent(streamEvent{Event: "init", ConversationID: "s"})
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
