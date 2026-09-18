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

// TestClaudeAPIAdapterQuota следит за форматом ответа: он должен разбираться
// рендером /usage. Проверки содержимого живут в quota_test.go.
func TestClaudeAPIAdapterQuota(t *testing.T) {
	adapter := NewClaudeAPIAdapter()

	quota, err := adapter.GetQuota(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(quota, &parsed); err != nil {
		t.Fatalf("ответ квоты не разбирается как JSON: %v (%s)", err, quota)
	}
	if _, ok := parsed["command"]; !ok {
		t.Errorf("в ответе нет блока command, рендер /usage его не поймёт: %s", quota)
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

// TestResultEventCarriesUsageAndDuration — трекеру токенов нужны длительность и
// число ходов, иначе /tokens показывает нулевую скорость, а кэш — пустым.
func TestResultEventCarriesUsageAndDuration(t *testing.T) {
	resetClaudeModelCache(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data":     []map[string]interface{}{{"id": "claude-sonnet-5", "display_name": "Claude Sonnet 5"}},
				"has_more": false,
			})
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":120,\"cache_read_input_tokens\":30,\"cache_creation_input_tokens\":10}}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Готово.\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":45}}\n\n"))
	}))
	t.Cleanup(srv.Close)

	adapter := &ClaudeAPIAdapter{HTTPClient: srv.Client(), BaseURL: srv.URL}
	out := readClaudeAnswer(t, adapter)

	result := lastResultEvent(t, out)
	usage, ok := result["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("в событии result нет usage: %+v", result)
	}

	for field, want := range map[string]float64{
		"input_tokens":                120,
		"output_tokens":               45,
		"cache_read_input_tokens":     30,
		"cache_creation_input_tokens": 10,
	} {
		if got, _ := usage[field].(float64); got != want {
			t.Errorf("usage[%s] = %v, ожидали %v", field, usage[field], want)
		}
	}

	if _, ok := result["duration_ms"].(float64); !ok {
		t.Errorf("в событии result нет duration_ms: %+v", result)
	}
	if turns, _ := result["num_turns"].(float64); turns != 1 {
		t.Errorf("num_turns = %v, ожидали 1", result["num_turns"])
	}
}

// lastResultEvent достаёт последнее событие result из потока NDJSON.
func lastResultEvent(t *testing.T, stream string) map[string]interface{} {
	t.Helper()
	var last map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(stream), "\n") {
		var evt map[string]interface{}
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		if evt["type"] == "result" {
			last = evt
		}
	}
	if last == nil {
		t.Fatalf("в потоке нет события result: %s", stream)
	}
	return last
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
