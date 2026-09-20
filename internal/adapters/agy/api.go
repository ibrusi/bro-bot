package agy

import (
	"bro-bot/internal/agents"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/tools"
	"bro-bot/internal/utils"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// errMissingAPIKey — в окружении нет ключа для работы через Gemini API.
// Тип общий с реестром агентов: обработчик переводит его в одном месте и не знает
// про конкретные адаптеры.
func errMissingAPIKey() error {
	return &agents.MissingAPIKeyError{Agent: "agy", Vars: apiKeyEnv}
}

// AgyAPIAdapter реализует работу с Gemini API напрямую
type AgyAPIAdapter struct {
}

func NewAgyAPIAdapter() *AgyAPIAdapter {
	return &AgyAPIAdapter{}
}

func (a *AgyAPIAdapter) AgentName() string {
	return "agy-api"
}

func (a *AgyAPIAdapter) ExecutionMode() string {
	return "api"
}

func (a *AgyAPIAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	apiKey := agents.FirstEnv(apiKeyEnv...)
	if apiKey == "" {
		return nil, errMissingAPIKey()
	}

	sessionID := args.ConversationID
	if sessionID == "" {
		sessionID = fmt.Sprintf("agy-api-%d", time.Now().UnixNano())
	}

	rPipe, wPipe := io.Pipe()

	proc := &AgyAPIProcess{
		rPipe:     rPipe,
		wPipe:     wPipe,
		ctx:       ctx,
		doneChan:  make(chan struct{}),
		sessionID: sessionID,
	}

	go proc.runStreaming(ctx, apiKey, args)

	return proc, nil
}

func (a *AgyAPIAdapter) GetModels(ctx context.Context) ([]byte, error) {
	apiKey := agents.FirstEnv(apiKeyEnv...)
	if apiKey == "" {
		return nil, errMissingAPIKey()
	}

	available, err := availableGeminiModels(ctx, apiKey, false)
	if err != nil {
		return nil, err
	}

	return []byte(formatGeminiModelsList(available)), nil
}

// formatGeminiModelsList приводит список моделей к формату "id Отображаемое имя",
// который понимает парсер реестра моделей.
func formatGeminiModelsList(available []geminiModel) string {
	items := make([]geminiModel, len(available))
	copy(items, available)
	if len(items) == 0 {
		items = append(items, fallbackGeminiModels...)
	}
	sortGeminiModels(items)

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

// GetQuota отдаёт ответ без групп: Gemini API не сообщает остаток квоты по ключу
// ни в теле ответа, ни в заголовках. Рисовать проценты было бы враньём, поэтому
// структурных данных нет, и обработчик покажет текст из GetQuotaText.
func (a *AgyAPIAdapter) GetQuota(_ context.Context, lang string) ([]byte, error) {
	payload := map[string]interface{}{
		"status": "SUCCESS",
		"command": map[string]interface{}{
			"name": "quota",
			"data": map[string]interface{}{
				"description": i18n.T(lang, "quota.gemini_description"),
			},
		},
	}
	return json.Marshal(payload)
}

// GetQuotaText — человекочитаемая сводка: лимиты выбранной модели плюс указание,
// где смотреть расход и квоты.
func (a *AgyAPIAdapter) GetQuotaText(_ context.Context, lang string) ([]byte, error) {
	var bldr strings.Builder

	bldr.WriteString(i18n.T(lang, "quota.gemini_text"))

	if model, ok := currentGeminiModelLimits(); ok {
		name := model.DisplayName
		if name == "" {
			name = model.ID
		}
		bldr.WriteString(i18n.Tf(lang, "quota.model_window",
			name, utils.FormatCount(int64(model.InputTokenLimit)), utils.FormatCount(int64(model.OutputTokenLimit))))
	}

	bldr.WriteString(i18n.T(lang, "quota.gemini_footer"))
	return []byte(bldr.String()), nil
}

// currentGeminiModelLimits возвращает лимиты модели, выбранной для api-режима.
// Список берётся из кэша: отдельный запрос ради /usage не делаем.
func currentGeminiModelLimits() (geminiModel, bool) {
	available := modelCache.cached()
	if len(available) == 0 {
		return geminiModel{}, false
	}

	resolved := resolveGeminiModel("", available)
	for _, m := range available {
		if strings.EqualFold(m.ID, resolved) && (m.InputTokenLimit > 0 || m.OutputTokenLimit > 0) {
			return m, true
		}
	}
	return geminiModel{}, false
}

// GetCredits — у ключа API нет понятия «кредиты»: это механика подписки CLI,
// поэтому отдаём пустой объект, и блок кредитов в /usage не показывается.
func (a *AgyAPIAdapter) GetCredits(ctx context.Context) ([]byte, error) {
	return []byte(`{}`), nil
}

type AgyAPIProcess struct {
	rPipe     *io.PipeReader
	wPipe     *io.PipeWriter
	ctx       context.Context
	doneChan  chan struct{}
	sessionID string
	err       error
	mu        sync.Mutex
}

func (p *AgyAPIProcess) Stdout() io.Reader {
	return p.rPipe
}

type dummyWriter struct{}

func (d dummyWriter) Write(b []byte) (n int, err error) {
	return len(b), nil
}

func (d dummyWriter) Close() error {
	return nil
}

func (p *AgyAPIProcess) Stdin() io.WriteCloser {
	return dummyWriter{}
}

func (p *AgyAPIProcess) Wait() error {
	<-p.doneChan
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *AgyAPIProcess) Kill() error {
	p.rPipe.Close()
	p.wPipe.Close()
	return nil
}

func (p *AgyAPIProcess) Close() error {
	return p.Kill()
}

// PID — у API-запроса нет процесса в ОС.
func (p *AgyAPIProcess) PID() int {
	return 0
}

func (p *AgyAPIProcess) runStreaming(ctx context.Context, apiKey string, args ports.ExecuteArgs) {
	defer func() {
		_ = p.wPipe.Close()
		close(p.doneChan)
	}()

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		p.handleError(err)
		return
	}
	defer client.Close()

	initEvt := map[string]interface{}{
		"type":       "system",
		"session_id": p.sessionID,
	}
	initBytes, _ := json.Marshal(initEvt)
	if _, err := fmt.Fprintf(p.wPipe, "%s\n", string(initBytes)); err != nil {
		return
	}

	modelName := resolveGeminiModelWithClient(ctx, client, args.ModelName)

	startedAt := time.Now()
	text, usage, turns, err := p.streamOnce(ctx, client, modelName, args)
	if err != nil && isModelUnavailableError(err) && text == "" {
		// Кэш моделей мог устареть (модель отключили): обновляем список и
		// повторяем запрос на актуальной модели по умолчанию.
		if fallback, ok := fallbackModelAfterFailure(ctx, client, modelName); ok {
			log.Printf("agy-api: model %q is unavailable (%v), retrying with %q", modelName, err, fallback)
			modelName = fallback
			text, usage, turns, err = p.streamOnce(ctx, client, modelName, args)
		}
	}
	if err != nil {
		if errors.Is(err, errStreamOutputClosed) {
			return
		}
		p.handleError(err)
		return
	}

	// duration_ms и num_turns нужны трекеру токенов: без них скорость ответа
	// считается нулевой.
	resEvt := map[string]interface{}{
		"type":        "result",
		"session_id":  p.sessionID,
		"is_error":    false,
		"result":      text,
		"duration_ms": float64(time.Since(startedAt).Milliseconds()),
		"num_turns":   turns,
	}
	if usage != nil {
		resEvt["usage"] = usage
	}
	resBytes, _ := json.Marshal(resEvt)
	_, _ = fmt.Fprintf(p.wPipe, "%s\n", string(resBytes))
}

// geminiHistoryContents переводит историю диалога в формат Gemini.
// У Gemini роль ответа модели называется "model", а не "assistant".
func geminiHistoryContents(history []ports.ChatMessage) []*genai.Content {
	if len(history) == 0 {
		return nil
	}
	contents := make([]*genai.Content, 0, len(history))
	for _, msg := range history {
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		role := "user"
		if strings.EqualFold(msg.Role, "assistant") || strings.EqualFold(msg.Role, "model") {
			role = "model"
		}
		contents = append(contents, &genai.Content{
			Role:  role,
			Parts: []genai.Part{genai.Text(text)},
		})
	}
	if len(contents) == 0 {
		return nil
	}
	return contents
}

// usageFromMetadata переводит счётчики токенов Gemini в формат события result.
func usageFromMetadata(meta *genai.UsageMetadata) map[string]interface{} {
	if meta == nil {
		return nil
	}
	return map[string]interface{}{
		"input_tokens":            int64(meta.PromptTokenCount),
		"output_tokens":           int64(meta.CandidatesTokenCount),
		"cache_read_input_tokens": int64(meta.CachedContentTokenCount),
	}
}

// errStreamOutputClosed означает, что читатель закрыл канал вывода — ошибку
// показывать не нужно, достаточно тихо завершиться.
var errStreamOutputClosed = errors.New("agy-api: output stream closed")

const maxGeminiTurns = 30

// streamOnce выполняет генерацию с поддержкой вызова инструментов и возвращает накопленный текст, счётчики и число ходов.
func (p *AgyAPIProcess) streamOnce(ctx context.Context, client *genai.Client, modelName string, args ports.ExecuteArgs) (string, map[string]interface{}, int, error) {
	model := client.GenerativeModel(modelName)
	if systemPrompt := strings.TrimSpace(args.SystemPrompt); systemPrompt != "" {
		model.SystemInstruction = genai.NewUserContent(genai.Text(systemPrompt))
	}

	isReadOnly := args.ReadOnly || strings.Contains(args.SystemPrompt, "РЕЖИМ ДИАЛОГА") || strings.Contains(args.SystemPrompt, "CHAT MODE")
	model.Tools = tools.GeminiToolDeclarations(isReadOnly)

	executor := tools.NewExecutor(args.WorkDir)

	// История диалога проигрывается на нашей стороне: Gemini API не хранит сессии.
	chat := model.StartChat()
	chat.History = geminiHistoryContents(args.History)

	var textAccumulator strings.Builder
	var lastUsage map[string]interface{}
	var parts []genai.Part = []genai.Part{genai.Text(args.Prompt)}
	turns := 0

	for turn := 0; turn < maxGeminiTurns; turn++ {
		turns++
		stream := chat.SendMessageStream(ctx, parts...)

		var fnCalls []genai.FunctionCall

		for {
			resp, err := stream.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return textAccumulator.String(), lastUsage, turns, err
			}
			if resp.UsageMetadata != nil {
				lastUsage = usageFromMetadata(resp.UsageMetadata)
			}

			for _, cand := range resp.Candidates {
				if cand.Content == nil {
					continue
				}
				for _, part := range cand.Content.Parts {
					switch pVal := part.(type) {
					case genai.Text:
						text := string(pVal)
						if text == "" {
							continue
						}
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
						if _, err := fmt.Fprintf(p.wPipe, "%s\n", string(msgBytes)); err != nil {
							return textAccumulator.String(), lastUsage, turns, errStreamOutputClosed
						}

					case genai.FunctionCall:
						fnCalls = append(fnCalls, pVal)

						toolEvt := map[string]interface{}{
							"type":       "assistant",
							"session_id": p.sessionID,
							"message": map[string]interface{}{
								"role": "assistant",
								"content": []map[string]interface{}{
									{
										"type":  "tool_use",
										"name":  pVal.Name,
										"input": pVal.Args,
									},
								},
							},
						}
						toolBytes, _ := json.Marshal(toolEvt)
						if _, err := fmt.Fprintf(p.wPipe, "%s\n", string(toolBytes)); err != nil {
							return textAccumulator.String(), lastUsage, turns, errStreamOutputClosed
						}
					}
				}
			}
		}

		if len(fnCalls) == 0 {
			break
		}

		var nextParts []genai.Part
		for _, fc := range fnCalls {
			out, execErr := executor.Execute(ctx, fc.Name, fc.Args, isReadOnly)
			if execErr != nil {
				out = fmt.Sprintf("Error: %v", execErr)
			}
			nextParts = append(nextParts, genai.FunctionResponse{
				Name: fc.Name,
				Response: map[string]any{
					"result": out,
				},
			})
		}
		parts = nextParts
	}

	return textAccumulator.String(), lastUsage, turns, nil
}

func (p *AgyAPIProcess) handleError(err error) {
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
