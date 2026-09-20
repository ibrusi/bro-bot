package whisper

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// HasFFmpeg сообщает, установлен ли исполняемый файл ffmpeg в системе.
func HasFFmpeg() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// ConvertToWAV16k конвертирует аудиопоток (например, Telegram OGG/Opus)
// в 16-битный моно PCM WAV 16kHz через ffmpeg. При loudnorm=true применяется
// нормализация громкости (EBU R128) для улучшения разборчивости тихого голоса и шёпота.
// Для гарантированной валидности RIFF-заголовков (с указанием точной длины фрагмента)
// вывод записывается во временный файл и сразу же удаляется после чтения.
func ConvertToWAV16k(ctx context.Context, src io.Reader, loudnorm bool) ([]byte, error) {
	if !HasFFmpeg() {
		return nil, fmt.Errorf("ffmpeg not found in PATH: required for voice conversion")
	}

	tmpWav, err := os.CreateTemp("", "bro-bot-voice-*.wav")
	if err != nil {
		return nil, fmt.Errorf("create temp wav file: %w", err)
	}
	tmpWavPath := tmpWav.Name()
	_ = tmpWav.Close()
	defer os.Remove(tmpWavPath)

	args := []string{
		"-loglevel", "error",
		"-i", "pipe:0",
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
	}
	if loudnorm {
		args = append(args, "-af", "loudnorm")
	}
	args = append(args, "-y", tmpWavPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdin = src
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg conversion failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	data, err := os.ReadFile(tmpWavPath)
	if err != nil {
		return nil, fmt.Errorf("read converted wav: %w", err)
	}
	return data, nil
}
