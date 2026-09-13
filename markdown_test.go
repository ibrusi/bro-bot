package main

import (
	"strings"
	"testing"
)

func TestMarkdownToTelegramHTML_Basic(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Headers",
			input:    "# Title\n## Subtitle\n### Subsub",
			expected: "<b>Title</b>\n<b>Subtitle</b>\n<b>Subsub</b>",
		},
		{
			name:     "Bold and Italic",
			input:    "This is **bold** and *italic* and ***both***.",
			expected: "This is <b>bold</b> and <i>italic</i> and <b><i>both</i></b>.",
		},
		{
			name:     "Underscore in variable name should not italicize",
			input:    "Check my_test_variable and _real italic_ here.",
			expected: "Check my_test_variable and <i>real italic</i> here.",
		},
		{
			name:     "Inline Code",
			input:    "Use `git commit -m \"fix: bug\"` to commit.",
			expected: "Use <code>git commit -m \"fix: bug\"</code> to commit.",
		},
		{
			name:     "Code block with language",
			input:    "```go\nfunc main() {\n\tfmt.Println(\"<hello> & <world>\")\n}\n```",
			expected: "<pre><code class=\"language-go\">func main() {\n\tfmt.Println(\"&lt;hello&gt; &amp; &lt;world&gt;\")\n}</code></pre>",
		},
		{
			name:     "Code block without language",
			input:    "```\nplain text code & stuff <tag>\n```",
			expected: "<pre>plain text code &amp; stuff &lt;tag&gt;</pre>",
		},
		{
			name:     "Bullet list and numbered list",
			input:    "- First item\n* Second item\n  + Sub-item\n1. Numbered item",
			expected: "• First item\n• Second item\n  • Sub-item\n1. Numbered item",
		},
		{
			name:     "Blockquotes",
			input:    "> First quote line\n> Second quote line\nNormal text",
			expected: "<blockquote>First quote line\nSecond quote line</blockquote>\nNormal text",
		},
		{
			name:     "Horizontal rule",
			input:    "Above\n---\nBelow",
			expected: "Above\n──────────\nBelow",
		},
		{
			name:     "Links",
			input:    "Visit [Google](https://google.com) now.",
			expected: "Visit <a href=\"https://google.com\">Google</a> now.",
		},
		{
			name:     "Strikethrough",
			input:    "This is ~~deleted~~ text.",
			expected: "This is <s>deleted</s> text.",
		},
		{
			name:     "HTML escaping in regular text",
			input:    "Look at x < 5 && y > 10.",
			expected: "Look at x &lt; 5 &amp;&amp; y &gt; 10.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := MarkdownToTelegramHTML(tc.input)
			if actual != tc.expected {
				t.Errorf("expected:\n%q\ngot:\n%q", tc.expected, actual)
			}
		})
	}
}

func TestEnsureTagsClosed(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Balanced tags",
			input:    "<b>bold</b> and <i>italic</i>",
			expected: "<b>bold</b> and <i>italic</i>",
		},
		{
			name:     "Unclosed single tag",
			input:    "<b>bold text",
			expected: "<b>bold text</b>",
		},
		{
			name:     "Unclosed nested tags",
			input:    "<b>bold <i>italic text",
			expected: "<b>bold <i>italic text</i></b>",
		},
		{
			name:     "Unclosed code block in pre",
			input:    "<pre><code class=\"language-go\">some code",
			expected: "<pre><code class=\"language-go\">some code</code></pre>",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := EnsureTagsClosed(tc.input)
			if actual != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, actual)
			}
		})
	}
}

func TestSplitMarkdown(t *testing.T) {
	t.Run("Short text returns single chunk", func(t *testing.T) {
		text := "Hello, world!"
		chunks := SplitMarkdown(text, 100)
		if len(chunks) != 1 || chunks[0] != text {
			t.Fatalf("unexpected chunks: %v", chunks)
		}
	})

	t.Run("Splits lines respecting max length", func(t *testing.T) {
		lines := []string{
			"Line 1: 1234567890",
			"Line 2: 1234567890",
			"Line 3: 1234567890",
		}
		text := strings.Join(lines, "\n")
		// Limit to 25 runes so that lines must be split
		chunks := SplitMarkdown(text, 25)
		if len(chunks) < 2 {
			t.Fatalf("expected multiple chunks, got %d", len(chunks))
		}
		for _, chunk := range chunks {
			if len([]rune(chunk)) > 35 {
				t.Errorf("chunk exceeds limit: %s", chunk)
			}
		}
	})

	t.Run("Splits code blocks cleanly by closing and reopening", func(t *testing.T) {
		text := "```python\ndef step1():\n    print('step1')\ndef step2():\n    print('step2')\n```"
		chunks := SplitMarkdown(text, 40)
		if len(chunks) < 2 {
			t.Fatalf("expected multiple chunks, got %d", len(chunks))
		}
		for i, chunk := range chunks {
			// Each chunk must have balanced code fences
			count := strings.Count(chunk, "```")
			if count%2 != 0 {
				t.Errorf("chunk %d has unclosed code fence:\n%s", i, chunk)
			}
		}
	})
}

func TestFormatAskQuestionParams(t *testing.T) {
	params := map[string]interface{}{
		"questions": []interface{}{
			map[string]interface{}{
				"question":        "Какую ветку использовать?",
				"is_multi_select": false,
				"options": []interface{}{
					"feat/login",
					"main",
				},
			},
		},
	}

	result := FormatAskQuestionParams(params)
	if !strings.Contains(result, "**Какую ветку использовать?**") {
		t.Errorf("expected question title, got: %s", result)
	}
	if !strings.Contains(result, "1. feat/login") || !strings.Contains(result, "2. main") {
		t.Errorf("expected options, got: %s", result)
	}
}
