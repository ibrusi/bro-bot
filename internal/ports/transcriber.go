package ports

import (
	"context"
	"io"
)

// Transcriber — нейтральный контракт для сервисов распознавания речи (speech-to-text).
type Transcriber interface {
	// Transcribe принимает поток аудиоданных и имя файла, возвращая распознанный текст.
	Transcribe(ctx context.Context, audio io.Reader, filename string) (string, error)
}
