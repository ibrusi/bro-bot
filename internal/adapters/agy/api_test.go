package agy

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"bro-bot/internal/ports"

	"github.com/google/generative-ai-go/genai"
)

func TestAgyAPIAdapter_AgentName(t *testing.T) {
	adapter := NewAgyAPIAdapter()
	if adapter.AgentName() != "agy-api" {
		t.Errorf("Expected agy-api, got %s", adapter.AgentName())
	}
}

// availableFixture имитирует ответ ListModels боевого Gemini API.
func availableFixture() []geminiModel {
	ids := []string{
		"gemini-3.1-pro",
		"gemini-3.1-pro-preview",
		"gemini-3.8-flash",
		"gemini-3.8-flash-high",
		"gemini-3.7-flash",
		"gemini-3.8-flash-lite",
		"gemini-2.5-pro",
		"gemini-2.5-flash",
	}
	items := make([]geminiModel, 0, len(ids))
	for _, id := range ids {
		items = append(items, parseGeminiModelName(id))
	}
	return items
}

func TestParseGeminiModelName(t *testing.T) {
	tests := []struct {
		id      string
		tier    int
		version []int
		effort  int
		preview bool
	}{
		{"gemini-2.5-flash", tierFlash, []int{2, 5}, 0, false},
		{"models/gemini-2.5-pro", tierPro, []int{2, 5}, 0, false},
		{"gemini-3.8-flash-high", tierFlash, []int{3, 8}, 3, false},
		{"gemini-flash-3.8-high", tierFlash, []int{3, 8}, 3, false},
		{"gemini-2.5-flash-lite", tierFlashLite, []int{2, 5}, 0, false},
		{"gemini-1.5-flash-8b", tierFlashLite, []int{1, 5}, 0, false},
		{"gemini-3.1-pro-preview-06-17", tierPro, []int{3, 1}, 0, true},
		{"gemini-3.1-pro-low", tierPro, []int{3, 1}, 1, false},
	}

	for _, tt := range tests {
		got := parseGeminiModelName(tt.id)
		if got.Tier != tt.tier {
			t.Errorf("parseGeminiModelName(%q).Tier = %d, want %d", tt.id, got.Tier, tt.tier)
		}
		if compareVersions(got.Version, tt.version) != 0 {
			t.Errorf("parseGeminiModelName(%q).Version = %v, want %v", tt.id, got.Version, tt.version)
		}
		if got.Effort != tt.effort {
			t.Errorf("parseGeminiModelName(%q).Effort = %d, want %d", tt.id, got.Effort, tt.effort)
		}
		if got.Preview != tt.preview {
			t.Errorf("parseGeminiModelName(%q).Preview = %v, want %v", tt.id, got.Preview, tt.preview)
		}
	}
}

func TestPickDefaultGeminiModel_JuniorOfSeniors(t *testing.T) {
	// Доступна флагманская 3.1 Pro, но по умолчанию берём младшую из старших —
	// максимальную доступную flash.
	def, ok := pickDefaultGeminiModel(availableFixture())
	if !ok {
		t.Fatal("ожидали выбор модели по умолчанию")
	}
	if def.ID != "gemini-3.8-flash-high" {
		t.Errorf("pickDefaultGeminiModel = %q, want gemini-3.8-flash-high", def.ID)
	}
}

func TestPickDefaultGeminiModel_NoFlash(t *testing.T) {
	only := []geminiModel{
		parseGeminiModelName("gemini-2.5-pro"),
		parseGeminiModelName("gemini-3.1-pro"),
	}
	def, ok := pickDefaultGeminiModel(only)
	if !ok || def.ID != "gemini-3.1-pro" {
		t.Errorf("pickDefaultGeminiModel = %q (ok=%v), want gemini-3.1-pro", def.ID, ok)
	}
}

func TestPickDefaultGeminiModel_Empty(t *testing.T) {
	if _, ok := pickDefaultGeminiModel(nil); ok {
		t.Error("ожидали ok=false для пустого списка")
	}
}

func TestMatchGeminiModel(t *testing.T) {
	available := availableFixture()
	tests := []struct {
		requested string
		want      string
	}{
		{"gemini-2.5-pro", "gemini-2.5-pro"},                 // точное совпадение
		{"gemini-3.8-flash-medium", "gemini-3.8-flash-high"}, // та же версия, лучший вариант уровня
		{"gemini-4.0-pro-high", "gemini-3.1-pro"},            // версии нет — максимальная pro
		{"gemini-1.5-flash", "gemini-3.8-flash-high"},        // устаревшая flash — максимальная flash
		{"gemini-1.5-flash-8b", "gemini-3.8-flash-lite"},     // lite остаётся lite
	}

	for _, tt := range tests {
		got, ok := matchGeminiModel(tt.requested, available)
		if !ok || got.ID != tt.want {
			t.Errorf("matchGeminiModel(%q) = %q (ok=%v), want %q", tt.requested, got.ID, ok, tt.want)
		}
	}

	if _, ok := matchGeminiModel("claude-opus-4-6-thinking", available); ok {
		t.Error("модель чужого семейства не должна сопоставляться")
	}
}

