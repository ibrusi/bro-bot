package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// resetClaudeModelCache очищает кэш моделей между тестами.
func resetClaudeModelCache(t *testing.T) {
	t.Helper()
	modelCache.mu.Lock()
	modelCache.models = nil
	modelCache.fetched = time.Time{}
	modelCache.mu.Unlock()
}

// availableClaudeFixture имитирует ответ /v1/models боевого API.
func availableClaudeFixture() []claudeModel {
	ids := []string{
		"claude-opus-5",
		"claude-opus-4-8",
		"claude-sonnet-5",
		"claude-sonnet-4-6",
		"claude-haiku-4-5",
		"claude-fable-5-1",
	}
	items := make([]claudeModel, 0, len(ids))
	for _, id := range ids {
		items = append(items, parseClaudeModelName(id))
	}
	return items
}

func TestParseClaudeModelName(t *testing.T) {
	tests := []struct {
		id      string
		tier    int
		version []int
		dated   bool
	}{
		{"claude-opus-5", claudeTierOpus, []int{5}, false},
		{"claude-opus-4-8", claudeTierOpus, []int{4, 8}, false},
		{"claude-sonnet-5", claudeTierSonnet, []int{5}, false},
		{"claude-sonnet-4-6", claudeTierSonnet, []int{4, 6}, false},
		{"claude-haiku-4-5", claudeTierHaiku, []int{4, 5}, false},
		{"claude-haiku-4-5-20251001", claudeTierHaiku, []int{4, 5}, true},
		{"claude-fable-5-1", claudeTierFable, []int{5, 1}, false},
		// Старое поколение: версия стоит перед семейством, в конце — дата снапшота.
		{"claude-3-5-sonnet-20241022", claudeTierSonnet, []int{3, 5}, true},
		{"claude-3-opus-20240229", claudeTierOpus, []int{3}, true},
		{"claude-3-7-sonnet-20250219", claudeTierSonnet, []int{3, 7}, true},
	}

	for _, tt := range tests {
		got := parseClaudeModelName(tt.id)
		if got.Tier != tt.tier {
			t.Errorf("parseClaudeModelName(%q).Tier = %d, ожидали %d", tt.id, got.Tier, tt.tier)
		}
		if compareClaudeVersions(got.Version, tt.version) != 0 {
			t.Errorf("parseClaudeModelName(%q).Version = %v, ожидали %v", tt.id, got.Version, tt.version)
		}
		if got.Dated != tt.dated {
			t.Errorf("parseClaudeModelName(%q).Dated = %v, ожидали %v", tt.id, got.Dated, tt.dated)
		}
	}
}

func TestPickDefaultClaudeModelJuniorOfSeniors(t *testing.T) {
	// Доступны opus и fable, но по умолчанию берём младшую из старших — свежайший sonnet.
	def, ok := pickDefaultClaudeModel(availableClaudeFixture())
	if !ok {
		t.Fatal("ожидали выбор модели по умолчанию")
	}
	if def.ID != "claude-sonnet-5" {
		t.Errorf("pickDefaultClaudeModel = %q, ожидали claude-sonnet-5", def.ID)
	}
}

func TestPickDefaultClaudeModelWithoutSonnet(t *testing.T) {
	only := []claudeModel{
		parseClaudeModelName("claude-opus-5"),
		parseClaudeModelName("claude-haiku-4-5"),
	}
	def, ok := pickDefaultClaudeModel(only)
	if !ok || def.ID != "claude-haiku-4-5" {
		t.Errorf("без sonnet ожидали haiku, получили %q (ok=%v)", def.ID, ok)
	}

	opusOnly := []claudeModel{parseClaudeModelName("claude-opus-4-8"), parseClaudeModelName("claude-opus-5")}
	def, ok = pickDefaultClaudeModel(opusOnly)
	if !ok || def.ID != "claude-opus-5" {
		t.Errorf("остались только opus — ожидали свежайший, получили %q (ok=%v)", def.ID, ok)
	}

	if _, ok := pickDefaultClaudeModel(nil); ok {
		t.Error("для пустого списка ожидали ok=false")
	}
}

// TestMatchRetiredClaudeModel — главный сторож исходного бага: сохранённая в базе снятая
// с обслуживания модель должна подменяться живой моделью того же семейства, а не давать 404.
func TestMatchRetiredClaudeModel(t *testing.T) {
	available := availableClaudeFixture()

	tests := []struct {
		requested string
		want      string
	}{
		{"claude-3-opus-20240229", "claude-opus-5"},       // снятый opus → живой opus
		{"claude-3-5-sonnet-20241022", "claude-sonnet-5"}, // снятый sonnet → живой sonnet
		{"claude-3-5-haiku-20241022", "claude-haiku-4-5"}, // снятый haiku → живой haiku
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},        // точное совпадение не трогаем
		{"claude-opus-5", "claude-opus-5"},
	}

	for _, tt := range tests {
		got, ok := matchClaudeModel(tt.requested, available)
		if !ok || got.ID != tt.want {
			t.Errorf("matchClaudeModel(%q) = %q (ok=%v), ожидали %q", tt.requested, got.ID, ok, tt.want)
		}
	}

	if _, ok := matchClaudeModel("gemini-3.1-pro-high", available); ok {
		t.Error("модель чужого семейства не должна сопоставляться")
	}
}

