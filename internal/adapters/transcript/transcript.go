// Package transcript читает историю переписки CLI-агента (claude/agy) напрямую
// из файла сессии на диске. Ничего не кэширует и не сохраняет в БД бота —
// каждый вызов заново читает актуальное состояние файла сессии.
package transcript

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Turn — одна реплика диалога: сообщение пользователя или ответ агента.
type Turn struct {
	Role      string // "user" or "assistant"
	Text      string
	Timestamp string
}

// ErrSessionNotFound возвращается, если файл сессии с таким ID не найден на диске.
var ErrSessionNotFound = errors.New("transcript: session file not found")

// sessionEntry — минимальный набор полей одной строки JSONL-файла сессии.
type sessionEntry struct {
	Type        string          `json:"type"`
	IsSidechain bool            `json:"isSidechain"`
	Timestamp   string          `json:"timestamp"`
	Message     json.RawMessage `json:"message"`
}

type sessionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// SessionFilePath возвращает путь к JSONL-файлу сессии claude/agy для рабочей
// директории проекта и ID разговора (совпадает со схемой хранения Claude Code:
// ~/.claude/projects/<slug-от-абсолютного-пути>/<conversationID>.jsonl).
func SessionFilePath(workDir, conversationID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("transcript: resolve home directory: %w", err)
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("transcript: resolve project path: %w", err)
	}
	return filepath.Join(home, ".claude", "projects", projectSlug(absWorkDir), conversationID+".jsonl"), nil
}

// projectSlug повторяет схему именования директорий Claude Code: все символы,
// кроме латинских букв и цифр, заменяются на дефис.
func projectSlug(absPath string) string {
	runes := []rune(absPath)
	out := make([]rune, len(runes))
	for i, r := range runes {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out[i] = r
		default:
			out[i] = '-'
		}
	}
	return string(out)
}

// ReadSession читает реплики пользователя и агента из файла сессии по ID
// разговора. Читает файл заново при каждом вызове — история нигде не
// сохраняется и не кэшируется ботом.
func ReadSession(workDir, conversationID string) ([]Turn, error) {
	path, err := SessionFilePath(workDir, conversationID)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("transcript: open session file: %w", err)
	}
	defer f.Close()

	var turns []Turn
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry sessionEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.IsSidechain {
			continue // реплики субагентов не показываем
		}
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}
		if len(entry.Message) == 0 {
			continue
		}

		var msg sessionMessage
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}

		text := extractText(msg.Content)
		if text == "" {
			continue
		}

		turns = append(turns, Turn{Role: entry.Type, Text: text, Timestamp: entry.Timestamp})
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("transcript: read session file: %w", err)
	}

	return turns, nil
}

// extractText достаёт читаемый текст реплики из поля content, которое у
// CLI-агентов бывает либо простой строкой, либо массивом блоков (text,
// thinking, tool_use, tool_result и т.д.) — из массива берём только text.
func extractText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}

	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return asString
	}

	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}

	result := ""
	for _, b := range blocks {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		if result != "" {
			result += "\n"
		}
		result += b.Text
	}
	return result
}
