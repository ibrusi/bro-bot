package utils

import (
	"fmt"
	"regexp"
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
