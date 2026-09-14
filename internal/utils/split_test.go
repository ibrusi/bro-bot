package utils

import (
	"strings"
	"testing"
)

func TestSplitTelegramHTML(t *testing.T) {
	t.Run("Short text returns single chunk", func(t *testing.T) {
		text := "<b>Hello</b> world!"
		chunks := SplitTelegramHTML(text, 100)
		if len(chunks) != 1 || chunks[0] != text {
			t.Fatalf("expected 1 chunk, got %v", chunks)
		}
	})

	t.Run("Splits lines with open tag properly closed and reopened", func(t *testing.T) {
		text := "<i>Line 1: 1234567890\nLine 2: 1234567890\nLine 3: 1234567890</i>"
		// Set limit so each line cannot fit in one chunk together
		chunks := SplitTelegramHTML(text, 35)
		if len(chunks) < 2 {
			t.Fatalf("expected multiple chunks, got %d: %v", len(chunks), chunks)
		}

		for i, chunk := range chunks {
			// Each chunk must have balanced <i> and </i>
			openCount := strings.Count(chunk, "<i>")
			closeCount := strings.Count(chunk, "</i>")
			if openCount != closeCount {
				t.Errorf("chunk %d has unbalanced <i> tags (%d open vs %d close):\n%s", i, openCount, closeCount, chunk)
			}
			if len([]rune(chunk)) > 50 {
				t.Errorf("chunk %d exceeds max length limit: %d runes", i, len([]rune(chunk)))
			}
		}
	})

	t.Run("Splits with nested tags", func(t *testing.T) {
		text := "<b>Bold start\n<i>Italic line 1\nItalic line 2</i>\nBold end</b>"
		chunks := SplitTelegramHTML(text, 30)
		if len(chunks) < 2 {
			t.Fatalf("expected multiple chunks, got %d", len(chunks))
		}

		for i, chunk := range chunks {
			bOpen := strings.Count(chunk, "<b>")
			bClose := strings.Count(chunk, "</b>")
			if bOpen != bClose {
				t.Errorf("chunk %d has unbalanced <b> tags in %s", i, chunk)
			}
			iOpen := strings.Count(chunk, "<i>")
			iClose := strings.Count(chunk, "</i>")
			if iOpen != iClose {
				t.Errorf("chunk %d has unbalanced <i> tags in %s", i, chunk)
			}
		}
	})

	t.Run("Preserves attributes on tags when reopening", func(t *testing.T) {
		text := `<a href="https://example.com/test">Line 1: alpha bravo charlie\nLine 2: delta echo foxtrot</a>`
		chunks := SplitTelegramHTML(text, 65)
		if len(chunks) >= 2 {
			if !strings.Contains(chunks[0], `</a>`) {
				t.Errorf("chunk 0 expected to end with </a>, got: %s", chunks[0])
			}
			if !strings.Contains(chunks[1], `<a href="https://example.com/test">`) {
				t.Errorf("chunk 1 expected to reopen <a href=...>, got: %s", chunks[1])
			}
		}
	})

	t.Run("Splits preformatted code block properly", func(t *testing.T) {
		lines := []string{
			"<pre>",
			"func step1() {",
			"    doAction1()",
			"}",
			"func step2() {",
			"    doAction2()",
			"}",
			"</pre>",
		}
		text := strings.Join(lines, "\n")
		chunks := SplitTelegramHTML(text, 45)
		if len(chunks) < 2 {
			t.Fatalf("expected multiple chunks, got %d", len(chunks))
		}

		for i, chunk := range chunks {
			preOpen := strings.Count(chunk, "<pre>")
			preClose := strings.Count(chunk, "</pre>")
			if preOpen != preClose {
				t.Errorf("chunk %d has unbalanced <pre> tags:\n%s", i, chunk)
			}
		}
	})
}

func TestSplitPlainText(t *testing.T) {
	lines := []string{
		"Alpha 1234567890",
		"Beta 1234567890",
		"Gamma 1234567890",
	}
	text := strings.Join(lines, "\n")
	chunks := SplitPlainText(text, 25)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if len([]rune(chunk)) > 35 {
			t.Errorf("chunk %d too long: %d", i, len([]rune(chunk)))
		}
	}
}

func TestStripTelegramHTML(t *testing.T) {
	input := `<b>Bold</b> &amp; <i>Italic</i> &lt;code&gt; <a href="http://url">link</a>`
	expected := `Bold & Italic <code> link`
	got := StripTelegramHTML(input)
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestSplitMessageByMode(t *testing.T) {
	longText := strings.Repeat("A", 5000)
	chunks := SplitMessageByMode(longText, "plain", 3800)
	if len(chunks) < 2 {
		t.Errorf("expected plain text to be split into >= 2 chunks, got %d", len(chunks))
	}

	longHTML := "<b>" + strings.Repeat("test\n", 1000) + "</b>"
	htmlChunks := SplitMessageByMode(longHTML, "HTML", 3800)
	if len(htmlChunks) < 2 {
		t.Errorf("expected HTML to be split into >= 2 chunks, got %d", len(htmlChunks))
	}
}
