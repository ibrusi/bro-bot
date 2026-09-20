package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"context"
	"fmt"
	"html"
	"log"
	"strings"
	"sync"
	"time"
)

var (
	globalTranscriberMu sync.RWMutex
	globalTranscriber   ports.Transcriber

	commandHandlersMu sync.RWMutex
	commandHandlers   map[string]ports.Handler
)

// SetTranscriber задаёт реализацию ports.Transcriber для распознавания голосовых сообщений.
func SetTranscriber(t ports.Transcriber) {
	globalTranscriberMu.Lock()
	defer globalTranscriberMu.Unlock()
	globalTranscriber = t
}

func getTranscriber() ports.Transcriber {
	globalTranscriberMu.RLock()
	defer globalTranscriberMu.RUnlock()
	return globalTranscriber
}

func registerCommandHandler(name string, h ports.Handler) {
	commandHandlersMu.Lock()
	defer commandHandlersMu.Unlock()
	if commandHandlers == nil {
		commandHandlers = make(map[string]ports.Handler)
	}
	commandHandlers[name] = h
}

// dispatchCommand проверяет, является ли текст командой (начинается с "/"), и если
// обработчик зарегистрирован, выполняет его.
func dispatchCommand(s ports.Session, raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "/") {
		return false, nil
	}
	cmdLine := strings.TrimPrefix(raw, "/")
	parts := strings.Fields(cmdLine)
	if len(parts) == 0 {
		return false, nil
	}
	cmdName := strings.ToLower(parts[0])
	if idx := strings.Index(cmdName, "@"); idx != -1 {
		cmdName = cmdName[:idx]
	}

	commandHandlersMu.RLock()
	h, ok := commandHandlers[cmdName]
	commandHandlersMu.RUnlock()

	if !ok {
		return false, nil
	}
	return true, h(s)
}

type voiceSession struct {
	ports.Session
	text string
	args []string
}

func (vs *voiceSession) Text() string {
	return vs.text
}

func (vs *voiceSession) Args() []string {
	return vs.args
}

func wrapVoiceSession(s ports.Session, text string) ports.Session {
	fields := strings.Fields(text)
	var args []string
	if len(fields) > 1 {
		args = fields[1:]
	}
	return &voiceSession{
		Session: s,
		text:    text,
		args:    args,
	}
}

// isBlankAudio проверяет, является ли распознанный текст пустым или служебным токеном тишины Whisper.
func isBlankAudio(text string) bool {
	clean := strings.ToLower(strings.TrimSpace(text))
	clean = strings.Trim(clean, " .!?,")
	return clean == "" ||
		clean == "[blank_audio]" ||
		clean == "[music]" ||
		clean == "[noise]" ||
		clean == "(blank_audio)" ||
		clean == "(silence)" ||
		clean == "(тишина)" ||
		clean == "[тишина]"
}

// formatSTTLatency форматирует задержку транскрибации (миллисекунды для <1s, секунды для >=1s).
func formatSTTLatency(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

// formatVoiceMetrics собирает строку метрик: время STT, длительность аудио и RTF (Real-Time Factor).
func formatVoiceMetrics(sttDuration time.Duration, audioDurationSeconds int) string {
	latencyStr := formatSTTLatency(sttDuration)
	if audioDurationSeconds > 0 {
		rtf := sttDuration.Seconds() / float64(audioDurationSeconds)
		return fmt.Sprintf("STT: %s | Audio: %ds | RTF: %.2f", latencyStr, audioDurationSeconds, rtf)
	}
	return fmt.Sprintf("STT: %s", latencyStr)
}

// handleVoice обрабатывает входящие голосовые сообщения (tele.OnVoice) и аудиозаписи (tele.OnAudio).
func handleVoice(s ports.Session) error {
	voice := s.Voice()
	if voice == nil {
		return nil
	}

	lang := uiLang()

	if config.WhisperServerURL == "" {
		return s.Send(i18n.T(lang, "voice.not_configured"), ports.Rich())
	}

	transcriber := getTranscriber()
	if transcriber == nil {
		return s.Send(i18n.T(lang, "voice.not_configured"), ports.Rich())
	}

	// 1. Отправляем временное статус-сообщение о процессе распознавания
	statusRef, err := s.Messenger().Send(context.Background(), s.Chat(), i18n.T(lang, "voice.recognizing"), ports.Rich())
	hasStatus := err == nil

	// 2. Скачиваем аудиопоток через адаптер мессенджера
	ctx, cancel := context.WithTimeout(context.Background(), config.WhisperTimeout)
	defer cancel()

	rc, err := s.OpenVoice(ctx)
	if err != nil {
		errMsg := i18n.Tf(lang, "voice.download_failed", html.EscapeString(err.Error()))
		if hasStatus {
			_ = s.Messenger().Edit(context.Background(), statusRef, errMsg, ports.Rich())
		} else {
			_ = s.Send(errMsg, ports.Rich())
		}
		return err
	}
	defer rc.Close()

	// 3. Отправляем аудио на сервер Whisper
	sttStart := time.Now()
	recognizedText, err := transcriber.Transcribe(ctx, rc, voice.FileName)
	sttDuration := time.Since(sttStart)
	if err != nil {
		log.Printf("voice transcription error after %v: %v", sttDuration, err)
		errMsg := i18n.Tf(lang, "voice.transcribe_failed", html.EscapeString(err.Error()))
		if hasStatus {
			_ = s.Messenger().Edit(context.Background(), statusRef, errMsg, ports.Rich())
		} else {
			_ = s.Send(errMsg, ports.Rich())
		}
		return err
	}

	recognizedText = strings.TrimSpace(recognizedText)
	if isBlankAudio(recognizedText) {
		emptyMsg := i18n.T(lang, "voice.empty")
		if hasStatus {
			_ = s.Messenger().Edit(context.Background(), statusRef, emptyMsg, ports.Rich())
		} else {
			_ = s.Send(emptyMsg, ports.Rich())
		}
		return nil
	}

	// 4. Заменяем статус-сообщение в чате цитатой распознанного текста
	metricsStr := formatVoiceMetrics(sttDuration, voice.Duration)
	log.Printf("voice: transcribed in %v (%s): %q", sttDuration, metricsStr, recognizedText)

	var quoteMsg string
	if config.Debug {
		quoteMsg = i18n.Tf(lang, "voice.quote_metrics", html.EscapeString(recognizedText), html.EscapeString(metricsStr))
	} else {
		quoteMsg = i18n.Tf(lang, "voice.quote", html.EscapeString(recognizedText))
	}

	if hasStatus {
		_ = s.Messenger().Edit(context.Background(), statusRef, quoteMsg, ports.Rich())
	} else {
		_ = s.Send(quoteMsg, ports.Rich())
	}

	// 5. Оборачиваем сессию с распознанным текстом
	vSession := wrapVoiceSession(s, recognizedText)

	// 6. Если распознанный текст — команда бота (например "/status"), выполняем её
	cleanCmd := strings.TrimSuffix(recognizedText, ".")
	if strings.HasPrefix(cleanCmd, "/") {
		if dispatched, cmdErr := dispatchCommand(vSession, cleanCmd); dispatched {
			return cmdErr
		}
	}

	// 7. Иначе передаем текст в общий обработчик пользовательского ввода
	return handleTextWithContent(vSession, recognizedText)
}
