package claude

import (
	"bro-bot/internal/agents"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// errMissingAPIKey — в окружении нет ключа для работы через Claude Messages API.
// Тип общий с реестром агентов: обработчик переводит его в одном месте и не знает
// про конкретные адаптеры.
func errMissingAPIKey() error {
	return &agents.MissingAPIKeyError{Agent: "claude", Vars: apiKeyEnv}
}

// APIAdapter абстрактный интерфейс для любого API-агента
type APIAdapter interface {
	ports.AgentFramework
	AgentName() string
}

// ClaudeAPIAdapter реализует прямую работу с Claude API (Anthropic Messages API)
type ClaudeAPIAdapter struct {
	HTTPClient *http.Client
	BaseURL    string
}

// defaultClaudeBaseURL — корень Claude API; тесты подменяют его на httptest-сервер.
const defaultClaudeBaseURL = "https://api.anthropic.com/v1"

func NewClaudeAPIAdapter() *ClaudeAPIAdapter {
	return &ClaudeAPIAdapter{
		HTTPClient: &http.Client{Timeout: 0}, // No client-level timeout for streaming
		BaseURL:    defaultClaudeBaseURL,
	}
}

// apiKeyFromEnv возвращает ключ Claude API из окружения: ANTHROPIC_API_KEY либо
// устаревший CLAUDE_API_KEY. Пустая строка — ключа нет.
func apiKeyFromEnv() string {
	return agents.FirstEnv(apiKeyEnv...)
}

func (a *ClaudeAPIAdapter) AgentName() string {
	return "claude-api"
}

// buildClaudeMessagesBody собирает тело запроса к Messages API: историю диалога
// (Claude API не хранит сессии на своей стороне) и текущий вопрос пользователя.
func buildClaudeMessagesBody(modelName string, args ports.ExecuteArgs) map[string]interface{} {
	maxTokens := claudeMaxTokensFor(modelName)

	messages := make([]map[string]interface{}, 0, len(args.History)+1)
	for _, msg := range args.History {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		role := "user"
		if strings.EqualFold(msg.Role, "assistant") || strings.EqualFold(msg.Role, "model") {
			role = "assistant"
		}
		messages = append(messages, map[string]interface{}{"role": role, "content": content})
	}
	messages = append(messages, map[string]interface{}{"role": "user", "content": args.Prompt})

	reqBody := map[string]interface{}{
		"model":      modelName,
		"max_tokens": maxTokens,
		"messages":   messages,
		"stream":     true,
	}
	if systemPrompt := strings.TrimSpace(args.SystemPrompt); systemPrompt != "" {
		reqBody["system"] = systemPrompt
	}
	return reqBody
}

// Пределы размера ответа. Жёсткое значение 8192 резало длинные ответы, поэтому берём
// предел самой модели из /v1/models, ограничивая его потолком для стриминга.
const (
	claudeDefaultMaxTokens = 8192  // когда лимит модели неизвестен
	claudeStreamMaxTokens  = 64000 // разумный потолок для потокового ответа
)

// claudeMaxTokensFor возвращает размер ответа для модели по её лимиту из списка API.
func claudeMaxTokensFor(modelName string) int {
	for _, m := range modelCache.cached() {
		if !strings.EqualFold(m.ID, modelName) || m.MaxTokens <= 0 {
			continue
		}
		if m.MaxTokens < claudeStreamMaxTokens {
			return m.MaxTokens
		}
		return claudeStreamMaxTokens
	}
	return claudeDefaultMaxTokens
}

// ExecuteTask запускает генерацию сообщений через Claude Messages API со стримингом в формате stream-json NDJSON
func (a *ClaudeAPIAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	apiKey := apiKeyFromEnv()

	if apiKey == "" {
		return nil, errMissingAPIKey()
	}

	// Имя модели подбирается по живому списку /v1/models: захардкоженные идентификаторы
	// устаревают вместе с моделями и дают 404 not_found_error.
	modelName := resolveClaudeModelForAPI(ctx, a.HTTPClient, a.BaseURL, apiKey, args.ModelName)

	sessionID := args.ConversationID
	if sessionID == "" {
		sessionID = fmt.Sprintf("claude-api-%d", time.Now().UnixNano())
	}

	rPipe, wPipe := io.Pipe()

	proc := &ClaudeAPIProcess{
		rPipe:     rPipe,
		wPipe:     wPipe,
		ctx:       ctx,
		doneChan:  make(chan struct{}),
		sessionID: sessionID,
	}

	stream := claudeStreamRequest{
		client:  a.HTTPClient,
		baseURL: a.BaseURL,
		apiKey:  apiKey,
		args:    args,
		model:   modelName,
	}

	go proc.runStreaming(ctx, stream)

	return proc, nil
}

// openClaudeStream открывает поток ответа Messages API.
//
// Если модель за это время отключили (404 not_found_error), список моделей обновляется
// принудительно и запрос повторяется один раз на актуальной модели по умолчанию.
func openClaudeStream(ctx context.Context, stream claudeStreamRequest) (*http.Response, error) {
	resp, err := doClaudeRequest(ctx, stream, stream.model)
	if err == nil {
		return resp, nil
	}
	if !isClaudeModelUnavailableError(err) {
		return nil, err
	}

	fallback, ok := fallbackClaudeModelAfterFailure(ctx, stream.client, stream.baseURL, stream.apiKey, stream.model)
	if !ok {
		return nil, err
	}

	log.Printf("claude-api: model %q is unavailable (%v), retrying with %q", stream.model, err, fallback)
	return doClaudeRequest(ctx, stream, fallback)
}

// doClaudeRequest выполняет один запрос к Messages API и проверяет статус ответа.
func doClaudeRequest(ctx context.Context, stream claudeStreamRequest, model string) (*http.Response, error) {
	req, err := stream.build(ctx, model)
	if err != nil {
		return nil, err
	}

	client := stream.client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	// Лимиты приходят заголовками и на успехе, и на ошибке (в том числе на 429):
	// снимаем их бесплатно, без отдельных запросов ради /usage.
	captureClaudeRateLimits(resp.Header)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return resp, nil
}

// claudeStreamRequest — всё необходимое, чтобы собрать (и при нужде пересобрать) запрос к API.
type claudeStreamRequest struct {
	client  *http.Client
	baseURL string
	apiKey  string
	args    ports.ExecuteArgs
	model   string
}

// build собирает HTTP-запрос к Messages API для указанной модели.
func (r claudeStreamRequest) build(ctx context.Context, model string) (*http.Request, error) {
	bodyBytes, err := json.Marshal(buildClaudeMessagesBody(model, r.args))
	if err != nil {
		return nil, fmt.Errorf("claude-api: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.baseURL, "/")+"/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("claude-api: build HTTP request: %w", err)
	}

	req.Header.Set("x-api-key", r.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	return req, nil
}

func (a *ClaudeAPIAdapter) GetModels(ctx context.Context) ([]byte, error) {
	apiKey := apiKeyFromEnv()
	if apiKey == "" {
		return nil, errMissingAPIKey()
	}

	available, err := listClaudeModels(ctx, a.HTTPClient, a.BaseURL, apiKey, false)
	if err != nil {
		return nil, err
	}

	return []byte(formatClaudeModelsList(available)), nil
}

// GetQuota отдаёт лимиты ключа API в том же формате, который понимает рендер /usage:
// группы и корзины с долей остатка и временем восполнения.
//
// Данные берутся из снимка заголовков последнего ответа Messages API. Пока запросов не было,
// групп в ответе нет — тогда обработчик покажет текст из GetQuotaText, а не пустые шкалы.
func (a *ClaudeAPIAdapter) GetQuota(_ context.Context, lang string) ([]byte, error) {
	snapshot := lastClaudeRateLimits()

	payload := map[string]interface{}{
		"status": "SUCCESS",
		"command": map[string]interface{}{
			"name": "quota",
			"data": map[string]interface{}{
				"description": claudeQuotaDescription(snapshot, lang),
			},
		},
	}

	if len(snapshot.Buckets) > 0 {
		buckets := make([]map[string]interface{}, 0, len(snapshot.Buckets))
		for _, bucket := range snapshot.Buckets {
			item := map[string]interface{}{
				"name":       i18n.T(lang, bucket.NameKey),
				"reset_time": bucket.Reset,
			}
			if frac := bucket.Fraction(); frac != nil {
				item["remaining_fraction"] = *frac
				item["description"] = i18n.Tf(lang, "quota.claude_remaining",
					utils.FormatCount(bucket.Remaining), utils.FormatCount(bucket.Limit))
			}
			buckets = append(buckets, item)
		}

		data := payload["command"].(map[string]interface{})["data"].(map[string]interface{})
		data["groups"] = []map[string]interface{}{{
			"name":        i18n.T(lang, "quota.claude_api_title"),
			"description": i18n.T(lang, "quota.claude_api_description"),
			"buckets":     buckets,
		}}
	}

	return json.Marshal(payload)
}

// claudeQuotaDescription поясняет, насколько свежий снимок лимитов.
func claudeQuotaDescription(snapshot claudeRateLimits, lang string) string {
	if len(snapshot.Buckets) == 0 {
		return i18n.T(lang, "quota.claude_empty")
	}
	return i18n.Tf(lang, "quota.claude_snapshot", snapshot.CapturedAt.Format("15:04:05"))
}

// GetQuotaText — человекочитаемая сводка для случаев, когда структурных данных нет.
func (a *ClaudeAPIAdapter) GetQuotaText(_ context.Context, lang string) ([]byte, error) {
	var bldr strings.Builder

	snapshot := lastClaudeRateLimits()
	if len(snapshot.Buckets) == 0 {
		bldr.WriteString(i18n.T(lang, "quota.claude_text_empty"))
	} else {
		bldr.WriteString(i18n.Tf(lang, "quota.claude_text_header", snapshot.CapturedAt.Format("15:04:05")))
		for _, bucket := range snapshot.Buckets {
			if frac := bucket.Fraction(); frac != nil {
				bldr.WriteString(i18n.Tf(lang, "quota.claude_text_bucket",
					i18n.T(lang, bucket.NameKey), utils.FormatCount(bucket.Remaining), utils.FormatCount(bucket.Limit), *frac*100))
				continue
			}
			bldr.WriteString(i18n.Tf(lang, "quota.claude_text_bucket_unknown", i18n.T(lang, bucket.NameKey)))
		}
	}

	if model, ok := currentClaudeModelLimits(); ok {
		bldr.WriteString(i18n.Tf(lang, "quota.model_window",
			model.ID, utils.FormatCount(int64(model.MaxInputTokens)), utils.FormatCount(int64(model.MaxTokens))))
	}

	bldr.WriteString(i18n.T(lang, "quota.claude_footer"))
	return []byte(bldr.String()), nil
}

// currentClaudeModelLimits возвращает лимиты модели, выбранной для api-режима.
func currentClaudeModelLimits() (claudeModel, bool) {
	available := modelCache.cached()
	if len(available) == 0 {
		return claudeModel{}, false
	}

	resolved := resolveClaudeAPIModel("", available)
	for _, m := range available {
		if strings.EqualFold(m.ID, resolved) && (m.MaxInputTokens > 0 || m.MaxTokens > 0) {
			return m, true
		}
	}
	return claudeModel{}, false
}

// GetCredits — у ключа API нет понятия «кредиты»: это механика подписки CLI,
// поэтому отдаём пустой объект, и блок кредитов в /usage не показывается.
func (a *ClaudeAPIAdapter) GetCredits(ctx context.Context) ([]byte, error) {
	return []byte(`{}`), nil
}

type ClaudeAPIProcess struct {
	rPipe     *io.PipeReader
	wPipe     *io.PipeWriter
	ctx       context.Context
	doneChan  chan struct{}
	sessionID string
	err       error
	mu        sync.Mutex
}

func (p *ClaudeAPIProcess) Stdout() io.Reader {
	return p.rPipe
}

func (p *ClaudeAPIProcess) Stdin() io.WriteCloser {
	return p.wPipe
}

func (p *ClaudeAPIProcess) Wait() error {
	<-p.doneChan
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *ClaudeAPIProcess) Kill() error {
	p.rPipe.Close()
	p.wPipe.Close()
	return nil
}

func (p *ClaudeAPIProcess) Close() error {
	return p.Kill()
}

// PID — у API-запроса нет процесса в ОС.
func (p *ClaudeAPIProcess) PID() int {
	return 0
}

// handleError сохраняет ошибку и отдаёт её в поток событий как результат с признаком ошибки.
func (p *ClaudeAPIProcess) handleError(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()

	errEvt := map[string]interface{}{
		"type":       "result",
		"session_id": p.sessionID,
		"is_error":   true,
		"errors":     []string{err.Error()},
	}
	errBytes, _ := json.Marshal(errEvt)
	_, _ = fmt.Fprintf(p.wPipe, "%s\n", string(errBytes))
}

func (p *ClaudeAPIProcess) runStreaming(ctx context.Context, stream claudeStreamRequest) {
	startedAt := time.Now()

	defer func() {
		_ = p.wPipe.Close()
		close(p.doneChan)
	}()

	// 1. Излучаем событие инициализации системы
	initEvt := map[string]interface{}{
		"type":       "system",
		"session_id": p.sessionID,
	}
	initBytes, _ := json.Marshal(initEvt)
	_, _ = fmt.Fprintf(p.wPipe, "%s\n", string(initBytes))

	resp, err := openClaudeStream(ctx, stream)
	if err != nil {
		p.handleError(err)
		return
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	scanner := io.Reader(resp.Body)
	readBuf := make([]byte, 4096)

	var textAccumulator strings.Builder
	var inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int64

	for {
		n, err := scanner.Read(readBuf)
		if n > 0 {
			buf.Write(readBuf[:n])
			for {
				line, readErr := buf.ReadString('\n')
				if readErr != nil {
					buf.WriteString(line) // Вернуть оставшийся фрагмент обратно
					break
				}

				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "data: ") {
					continue
				}

				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					break
				}

				var rawEvt map[string]interface{}
				if err := json.Unmarshal([]byte(data), &rawEvt); err != nil {
					continue
				}

				evtType, _ := rawEvt["type"].(string)

				if evtType == "content_block_delta" {
					if delta, ok := rawEvt["delta"].(map[string]interface{}); ok {
						if deltaType, _ := delta["type"].(string); deltaType == "text_delta" {
							if text, _ := delta["text"].(string); text != "" {
								textAccumulator.WriteString(text)

								msgEvt := map[string]interface{}{
									"type":       "assistant",
									"session_id": p.sessionID,
									"message": map[string]interface{}{
										"role": "assistant",
										"content": []map[string]interface{}{
											{
												"type": "text",
												"text": text,
											},
										},
									},
								}
								msgBytes, _ := json.Marshal(msgEvt)
								_, _ = fmt.Fprintf(p.wPipe, "%s\n", string(msgBytes))
							}
						}
					}
				} else if evtType == "message_start" {
					if message, ok := rawEvt["message"].(map[string]interface{}); ok {
						if usage, ok := message["usage"].(map[string]interface{}); ok {
							if inp, ok := usage["input_tokens"].(float64); ok {
								inputTokens = int64(inp)
							}
							// Токены кэша считаются отдельно от входных и нужны /tokens.
							if cached, ok := usage["cache_read_input_tokens"].(float64); ok {
								cacheReadTokens = int64(cached)
							}
							if created, ok := usage["cache_creation_input_tokens"].(float64); ok {
								cacheCreationTokens = int64(created)
							}
						}
					}
				} else if evtType == "message_delta" {
					if usage, ok := rawEvt["usage"].(map[string]interface{}); ok {
						if out, ok := usage["output_tokens"].(float64); ok {
							outputTokens = int64(out)
						}
					}
				}
			}
		}

		if err != nil {
			break
		}
	}

	// 2. Отправляем итоговый результат.
	// Длительность и число ходов нужны трекеру токенов: без них /tokens показывает нулевую скорость.
	resEvt := map[string]interface{}{
		"type":        "result",
		"session_id":  p.sessionID,
		"is_error":    false,
		"result":      textAccumulator.String(),
		"duration_ms": float64(time.Since(startedAt).Milliseconds()),
		"num_turns":   1,
		"usage": map[string]interface{}{
			"input_tokens":                inputTokens,
			"output_tokens":               outputTokens,
			"cache_read_input_tokens":     cacheReadTokens,
			"cache_creation_input_tokens": cacheCreationTokens,
		},
	}
	resBytes, _ := json.Marshal(resEvt)
	_, _ = fmt.Fprintf(p.wPipe, "%s\n", string(resBytes))
}
