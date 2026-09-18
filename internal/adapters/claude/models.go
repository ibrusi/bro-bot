package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Уровни моделей Claude: чем больше значение, тем «старше» модель.
const (
	claudeTierUnknown = 0
	claudeTierHaiku   = 1 // самые лёгкие и дешёвые
	claudeTierSonnet  = 2 // младшая из старших: рабочая лошадка
	claudeTierOpus    = 3 // флагманы
	claudeTierFable   = 4 // самый дорогой верхний уровень (fable / mythos)
)

// defaultClaudeTierPreference задаёт порядок выбора модели по умолчанию:
// берём «младшую из старших» (sonnet), затем лёгкую haiku, и только если их нет —
// поднимаемся к opus и совсем верхнему уровню. Та же логика, что у агента agy.
var defaultClaudeTierPreference = []int{claudeTierSonnet, claudeTierHaiku, claudeTierOpus, claudeTierFable}

// fallbackClaudeModel используется только как аварийный путь, когда список моделей
// получить не удалось (например, /v1/models недоступен). Во всех остальных случаях
// имя модели берётся из живого ответа API.
const fallbackClaudeModel = "claude-sonnet-5"

// Ограничения запроса списка моделей.
const (
	claudeModelsPageLimit = 1000 // максимум, который принимает API (по умолчанию было бы 20)
	claudeModelsMaxPages  = 5    // страховка от бесконечной пагинации
)

// claudeVersionTokenRegex — часть версии в идентификаторе модели.
// Даты снапшотов (20241022) под него не подходят: у них слишком много цифр.
var claudeVersionTokenRegex = regexp.MustCompile(`^\d{1,4}$`)

// claudeModel — разобранный идентификатор модели Claude API.
type claudeModel struct {
	ID          string
	DisplayName string
	Tier        int
	Version     []int
	CreatedAt   time.Time
	Dated       bool // в идентификаторе есть суффикс-дата снапшота

	// Лимиты модели из /v1/models: окно контекста и предельный размер ответа.
	MaxInputTokens int
	MaxTokens      int
}

// parseClaudeModelName разбирает идентификатор модели в структуру с семейством и версией.
//
// Понимает оба поколения имён: новое, где версия идёт после семейства
// (claude-opus-5, claude-sonnet-4-6), и старое, где версия стоит перед ним
// (claude-3-5-sonnet-20241022). Суффикс-дата снапшота в версию не попадает.
func parseClaudeModelName(raw string) claudeModel {
	id := strings.ToLower(strings.TrimSpace(raw))
	m := claudeModel{ID: id}

	switch {
	case strings.Contains(id, "haiku"):
		m.Tier = claudeTierHaiku
	case strings.Contains(id, "sonnet"):
		m.Tier = claudeTierSonnet
	case strings.Contains(id, "opus"):
		m.Tier = claudeTierOpus
	case strings.Contains(id, "fable"), strings.Contains(id, "mythos"):
		m.Tier = claudeTierFable
	}

	for _, token := range strings.Split(id, "-") {
		if isClaudeDateToken(token) {
			m.Dated = true
			continue
		}
		if claudeVersionTokenRegex.MatchString(token) {
			n, err := strconv.Atoi(token)
			if err != nil {
				continue
			}
			m.Version = append(m.Version, n)
		}
	}

	return m
}