func TestResolveClaudeAPIModel(t *testing.T) {
	available := availableClaudeFixture()

	tests := []struct {
		requested string
		want      string
	}{
		{"", "claude-sonnet-5"},                     // дефолт — младшая из старших
		{"default", "claude-sonnet-5"},              // алиас дефолта
		{"claude-3-opus-20240229", "claude-opus-5"}, // снятая модель из базы
		{"claude-haiku-4-5", "claude-haiku-4-5"},    // точное совпадение
		{"gemini-3.1-pro-high", "claude-sonnet-5"},  // чужое семейство → дефолт
	}

	for _, tt := range tests {
		if got := resolveClaudeAPIModel(tt.requested, available); got != tt.want {
			t.Errorf("resolveClaudeAPIModel(%q) = %q, ожидали %q", tt.requested, got, tt.want)
		}
	}
}

// TestResolveClaudeAPIModelAliasesAreNotDead — короткие алиасы больше не превращаются
// в снятые с обслуживания claude-3-*.
func TestResolveClaudeAPIModelAliases(t *testing.T) {
	available := availableClaudeFixture()

	// Реестр моделей разворачивает короткие алиасы в полные имена ещё до адаптера,
	// поэтому сюда приходят имена семейств с версией.
	for requested, want := range map[string]string{
		"claude-sonnet-4-6":        "claude-sonnet-4-6",
		"claude-opus-4-6-thinking": "claude-opus-5",
		"claude-haiku-4-5":         "claude-haiku-4-5",
	} {
		got := resolveClaudeAPIModel(requested, available)
		if got != want {
			t.Errorf("resolveClaudeAPIModel(%q) = %q, ожидали %q", requested, got, want)
		}
		if strings.HasPrefix(got, "claude-3-") {
			t.Errorf("resolveClaudeAPIModel(%q) вернул снятую модель %q", requested, got)
		}
	}
}

func TestResolveClaudeAPIModelEnvOverride(t *testing.T) {
	t.Setenv("CLAUDE_API_MODEL", "claude-opus-4-8")
	if got := resolveClaudeAPIModel("claude-sonnet-5", availableClaudeFixture()); got != "claude-opus-4-8" {
		t.Errorf("CLAUDE_API_MODEL не сработал: %q", got)
	}
}

func TestResolveClaudeAPIModelWithoutList(t *testing.T) {
	// Список недоступен: реальное имя используем как есть...
	if got := resolveClaudeAPIModel("claude-opus-5", nil); got != "claude-opus-5" {
		t.Errorf("resolveClaudeAPIModel(без списка) = %q, ожидали claude-opus-5", got)
	}
	// ...а для непонятного имени берём аварийное значение, а не выдуманный id.
	if got := resolveClaudeAPIModel("gemini-3.1-pro-high", nil); got != fallbackClaudeModel {
		t.Errorf("resolveClaudeAPIModel(чужая модель, без списка) = %q, ожидали %q", got, fallbackClaudeModel)
	}
}

func TestIsClaudeModelUnavailableError(t *testing.T) {
	// Ровно та ошибка, которую отдавал API на снятой модели.
	reported := fmt.Errorf(`API HTTP 404: {"type":"error","error":{"type":"not_found_error","message":"model: claude-3-opus-20240229"}}`)
	if !isClaudeModelUnavailableError(reported) {
		t.Error("ожидали распознавание ошибки об отсутствующей модели")
	}
	if isClaudeModelUnavailableError(fmt.Errorf("context deadline exceeded")) {
		t.Error("сетевую ошибку не считаем проблемой модели")
	}
	if isClaudeModelUnavailableError(nil) {
		t.Error("nil не является ошибкой модели")
	}
}

func TestFormatClaudeModelsList(t *testing.T) {
	out := formatClaudeModelsList(availableClaudeFixture())
	lines := strings.Split(strings.TrimSpace(out), "\n")

	if len(lines) != len(availableClaudeFixture()) {
		t.Fatalf("ожидали %d строк, получили %d", len(availableClaudeFixture()), len(lines))
	}
	// Первым идёт верхний уровень.
	if !strings.HasPrefix(lines[0], "claude-fable-5-1 ") {
		t.Errorf("первая строка = %q, ожидали claude-fable-5-1", lines[0])
	}
	if strings.Contains(out, "claude-3-") {
		t.Error("в списке не должно быть снятых с обслуживания моделей")
	}
	// Формат строки — «id Отображаемое имя», как его ждёт реестр моделей.
	for _, line := range lines {
		if len(strings.Fields(line)) < 2 {
			t.Errorf("строка %q не содержит отображаемого имени", line)
		}
	}
}

