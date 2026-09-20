package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config задаёт параметры подключения к Whisper HTTP-серверу.
type Config struct {
	BaseURL     string
	APIKey      string
	Model       string
	Language    string
	Prompt      string
	Temperature float64
	Loudnorm    bool
	Timeout     time.Duration
	ConvertWAV  bool
	HTTPClient  *http.Client
}

// Client — реализация ports.Transcriber для взаимодействия с Whisper через HTTP API.
type Client struct {
	mu          sync.RWMutex
	endpoint    string
	apiKey      string
	model       string
	language    string
	prompt      string
	temperature float64
	loudnorm    bool
	convertWAV  bool
	client      *http.Client
}

// New создает новый клиент Whisper.
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	model := cfg.Model
	if model == "" {
		model = "small"
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	endpoint := normalizeEndpoint(cfg.BaseURL)

	return &Client{
		endpoint:    endpoint,
		apiKey:      cfg.APIKey,
		model:       model,
		language:    cfg.Language,
		prompt:      cfg.Prompt,
		temperature: cfg.Temperature,
		loudnorm:    cfg.Loudnorm,
		convertWAV:  cfg.ConvertWAV,
		client:      httpClient,
	}
}

func (c *Client) getEndpoint() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.endpoint
}

func (c *Client) setEndpoint(ep string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endpoint = ep
}

// normalizeEndpoint приводит URL к полному пути эндпоинта транскрипции.
// Если путь не указан, по умолчанию используется /inference (родной эндпоинт whisper.cpp).
// Если URL оканчивается на /v1, дописывается /audio/transcriptions (OpenAI-совместимый путь).
func normalizeEndpoint(rawURL string) string {
	rawURL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if rawURL == "" {
		return ""
	}
	if strings.HasSuffix(rawURL, "/inference") || strings.HasSuffix(rawURL, "/v1/audio/transcriptions") {
		return rawURL
	}
	if strings.HasSuffix(rawURL, "/v1") {
		return rawURL + "/audio/transcriptions"
	}
	return rawURL + "/inference"
}

// alternateEndpoint возвращает альтернативный путь API при ошибке 404
// (/inference <-> /v1/audio/transcriptions).
func alternateEndpoint(current string) string {
	if strings.HasSuffix(current, "/inference") {
		return strings.TrimSuffix(current, "/inference") + "/v1/audio/transcriptions"
	}
	if strings.HasSuffix(current, "/v1/audio/transcriptions") {
		return strings.TrimSuffix(current, "/v1/audio/transcriptions") + "/inference"
	}
	return ""
}

type transcriptionResponse struct {
	Text  string `json:"text"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *Client) doRequest(ctx context.Context, endpoint string, bodyBytes []byte, contentType string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	return respBytes, resp.StatusCode, nil
}

// Transcribe отправляет аудиофайл на сервер Whisper и возвращает распознанный текст.
func (c *Client) Transcribe(ctx context.Context, audio io.Reader, filename string) (string, error) {
	endpoint := c.getEndpoint()
	if endpoint == "" {
		return "", fmt.Errorf("whisper: endpoint URL is empty")
	}

	var audioData []byte
	outFilename := filename

	if c.convertWAV || !strings.HasSuffix(strings.ToLower(filename), ".wav") {
		var err error
		audioData, err = ConvertToWAV16k(ctx, audio, c.loudnorm)
		if err != nil {
			return "", fmt.Errorf("whisper: audio conversion: %w", err)
		}
		outFilename = "audio.wav"
	} else {
		var err error
		audioData, err = io.ReadAll(audio)
		if err != nil {
			return "", fmt.Errorf("whisper: read audio stream: %w", err)
		}
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", outFilename)
	if err != nil {
		return "", fmt.Errorf("whisper: create form file: %w", err)
	}
	if _, err := part.Write(audioData); err != nil {
		return "", fmt.Errorf("whisper: write audio to multipart: %w", err)
	}

	if c.model != "" {
		_ = writer.WriteField("model", c.model)
	}
	lang := c.language
	if lang == "" {
		lang = "en"
	}
	_ = writer.WriteField("language", lang)
	if strings.TrimSpace(c.prompt) != "" {
		_ = writer.WriteField("prompt", strings.TrimSpace(c.prompt))
	}
	_ = writer.WriteField("temperature", fmt.Sprintf("%.2f", c.temperature))
	_ = writer.WriteField("translate", "false")
	_ = writer.WriteField("task", "transcribe")
	_ = writer.WriteField("response_format", "json")

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("whisper: close multipart writer: %w", err)
	}

	bodyBytes := body.Bytes()
	contentType := writer.FormDataContentType()

	respBytes, statusCode, err := c.doRequest(ctx, endpoint, bodyBytes, contentType)
	if err != nil {
		return "", fmt.Errorf("whisper: request failed: %w", err)
	}

	// Автоматический fallback при 404: если /inference вернет 404, пробуем /v1/audio/transcriptions (или наоборот)
	if statusCode == http.StatusNotFound {
		alt := alternateEndpoint(endpoint)
		if alt != "" {
			altRespBytes, altStatusCode, altErr := c.doRequest(ctx, alt, bodyBytes, contentType)
			if altErr == nil && altStatusCode == http.StatusOK {
				c.setEndpoint(alt)
				respBytes = altRespBytes
				statusCode = altStatusCode
			}
		}
	}

	if statusCode != http.StatusOK {
		return "", fmt.Errorf("whisper: server returned HTTP %d: %s", statusCode, string(respBytes))
	}

	var tr transcriptionResponse
	if err := json.Unmarshal(respBytes, &tr); err != nil {
		// Некоторые сервера или конфигурации могут возвращать plain text
		plainText := strings.TrimSpace(string(respBytes))
		if plainText != "" && !strings.HasPrefix(plainText, "{") {
			return plainText, nil
		}
		return "", fmt.Errorf("whisper: decode response json: %w", err)
	}

	if tr.Error != nil && tr.Error.Message != "" {
		return "", fmt.Errorf("whisper: server error: %s", tr.Error.Message)
	}

	return strings.TrimSpace(tr.Text), nil
}
