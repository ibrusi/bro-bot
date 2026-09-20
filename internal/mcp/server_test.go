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

	exp, ok := caps["experimental"].(map[string]interface{})
	if !ok || exp["claude/channel"] == nil {
		t.Errorf("expected experimental claude/channel capability to be declared, got %v", caps["experimental"])
	}
}

func TestMCPServer_ToolsList(t *testing.T) {
	srv := NewServer(nil)
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

func TestMCPServer_ToolsCall(t *testing.T) {
	srv := NewServer(nil)
	ctx := context.Background()

	// 1. Успешный вызов telegram_send_message
	req := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"telegram_send_message","arguments":{"message":"Hello from agent"}}}`
	respBytes := srv.HandleLine(ctx, []byte(req))

	var resp Response
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resMap := resp.Result.(map[string]interface{})
	if resMap["isError"] == true {
		t.Fatalf("expected isError=false, got true")
	}

	content := resMap["content"].([]interface{})
	if len(content) == 0 {
		t.Fatalf("expected content in result")
	}
	firstItem := content[0].(map[string]interface{})
	if !strings.Contains(firstItem["text"].(string), "Hello from agent") {
		t.Errorf("expected text to contain message, got %v", firstItem["text"])
	}

	// 2. Вызов несуществующего инструмента
	reqUnknown := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"non_existent","arguments":{}}}`
	respBytesUnknown := srv.HandleLine(ctx, []byte(reqUnknown))
	var respUnknown Response
	_ = json.Unmarshal(respBytesUnknown, &respUnknown)
	resMapUnknown := respUnknown.Result.(map[string]interface{})
	if resMapUnknown["isError"] != true {
		t.Errorf("expected isError=true for unknown tool")
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
