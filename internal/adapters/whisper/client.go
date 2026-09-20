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
	"time"
)

// Config задаёт параметры подключения к Whisper HTTP-серверу.
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	Language   string
	Timeout    time.Duration
	ConvertWAV bool
	HTTPClient *http.Client
}

// Client — реализация ports.Transcriber для взаимодействия с Whisper через HTTP API.
type Client struct {
	endpoint   string
	apiKey     string
	model      string
	language   string
	convertWAV bool
	client     *http.Client
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
		endpoint:   endpoint,
		apiKey:     cfg.APIKey,
		model:      model,
		language:   cfg.Language,
		convertWAV: cfg.ConvertWAV,
		client:     httpClient,
	}
}

// normalizeEndpoint приводит URL к полному пути эндпоинта транскрипции.
func normalizeEndpoint(rawURL string) string {
	rawURL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if rawURL == "" {
		return ""
	}
	if strings.HasSuffix(rawURL, "/v1/audio/transcriptions") || strings.HasSuffix(rawURL, "/inference") {
		return rawURL
	}
	return rawURL + "/v1/audio/transcriptions"
}

type transcriptionResponse struct {
	Text  string `json:"text"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Transcribe отправляет аудиофайл на сервер Whisper и возвращает распознанный текст.
func (c *Client) Transcribe(ctx context.Context, audio io.Reader, filename string) (string, error) {
	if c.endpoint == "" {
		return "", fmt.Errorf("whisper: endpoint URL is empty")
	}

	var audioData []byte
	outFilename := filename

	if c.convertWAV || !strings.HasSuffix(strings.ToLower(filename), ".wav") {
		var err error
		audioData, err = ConvertToWAV16k(ctx, audio)
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
	if c.language != "" {
		_ = writer.WriteField("language", c.language)
	}
	_ = writer.WriteField("response_format", "json")

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("whisper: close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, body)
	if err != nil {
		return "", fmt.Errorf("whisper: create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("whisper: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper: server returned HTTP %d: %s", resp.StatusCode, string(respBytes))
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
