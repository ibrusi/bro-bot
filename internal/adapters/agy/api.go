package agy

import (
	"bro-bot/internal/models"
	"bro-bot/internal/ports"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func resolveGeminiModel(modelName string) string {
	if modelName == "default" || modelName == "" {
		return "gemini-2.5-pro"
	}

	if models.GlobalModelRegistry != nil {
		if resolved, ok := models.GlobalModelRegistry.ResolveModel(modelName); ok {
			// Models from registry might have CLI specific names, let's map known aliases
			if strings.Contains(strings.ToLower(resolved), "flash") {
			    if strings.Contains(strings.ToLower(resolved), "2.5") {
					return "gemini-2.5-flash"
				}
				if strings.Contains(strings.ToLower(resolved), "8b") {
					return "gemini-1.5-flash-8b"
				}
				return "gemini-1.5-flash"
			}
			if strings.Contains(strings.ToLower(resolved), "pro") {
				if strings.Contains(strings.ToLower(resolved), "2.5") {
					return "gemini-2.5-pro"
				}
				return "gemini-1.5-pro"
			}
			return resolved
		}
	}

	lower := strings.ToLower(modelName)
	if strings.Contains(lower, "flash") {
		return "gemini-1.5-flash"
	}
	if strings.Contains(lower, "pro") {
		return "gemini-1.5-pro"
	}

	return modelName
}

func (a *AgyAPIAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("API ключ не найден. Задайте GEMINI_API_KEY в .env для работы в режиме API")
	}

	modelName := resolveGeminiModel(args.ModelName)

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

	go proc.runStreaming(ctx, apiKey, modelName, args.Prompt)

	return proc, nil
}

func (a *AgyAPIAdapter) GetModels(ctx context.Context) ([]byte, error) {
	modelsText := "gemini-2.5-pro Gemini 2.5 Pro\n" +
		"gemini-2.5-flash Gemini 2.5 Flash\n" +
		"gemini-1.5-pro Gemini 1.5 Pro\n" +
		"gemini-1.5-flash Gemini 1.5 Flash\n" +
		"gemini-1.5-flash-8b Gemini 1.5 Flash-8B\n"
	return []byte(modelsText), nil
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

func (p *AgyAPIProcess) runStreaming(ctx context.Context, apiKey string, modelName string, prompt string) {
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

	model := client.GenerativeModel(modelName)
	stream := model.GenerateContentStream(ctx, genai.Text(prompt))

	var textAccumulator strings.Builder

	for {
		resp, err := stream.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			p.handleError(err)
			return
		}

		for _, cand := range resp.Candidates {
			if cand.Content != nil {
				for _, part := range cand.Content.Parts {
					if textPart, ok := part.(genai.Text); ok {
						text := string(textPart)
						if text != "" {
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
								return
							}
						}
					}
				}
			}
		}
	}

	resEvt := map[string]interface{}{
		"type":       "result",
		"session_id": p.sessionID,
		"is_error":   false,
		"result":     textAccumulator.String(),
	}
	resBytes, _ := json.Marshal(resEvt)
	_, _ = fmt.Fprintf(p.wPipe, "%s\n", string(resBytes))
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
