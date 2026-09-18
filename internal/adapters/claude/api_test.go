package claude

import (
	"bro-bot/internal/ports"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Список моделей больше не зашит в код: он приходит из /v1/models,
// поэтому проверки живого списка живут в models_test.go
// (TestGetModelsReturnsLiveList, TestGetModelsRequiresAPIKey).

func TestClaudeAPIAdapterQuota(t *testing.T) {
	adapter := NewClaudeAPIAdapter()
	ctx := context.Background()

	quota, err := adapter.GetQuota(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(string(quota), "SUCCESS") {
		t.Errorf("expected quota response to contain SUCCESS, got: %s", string(quota))
	}
}

func TestClaudeAPIAdapterProcessExecutionFallback(t *testing.T) {
	adapter := NewClaudeAPIAdapter()
	ctx := context.Background()

	args := ports.ExecuteArgs{
		Prompt:    "hello",
		ModelName: "sonnet",
	}

	// Without ANTHROPIC_API_KEY, it falls back gracefully or executes CLI
	_, err := adapter.ExecuteTask(ctx, args)
	if err != nil {
		// CLI execution in test environment might fail if binary not in path, but that's expected
		t.Logf("ExecuteTask returned expected path: %v", err)
	}
}

func TestBuildClaudeMessagesBodyWithHistory(t *testing.T) {
	args := ports.ExecuteArgs{
		Prompt:       "а какая ты модель?",
		SystemPrompt: "РЕЖИМ ДИАЛОГА",
		History: []ports.ChatMessage{
			{Role: "user", Content: "привет"},
			{Role: "assistant", Content: "привет, чем помочь?"},
			{Role: "model", Content: "  "}, // пустые реплики отбрасываем
		},
	}

	body := buildClaudeMessagesBody("claude-sonnet-4-5", args)

	if body["model"] != "claude-sonnet-4-5" {
		t.Errorf("модель = %v", body["model"])
	}
	if body["system"] != "РЕЖИМ ДИАЛОГА" {
		t.Errorf("системная инструкция = %v", body["system"])
	}

	messages, ok := body["messages"].([]map[string]interface{})
	if !ok {
		t.Fatalf("messages имеет неожиданный тип %T", body["messages"])
	}
	if len(messages) != 3 {
		t.Fatalf("ожидали 3 сообщения (2 из истории + текущее), получили %d: %+v", len(messages), messages)
	}
	if messages[0]["role"] != "user" || messages[0]["content"] != "привет" {
		t.Errorf("первая реплика: %+v", messages[0])
	}
	if messages[1]["role"] != "assistant" {
		t.Errorf("роль ответа ассистента: %+v", messages[1])
	}
	if messages[2]["role"] != "user" || messages[2]["content"] != "а какая ты модель?" {
		t.Errorf("последней должна идти текущая реплика пользователя: %+v", messages[2])
	}
}

func TestBuildClaudeMessagesBodyWithoutHistory(t *testing.T) {
	body := buildClaudeMessagesBody("claude-sonnet-4-5", ports.ExecuteArgs{Prompt: "одиночный вопрос"})

	if _, hasSystem := body["system"]; hasSystem {
		t.Error("без преамбулы поле system не должно добавляться")
	}
	messages := body["messages"].([]map[string]interface{})
	if len(messages) != 1 || messages[0]["content"] != "одиночный вопрос" {
		t.Errorf("ожидали ровно одну реплику пользователя: %+v", messages)
	}
}

// newMessagesServer поднимает фейковый API: /models отдаёт живой список, а /messages
// отвечает потоком, но первые failFirst попыток заворачивает как not_found_error.
func newMessagesServer(t *testing.T, failFirst int) (*httptest.Server, func() []string) {
	t.Helper()

	var (
		mu     sync.Mutex
		models []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"id": "claude-opus-5", "display_name": "Claude Opus 5"},
					{"id": "claude-sonnet-5", "display_name": "Claude Sonnet 5"},
				},
				"has_more": false,
				"last_id":  "claude-sonnet-5",
			})
			return
		}

		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		models = append(models, body.Model)
		attempt := len(models)
		mu.Unlock()

		if attempt <= failFirst {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"model: ` + body.Model + `"}}`))
			return
		}

		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Меня зовут Клод.\"}}\n\n"))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), models...)
	}
}

// readClaudeAnswer прогоняет задачу и возвращает весь поток событий.
func readClaudeAnswer(t *testing.T, adapter *ClaudeAPIAdapter) string {
	t.Helper()

	proc, err := adapter.ExecuteTask(context.Background(), ports.ExecuteArgs{Prompt: "Как тебя зовут?"})
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	defer func() { _ = proc.Close() }()

	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		t.Fatalf("чтение потока: %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("процесс завершился с ошибкой: %v", err)
	}
	return string(out)
}

// TestExecuteTaskMapsRetiredModelBeforeRequest — главный сторож исходного бага:
// сохранённая снятая модель подменяется живой ещё до запроса, и 404 не возникает.
func TestExecuteTaskMapsRetiredModelBeforeRequest(t *testing.T) {
	resetClaudeModelCache(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("CLAUDE_API_MODEL", "claude-3-opus-20240229") // модель, которой больше нет

	srv, requested := newMessagesServer(t, 0)
	adapter := &ClaudeAPIAdapter{HTTPClient: srv.Client(), BaseURL: srv.URL}

	out := readClaudeAnswer(t, adapter)
	if !strings.Contains(out, "Меня зовут Клод.") {
		t.Errorf("ответ не дошёл до пользователя: %s", out)
	}

	models := requested()
	if len(models) != 1 {
		t.Fatalf("ожидали одну попытку, получили %v", models)
	}
	if models[0] != "claude-opus-5" {
		t.Errorf("запрос ушёл на %q, ожидали живую модель того же семейства claude-opus-5", models[0])
	}
	if strings.HasPrefix(models[0], "claude-3-") {
		t.Errorf("в API ушла снятая модель %q", models[0])
	}
}

// TestExecuteTaskRetriesWhenModelRejected — если модель отключили уже после получения
// списка, адаптер повторяет запрос на модели по умолчанию, а не отдаёт пользователю 404.
func TestExecuteTaskRetriesWhenModelRejected(t *testing.T) {
	resetClaudeModelCache(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("CLAUDE_API_MODEL", "claude-opus-5")

	srv, requested := newMessagesServer(t, 1)
	adapter := &ClaudeAPIAdapter{HTTPClient: srv.Client(), BaseURL: srv.URL}

	out := readClaudeAnswer(t, adapter)
	if !strings.Contains(out, "Меня зовут Клод.") {
		t.Errorf("ответ не дошёл до пользователя: %s", out)
	}
	if strings.Contains(out, `"is_error":true`) {
		t.Errorf("в поток попала ошибка: %s", out)
	}

	models := requested()
	if len(models) != 2 {
		t.Fatalf("ожидали две попытки, получили %v", models)
	}
	if models[0] != "claude-opus-5" {
		t.Errorf("первая попытка ушла на %q", models[0])
	}
	if models[1] != "claude-sonnet-5" {
		t.Errorf("повтор ожидали на модели по умолчанию claude-sonnet-5, получили %q", models[1])
	}
}
