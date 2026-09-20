package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
)

type mockTranscriber struct {
	text string
	err  error
}

func (m *mockTranscriber) Transcribe(_ context.Context, _ io.Reader, _ string) (string, error) {
	return m.text, m.err
}

func TestHandleVoiceWhenNotConfigured(t *testing.T) {
	prevURL := config.WhisperServerURL
	config.WhisperServerURL = ""
	defer func() { config.WhisperServerURL = prevURL }()

	m := mock.New()
	sess := &mock.Session{
		M:       m,
		ChatID:  "12345",
		Sender:  "12345",
		VoiceVal: &ports.VoiceMessage{FileID: "v1"},
	}

	err := handleVoice(sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	last := m.LastSent()
	if last == nil {
		t.Fatal("expected message to be sent")
	}
	if !strings.Contains(last.Text, "WHISPER_SERVER_URL") {
		t.Errorf("expected not configured prompt, got %q", last.Text)
	}
}

func TestHandleVoiceNilVoice(t *testing.T) {
	prevURL := config.WhisperServerURL
	config.WhisperServerURL = "http://127.0.0.1:8080"
	defer func() { config.WhisperServerURL = prevURL }()

	m := mock.New()
	sess := &mock.Session{
		M:        m,
		ChatID:   "12345",
		Sender:   "12345",
		VoiceVal: nil,
	}

	err := handleVoice(sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Sent) != 0 {
		t.Errorf("expected no messages for nil voice, got %d", len(m.Sent))
	}
}

func TestHandleVoiceDownloadFailure(t *testing.T) {
	prevURL := config.WhisperServerURL
	config.WhisperServerURL = "http://127.0.0.1:8080"
	defer func() { config.WhisperServerURL = prevURL }()

	SetTranscriber(&mockTranscriber{text: "hi"})

	m := mock.New()
	sess := &mock.Session{
		M:            m,
		ChatID:       "12345",
		Sender:       "12345",
		VoiceVal:     &ports.VoiceMessage{FileID: "v1", FileName: "voice.oga"},
		OpenVoiceErr: errors.New("network down"),
	}

	err := handleVoice(sess)
	if err == nil {
		t.Fatal("expected download error, got nil")
	}

	all := m.AllTexts()
	found := false
	for _, text := range all {
		if strings.Contains(text, "network down") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error message mentioning 'network down' in %v", all)
	}
}

func TestHandleVoiceTranscriptionError(t *testing.T) {
	prevURL := config.WhisperServerURL
	config.WhisperServerURL = "http://127.0.0.1:8080"
	defer func() { config.WhisperServerURL = prevURL }()

	SetTranscriber(&mockTranscriber{err: errors.New("server 500 error")})

	m := mock.New()
	sess := &mock.Session{
		M:           m,
		ChatID:      "12345",
		Sender:      "12345",
		VoiceVal:    &ports.VoiceMessage{FileID: "v1", FileName: "voice.oga"},
		VoiceReader: io.NopCloser(bytes.NewReader([]byte("dummy"))),
	}

	err := handleVoice(sess)
	if err == nil {
		t.Fatal("expected transcription error, got nil")
	}

	all := m.AllTexts()
	found := false
	for _, text := range all {
		if strings.Contains(text, "server 500 error") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error message mentioning 'server 500 error' in %v", all)
	}
}

func TestHandleVoiceEmptyRecognition(t *testing.T) {
	prevURL := config.WhisperServerURL
	config.WhisperServerURL = "http://127.0.0.1:8080"
	defer func() { config.WhisperServerURL = prevURL }()

	SetTranscriber(&mockTranscriber{text: "  .  "})

	m := mock.New()
	sess := &mock.Session{
		M:           m,
		ChatID:      "12345",
		Sender:      "12345",
		VoiceVal:    &ports.VoiceMessage{FileID: "v1", FileName: "voice.oga"},
		VoiceReader: io.NopCloser(bytes.NewReader([]byte("dummy"))),
	}

	err := handleVoice(sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all := m.AllTexts()
	found := false
	for _, text := range all {
		if strings.Contains(text, i18n.T(i18n.Default, "voice.empty")) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected empty speech notice in %v", all)
	}
}

func TestHandleVoiceCommandExecution(t *testing.T) {
	prevURL := config.WhisperServerURL
	config.WhisperServerURL = "http://127.0.0.1:8080"
	defer func() { config.WhisperServerURL = prevURL }()

	executedCmd := false
	registerCommandHandler("mytestcmd", func(s ports.Session) error {
		executedCmd = true
		return s.Send("executed test command!", ports.Rich())
	})

	SetTranscriber(&mockTranscriber{text: "/mytestcmd arg1 arg2."})

	m := mock.New()
	sess := &mock.Session{
		M:           m,
		ChatID:      "12345",
		Sender:      "12345",
		VoiceVal:    &ports.VoiceMessage{FileID: "v1", FileName: "voice.oga"},
		VoiceReader: io.NopCloser(bytes.NewReader([]byte("dummy"))),
	}

	err := handleVoice(sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !executedCmd {
		t.Error("expected command to be executed from voice recognition")
	}

	all := m.AllTexts()
	quoteFound := false
	for _, text := range all {
		if strings.Contains(text, "«/mytestcmd arg1 arg2.»") {
			quoteFound = true
			break
		}
	}
	if !quoteFound {
		t.Errorf("expected quote of recognized command in %v", all)
	}
}
