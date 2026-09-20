package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"http://localhost:8080", "http://localhost:8080/v1/audio/transcriptions"},
		{"http://localhost:8080/", "http://localhost:8080/v1/audio/transcriptions"},
		{"http://localhost:8080/v1/audio/transcriptions", "http://localhost:8080/v1/audio/transcriptions"},
		{"http://localhost:8080/inference", "http://localhost:8080/inference"},
		{"http://localhost:8080/inference/", "http://localhost:8080/inference"},
	}

	for _, tt := range tests {
		got := normalizeEndpoint(tt.input)
		if got != tt.want {
			t.Errorf("normalizeEndpoint(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTranscribeSuccess(t *testing.T) {
	var receivedAuth string
	var receivedModel string
	var receivedLanguage string
	var receivedResponseFormat string
	var fileContent []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		receivedAuth = r.Header.Get("Authorization")

		err := r.ParseMultipartForm(10 << 20)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		receivedModel = r.FormValue("model")
		receivedLanguage = r.FormValue("language")
		receivedResponseFormat = r.FormValue("response_format")

		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		fileContent, _ = io.ReadAll(file)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"text": "Привет, бот!",
		})
	}))
	defer server.Close()

	client := New(Config{
		BaseURL:    server.URL,
		APIKey:     "test-secret-key",
		Model:      "small",
		Language:   "ru",
		ConvertWAV: false, // в тесте передаем WAV напрямую
	})

	dummyWAV := []byte("RIFF1234WAVEfmt ")
	res, err := client.Transcribe(context.Background(), bytes.NewReader(dummyWAV), "test.wav")
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}

	if res != "Привет, бот!" {
		t.Errorf("got %q, want %q", res, "Привет, бот!")
	}
	if receivedAuth != "Bearer test-secret-key" {
		t.Errorf("got auth %q, want %q", receivedAuth, "Bearer test-secret-key")
	}
	if receivedModel != "small" {
		t.Errorf("got model %q, want %q", receivedModel, "small")
	}
	if receivedLanguage != "ru" {
		t.Errorf("got language %q, want %q", receivedLanguage, "ru")
	}
	if receivedResponseFormat != "json" {
		t.Errorf("got response_format %q, want %q", receivedResponseFormat, "json")
	}
	if string(fileContent) != string(dummyWAV) {
		t.Errorf("file content mismatch")
	}
}

func TestTranscribeServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "CUDA out of memory", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := New(Config{
		BaseURL:    server.URL,
		ConvertWAV: false,
	})

	_, err := client.Transcribe(context.Background(), strings.NewReader("dummy"), "test.wav")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "CUDA out of memory") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestTranscribeJSONError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "Invalid audio format",
			},
		})
	}))
	defer server.Close()

	client := New(Config{
		BaseURL:    server.URL,
		ConvertWAV: false,
	})

	_, err := client.Transcribe(context.Background(), strings.NewReader("dummy"), "test.wav")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid audio format") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestTranscribePlainTextResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Простой текст ответа"))
	}))
	defer server.Close()

	client := New(Config{
		BaseURL:    server.URL,
		ConvertWAV: false,
	})

	res, err := client.Transcribe(context.Background(), strings.NewReader("dummy"), "test.wav")
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if res != "Простой текст ответа" {
		t.Errorf("got %q, want %q", res, "Простой текст ответа")
	}
}

func TestTranscribeEmptyURL(t *testing.T) {
	client := New(Config{BaseURL: ""})
	_, err := client.Transcribe(context.Background(), strings.NewReader("dummy"), "test.wav")
	if err == nil {
		t.Fatal("expected error on empty URL, got nil")
	}
}

func TestFFmpegAudioConversion(t *testing.T) {
	if !HasFFmpeg() {
		t.Skip("ffmpeg not installed, skipping conversion test")
	}

	// Создаем минимальный валидный аудиопоток или проверяем вызов ffmpeg
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Пустой ввод должен давать понятную ошибку от ffmpeg
	_, err := ConvertToWAV16k(ctx, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error on empty input, got nil")
	}
}
