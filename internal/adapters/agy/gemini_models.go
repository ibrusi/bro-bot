package agy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bro-bot/internal/config"
	"bro-bot/internal/models"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// Уровни моделей Gemini: чем больше значение, тем "старше" модель.
const (
	tierUnknown   = 0
	tierFlashLite = 1 // flash-lite / 8b / nano — самые лёгкие
	tierFlash     = 2 // flash — младшая из старших
	tierPro       = 3 // pro / ultra — флагманы
)

// defaultTierPreference задаёт порядок выбора модели по умолчанию:
// берём "младшую из старших" (flash), затем совсем лёгкую (flash-lite),
// и только если ничего из этого нет — флагманскую pro.
var defaultTierPreference = []int{tierFlash, tierFlashLite, tierPro}

// fallbackGeminiModel используется, только если список моделей получить не удалось.
// Это скользящий алиас API, поэтому он не устаревает вместе с конкретными версиями.
const fallbackGeminiModel = "gemini-flash-latest"

// fallbackGeminiModels — статический список на случай недоступности ListModels.
var fallbackGeminiModels = []geminiModel{
	{ID: "gemini-flash-latest", DisplayName: "Gemini Flash (latest)"},
	{ID: "gemini-flash-lite-latest", DisplayName: "Gemini Flash-Lite (latest)"},
	{ID: "gemini-pro-latest", DisplayName: "Gemini Pro (latest)"},
}

var (
	versionTokenRegex = regexp.MustCompile(`^\d+(\.\d+)*$`)

	// Модели, которые не умеют в обычный текстовый чат либо решают другие задачи.
	nonChatModelMarkers = []string{
		"embedding", "aqa", "imagen", "veo", "tts", "-image", "image-",
		"audio", "live", "vision", "learnlm", "gemma", "computer-use",
	}
)

// geminiModel — разобранное имя модели Gemini API.
type geminiModel struct {
	ID          string // имя для generateContent, без префикса models/
	DisplayName string
	Tier        int
	Version     []int
	Effort      int  // high=3, medium=2, low=1, не указан=0
	Preview     bool // preview / exp / experimental

	// Лимиты модели из ListModels: окно контекста и предельный размер ответа.
	InputTokenLimit  int
	OutputTokenLimit int
}

// parseGeminiModelName разбирает имя модели в структуру с семейством и версией.
// Понимает оба порядка токенов: gemini-3.8-flash-high и gemini-flash-3.8-high.
func parseGeminiModelName(raw string) geminiModel {
	id := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "models/")
	m := geminiModel{ID: id}

	switch {
	case strings.Contains(id, "lite"), strings.Contains(id, "8b"), strings.Contains(id, "nano"):
		m.Tier = tierFlashLite
	case strings.Contains(id, "flash"):
		m.Tier = tierFlash
	case strings.Contains(id, "pro"), strings.Contains(id, "ultra"):
		m.Tier = tierPro
	}

	if strings.Contains(id, "preview") || strings.Contains(id, "exp") {
		m.Preview = true
	}

	for _, token := range strings.Split(id, "-") {
		switch token {
		case "high":
			m.Effort = 3
		case "medium":
			m.Effort = 2
		case "low":
			m.Effort = 1
		}
		if len(m.Version) == 0 && versionTokenRegex.MatchString(token) {
			m.Version = parseVersionToken(token)
		}
	}

	return m
}

func parseVersionToken(token string) []int {
	parts := strings.Split(token, ".")
	version := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		version = append(version, n)
	}
	return version
}

// compareVersions возвращает 1, если a новее b, -1 если старее, 0 при равенстве.
func compareVersions(a, b []int) int {
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	for i := 0; i < maxLen; i++ {
		var ai, bi int
		if i < len(a) {
			ai = a[i]
		}
		if i < len(b) {
			bi = b[i]
		}
		if ai != bi {
			if ai > bi {
				return 1
			}
			return -1
		}
	}
	return 0
}

// isBetterModel сообщает, предпочтительнее ли a по сравнению с b внутри одного уровня:
// свежее версия → стабильная вместо preview → выше effort → короче имя.
func isBetterModel(a, b geminiModel) bool {
	if c := compareVersions(a.Version, b.Version); c != 0 {
		return c > 0
	}
	if a.Preview != b.Preview {
		return !a.Preview
	}
	if a.Effort != b.Effort {
		return a.Effort > b.Effort
	}
	if len(a.ID) != len(b.ID) {
		return len(a.ID) < len(b.ID)
	}
	return a.ID < b.ID
}

