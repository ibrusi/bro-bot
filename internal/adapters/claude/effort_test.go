package claude

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"bro-bot/internal/effort"
	"bro-bot/internal/ports"
)

func TestClaudeEffortSupportUnmarshal(t *testing.T) {
	raw := `{
		"supported": true,
		"low": {"supported": true},
		"medium": {"supported": true},
		"high": {"supported": true},
		"xhigh": {"supported": false},
		"ultra": {"supported": true}
	}`

	var support claudeEffortSupport
	if err := json.Unmarshal([]byte(raw), &support); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !support.Known {
		t.Fatal("поле capabilities.effort было в ответе, Known должен быть true")
	}
	for _, level := range []effort.Level{effort.Low, effort.Medium, effort.High} {
		if !support.supports(level) {
			t.Errorf("ожидали поддержку %q", level)
		}
	}
	for _, level := range []effort.Level{effort.XHigh, effort.Max} {
		if support.supports(level) {
			t.Errorf("уровень %q не поддерживается моделью", level)
		}
	}
	if len(support.Levels) != 3 {
		t.Errorf("незнакомые уровни должны пропускаться: %v", support.Levels)
	}
}

func TestClaudeEffortSupportUnmarshalUnsupportedModel(t *testing.T) {
	// Модель без effort (Haiku 4.5): общий флаг выключен, уровни не учитываются.
	raw := `{"supported": false, "low": {"supported": true}}`

	var support claudeEffortSupport
	if err := json.Unmarshal([]byte(raw), &support); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if support.supports(effort.Low) {
		t.Error("при supported=false ни один уровень не поддерживается")
	}
}

func TestClaudeEffortSupportUnmarshalInvalid(t *testing.T) {
	var support claudeEffortSupport
	if err := json.Unmarshal([]byte(`{"supported": true, "low": "yes"}`), &support); err == nil {
		t.Error("ожидали ошибку на некорректном описании уровня")
	}
}

func TestApplyClaudeEffort(t *testing.T) {
	resetClaudeModelCache(t)
	opus := parseClaudeModelName("claude-opus-5")
	opus.Effort = claudeEffortSupport{Known: true, Levels: map[effort.Level]bool{effort.High: true, effort.XHigh: true}}
	haiku := parseClaudeModelName("claude-haiku-4-5") // capabilities неизвестны
	modelCache.set([]claudeModel{opus, haiku})

	cases := []struct {
		name  string
		model string
		level effort.Level
		want  string // пусто — output_config не передаётся
	}{
		{"auto ничего не передаёт", "claude-opus-5", effort.Auto, ""},
		{"поддерживаемый уровень", "claude-opus-5", effort.XHigh, "xhigh"},
		{"неподдерживаемый уровень", "claude-opus-5", effort.Low, ""},
		{"неизвестные возможности", "claude-haiku-4-5", effort.High, ""},
		{"модели нет в списке", "claude-неизвестная", effort.High, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Тело уже содержит уровень от другой модели: при смене модели он
			// должен пересчитаться, а не остаться как есть.
			body := map[string]interface{}{"output_config": map[string]interface{}{"effort": "max"}}
			applyClaudeEffort(body, tc.model, tc.level)

			got := ""
			if cfg, ok := body["output_config"].(map[string]interface{}); ok {
				got, _ = cfg["effort"].(string)
			}
			if got != tc.want {
				t.Errorf("output_config.effort = %q, ожидали %q", got, tc.want)
			}
		})
	}
}

// TestExecuteTaskSendsEffort — сквозная проверка: возможности модели приходят
// из /v1/models, и уровень /effort доходит до тела запроса к /messages.
func TestExecuteTaskSendsEffort(t *testing.T) {
	cases := []struct {
		name  string
		level effort.Level
		want  string
	}{
		{"поддерживаемый уровень", effort.High, "high"},
		{"уровень, которого у модели нет", effort.Max, ""},
		{"auto", effort.Auto, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetClaudeModelCache(t)
			t.Setenv("ANTHROPIC_API_KEY", "test-key")
			t.Setenv("CLAUDE_API_MODEL", "claude-opus-5")

			srv, sentEffort := newEffortServer(t)
			adapter := &ClaudeAPIAdapter{HTTPClient: srv.Client(), BaseURL: srv.URL}

			proc, err := adapter.ExecuteTask(context.Background(), ports.ExecuteArgs{Prompt: "вопрос", Effort: tc.level})
			if err != nil {
				t.Fatalf("ExecuteTask: %v", err)
			}
			_, _ = io.ReadAll(proc.Stdout())
			if err := proc.Wait(); err != nil {
				t.Fatalf("процесс завершился с ошибкой: %v", err)
			}
			_ = proc.Close()

			if got := sentEffort(); got != tc.want {
				t.Errorf("output_config.effort = %q, ожидали %q", got, tc.want)
			}
		})
	}
}

// newEffortServer поднимает фейковый API: модель claude-opus-5 поддерживает
// уровни low–high, а /messages запоминает присланный output_config.effort.
func newEffortServer(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()

	var (
		mu   sync.Mutex
		sent string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{
				"data": [{
					"id": "claude-opus-5",
					"display_name": "Claude Opus 5",
					"capabilities": {"effort": {
						"supported": true,
						"low": {"supported": true},
						"medium": {"supported": true},
						"high": {"supported": true},
						"max": {"supported": false}
					}}
				}],
				"has_more": false
			}`))
			return
		}

		var body struct {
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		sent = body.OutputConfig.Effort
		mu.Unlock()

		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"))
	}))
	t.Cleanup(srv.Close)

	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		return sent
	}
}