// newModelsServer поднимает фейковый /v1/models с постраничной выдачей.
func newModelsServer(t *testing.T, pages [][]string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("неожиданный путь запроса: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("не передан ключ API: %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("не передана версия API: %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "1000" {
			t.Errorf("limit = %q, ожидали 1000 (иначе список обрежется до 20)", got)
		}

		pageIdx := 0
		if after := r.URL.Query().Get("after_id"); after != "" {
			for i, page := range pages {
				if len(page) > 0 && page[len(page)-1] == after {
					pageIdx = i + 1
				}
			}
		}
		if pageIdx >= len(pages) {
			pageIdx = len(pages) - 1
		}

		page := pages[pageIdx]
		data := make([]map[string]interface{}, 0, len(page))
		for _, id := range page {
			data = append(data, map[string]interface{}{
				"type":         "model",
				"id":           id,
				"display_name": strings.ToUpper(id[:1]) + id[1:],
				"created_at":   "2026-01-01T00:00:00Z",
			})
		}

		resp := map[string]interface{}{
			"data":     data,
			"has_more": pageIdx < len(pages)-1,
			"last_id":  page[len(page)-1],
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListClaudeModelsPagination(t *testing.T) {
	resetClaudeModelCache(t)

	srv := newModelsServer(t, [][]string{
		{"claude-opus-5", "claude-sonnet-5"},
		{"claude-haiku-4-5"},
	})

	models, err := listClaudeModels(context.Background(), srv.Client(), srv.URL, "test-key", true)
	if err != nil {
		t.Fatalf("listClaudeModels: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("ожидали 3 модели со всех страниц, получили %d: %+v", len(models), models)
	}
	if models[0].DisplayName == "" {
		t.Error("отображаемое имя не заполнено")
	}
	if models[0].CreatedAt.IsZero() {
		t.Error("дата выпуска не разобрана")
	}
}

func TestListClaudeModelsUsesCache(t *testing.T) {
	resetClaudeModelCache(t)

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data":     []map[string]interface{}{{"id": "claude-sonnet-5", "display_name": "Claude Sonnet 5"}},
			"has_more": false,
			"last_id":  "claude-sonnet-5",
		})
	}))
	defer srv.Close()

	ctx := context.Background()
	if _, err := listClaudeModels(ctx, srv.Client(), srv.URL, "k", true); err != nil {
		t.Fatalf("первый запрос: %v", err)
	}
	if _, err := listClaudeModels(ctx, srv.Client(), srv.URL, "k", false); err != nil {
		t.Fatalf("второй запрос: %v", err)
	}
	if calls != 1 {
		t.Errorf("ожидали один сетевой запрос благодаря кэшу, получили %d", calls)
	}
}

func TestListClaudeModelsFallsBackToCache(t *testing.T) {
	resetClaudeModelCache(t)

	okSrv := newModelsServer(t, [][]string{{"claude-sonnet-5"}})
	ctx := context.Background()
	if _, err := listClaudeModels(ctx, okSrv.Client(), okSrv.URL, "test-key", true); err != nil {
		t.Fatalf("первый запрос: %v", err)
	}

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	models, err := listClaudeModels(ctx, failSrv.Client(), failSrv.URL, "test-key", true)
	if err != nil {
		t.Fatalf("при сбое ожидали прошлый список, получили ошибку: %v", err)
	}
	if len(models) != 1 || models[0].ID != "claude-sonnet-5" {
		t.Errorf("ожидали закэшированный список, получили %+v", models)
	}
}

func TestGetModelsReturnsLiveList(t *testing.T) {
	resetClaudeModelCache(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	srv := newModelsServer(t, [][]string{{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"}})

	adapter := &ClaudeAPIAdapter{HTTPClient: srv.Client(), BaseURL: srv.URL}
	out, err := adapter.GetModels(context.Background())
	if err != nil {
		t.Fatalf("GetModels: %v", err)
	}

	text := string(out)
	for _, id := range []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"} {
		if !strings.Contains(text, id) {
			t.Errorf("в списке нет модели %s: %q", id, text)
		}
	}
	if strings.Contains(text, "claude-3-") {
		t.Errorf("в списке остались снятые модели: %q", text)
	}
}

func TestGetModelsRequiresAPIKey(t *testing.T) {
	resetClaudeModelCache(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_API_KEY", "")

	adapter := NewClaudeAPIAdapter()
	if _, err := adapter.GetModels(context.Background()); err == nil {
		t.Error("ожидали ошибку об отсутствующем ключе API")
	}
}