func isChatModel(id string, methods []string) bool {
	lower := strings.ToLower(id)
	if !strings.HasPrefix(lower, "gemini") {
		return false
	}
	for _, marker := range nonChatModelMarkers {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	if len(methods) == 0 {
		return true
	}
	for _, method := range methods {
		if strings.EqualFold(method, "generateContent") {
			return true
		}
	}
	return false
}

// bestInTier выбирает лучшую модель указанного уровня.
func bestInTier(available []geminiModel, tier int) (geminiModel, bool) {
	var best geminiModel
	found := false
	for _, m := range available {
		if m.Tier != tier {
			continue
		}
		if !found || isBetterModel(m, best) {
			best = m
			found = true
		}
	}
	return best, found
}

// pickDefaultGeminiModel возвращает модель по умолчанию — младшую из старших:
// семейство flash в максимально доступной версии, и только при его отсутствии
// спускаемся к flash-lite или поднимаемся к pro.
func pickDefaultGeminiModel(available []geminiModel) (geminiModel, bool) {
	for _, tier := range defaultTierPreference {
		if best, ok := bestInTier(available, tier); ok {
			return best, true
		}
	}
	var best geminiModel
	found := false
	for _, m := range available {
		if !found || isBetterModel(m, best) {
			best = m
			found = true
		}
	}
	return best, found
}

// matchGeminiModel подбирает ближайшую доступную модель к запрошенной:
// точное совпадение → лучшая модель того же уровня → пусто.
func matchGeminiModel(requested string, available []geminiModel) (geminiModel, bool) {
	want := parseGeminiModelName(requested)
	if want.ID == "" {
		return geminiModel{}, false
	}

	for _, m := range available {
		if strings.EqualFold(m.ID, want.ID) {
			return m, true
		}
	}

	if want.Tier == tierUnknown {
		return geminiModel{}, false
	}

	// Точное попадание по версии внутри уровня (например, запросили -high, а есть базовая).
	var exact geminiModel
	exactFound := false
	for _, m := range available {
		if m.Tier != want.Tier || len(want.Version) == 0 {
			continue
		}
		if compareVersions(m.Version, want.Version) != 0 {
			continue
		}
		if !exactFound || isBetterModel(m, exact) {
			exact = m
			exactFound = true
		}
	}
	if exactFound {
		return exact, true
	}

	// Иначе — максимальная доступная модель того же уровня.
	return bestInTier(available, want.Tier)
}

// configuredGeminiModel возвращает имя модели из конфигурации в порядке приоритета:
// GEMINI_API_MODEL → модель задачи/чата → модель бота по умолчанию.
func configuredGeminiModel(requested string) string {
	if envModel := strings.TrimSpace(os.Getenv("GEMINI_API_MODEL")); envModel != "" {
		return envModel
	}

	requested = strings.TrimSpace(requested)
	if requested != "" && !strings.EqualFold(requested, "default") {
		if models.GlobalModelRegistry != nil {
			if resolved, ok := models.GlobalModelRegistry.ResolveModel(requested); ok {
				return resolved
			}
		}
		return requested
	}

	if model := strings.TrimSpace(config.DefaultModel); model != "" {
		return model
	}

	return ""
}

// resolveGeminiModel сопоставляет запрошенное имя модели со списком доступных
// в Gemini API. Если из конфига ничего взять нельзя (или запрошенной модели нет),
// выбирается модель по умолчанию из реально доступных.
func resolveGeminiModel(requested string, available []geminiModel) string {
	configured := configuredGeminiModel(requested)

	if len(available) == 0 {
		// Список моделей недоступен: используем конфиг как есть, только если это
		// похоже на настоящее имя модели Gemini API, иначе — скользящий алиас.
		normalized := strings.TrimPrefix(strings.ToLower(configured), "models/")
		if strings.HasPrefix(normalized, "gemini-") && parseGeminiModelName(normalized).Tier != tierUnknown {
			return normalized
		}
		return fallbackGeminiModel
	}

	if configured != "" {
		if matched, ok := matchGeminiModel(configured, available); ok {
			return matched.ID
		}
	}

	if def, ok := pickDefaultGeminiModel(available); ok {
		return def.ID
	}

	return fallbackGeminiModel
}

// geminiModelCache кэширует список доступных моделей, чтобы не дёргать ListModels
// на каждую задачу.
type geminiModelCache struct {
	mu       sync.Mutex
	models   []geminiModel
	fetched  time.Time
	cacheTTL time.Duration
}

var modelCache = &geminiModelCache{cacheTTL: 30 * time.Minute}

func (c *geminiModelCache) get() []geminiModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.models) > 0 && time.Since(c.fetched) < c.cacheTTL {
		return c.models
	}
	return nil
}

