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
		{"http://localhost:8080", "http://localhost:8080/inference"},
		{"http://localhost:8080/", "http://localhost:8080/inference"},
		{"http://localhost:8080/inference", "http://localhost:8080/inference"},
		{"http://localhost:8080/inference/", "http://localhost:8080/inference"},
		{"http://localhost:8080/v1", "http://localhost:8080/v1/audio/transcriptions"},
		{"http://localhost:8080/v1/", "http://localhost:8080/v1/audio/transcriptions"},
		{"http://localhost:8080/v1/audio/transcriptions", "http://localhost:8080/v1/audio/transcriptions"},
		{"http://localhost:8080/v1/audio/transcriptions/", "http://localhost:8080/v1/audio/transcriptions"},
	}

	for _, tt := range tests {
		got := normalizeEndpoint(tt.input)
		if got != tt.want {
			t.Errorf("normalizeEndpoint(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTranscribeFallback404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/inference" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if r.URL.Path == "/v1/audio/transcriptions" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text": "Распознано через fallback",
			})
			return
		}
		http.Error(w, "unexpected path: "+r.URL.Path, http.StatusBadRequest)
	}))
	defer server.Close()

	client := New(Config{
		BaseURL:    server.URL, // нормализуется в server.URL + "/inference"
		ConvertWAV: false,
	})

	res, err := client.Transcribe(context.Background(), strings.NewReader("dummy"), "test.wav")
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if res != "Распознано через fallback" {
		t.Errorf("got %q, want %q", res, "Распознано через fallback")
	}

	if client.getEndpoint() != server.URL+"/v1/audio/transcriptions" {
		t.Errorf("endpoint not updated, got %q", client.getEndpoint())
	}
}

func TestTranscribeFallbackReverse404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/transcriptions" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if r.URL.Path == "/inference" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text": "Распознано через whisper.cpp",
			})
			return
		}
		http.Error(w, "unexpected path: "+r.URL.Path, http.StatusBadRequest)
	}))
	defer server.Close()

	client := New(Config{
		BaseURL:    server.URL + "/v1/audio/transcriptions",
		ConvertWAV: false,
	})

	res, err := client.Transcribe(context.Background(), strings.NewReader("dummy"), "test.wav")
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if res != "Распознано через whisper.cpp" {
		t.Errorf("got %q, want %q", res, "Распознано через whisper.cpp")
	}

	if client.getEndpoint() != server.URL+"/inference" {
		t.Errorf("endpoint not updated, got %q", client.getEndpoint())
	}
}

func TestTranscribeSuccess(t *testing.T) {
	var receivedAuth string
	var receivedModel string
	var receivedLanguage string
	var receivedPrompt string
	var receivedTemperature string
	var receivedTranslate string
	var receivedTask string
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
		receivedPrompt = r.FormValue("prompt")
		receivedTemperature = r.FormValue("temperature")
		receivedTranslate = r.FormValue("translate")
		receivedTask = r.FormValue("task")
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
		BaseURL:     server.URL,
		APIKey:      "test-secret-key",
		Model:       "small",
		Language:    "ru",
		Prompt:      "дебаг режим, код",
		Temperature: 0.0,
		ConvertWAV:  false, // в тесте передаем WAV напрямую
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
	if receivedPrompt != "дебаг режим, код" {
		t.Errorf("got prompt %q, want %q", receivedPrompt, "дебаг режим, код")
	}
	if receivedTemperature != "0.00" {
		t.Errorf("got temperature %q, want %q", receivedTemperature, "0.00")
	}
	if receivedTranslate != "false" {
		t.Errorf("got translate %q, want %q", receivedTranslate, "false")
	}
	if receivedTask != "transcribe" {
		t.Errorf("got task %q, want %q", receivedTask, "transcribe")
	}
	if receivedResponseFormat != "json" {
		t.Errorf("got response_format %q, want %q", receivedResponseFormat, "json")
	}
	if string(fileContent) != string(dummyWAV) {
		t.Errorf("file content mismatch")
	}
}

func TestTranscribeDefaultLanguage(t *testing.T) {
	var receivedLanguage string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		receivedLanguage = r.FormValue("language")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "Hello"})
	}))
	defer server.Close()

	client := New(Config{
		BaseURL:    server.URL,
		Language:   "", // не задан - должен отправиться "en"
		ConvertWAV: false,
	})

	_, err := client.Transcribe(context.Background(), strings.NewReader("RIFF1234WAVEfmt "), "test.wav")
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}

	if receivedLanguage != "en" {
		t.Errorf("got language %q, want %q", receivedLanguage, "en")
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

func TestTranscribePromptOmittedWhenEmpty(t *testing.T) {
	var promptFieldPresent bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		if r.MultipartForm != nil {
			if _, ok := r.MultipartForm.Value["prompt"]; ok {
				promptFieldPresent = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "Hello"})
	}))
	defer server.Close()

	client := New(Config{
		BaseURL:    server.URL,
		Prompt:     "", // пустой промпт
		ConvertWAV: false,
	})

	_, err := client.Transcribe(context.Background(), strings.NewReader("RIFF1234WAVEfmt "), "test.wav")
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}

	if promptFieldPresent {
		t.Errorf("expected prompt field to be omitted when empty")
	}
}

func TestFFmpegAudioConversion(t *testing.T) {
	if !HasFFmpeg() {
		t.Skip("ffmpeg not installed, skipping conversion test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Пустой ввод должен давать понятную ошибку от ffmpeg (с loudnorm=false и loudnorm=true)
	_, err := ConvertToWAV16k(ctx, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected error on empty input, got nil")
	}

	_, err = ConvertToWAV16k(ctx, strings.NewReader(""), true)
	if err == nil {
		t.Fatal("expected error on empty input with loudnorm, got nil")
	}
}