func TestResolveGeminiModel(t *testing.T) {
	available := availableFixture()
	tests := []struct {
		requested string
		want      string
	}{
		{"", "gemini-3.8-flash-high"},                  // дефолт — младшая из старших
		{"default", "gemini-3.8-flash-high"},           // алиас дефолта
		{"gemini-1.5-flash", "gemini-3.8-flash-high"},  // отключённая модель → живая flash
		{"gemini-3.1-pro", "gemini-3.1-pro"},           // явный выбор уважаем
		{"claude-sonnet-4-6", "gemini-3.8-flash-high"}, // чужое семейство → дефолт
		{"gemini-2.5-flash", "gemini-2.5-flash"},       // точное совпадение
	}

	for _, tt := range tests {
		if got := resolveGeminiModel(tt.requested, available); got != tt.want {
			t.Errorf("resolveGeminiModel(%q) = %q, want %q", tt.requested, got, tt.want)
		}
	}
}

func TestResolveGeminiModel_EnvOverride(t *testing.T) {
	t.Setenv("GEMINI_API_MODEL", "gemini-2.5-pro")
	if got := resolveGeminiModel("gemini-3.8-flash", availableFixture()); got != "gemini-2.5-pro" {
		t.Errorf("resolveGeminiModel с GEMINI_API_MODEL = %q, want gemini-2.5-pro", got)
	}
}

func TestResolveGeminiModel_NoAvailableList(t *testing.T) {
	// Список моделей недоступен: реальное имя используем как есть...
	if got := resolveGeminiModel("gemini-2.5-flash", nil); got != "gemini-2.5-flash" {
		t.Errorf("resolveGeminiModel(без списка) = %q, want gemini-2.5-flash", got)
	}
	// ...а для непонятного имени берём скользящий алиас, а не выдуманную модель.
	if got := resolveGeminiModel("claude-sonnet-4-6", nil); got != fallbackGeminiModel {
		t.Errorf("resolveGeminiModel(чужая модель, без списка) = %q, want %q", got, fallbackGeminiModel)
	}
}

func TestFormatGeminiModelsList(t *testing.T) {
	out := formatGeminiModelsList(availableFixture())
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(availableFixture()) {
		t.Fatalf("ожидали %d строк, получили %d", len(availableFixture()), len(lines))
	}
	// Первыми идут старшие модели
	if !strings.HasPrefix(lines[0], "gemini-3.1-pro ") {
		t.Errorf("первая строка = %q, ожидали gemini-3.1-pro", lines[0])
	}
	if strings.Contains(out, "gemini-1.5") {
		t.Error("список не должен содержать отключённые модели 1.5")
	}
}

func TestFormatGeminiModelsList_FallbackWhenEmpty(t *testing.T) {
	out := formatGeminiModelsList(nil)
	if !strings.Contains(out, fallbackGeminiModel) {
		t.Errorf("ожидали запасной список с %q, получили %q", fallbackGeminiModel, out)
	}
}

func TestAgyAPIAdapter_GetModels_NoKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	if err := os.Unsetenv("GEMINI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	adapter := NewAgyAPIAdapter()
	if _, err := adapter.GetModels(context.Background()); err == nil {
		t.Error("ожидали ошибку об отсутствующем GEMINI_API_KEY")
	}
}

func TestIsModelUnavailableError(t *testing.T) {
	// Ровно та ошибка, которую отдавал API на отключённой модели.
	reported := errors.New("googleapi: Error 404: models/gemini-1.5-flash is not found for API version v1beta, " +
		"or is not supported for generateContent. Call ModelService.ListModels to see the list of available models " +
		"and their supported methods.")
	if !isModelUnavailableError(reported) {
		t.Error("ожидали распознавание ошибки об отсутствующей модели")
	}
	if isModelUnavailableError(errors.New("context deadline exceeded")) {
		t.Error("сетевую ошибку не считаем проблемой модели")
	}
	if isModelUnavailableError(nil) {
		t.Error("nil не является ошибкой модели")
	}
}

func TestGeminiHistoryContentsRoleMapping(t *testing.T) {
	history := []ports.ChatMessage{
		{Role: "user", Content: "привет"},
		{Role: "assistant", Content: "здравствуй"},
		{Role: "model", Content: "уточню"},
		{Role: "user", Content: "   "}, // пустые реплики отбрасываем
	}

	contents := geminiHistoryContents(history)
	if len(contents) != 3 {
		t.Fatalf("ожидали 3 реплики, получили %d", len(contents))
	}
	wantRoles := []string{"user", "model", "model"}
	for i, want := range wantRoles {
		if contents[i].Role != want {
			t.Errorf("роль реплики %d = %q, ожидали %q", i, contents[i].Role, want)
		}
	}
	if text, ok := contents[0].Parts[0].(genai.Text); !ok || string(text) != "привет" {
		t.Errorf("текст первой реплики: %+v", contents[0].Parts[0])
	}

	if geminiHistoryContents(nil) != nil {
		t.Error("пустая история должна давать nil")
	}
	if geminiHistoryContents([]ports.ChatMessage{{Role: "user", Content: " "}}) != nil {
		t.Error("история из пустых реплик должна давать nil")
	}
}

func TestUsageFromMetadata(t *testing.T) {
	if usageFromMetadata(nil) != nil {
		t.Error("без метаданных ожидали nil")
	}

	usage := usageFromMetadata(&genai.UsageMetadata{
		PromptTokenCount:        120,
		CandidatesTokenCount:    45,
		CachedContentTokenCount: 10,
	})
	if usage["input_tokens"] != int64(120) || usage["output_tokens"] != int64(45) || usage["cache_read_input_tokens"] != int64(10) {
		t.Errorf("неожиданные счётчики токенов: %+v", usage)
	}
}
