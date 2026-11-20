package utils

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var AnsiRegex = regexp.MustCompile(`\x1b(\[[0-9;?><=$]*[a-zA-Z~]|\][0-9;]*\x07|\([B0-9])|\r`)

func TruncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen-3]) + "..."
	}
	return s
}

// TruncateWithNote обрезает строку до maxRunes рун и дописывает note, если обрезка
// была. Работает по рунам, а не по байтам: срез s[:n] на кириллице рвёт символ пополам.
func TruncateWithNote(s string, maxRunes int, note string) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + note
}

// FormatCount печатает крупные числа компактно: 1.2M, 45K, 900.
func FormatCount(value int64) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.0fK", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

// ExtractCommandPayload отделяет ведущую /команду (включая /cmd@botname) и возвращает
// полный текст аргументов с сохранением всех переносов строк и внутреннего форматирования.
// Обходит ограничение gopkg.in/telebot.v3 (cmdRx без флага (?s)), где Payload обрезается на '\n'.
// Возвращает ("", false), если rawText не начинается с "/".
func ExtractCommandPayload(rawText string) (string, bool) {
	trimmed := strings.TrimSpace(rawText)
	if !strings.HasPrefix(trimmed, "/") {
		return "", false
	}
	idx := 0
	for idx < len(trimmed) {
		r, size := utf8.DecodeRuneInString(trimmed[idx:])
		if unicode.IsSpace(r) {
			break
		}
		idx += size
	}
	return strings.TrimSpace(trimmed[idx:]), true
}
