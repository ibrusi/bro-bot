package agy

import (
	"bro-bot/internal/ports"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// AgyAPIAdapter реализует работу с Gemini API напрямую
type AgyAPIAdapter struct {
}

func NewAgyAPIAdapter() *AgyAPIAdapter {
	return &AgyAPIAdapter{}
}

func (a *AgyAPIAdapter) AgentName() string {
	return "agy-api"
}

func (a *AgyAPIAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("API ключ не найден. Задайте GEMINI_API_KEY в .env для работы в режиме API")
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

	go proc.runStreaming(ctx, apiKey, args.ModelName, args.Prompt)

	return proc, nil
}

func (a *AgyAPIAdapter) GetModels(ctx context.Context) ([]byte, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("API ключ не найден. Задайте GEMINI_API_KEY в .env для работы в режиме API")
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

func (a *AgyAPIAdapter) GetQuota(ctx context.Context) ([]byte, error) {
	return []byte(`{"status":"SUCCESS","response":"Gemini API usage is managed in Google AI Studio / Google Cloud Console"}`), nil
}

func (a *AgyAPIAdapter) GetQuotaText(ctx context.Context) ([]byte, error) {
	return []byte("Gemini API integration active via Google AI Studio / Google Cloud."), nil
}

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

func (p *AgyAPIProcess) GetCmd() *exec.Cmd {
	return nil
}

func (p *AgyAPIProcess) runStreaming(ctx context.Context, apiKey string, requestedModel string, prompt string) {
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

	modelName := resolveGeminiModelWithClient(ctx, client, requestedModel)

	text, err := p.streamOnce(ctx, client, modelName, prompt)
	if err != nil && isModelUnavailableError(err) && text == "" {
		// Кэш моделей мог устареть (модель отключили): обновляем список и
		// повторяем запрос на актуальной модели по умолчанию.
		if fallback, ok := fallbackModelAfterFailure(ctx, client, modelName); ok {
			log.Printf("agy-api: модель %q недоступна (%v), повторяем на %q", modelName, err, fallback)
			modelName = fallback
			text, err = p.streamOnce(ctx, client, modelName, prompt)
		}
	}
	if err != nil {
		if errors.Is(err, errStreamOutputClosed) {
			return
		}
		p.handleError(err)
		return
	}

	resEvt := map[string]interface{}{
		"type":       "result",
		"session_id": p.sessionID,
		"is_error":   false,
		"result":     text,
	}
	resBytes, _ := json.Marshal(resEvt)
	_, _ = fmt.Fprintf(p.wPipe, "%s\n", string(resBytes))
}

// errStreamOutputClosed означает, что читатель закрыл канал вывода — ошибку
// показывать не нужно, достаточно тихо завершиться.
var errStreamOutputClosed = errors.New("поток вывода закрыт")

// streamOnce выполняет один проход генерации и возвращает накопленный текст.
func (p *AgyAPIProcess) streamOnce(ctx context.Context, client *genai.Client, modelName string, prompt string) (string, error) {
	model := client.GenerativeModel(modelName)
	stream := model.GenerateContentStream(ctx, genai.Text(prompt))

	var textAccumulator strings.Builder

	for {
		resp, err := stream.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return textAccumulator.String(), err
		}

		for _, cand := range resp.Candidates {
			if cand.Content == nil {
				continue
			}
			for _, part := range cand.Content.Parts {
				textPart, ok := part.(genai.Text)
				if !ok {
					continue
				}
				text := string(textPart)
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
					return textAccumulator.String(), errStreamOutputClosed
				}
			}
		}
	}

	return textAccumulator.String(), nil
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
