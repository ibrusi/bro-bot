package scripts

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTailBufferKeepsTail(t *testing.T) {
	b := NewTailBuffer(5)
	_, _ = b.Write([]byte("abc"))
	if got, truncated := b.Snapshot(); got != "abc" || truncated {
		t.Errorf("Snapshot = %q, %v", got, truncated)
	}
	_, _ = b.Write([]byte("defg"))
	if got, truncated := b.Snapshot(); got != "cdefg" || !truncated {
		t.Errorf("Snapshot = %q, %v; ожидали хвост cdefg", got, truncated)
	}
}

// TestTailBufferCutsOnRuneBoundary — обрезка не оставляет обрывок многобайтного символа.
func TestTailBufferCutsOnRuneBoundary(t *testing.T) {
	b := NewTailBuffer(5)
	_, _ = b.Write([]byte(strings.Repeat("я", 10)))
	got, _ := b.Snapshot()
	if !utf8.ValidString(got) || got != "яя" {
		t.Errorf("Snapshot = %q, ожидали целые символы", got)
	}
}