// isClaudeDateToken отличает дату снапшота (20241022) от номера версии.
func isClaudeDateToken(token string) bool {
	if len(token) < 5 {
		return false
	}
	for _, r := range token {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compareClaudeVersions возвращает 1, если a новее b, -1 если старее, 0 при равенстве.
func compareClaudeVersions(a, b []int) int {
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

// isBetterClaudeModel сообщает, предпочтительнее ли a по сравнению с b внутри одного уровня:
// свежее версия → новее дата выпуска → без суффикса-даты в имени → короче имя.
func isBetterClaudeModel(a, b claudeModel) bool {
	if c := compareClaudeVersions(a.Version, b.Version); c != 0 {
		return c > 0
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	if a.Dated != b.Dated {
		return !a.Dated
	}
	if len(a.ID) != len(b.ID) {
		return len(a.ID) < len(b.ID)
	}
	return a.ID < b.ID
}

// bestClaudeInTier выбирает лучшую модель указанного уровня.
func bestClaudeInTier(available []claudeModel, tier int) (claudeModel, bool) {
	var best claudeModel
	found := false
	for _, m := range available {
		if m.Tier != tier {
			continue
		}
		if !found || isBetterClaudeModel(m, best) {
			best = m
			found = true
		}
	}
	return best, found
}

// pickDefaultClaudeModel возвращает модель по умолчанию — младшую из старших:
// семейство sonnet в максимально доступной версии, и только при его отсутствии
// спускаемся к haiku или поднимаемся к opus и выше.
func pickDefaultClaudeModel(available []claudeModel) (claudeModel, bool) {
	for _, tier := range defaultClaudeTierPreference {
		if best, ok := bestClaudeInTier(available, tier); ok {
			return best, true
		}
	}

	var best claudeModel
	found := false
	for _, m := range available {
		if !found || isBetterClaudeModel(m, best) {
			best = m
			found = true
		}
	}
	return best, found
}

// matchClaudeModel подбирает ближайшую доступную модель к запрошенной:
// точное совпадение → лучшая модель того же уровня → пусто.
//
// Именно второй шаг чинит сохранённые в базе снятые с обслуживания идентификаторы
// вроде claude-3-opus-20240229: запрос уходит на живую модель того же семейства.
func matchClaudeModel(requested string, available []claudeModel) (claudeModel, bool) {
	want := parseClaudeModelName(requested)
	if want.ID == "" {
		return claudeModel{}, false
	}

	for _, m := range available {
		if strings.EqualFold(m.ID, want.ID) {
			return m, true
		}
	}

	if want.Tier == claudeTierUnknown {
		return claudeModel{}, false
	}
	return bestClaudeInTier(available, want.Tier)
}

// configuredClaudeModel возвращает имя модели из конфигурации в порядке приоритета:
// CLAUDE_API_MODEL → модель задачи или чата.
func configuredClaudeModel(requested string) string {
	if envModel := strings.TrimSpace(os.Getenv("CLAUDE_API_MODEL")); envModel != "" {
		return envModel
	}

	requested = strings.TrimSpace(requested)
	if requested == "" || strings.EqualFold(requested, "default") {
		return ""
	}
	return requested
}

// resolveClaudeAPIModel сопоставляет запрошенное имя модели со списком доступных.
// Если из конфига взять нечего (или запрошенной модели нет), выбирается модель
// по умолчанию из реально доступных.
func resolveClaudeAPIModel(requested string, available []claudeModel) string {
	configured := configuredClaudeModel(requested)

	if len(available) == 0 {
		// Список моделей недоступен: используем конфиг как есть, если это похоже
		// на настоящий идентификатор Claude, иначе — аварийное значение.
		normalized := strings.ToLower(strings.TrimSpace(configured))
		if strings.HasPrefix(normalized, "claude-") && parseClaudeModelName(normalized).Tier != claudeTierUnknown {
			return normalized
		}
		return fallbackClaudeModel
	}

	if configured != "" {
		if matched, ok := matchClaudeModel(configured, available); ok {
			return matched.ID
		}
	}

	if def, ok := pickDefaultClaudeModel(available); ok {
		return def.ID
	}
	return fallbackClaudeModel
}

// sortClaudeModels упорядочивает модели: сначала старшие уровни, внутри — свежие версии.
func sortClaudeModels(items []claudeModel) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Tier != items[j].Tier {
			return items[i].Tier > items[j].Tier
		}
		return isBetterClaudeModel(items[i], items[j])
	})
}

// claudeModelsResponse — ответ GET /v1/models.
type claudeModelsResponse struct {
	Data []struct {
		ID             string `json:"id"`
		DisplayName    string `json:"display_name"`
		CreatedAt      string `json:"created_at"`
		MaxInputTokens int    `json:"max_input_tokens"`
		MaxTokens      int    `json:"max_tokens"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// claudeModelCache кэширует список доступных моделей, чтобы не дёргать API на каждый запрос.
type claudeModelCache struct {
	mu       sync.Mutex
	models   []claudeModel
	fetched  time.Time
	cacheTTL time.Duration
}

var modelCache = &claudeModelCache{cacheTTL: 30 * time.Minute}

func (c *claudeModelCache) get() []claudeModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.models) > 0 && time.Since(c.fetched) < c.cacheTTL {
		return c.models
	}
	return nil
}

func (c *claudeModelCache) set(items []claudeModel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = items
	c.fetched = time.Now()
}

func (c *claudeModelCache) cached() []claudeModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.models
}

// listClaudeModels запрашивает у API реально доступные модели. Результат кэшируется.
func listClaudeModels(ctx context.Context, httpClient *http.Client, baseURL, apiKey string, force bool) ([]claudeModel, error) {
	if !force {
		if cached := modelCache.get(); cached != nil {
			return cached, nil
		}
	}

	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	var items []claudeModel
	afterID := ""

	for page := 0; page < claudeModelsMaxPages; page++ {
		url := fmt.Sprintf("%s/models?limit=%d", strings.TrimRight(baseURL, "/"), claudeModelsPageLimit)
		if afterID != "" {
			url += "&after_id=" + afterID
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return claudeModelsFallback(err)
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := httpClient.Do(req)
		if err != nil {
			return claudeModelsFallback(err)
		}

		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return claudeModelsFallback(readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return claudeModelsFallback(fmt.Errorf("список моделей Claude недоступен: HTTP %d: %s",
				resp.StatusCode, strings.TrimSpace(string(body))))
		}

		var parsed claudeModelsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return claudeModelsFallback(fmt.Errorf("не удалось разобрать список моделей Claude: %w", err))
		}

		for _, item := range parsed.Data {
			if strings.TrimSpace(item.ID) == "" {
				continue
			}
			model := parseClaudeModelName(item.ID)
			model.ID = item.ID
			model.DisplayName = strings.TrimSpace(item.DisplayName)
			if model.DisplayName == "" {
				model.DisplayName = item.ID
			}
			if ts, err := time.Parse(time.RFC3339, item.CreatedAt); err == nil {
				model.CreatedAt = ts
			}
			model.MaxInputTokens = item.MaxInputTokens
			model.MaxTokens = item.MaxTokens
			items = append(items, model)
		}

		if !parsed.HasMore || parsed.LastID == "" {
			break
		}
		afterID = parsed.LastID
	}

	if len(items) == 0 {
		return claudeModelsFallback(fmt.Errorf("Claude API вернул пустой список моделей"))
	}

	modelCache.set(items)
	return items, nil
}

// claudeModelsFallback отдаёт последний известный список, если свежий получить не удалось.
func claudeModelsFallback(err error) ([]claudeModel, error) {
	if cached := modelCache.cached(); len(cached) > 0 {
		return cached, nil
	}
	return nil, err
}

// isClaudeModelUnavailableError распознаёт ответ API о том, что модель не существует.
func isClaudeModelUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not_found_error"):
		return true
	case strings.Contains(msg, "404") && strings.Contains(msg, "model"):
		return true
	default:
		return false
	}
}

// formatClaudeModelsList приводит список моделей к формату «id Отображаемое имя»,
// который понимает парсер реестра моделей.
func formatClaudeModelsList(available []claudeModel) string {
	items := make([]claudeModel, len(available))
	copy(items, available)
	sortClaudeModels(items)

	var bldr strings.Builder
	for _, m := range items {
		displayName := m.DisplayName
		if displayName == "" {
			displayName = m.ID
		}
		bldr.WriteString(fmt.Sprintf("%s %s\n", m.ID, displayName))
	}
	return bldr.String()
}

// resolveClaudeModelForAPI — полный цикл выбора модели: конфиг плюс живой список API.
func resolveClaudeModelForAPI(ctx context.Context, httpClient *http.Client, baseURL, apiKey, requested string) string {
	available, err := listClaudeModels(ctx, httpClient, baseURL, apiKey, false)
	if err != nil {
		log.Printf("claude-api: не удалось получить список моделей (%v), выбираем модель по конфигу", err)
	}

	resolved := resolveClaudeAPIModel(requested, available)
	if !strings.EqualFold(resolved, strings.TrimSpace(requested)) {
		log.Printf("claude-api: модель %q сопоставлена с %q", requested, resolved)
	}
	return resolved
}

// fallbackClaudeModelAfterFailure принудительно обновляет список моделей и подбирает
// замену модели, на которой запрос не прошёл.
func fallbackClaudeModelAfterFailure(ctx context.Context, httpClient *http.Client, baseURL, apiKey, failed string) (string, bool) {
	available, err := listClaudeModels(ctx, httpClient, baseURL, apiKey, true)
	if err != nil {
		log.Printf("claude-api: не удалось обновить список моделей: %v", err)
		return "", false
	}

	filtered := make([]claudeModel, 0, len(available))
	for _, m := range available {
		if !strings.EqualFold(m.ID, failed) {
			filtered = append(filtered, m)
		}
	}

	def, ok := pickDefaultClaudeModel(filtered)
	if !ok || strings.EqualFold(def.ID, failed) {
		return "", false
	}
	return def.ID, true
}

// claudeRateLimitBucket — одно ограничение API: предел, остаток и время восполнения.
type claudeRateLimitBucket struct {
	Name      string
	Limit     int64
	Remaining int64
	Reset     string // RFC 3339, как его отдаёт API
}

// Fraction возвращает долю оставшегося ресурса (0..1) или nil, если предел неизвестен.
func (b claudeRateLimitBucket) Fraction() *float64 {
	if b.Limit <= 0 {
		return nil
	}
	frac := float64(b.Remaining) / float64(b.Limit)
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return &frac
}

// claudeRateLimits — снимок лимитов, снятый с заголовков последнего ответа API.
type claudeRateLimits struct {
	Buckets    []claudeRateLimitBucket
	CapturedAt time.Time
}

// rateLimitsStore хранит последний снимок лимитов.
// Лимиты приходят заголовками в каждом ответе Messages API, поэтому отдельные
// запросы ради /usage не нужны — снимок обновляется бесплатно по ходу работы.
var rateLimitsStore struct {
	mu   sync.Mutex
	last claudeRateLimits
}

// claudeRateLimitHeaders перечисляет группы заголовков лимитов в порядке показа.
var claudeRateLimitHeaders = []struct {
	Name   string
	Prefix string
}{
	{"Запросы", "anthropic-ratelimit-requests"},
	{"Входные токены", "anthropic-ratelimit-input-tokens"},
	{"Выходные токены", "anthropic-ratelimit-output-tokens"},
	{"Токены суммарно", "anthropic-ratelimit-tokens"},
}

// captureClaudeRateLimits снимает лимиты с заголовков ответа API.
// Заголовки приходят и на успешных ответах, и на ошибках (в том числе на 429).
func captureClaudeRateLimits(header http.Header) {
	if header == nil {
		return
	}

	snapshot := claudeRateLimits{CapturedAt: time.Now()}
	for _, group := range claudeRateLimitHeaders {
		limit, hasLimit := parseHeaderInt(header, group.Prefix+"-limit")
		remaining, hasRemaining := parseHeaderInt(header, group.Prefix+"-remaining")
		reset := strings.TrimSpace(header.Get(group.Prefix + "-reset"))

		if !hasLimit && !hasRemaining && reset == "" {
			continue
		}
		snapshot.Buckets = append(snapshot.Buckets, claudeRateLimitBucket{
			Name:      group.Name,
			Limit:     limit,
			Remaining: remaining,
			Reset:     reset,
		})
	}

	if len(snapshot.Buckets) == 0 {
		return
	}

	rateLimitsStore.mu.Lock()
	defer rateLimitsStore.mu.Unlock()
	rateLimitsStore.last = snapshot
}

// lastClaudeRateLimits возвращает последний снимок лимитов.
func lastClaudeRateLimits() claudeRateLimits {
	rateLimitsStore.mu.Lock()
	defer rateLimitsStore.mu.Unlock()

	snapshot := rateLimitsStore.last
	snapshot.Buckets = append([]claudeRateLimitBucket(nil), snapshot.Buckets...)
	return snapshot
}

func parseHeaderInt(header http.Header, name string) (int64, bool) {
	raw := strings.TrimSpace(header.Get(name))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
