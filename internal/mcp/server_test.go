package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPServer_Initialize(t *testing.T) {
	srv := NewServer(nil)
	ctx := context.Background()

	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}}`
	respBytes := srv.HandleLine(ctx, []byte(req))
	if respBytes == nil {
		t.Fatalf("expected response, got nil")
	}

	var resp Response
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resMap, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}

	if resMap["protocolVersion"] != "2024-11-05" {
		t.Errorf("expected protocolVersion 2024-11-05, got %v", resMap["protocolVersion"])
	}

	caps, ok := resMap["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected capabilities map, got %T", resMap["capabilities"])
	}

	if caps["tools"] == nil {
		t.Errorf("expected tools capability to be present")
	}

	// Каналы claude/channel сервер не реализует, поэтому и не объявляет.
	if caps["experimental"] != nil {
		t.Errorf("unexpected experimental capabilities: %v", caps["experimental"])
	}
}

func TestMCPServer_ToolsList(t *testing.T) {
	srv := NewServer(NewBridgeTools(shortSocketPath(t)))
	ctx := context.Background()

	req := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	respBytes := srv.HandleLine(ctx, []byte(req))

	var resp Response
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resMap := resp.Result.(map[string]interface{})
	toolsList := resMap["tools"].([]interface{})

	names := make(map[string]bool)
	for _, toolItem := range toolsList {
		tObj := toolItem.(map[string]interface{})
		names[tObj["name"].(string)] = true
	}

	if !names["telegram_send_message"] {
		t.Errorf("missing tool telegram_send_message")
	}
	if !names["ask_user"] {
		t.Errorf("missing tool ask_user")
	}
	if !names["report_progress"] {
		t.Errorf("missing tool report_progress")
	}
}

// callTool выполняет tools/call и возвращает текст и признак ошибки результата.
func callTool(t *testing.T, srv *Server, name, args string) (string, bool) {
	t.Helper()
	req := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	var resp Response
	if err := json.Unmarshal(srv.HandleLine(context.Background(), []byte(req)), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	resMap := resp.Result.(map[string]interface{})
	content := resMap["content"].([]interface{})
	if len(content) == 0 {
		t.Fatalf("expected content in result")
	}
	return content[0].(map[string]interface{})["text"].(string), resMap["isError"] == true
}

// TestMCPServer_ToolsCallGoesThroughBridge — вызов инструмента доходит до бота,
// и агент получает ответ бота, а не выдуманное «доставлено».
func TestMCPServer_ToolsCallGoesThroughBridge(t *testing.T) {
	socket := shortSocketPath(t)
	var got BridgeCall
	bridge, err := ListenBridge(socket, func(_ context.Context, _ int, call BridgeCall) BridgeResult {
		got = call
		return BridgeResult{Text: "delivered by bot"}
	})
	if err != nil {
		t.Fatalf("ListenBridge: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })

	srv := NewServer(NewBridgeTools(socket))
	text, isErr := callTool(t, srv, ToolSendMessage, `{"message":"Hello from agent"}`)
	if isErr || text != "delivered by bot" {
		t.Errorf("result = %q, isError=%v", text, isErr)
	}
	if got.Tool != ToolSendMessage || got.Arguments["message"] != "Hello from agent" {
		t.Errorf("bot received %+v", got)
	}

	if text, isErr := callTool(t, srv, "non_existent", `{}`); !isErr {
		t.Errorf("expected isError=true for unknown tool, got %q", text)
	}
}

// TestMCPServer_ToolsCallWithoutBot — без бота инструмент сообщает агенту об ошибке.
func TestMCPServer_ToolsCallWithoutBot(t *testing.T) {
	srv := NewServer(NewBridgeTools(shortSocketPath(t)))
	text, isErr := callTool(t, srv, ToolAskUser, `{"question":"Which branch?"}`)
	if !isErr || !strings.Contains(text, "not reachable") {
		t.Errorf("expected bridge error, got %q (isError=%v)", text, isErr)
	}
}

func TestMCPServer_Errors(t *testing.T) {
	srv := NewServer(nil)
	ctx := context.Background()

	// Некорректный JSON
	badJSON := srv.HandleLine(ctx, []byte(`{not valid json}`))
	var respParse Response
	_ = json.Unmarshal(badJSON, &respParse)
	if respParse.Error == nil || respParse.Error.Code != ParseError {
		t.Errorf("expected ParseError (-32700), got %+v", respParse.Error)
	}

	// Неизвестный метод
	unknownMethod := srv.HandleLine(ctx, []byte(`{"jsonrpc":"2.0","id":5,"method":"some_random_method"}`))
	var respMethod Response
	_ = json.Unmarshal(unknownMethod, &respMethod)
	if respMethod.Error == nil || respMethod.Error.Code != MethodNotFound {
		t.Errorf("expected MethodNotFound (-32601), got %+v", respMethod.Error)
	}
}

func TestMCPServer_RunStream(t *testing.T) {
	srv := NewServer(nil)
	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n"
	in := bytes.NewBufferString(input)
	out := &bytes.Buffer{}

	err := srv.Run(context.Background(), in, out)
	if err != nil {
		t.Fatalf("srv.Run failed: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, `"id":1`) {
		t.Errorf("expected response to id 1, got %s", outStr)
	}
}
