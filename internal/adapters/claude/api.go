package claude

import (
	"bro-bot/internal/ports"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

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

func NewClaudeAPIAdapter() *ClaudeAPIAdapter {
	return &ClaudeAPIAdapter{
		HTTPClient: &http.Client{Timeout: 0}, // No client-level timeout for streaming
		BaseURL:    "https://api.anthropic.com/v1",
	}
}

func (a *ClaudeAPIAdapter) AgentName() string {
	return "claude-api"
}

// ExecuteTask запускает генерацию сообщений через Claude Messages API со стримингом в формате stream-json NDJSON
func (a *ClaudeAPIAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("CLAUDE_API_KEY")
	}

	// Если API ключ не передан в окружении, пробуем запустить CLI адаптер как фолбэк с информационным логом
	if apiKey == "" {
		cliAdapter := NewClaudeAdapter()
		return cliAdapter.ExecuteTask(ctx, args)
	}

	modelName := resolveClaudeModel(args.ModelName)
	if modelName == "sonnet" {
		modelName = "claude-3-7-sonnet-20250219"
	} else if modelName == "opus" {
		modelName = "claude-3-opus-20240229"
	} else if modelName == "haiku" {
		modelName = "claude-3-5-haiku-20241022"
	}

	sessionID := args.ConversationID
	if sessionID == "" {
		sessionID = fmt.Sprintf("claude-api-%d", time.Now().UnixNano())
	}

	reqBody := map[string]interface{}{
		"model":      modelName,
		"max_tokens": 8192,
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": args.Prompt,
			},
		},
		"stream": true,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ошибка маршалинга запроса Claude API: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", a.BaseURL+"/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("ошибка создания HTTP запроса к Claude API: %w", err)
	}

	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	rPipe, wPipe := io.Pipe()

	proc := &ClaudeAPIProcess{
		rPipe:     rPipe,
		wPipe:     wPipe,
		ctx:       ctx,
		doneChan:  make(chan struct{}),
		sessionID: sessionID,
	}

	go proc.runStreaming(req, a.HTTPClient)

	return proc, nil
}

func (a *ClaudeAPIAdapter) GetModels(ctx context.Context) ([]byte, error) {
	modelsText := "claude-3-7-sonnet-20250219 Claude 3.7 Sonnet (Hybrid Reasoning)\n" +
		"claude-3-5-sonnet-20241022 Claude 3.5 Sonnet\n" +
		"claude-3-opus-20240229 Claude 3 Opus\n" +
		"claude-3-5-haiku-20241022 Claude 3.5 Haiku\n"
	return []byte(modelsText), nil
}

func (a *ClaudeAPIAdapter) GetQuota(ctx context.Context) ([]byte, error) {
	return []byte(`{"status":"SUCCESS","response":"Claude API usage is managed in Anthropic Console"}`), nil
}

func (a *ClaudeAPIAdapter) GetQuotaText(ctx context.Context) ([]byte, error) {
	return []byte("Claude API integration active via Anthropic Console."), nil
}

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

func (p *ClaudeAPIProcess) GetCmd() *exec.Cmd {
	return nil
}

func (p *ClaudeAPIProcess) runStreaming(req *http.Request, client *http.Client) {
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

	resp, err := client.Do(req)
	if err != nil {
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
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		p.mu.Lock()
		p.err = fmt.Errorf("%s", errMsg)
		p.mu.Unlock()

		errEvt := map[string]interface{}{
			"type":       "result",
			"session_id": p.sessionID,
			"is_error":   true,
			"errors":     []string{errMsg},
		}
		errBytes, _ := json.Marshal(errEvt)
		_, _ = fmt.Fprintf(p.wPipe, "%s\n", string(errBytes))
		return
	}

	buf := new(bytes.Buffer)
	scanner := io.Reader(resp.Body)
	readBuf := make([]byte, 4096)

	var textAccumulator strings.Builder
	var inputTokens, outputTokens int64

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

	// 2. Отправляем итоговый результат
	resEvt := map[string]interface{}{
		"type":       "result",
		"session_id": p.sessionID,
		"is_error":   false,
		"result":     textAccumulator.String(),
		"usage": map[string]interface{}{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
	resBytes, _ := json.Marshal(resEvt)
	_, _ = fmt.Fprintf(p.wPipe, "%s\n", string(resBytes))
}
