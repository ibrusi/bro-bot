package agy

import (
	"bro-bot/internal/i18n"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// seedGeminiModelCache кладёт в кэш список моделей с лимитами и чистит его после теста.
func seedGeminiModelCache(t *testing.T, items []geminiModel) {
	t.Helper()
	modelCache.set(items)
	t.Cleanup(func() {
		modelCache.mu.Lock()
		modelCache.models = nil
		modelCache.fetched = time.Time{}
		modelCache.mu.Unlock()
	})
}

func withGeminiLimits(m geminiModel, input, output int) geminiModel {
	m.InputTokenLimit = input
	m.OutputTokenLimit = output
	return m
}

// TestAgyGetQuotaHasNoGroups — Gemini API не сообщает остаток квоты по ключу,
// поэтому структурных данных быть не должно: проценты рисовать не из чего.
func TestAgyGetQuotaHasNoGroups(t *testing.T) {
	raw, err := NewAgyAPIAdapter().GetQuota(context.Background(), i18n.Default)
	if err != nil {
		t.Fatalf("GetQuota: %v", err)
	}

	var parsed struct {
		Command struct {
			Data struct {
				Description string        `json:"description"`
				Groups      []interface{} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("ответ не разбирается рендером /usage: %v (%s)", err, raw)
	}
	if len(parsed.Command.Data.Groups) != 0 {
		t.Errorf("групп быть не должно: %s", raw)
	}
	if strings.TrimSpace(parsed.Command.Data.Description) == "" {
		t.Errorf("ожидали пояснение вместо групп: %s", raw)
	}
}

func TestAgyGetQuotaTextShowsModelLimits(t *testing.T) {
	seedGeminiModelCache(t, []geminiModel{
		withGeminiLimits(parseGeminiModelName("gemini-3.8-flash-high"), 1048576, 65536),
		withGeminiLimits(parseGeminiModelName("gemini-3.1-pro"), 2097152, 65536),
	})

	raw, err := NewAgyAPIAdapter().GetQuotaText(context.Background(), i18n.Default)
	if err != nil {
		t.Fatalf("GetQuotaText: %v", err)
	}

	text := string(raw)
	// Модель по умолчанию — младшая из старших, её лимиты и показываем.
	for _, want := range []string{"gemini-3.8-flash-high", "context 1.0M", "66K", "/tokens", "AI Studio"} {
		if !strings.Contains(text, want) {
			t.Errorf("в сводке нет %q: %s", want, text)
		}
	}
	if strings.Contains(text, "SUCCESS") || strings.Contains(text, "{") {
		t.Errorf("в человеческую сводку утёк служебный JSON: %s", text)
	}
}

func TestAgyGetQuotaTextWithoutModelCache(t *testing.T) {
	seedGeminiModelCache(t, nil)

	raw, err := NewAgyAPIAdapter().GetQuotaText(context.Background(), i18n.Default)
	if err != nil {
		t.Fatalf("GetQuotaText: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, "Модель ") {
		t.Errorf("без кэша моделей лимиты выдумывать нельзя: %s", text)
	}
	if !strings.Contains(text, "Google Cloud Console") {
		t.Errorf("ожидали указание, где смотреть квоты: %s", text)
	}
}

func TestAgyGetCreditsIsEmptyForAPIKey(t *testing.T) {
	raw, err := NewAgyAPIAdapter().GetCredits(context.Background())
	if err != nil {
		t.Fatalf("GetCredits: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "{}" {
		t.Errorf("у ключа API нет кредитов, ожидали пустой объект, получили %s", raw)
	}
}