func (c *geminiModelCache) set(items []geminiModel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = items
	c.fetched = time.Now()
}

func (c *geminiModelCache) cached() []geminiModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.models
}

// listGeminiModels запрашивает у API реально доступные модели с поддержкой
// generateContent. Результат кэшируется на время cacheTTL.
func listGeminiModels(ctx context.Context, client *genai.Client, force bool) ([]geminiModel, error) {
	if !force {
		if cached := modelCache.get(); cached != nil {
			return cached, nil
		}
	}

	var items []geminiModel
	it := client.ListModels(ctx)
	for {
		info, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			if cached := modelCache.cached(); len(cached) > 0 {
				return cached, nil
			}
			return nil, fmt.Errorf("agy-api: cannot fetch the Gemini model list: %w", err)
		}

		id := strings.TrimPrefix(info.Name, "models/")
		if !isChatModel(id, info.SupportedGenerationMethods) {
			continue
		}

		parsed := parseGeminiModelName(id)
		parsed.ID = id
		parsed.InputTokenLimit = int(info.InputTokenLimit)
		parsed.OutputTokenLimit = int(info.OutputTokenLimit)
		parsed.DisplayName = strings.TrimSpace(info.DisplayName)
		if parsed.DisplayName == "" {
			parsed.DisplayName = id
		}
		items = append(items, parsed)
	}

	if len(items) == 0 {
		if cached := modelCache.cached(); len(cached) > 0 {
			return cached, nil
		}
		return nil, errors.New("agy-api: the Gemini model list came back empty")
	}

	modelCache.set(items)
	return items, nil
}

// availableGeminiModels создаёт клиент и возвращает список доступных моделей.
func availableGeminiModels(ctx context.Context, apiKey string, force bool) ([]geminiModel, error) {
	if !force {
		if cached := modelCache.get(); cached != nil {
			return cached, nil
		}
	}

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, err
	}
	defer client.Close()

	return listGeminiModels(ctx, client, force)
}

// resolveGeminiModelWithClient — полный цикл выбора модели: конфиг + живой список.
func resolveGeminiModelWithClient(ctx context.Context, client *genai.Client, requested string) string {
	available, err := listGeminiModels(ctx, client, false)
	if err != nil {
		log.Printf("agy-api: cannot fetch the model list (%v), falling back to the configured model", err)
	}

	resolved := resolveGeminiModel(requested, available)
	if !strings.EqualFold(resolved, strings.TrimSpace(requested)) {
		log.Printf("agy-api: model %q resolved to %q", requested, resolved)
	}
	return resolved
}

// sortGeminiModels упорядочивает модели: сначала старшие уровни, внутри — свежие версии.
func sortGeminiModels(items []geminiModel) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Tier != items[j].Tier {
			return items[i].Tier > items[j].Tier
		}
		return isBetterModel(items[i], items[j])
	})
}

// isModelUnavailableError распознаёт ответ API о том, что модель не существует
// или не поддерживает generateContent (например, её отключили).
func isModelUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "is not found for api version"):
		return true
	case strings.Contains(msg, "is not supported for generatecontent"):
		return true
	case strings.Contains(msg, "404") && strings.Contains(msg, "model"):
		return true
	default:
		return false
	}
}

// fallbackModelAfterFailure принудительно обновляет список моделей и подбирает
// замену модели, на которой запрос не прошёл.
func fallbackModelAfterFailure(ctx context.Context, client *genai.Client, failed string) (string, bool) {
	available, err := listGeminiModels(ctx, client, true)
	if err != nil {
		log.Printf("agy-api: cannot refresh the model list: %v", err)
		return "", false
	}

	filtered := make([]geminiModel, 0, len(available))
	for _, m := range available {
		if !strings.EqualFold(m.ID, failed) {
			filtered = append(filtered, m)
		}
	}

	def, ok := pickDefaultGeminiModel(filtered)
	if !ok || strings.EqualFold(def.ID, failed) {
		return "", false
	}
	return def.ID, true
}
