package utils

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

func TestExtractAskQuestionOptions(t *testing.T) {
	// Nested in questions array
	params := map[string]interface{}{
		"questions": []interface{}{
			map[string]interface{}{
				"question": "Выберите действие:",
				"options": []interface{}{
					"Вариант A",
					"Вариант B",
				},
			},
		},
	}
	opts := ExtractAskQuestionOptions(params)
	if len(opts) != 2 || opts[0] != "Вариант A" || opts[1] != "Вариант B" {
		t.Errorf("unexpected options: %v", opts)
	}

	// Direct options
	paramsDirect := map[string]interface{}{
		"options": []string{"Option 1", "Option 2"},
	}
	optsDirect := ExtractAskQuestionOptions(paramsDirect)
	if len(optsDirect) != 2 || optsDirect[0] != "Option 1" || optsDirect[1] != "Option 2" {
		t.Errorf("unexpected options: %v", optsDirect)
	}

	// Nil / empty
	if ExtractAskQuestionOptions(nil) != nil {
		t.Errorf("expected nil for nil params")
	}
}

func TestExtractPlanVariantOptions(t *testing.T) {
	planText := `
### Анализ вариантов

Вариант 1: Использовать встроенную библиотеку
Здесь описание первого варианта...

Вариант 2: Подключить сторонний SDK
Здесь описание второго варианта...
`
	variants := ExtractPlanVariantOptions(planText)
	if len(variants) != 2 {
		t.Fatalf("expected 2 variants, got %d: %v", len(variants), variants)
	}
	if !strings.HasPrefix(variants[0], "Вариант 1") {
		t.Errorf("expected variant 1, got %s", variants[0])
	}
	if !strings.HasPrefix(variants[1], "Вариант 2") {
		t.Errorf("expected variant 2, got %s", variants[1])
	}

	// Single occurrence should not trigger variants
	singleText := "Мы выбрали вариант 1 для архитектуры."
	if ExtractPlanVariantOptions(singleText) != nil {
		t.Errorf("expected nil for text without multiple variants")
	}
}

func TestExtractPlanSummary(t *testing.T) {
	t.Run("Short plan is preserved", func(t *testing.T) {
		short := "# План\n1. Шаг один\n2. Шаг два"
		summary := ExtractPlanSummary(short, 100)
		if summary != short {
			t.Errorf("expected %q, got %q", short, summary)
		}
	})

	t.Run("Long plan cut at paragraph boundary", func(t *testing.T) {
		p1 := "Параграф один с описанием архитектуры."
		p2 := "Параграф два с описанием компонентов."
		p3 := "Параграф три с описанием тестов."
		full := p1 + "\n\n" + p2 + "\n\n" + p3

		// Limit to cut between p2 and p3
		maxRunes := len([]rune(p1)) + len([]rune(p2)) + 5
		summary := ExtractPlanSummary(full, maxRunes)
		if strings.Contains(summary, p3) {
			t.Errorf("expected summary to not contain p3, got: %s", summary)
		}
		if !strings.Contains(summary, p1) || !strings.Contains(summary, p2) {
			t.Errorf("expected summary to contain p1 and p2, got: %s", summary)
		}
	})

	t.Run("Unclosed code block is closed", func(t *testing.T) {
		text := "Начало плана\n\n```go\nfunc step1() {\n    do()\n}\nfunc step2() {\n    do()\n}"
		summary := ExtractPlanSummary(text, 35)
		if strings.Count(summary, "```")%2 != 0 {
			t.Errorf("expected balanced code fence in summary, got: %s", summary)
		}
	})
}
