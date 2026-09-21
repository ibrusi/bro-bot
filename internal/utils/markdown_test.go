package utils

import (
	"bro-bot/internal/i18n"
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

func TestMarkdownToTelegramHTML_Tables(t *testing.T) {
	t.Run("User case with emojis and Cyrillic", func(t *testing.T) {
		input := "| Критерий | language = auto без словаря | language = ru + расширенный словарь |\n" +
			"| :--- | :--- | :--- |\n" +
			"| Слово «дебаг» | ❌ Превращается в «ты, бак» | ✅ Распознаётся идеально |\n" +
			"| Скорость | Медленнее на ~100 мс | Максимальная (нет шага LID) |\n" +
			"| Короткие фразы («да», «ок») | ❌ Риск улететь в другой язык | ✅ 100% стабильность |\n" +
			"| Английские команды (git, docker)| Средне | Отлично (за счёт словаря) |"

		actual := MarkdownToTelegramHTML(input)
		if !strings.HasPrefix(actual, "<pre>") || !strings.HasSuffix(actual, "</pre>") {
			t.Fatalf("expected <pre>...</pre> block, got:\n%s", actual)
		}

		content := strings.TrimPrefix(strings.TrimSuffix(actual, "</pre>"), "<pre>")
		lines := strings.Split(content, "\n")
		if len(lines) != 6 {
			t.Fatalf("expected 6 lines in formatted table, got %d:\n%s", len(lines), content)
		}

		// Проверяем, что в моноширинном отображении все строки имеют строго одинаковую визуальную ширину
		headerWidth := stringVisualWidth(lines[0])
		for i, line := range lines {
			w := stringVisualWidth(line)
			if w != headerWidth {
				t.Errorf("line %d visual width mismatch: got %d, expected %d\nLine: %q", i, w, headerWidth, line)
			}
		}
	})

	t.Run("Alignments (left, center, right)", func(t *testing.T) {
		input := "| Left | Center | Right |\n" +
			"| :--- | :---: | ---: |\n" +
			"| L1 | C1 | R1 |\n" +
			"| LeftLong | Mid | 100 |"

		actual := MarkdownToTelegramHTML(input)
		if !strings.HasPrefix(actual, "<pre>") || !strings.HasSuffix(actual, "</pre>") {
			t.Fatalf("expected <pre> block, got:\n%s", actual)
		}

		content := strings.TrimPrefix(strings.TrimSuffix(actual, "</pre>"), "<pre>")
		lines := strings.Split(content, "\n")
		if len(lines) != 4 {
			t.Fatalf("expected 4 lines, got %d", len(lines))
		}

		// В строке-разделителе должны присутствовать маркеры выравнивания
		delim := lines[1]
		if !strings.Contains(delim, ":") {
			t.Errorf("expected alignment markers in delimiter line: %q", delim)
		}

		// Проверяем одинаковую визуальную ширину всех строк
		expectedWidth := stringVisualWidth(lines[0])
		for i, line := range lines {
			if w := stringVisualWidth(line); w != expectedWidth {
				t.Errorf("line %d visual width %d != %d", i, w, expectedWidth)
			}
		}
	})

	t.Run("HTML escaping inside cells", func(t *testing.T) {
		input := "| Tag | Condition |\n" +
			"| --- | --- |\n" +
			"| <div> | x < 5 && y > 10 |"

		actual := MarkdownToTelegramHTML(input)
		if !strings.Contains(actual, "&lt;div&gt;") {
			t.Errorf("expected &lt;div&gt; in output, got:\n%s", actual)
		}
		if !strings.Contains(actual, "x &lt; 5 &amp;&amp; y &gt; 10") {
			t.Errorf("expected escaped condition in output, got:\n%s", actual)
		}

		// Проверяем, что экранированные символы не сломали визуальное выравнивание
		content := strings.TrimPrefix(strings.TrimSuffix(actual, "</pre>"), "<pre>")
		lines := strings.Split(content, "\n")
		// При unescape (как рендерит клиент Telegram) ширина должна идеально совпадать
		unescapedHeader := strings.ReplaceAll(lines[0], "&lt;", "<")
		unescapedHeader = strings.ReplaceAll(unescapedHeader, "&gt;", ">")
		unescapedHeader = strings.ReplaceAll(unescapedHeader, "&amp;", "&")

		unescapedRow := strings.ReplaceAll(lines[2], "&lt;", "<")
		unescapedRow = strings.ReplaceAll(unescapedRow, "&gt;", ">")
		unescapedRow = strings.ReplaceAll(unescapedRow, "&amp;", "&")

		if stringVisualWidth(unescapedHeader) != stringVisualWidth(unescapedRow) {
			t.Errorf("rendered visual width mismatch: %d != %d", stringVisualWidth(unescapedHeader), stringVisualWidth(unescapedRow))
		}
	})

	t.Run("Table without outer pipes", func(t *testing.T) {
		input := "Name | Age | Role\n" +
			"--- | --- | ---\n" +
			"Alice | 30 | Admin\n" +
			"Bob | 25 | User"

		actual := MarkdownToTelegramHTML(input)
		if !strings.HasPrefix(actual, "<pre>") || !strings.HasSuffix(actual, "</pre>") {
			t.Fatalf("expected <pre>...</pre>, got:\n%s", actual)
		}
		if !strings.Contains(actual, "Alice") || !strings.Contains(actual, "Admin") {
			t.Errorf("expected Alice and Admin in output, got:\n%s", actual)
		}
	})

	t.Run("Uneven rows with missing and extra cells", func(t *testing.T) {
		input := "| A | B | C |\n" +
			"| --- | --- | --- |\n" +
			"| 1 | 2 |\n" +
			"| 1 | 2 | 3 | 4 |"

		actual := MarkdownToTelegramHTML(input)
		if !strings.HasPrefix(actual, "<pre>") || !strings.HasSuffix(actual, "</pre>") {
			t.Fatalf("expected <pre>...</pre>, got:\n%s", actual)
		}
		content := strings.TrimPrefix(strings.TrimSuffix(actual, "</pre>"), "<pre>")
		lines := strings.Split(content, "\n")
		expectedW := stringVisualWidth(lines[0])
		for i, line := range lines {
			if w := stringVisualWidth(line); w != expectedW {
				t.Errorf("line %d visual width %d != %d", i, w, expectedW)
			}
		}
	})

	t.Run("Escaped pipes in cell", func(t *testing.T) {
		input := "| Syntax | Description |\n" +
			"| --- | --- |\n" +
			"| a \\| b | bitwise OR |"

		actual := MarkdownToTelegramHTML(input)
		if !strings.Contains(actual, "a | b") {
			t.Errorf("expected unescaped pipe in cell, got:\n%s", actual)
		}
	})

	t.Run("Single pipe in text is not a table", func(t *testing.T) {
		input := "Choose option A | option B\nNext line is normal text."
		actual := MarkdownToTelegramHTML(input)
		if strings.Contains(actual, "<pre>") {
			t.Errorf("should not create <pre> for regular text with pipe, got:\n%s", actual)
		}
		if !strings.Contains(actual, "Choose option A | option B") {
			t.Errorf("expected original text preserved, got:\n%s", actual)
		}
	})

	t.Run("Table inside code block is preserved verbatim", func(t *testing.T) {
		input := "```markdown\n" +
			"| Col1 | Col2 |\n" +
			"| --- | --- |\n" +
			"| Val1 | Val2 |\n" +
			"```"

		actual := MarkdownToTelegramHTML(input)
		// Не должно быть двойного <pre> или поломки структуры блока кода
		if strings.Count(actual, "<pre>") != 1 {
			t.Errorf("expected exactly 1 <pre> block, got:\n%s", actual)
		}
		if !strings.Contains(actual, "language-markdown") {
			t.Errorf("expected language-markdown code block, got:\n%s", actual)
		}
	})

	t.Run("Table surrounded by Markdown content", func(t *testing.T) {
		input := "# Report\n\n" +
			"Here is the comparison:\n\n" +
			"| Metric | Value |\n" +
			"| --- | --- |\n" +
			"| CPU | 15% |\n\n" +
			"Conclusion: all good."

		actual := MarkdownToTelegramHTML(input)
		if !strings.Contains(actual, "<b>Report</b>") {
			t.Errorf("expected header <b>Report</b>, got:\n%s", actual)
		}
		if !strings.Contains(actual, "<pre>") {
			t.Errorf("expected <pre> table block, got:\n%s", actual)
		}
		if !strings.Contains(actual, "Conclusion: all good.") {
			t.Errorf("expected conclusion text, got:\n%s", actual)
		}
	})

	t.Run("Markdown formatting in cells is cleaned for monospace output", func(t *testing.T) {
		input := "| **Name** | *Role* | `Command` |\n" +
			"| --- | --- | --- |\n" +
			"| **Alice** | *Admin* | `sudo apt update` |\n" +
			"| Bob_dev | Member | `git status` |"

		actual := MarkdownToTelegramHTML(input)
		if strings.Contains(actual, "**") || strings.Contains(actual, "`") {
			t.Errorf("expected stripped markdown markers inside <pre> table, got:\n%s", actual)
		}
		// Имя переменной с подчеркиванием должно остаться нетронутым
		if !strings.Contains(actual, "Bob_dev") {
			t.Errorf("expected Bob_dev preserved, got:\n%s", actual)
		}
	})
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

	result := FormatAskQuestionParams(params, i18n.Default)
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

func TestIsFinalResponseAQuestion(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "Empty text",
			input:    "",
			expected: false,
		},
		{
			name:     "Normal completion text",
			input:    "Задача выполнена успешно. Все тесты пройдены.",
			expected: false,
		},
		{
			name:     "Markdown header ending with question mark",
			input:    "Вот план реализации:\n\n### Нужны ли дополнительные тесты?",
			expected: false,
		},
		{
			name:     "Code block ending with question",
			input:    "Код функции:\n```go\n// is valid?\n```",
			expected: false,
		},
		{
			name:     "Explicit question to user",
			input:    "Я проанализировал задачу. Какой подход к базе данных вы предпочитаете?",
			expected: true,
		},
		{
			name:     "Confirmation request",
			input:    "Перед началом изменений подтвердите выбор библиотеки.",
			expected: true,
		},
		{
			name:     "Multi-line response ending with question",
			input:    "Шаг 1 выполнен.\nШаг 2 выполнен.\n\nПродолжить реализацию следующего этапа?",
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsFinalResponseAQuestion(tc.input)
			if got != tc.expected {
				t.Errorf("IsFinalResponseAQuestion(%q) = %v; want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestExtractQuestionFromResponse(t *testing.T) {
	short := "Какой цвет выбрать?"
	if got := ExtractQuestionFromResponse(short); got != short {
		t.Errorf("expected %q, got %q", short, got)
	}

	long := strings.Repeat("Анализ проекта и кодовой базы.\n\n", 30) + "Итоговый вопрос: какой фреймворк подключить?"
	got := ExtractQuestionFromResponse(long)
	if !strings.Contains(got, "Итоговый вопрос: какой фреймворк подключить?") {
		t.Errorf("expected got to contain the final paragraph, got %q", got)
	}
	if strings.HasPrefix(got, "Анализ проекта") {
		t.Errorf("expected early paragraphs to be excluded for long text, got %q", got)
	}
}

func TestSanitizePlanText(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Empty input",
			input:    "",
			expected: "",
		},
		{
			name: "Normal clean plan untouched",
			input: `# План реализации
1. Создать модели
2. Написать тесты`,
			expected: `# План реализации
1. Создать модели
2. Написать тесты`,
		},
		{
			name: "Plan with leading XML request tag and prompt echo",
			input: `<USER_REQUEST>
Задача пользователя: Добавить фичу X

ВНИМАНИЕ: Сейчас выполняется ЭТАП ПЛАНИРОВАНИЯ.
НЕ создавай git-ветку, НЕ модифицируй файлы проекта, НЕ делай git commit, НЕ делай git push и НЕ открывай PR.
Твоя цель сейчас:
1. Тщательно исследуй кодовую базу и архитектуру проекта.
2. Сформируй чёткий, пошаговый и структурированный план реализации задачи.
3. Опиши:
   - Какие файлы и компоненты будут созданы или изменены.
   - Ключевые архитектурные решения и интерфейсы.
   - План тестирования и проверки работоспособности.
   - Возможные риски, краевые случаи и пути их решения.
4. Выведи итоговый план в понятном и структурированном виде для пользователя.
</USER_REQUEST>
<ADDITIONAL_METADATA>
The current local time is: 2026-09-21T09:22:31Z.
</ADDITIONAL_METADATA>

---

# Архитектурный план: Фича X

## Шаг 1. Реализация`,
			expected: `# Архитектурный план: Фича X

## Шаг 1. Реализация`,
		},
		{
			name: "English prompt echo stripped",
			input: `User task: Fix issue Y

ATTENTION: the PLANNING STAGE is in progress right now.
Do NOT create a git branch, do NOT modify project files, do NOT run git commit, do NOT run git push and do NOT open a PR.
Your goal right now:
1. Thoroughly explore the codebase.
2. Output the resulting plan in a clear form.

---

### Step 1: Fix bug in handlers`,
			expected: `### Step 1: Fix bug in handlers`,
		},
		{
			name: "Input consisting ONLY of planning prompt echo returns empty",
			input: `ВНИМАНИЕ: Сейчас выполняется ЭТАП ПЛАНИРОВАНИЯ.
НЕ создавай git-ветку, НЕ модифицируй файлы проекта, НЕ делай git commit, НЕ делай git push и НЕ открывай PR.
Твоя цель сейчас:
1. Тщательно исследуй кодовую базу и архитектуру проекта.
2. Сформируй чёткий, пошаговый план.`,
			expected: "",
		},
		{
			name: "Plan with disclaimer note at end is preserved",
			input: `# План
1. Сделать А

> [!NOTE]
> В соответствии с инструкцией этапа планирования файлы проекта не изменялись.`,
			expected: `# План
1. Сделать А

> [!NOTE]
> В соответствии с инструкцией этапа планирования файлы проекта не изменялись.`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizePlanText(tc.input)
			if got != tc.expected {
				t.Errorf("SanitizePlanText() =\n%q\nwant:\n%q", got, tc.expected)
			}
		})
	}
}

