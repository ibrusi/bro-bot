package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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

func TestHandleVoiceBlankAudioRecognition(t *testing.T) {
	prevURL := config.WhisperServerURL
	config.WhisperServerURL = "http://127.0.0.1:8080"
	defer func() { config.WhisperServerURL = prevURL }()

	SetTranscriber(&mockTranscriber{text: " [BLANK_AUDIO]\n"})

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
		t.Errorf("expected empty speech notice for [BLANK_AUDIO] in %v", all)
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

func TestFormatVoiceMetrics(t *testing.T) {
	cases := []struct {
		name          string
		sttDur        time.Duration
		audioDur      int
		expectedSub   []string
		unexpectedSub []string
	}{
		{
			name:        "sub-second latency with valid audio duration",
			sttDur:      850 * time.Millisecond,
			audioDur:    4,
			expectedSub: []string{"STT: 850ms", "Audio: 4s", "RTF: 0.21"},
		},
		{
			name:        "multi-second latency with valid audio duration",
			sttDur:      2500 * time.Millisecond,
			audioDur:    5,
			expectedSub: []string{"STT: 2.50s", "Audio: 5s", "RTF: 0.50"},
		},
		{
			name:          "zero audio duration omits RTF and audio info",
			sttDur:        720 * time.Millisecond,
			audioDur:      0,
			expectedSub:   []string{"STT: 720ms"},
			unexpectedSub: []string{"Audio:", "RTF:"},
		},
		{
			name:          "negative audio duration omits RTF and audio info",
			sttDur:        500 * time.Millisecond,
			audioDur:      -1,
			expectedSub:   []string{"STT: 500ms"},
			unexpectedSub: []string{"Audio:", "RTF:"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := formatVoiceMetrics(tc.sttDur, tc.audioDur)
			for _, exp := range tc.expectedSub {
				if !strings.Contains(res, exp) {
					t.Errorf("expected %q in %q", exp, res)
				}
			}
			for _, unexp := range tc.unexpectedSub {
				if strings.Contains(res, unexp) {
					t.Errorf("unexpected %q in %q", unexp, res)
				}
			}
		})
	}
}

func TestHandleVoiceDebugModeBehavior(t *testing.T) {
	prevURL := config.WhisperServerURL
	prevDebug := config.Debug
	config.WhisperServerURL = "http://127.0.0.1:8080"
	defer func() {
		config.WhisperServerURL = prevURL
		config.Debug = prevDebug
	}()

	SetTranscriber(&mockTranscriber{text: "Тестовая речь"})

	// 1. При Debug = false плашка с метриками НЕ должна отправляться в чат
	config.Debug = false
	m1 := mock.New()
	sess1 := &mock.Session{
		M:      m1,
		ChatID: "12345",
		Sender: "12345",
		VoiceVal: &ports.VoiceMessage{
			FileID:   "v1",
			FileName: "voice.oga",
			Duration: 5,
		},
		VoiceReader: io.NopCloser(bytes.NewReader([]byte("dummy"))),
	}

	if err := handleVoice(sess1); err != nil {
		t.Fatalf("handleVoice failed: %v", err)
	}
	all1 := m1.AllTexts()
	for _, text := range all1 {
		if strings.Contains(text, "STT:") || strings.Contains(text, "RTF:") {
			t.Errorf("unexpected metrics in chat when Debug=false: %s", text)
		}
	}

	// 2. При Debug = true плашка с метриками должна присутствовать в чате
	config.Debug = true
	m2 := mock.New()
	sess2 := &mock.Session{
		M:      m2,
		ChatID: "12345",
		Sender: "12345",
		VoiceVal: &ports.VoiceMessage{
			FileID:   "v2",
			FileName: "voice.oga",
			Duration: 5,
		},
		VoiceReader: io.NopCloser(bytes.NewReader([]byte("dummy"))),
	}

	if err := handleVoice(sess2); err != nil {
		t.Fatalf("handleVoice failed: %v", err)
	}
	all2 := m2.AllTexts()
	found := false
	for _, text := range all2 {
		if strings.Contains(text, "«Тестовая речь»") && strings.Contains(text, "STT:") && strings.Contains(text, "Audio: 5s") && strings.Contains(text, "RTF:") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected quote with metrics in chat when Debug=true, got: %v", all2)
	}
}

func TestHandleVoiceDebugModeZeroDuration(t *testing.T) {
	prevURL := config.WhisperServerURL
	prevDebug := config.Debug
	config.WhisperServerURL = "http://127.0.0.1:8080"
	defer func() {
		config.WhisperServerURL = prevURL
		config.Debug = prevDebug
	}()

	SetTranscriber(&mockTranscriber{text: "Речь без длительности"})

	config.Debug = true
	m := mock.New()
	sess := &mock.Session{
		M:      m,
		ChatID: "12345",
		Sender: "12345",
		VoiceVal: &ports.VoiceMessage{
			FileID:   "v3",
			FileName: "voice.oga",
			Duration: 0,
		},
		VoiceReader: io.NopCloser(bytes.NewReader([]byte("dummy"))),
	}

	if err := handleVoice(sess); err != nil {
		t.Fatalf("handleVoice failed: %v", err)
	}
	all := m.AllTexts()
	found := false
	for _, text := range all {
		if strings.Contains(text, "«Речь без длительности»") && strings.Contains(text, "STT:") {
			if strings.Contains(text, "Audio:") || strings.Contains(text, "RTF:") {
				t.Errorf("unexpected audio/RTF metrics when duration=0: %s", text)
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected quote with STT latency in %v", all)
	}
}
