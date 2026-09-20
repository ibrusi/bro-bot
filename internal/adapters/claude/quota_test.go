package claude

import (
	"bro-bot/internal/i18n"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// resetClaudeRateLimits очищает снимок лимитов между тестами.
func resetClaudeRateLimits(t *testing.T) {
	t.Helper()
	rateLimitsStore.mu.Lock()
	rateLimitsStore.last = claudeRateLimits{}
	rateLimitsStore.mu.Unlock()
	t.Cleanup(func() {
		rateLimitsStore.mu.Lock()
		rateLimitsStore.last = claudeRateLimits{}
		rateLimitsStore.mu.Unlock()
	})
}

// rateLimitHeaders собирает заголовки лимитов так, как их отдаёт Messages API.
func rateLimitHeaders() http.Header {
	h := http.Header{}
	h.Set("anthropic-ratelimit-requests-limit", "50")
	h.Set("anthropic-ratelimit-requests-remaining", "40")
	h.Set("anthropic-ratelimit-requests-reset", "2026-09-18T12:00:00Z")
	h.Set("anthropic-ratelimit-input-tokens-limit", "20000")
	h.Set("anthropic-ratelimit-input-tokens-remaining", "5000")
	h.Set("anthropic-ratelimit-input-tokens-reset", "2026-09-18T12:01:00Z")
	h.Set("anthropic-ratelimit-output-tokens-limit", "4000")
	h.Set("anthropic-ratelimit-output-tokens-remaining", "0")
	h.Set("anthropic-ratelimit-output-tokens-reset", "2026-09-18T12:02:00Z")
	return h
}

// newHeaderServer отвечает заданным кодом и заголовками — этого достаточно,
// чтобы проверить снятие лимитов с ответа.
func newHeaderServer(t *testing.T, status int, header http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range header {
			for _, v := range values {
				w.Header().Add(name, v)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCaptureClaudeRateLimitsReadsHeaders(t *testing.T) {
	resetClaudeRateLimits(t)

	captureClaudeRateLimits(rateLimitHeaders())

	snapshot := lastClaudeRateLimits()
	if len(snapshot.Buckets) != 3 {
		t.Fatalf("ожидали три корзины лимитов, получили %+v", snapshot.Buckets)
	}
	if snapshot.CapturedAt.IsZero() {
		t.Error("время снимка не проставлено")
	}

	byKey := map[string]claudeRateLimitBucket{}
	for _, b := range snapshot.Buckets {
		byKey[b.NameKey] = b
	}

	requests, ok := byKey["quota.bucket_requests"]
	if !ok {
		t.Fatalf("нет корзины запросов: %+v", snapshot.Buckets)
	}
	if requests.Limit != 50 || requests.Remaining != 40 {
		t.Errorf("лимит запросов разобран как %d/%d", requests.Remaining, requests.Limit)
	}
	if frac := requests.Fraction(); frac == nil || *frac != 0.8 {
		t.Errorf("доля остатка запросов = %v, ожидали 0.8", frac)
	}
	if requests.Reset != "2026-09-18T12:00:00Z" {
		t.Errorf("время сброса = %q", requests.Reset)
	}

	if frac := byKey["quota.bucket_output_tokens"].Fraction(); frac == nil || *frac != 0 {
		t.Errorf("исчерпанная корзина должна давать 0, получили %v", frac)
	}
}

func TestCaptureClaudeRateLimitsIgnoresEmptyHeaders(t *testing.T) {
	resetClaudeRateLimits(t)

	captureClaudeRateLimits(rateLimitHeaders())
	captureClaudeRateLimits(http.Header{}) // ответ без заголовков лимитов

	if len(lastClaudeRateLimits().Buckets) != 3 {
		t.Error("ответ без заголовков не должен затирать прошлый снимок")
	}
}

// TestDoClaudeRequestCapturesLimitsOn429 — на 429 заголовки лимитов приходят тоже,
// и это самый нужный момент, чтобы их запомнить.
func TestDoClaudeRequestCapturesLimitsOn429(t *testing.T) {
	resetClaudeRateLimits(t)
	resetClaudeModelCache(t)

	srv := newHeaderServer(t, http.StatusTooManyRequests, rateLimitHeaders())
	stream := claudeStreamRequest{
		client:  srv.Client(),
		baseURL: srv.URL,
		apiKey:  "test-key",
	}

	if _, err := doClaudeRequest(context.Background(), stream, "claude-sonnet-5"); err == nil {
		t.Fatal("ожидали ошибку на 429")
	}

	if len(lastClaudeRateLimits().Buckets) == 0 {
		t.Error("лимиты с ответа 429 не сохранены")
	}
}

func TestGetQuotaBuildsGroupsFromSnapshot(t *testing.T) {
	resetClaudeRateLimits(t)
	captureClaudeRateLimits(rateLimitHeaders())

	raw, err := (&ClaudeAPIAdapter{}).GetQuota(context.Background(), i18n.Default)
	if err != nil {
		t.Fatalf("GetQuota: %v", err)
	}

	var parsed struct {
		Command struct {
			Data struct {
				Description string `json:"description"`
				Groups      []struct {
					Name    string `json:"name"`
					Buckets []struct {
						Name              string   `json:"name"`
						RemainingFraction *float64 `json:"remaining_fraction"`
						ResetTime         string   `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("ответ не разбирается рендером /usage: %v (%s)", err, raw)
	}

	if len(parsed.Command.Data.Groups) != 1 {
		t.Fatalf("ожидали одну группу лимитов, получили %d", len(parsed.Command.Data.Groups))
	}
	buckets := parsed.Command.Data.Groups[0].Buckets
	if len(buckets) != 3 {
		t.Fatalf("ожидали три корзины, получили %d", len(buckets))
	}
	if buckets[0].RemainingFraction == nil || *buckets[0].RemainingFraction != 0.8 {
		t.Errorf("доля остатка первой корзины = %v", buckets[0].RemainingFraction)
	}
	if _, err := time.Parse(time.RFC3339, buckets[0].ResetTime); err != nil {
		t.Errorf("время сброса не в RFC 3339: %q", buckets[0].ResetTime)
	}
}

// TestGetQuotaWithoutSnapshotHasNoGroups — пустых шкал не рисуем: пока запросов
// не было, структурных данных нет, и обработчик покажет текст.
func TestGetQuotaWithoutSnapshotHasNoGroups(t *testing.T) {
	resetClaudeRateLimits(t)

	raw, err := (&ClaudeAPIAdapter{}).GetQuota(context.Background(), i18n.Default)
	if err != nil {
		t.Fatalf("GetQuota: %v", err)
	}
	if strings.Contains(string(raw), "groups") {
		t.Errorf("без снимка групп быть не должно: %s", raw)
	}
}

func TestGetQuotaTextExplainsMissingSnapshot(t *testing.T) {
	resetClaudeRateLimits(t)
	resetClaudeModelCache(t)

	raw, err := (&ClaudeAPIAdapter{}).GetQuotaText(context.Background(), i18n.Default)
	if err != nil {
		t.Fatalf("GetQuotaText: %v", err)
	}

	text := string(raw)
	if !strings.Contains(text, "after the agent's first answer") {
		t.Errorf("текст не объясняет, откуда возьмутся лимиты: %s", text)
	}
	if strings.Contains(text, "SUCCESS") || strings.Contains(text, "{") {
		t.Errorf("в человеческую сводку утёк служебный JSON: %s", text)
	}
}

func TestGetQuotaTextShowsLimitsAndModel(t *testing.T) {
	resetClaudeRateLimits(t)
	resetClaudeModelCache(t)
	captureClaudeRateLimits(rateLimitHeaders())

	modelCache.set([]claudeModel{
		withLimits(parseClaudeModelName("claude-sonnet-5"), 200000, 64000),
		withLimits(parseClaudeModelName("claude-opus-5"), 200000, 32000),
	})

	raw, err := (&ClaudeAPIAdapter{}).GetQuotaText(context.Background(), i18n.Default)
	if err != nil {
		t.Fatalf("GetQuotaText: %v", err)
	}

	text := string(raw)
	for _, want := range []string{"Requests", "claude-sonnet-5", "context 200K", "/tokens"} {
		if !strings.Contains(text, want) {
			t.Errorf("в сводке нет %q: %s", want, text)
		}
	}
}

func TestGetCreditsIsEmptyForAPIKey(t *testing.T) {
	raw, err := (&ClaudeAPIAdapter{}).GetCredits(context.Background())
	if err != nil {
		t.Fatalf("GetCredits: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "{}" {
		t.Errorf("у ключа API нет кредитов, ожидали пустой объект, получили %s", raw)
	}
}

func TestClaudeMaxTokensFromModelLimit(t *testing.T) {
	resetClaudeModelCache(t)
	modelCache.set([]claudeModel{
		withLimits(parseClaudeModelName("claude-sonnet-5"), 200000, 32000),
		withLimits(parseClaudeModelName("claude-opus-5"), 200000, 200000), // выше потолка стриминга
		parseClaudeModelName("claude-haiku-4-5"),                          // лимит неизвестен
	})

	cases := []struct {
		model string
		want  int
	}{
		{"claude-sonnet-5", 32000},
		{"claude-opus-5", claudeStreamMaxTokens},
		{"claude-haiku-4-5", claudeDefaultMaxTokens},
		{"claude-неизвестная", claudeDefaultMaxTokens},
	}
	for _, tc := range cases {
		if got := claudeMaxTokensFor(tc.model); got != tc.want {
			t.Errorf("claudeMaxTokensFor(%q) = %d, ожидали %d", tc.model, got, tc.want)
		}
	}
}

func withLimits(m claudeModel, maxInput, maxTokens int) claudeModel {
	m.MaxInputTokens = maxInput
	m.MaxTokens = maxTokens
	return m
}
