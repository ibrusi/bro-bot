package utils

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	headerRegex     = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	bulletRegex     = regexp.MustCompile(`^(\s*)([-*+])\s+(.*)$`)
	hrRegex         = regexp.MustCompile(`^(?:---|\*\*\*|___)\s*$`)
	blockquoteRegex = regexp.MustCompile(`^>\s?(.*)$`)
	linkRegex       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	boldItalicRegex = regexp.MustCompile(`\*\*\*([^*\n]+)\*\*\*|___([^_\n]+)___`)
	boldRegex       = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	italicStarRegex = regexp.MustCompile(`\*([^*\n]+)\*`)
	italicUndRegex  = regexp.MustCompile(`(^|[\s\p{P}])_([^_\n]+)_([\s\p{P}]|$)`)
	strikeRegex     = regexp.MustCompile(`~~([^~\n]+)~~|~([^~\n]+)~`)
	tagRegex        = regexp.MustCompile(`(?i)</?([a-z0-9_-]+)(?:\s+[^>]*)?>`)
)

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// MarkdownToTelegramHTML преобразует стандартный Markdown в HTML, поддерживаемый Telegram Bot API.
func MarkdownToTelegramHTML(md string) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}

	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")

	// 1. Извлечение многострочных блоков кода ```...```
	var codeBlocks []string
	lines := strings.Split(md, "\n")
	var processedLines []string
	var inCodeBlock bool
	var curCodeLang string
	var curCodeLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				curCodeLang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				curCodeLines = nil
				continue
			} else {
				inCodeBlock = false
				codeContent := strings.Join(curCodeLines, "\n")
				escapedCode := escapeHTML(codeContent)

				var htmlBlock string
				if curCodeLang != "" {
					htmlBlock = fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>", escapeHTML(curCodeLang), escapedCode)
				} else {
					htmlBlock = fmt.Sprintf("<pre>%s</pre>", escapedCode)
				}

				placeholder := fmt.Sprintf("\x00CB_%d\x00", len(codeBlocks))
				codeBlocks = append(codeBlocks, htmlBlock)
				processedLines = append(processedLines, placeholder)
				continue
			}
		}

		if inCodeBlock {
			curCodeLines = append(curCodeLines, line)
		} else {
			processedLines = append(processedLines, line)
		}
	}

	// Если блок кода не был закрыт в конце текста
	if inCodeBlock {
		codeContent := strings.Join(curCodeLines, "\n")
		escapedCode := escapeHTML(codeContent)
		var htmlBlock string
		if curCodeLang != "" {
			htmlBlock = fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>", escapeHTML(curCodeLang), escapedCode)
		} else {
			htmlBlock = fmt.Sprintf("<pre>%s</pre>", escapedCode)
		}
		placeholder := fmt.Sprintf("\x00CB_%d\x00", len(codeBlocks))
		codeBlocks = append(codeBlocks, htmlBlock)
		processedLines = append(processedLines, placeholder)
	}

	text := strings.Join(processedLines, "\n")

	// 2. Извлечение инлайн-кода `...`
	var inlineCodes []string
	var textBuf strings.Builder
	idx := 0
	for idx < len(text) {
		if text[idx] == '`' {
			endIdx := strings.IndexByte(text[idx+1:], '`')
			if endIdx != -1 {
				realEnd := idx + 1 + endIdx
				codeContent := text[idx+1 : realEnd]
				if !strings.Contains(codeContent, "\n") {
					escapedCode := escapeHTML(codeContent)
					htmlSnippet := fmt.Sprintf("<code>%s</code>", escapedCode)
					placeholder := fmt.Sprintf("\x00IC_%d\x00", len(inlineCodes))
					inlineCodes = append(inlineCodes, htmlSnippet)
					textBuf.WriteString(placeholder)
					idx = realEnd + 1
					continue
				}
			}
		}
		textBuf.WriteByte(text[idx])
		idx++
	}
	text = textBuf.String()

	// 3. Построчная обработка блочных элементов (заголовки, цитаты, списки, разделители)
	lines = strings.Split(text, "\n")
	var resultLines []string
	var inBlockquote bool
	var bqLines []string

	flushBlockquote := func() {
		if len(bqLines) > 0 {
			content := strings.Join(bqLines, "\n")
			resultLines = append(resultLines, fmt.Sprintf("<blockquote>%s</blockquote>", content))
			bqLines = nil
		}
		inBlockquote = false
	}

	for _, line := range lines {
		// Цитаты: > цитата
		if bqMatch := blockquoteRegex.FindStringSubmatch(line); len(bqMatch) > 1 {
			inBlockquote = true
			escapedQuote := escapeHTML(bqMatch[1])
			bqLines = append(bqLines, escapedQuote)
			continue
		} else if inBlockquote {
			flushBlockquote()
		}

		// Горизонтальный разделитель: ---, ***, ___
		if hrRegex.MatchString(line) {
			resultLines = append(resultLines, "──────────")
			continue
		}

		// Заголовки: # Заголовок
		if hMatch := headerRegex.FindStringSubmatch(line); len(hMatch) > 2 {
			escapedTitle := escapeHTML(strings.TrimSpace(hMatch[2]))
			resultLines = append(resultLines, fmt.Sprintf("<b>%s</b>", escapedTitle))
			continue
		}

		// Маркированные списки: - пункт, * пункт
		if bMatch := bulletRegex.FindStringSubmatch(line); len(bMatch) > 3 {
			indent := bMatch[1]
			escapedItem := escapeHTML(bMatch[3])
			resultLines = append(resultLines, fmt.Sprintf("%s• %s", indent, escapedItem))
			continue
		}

		// Обычная строка текста
		resultLines = append(resultLines, escapeHTML(line))
	}
	flushBlockquote()

	text = strings.Join(resultLines, "\n")

	// 4. Инлайн-форматирование: ссылки, жирный, курсив, зачёркнутый
	// Ссылки: [текст](url)
	text = linkRegex.ReplaceAllStringFunc(text, func(m string) string {
		sub := linkRegex.FindStringSubmatch(m)
		if len(sub) > 2 {
			title := sub[1]
			url := strings.TrimSpace(sub[2])
			// Базовая валидация URL
			if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "tg://") {
				return fmt.Sprintf("<a href=\"%s\">%s</a>", url, title)
			}
		}
		return m
	})

	// Жирный + курсив: ***текст*** или ___текст___
	text = boldItalicRegex.ReplaceAllStringFunc(text, func(m string) string {
		sub := boldItalicRegex.FindStringSubmatch(m)
		content := sub[1]
		if content == "" {
			content = sub[2]
		}
		return fmt.Sprintf("<b><i>%s</i></b>", content)
	})

	// Жирный: **текст** или __текст__
	text = boldRegex.ReplaceAllStringFunc(text, func(m string) string {
		sub := boldRegex.FindStringSubmatch(m)
		content := sub[1]
		if content == "" {
			content = sub[2]
		}
		return fmt.Sprintf("<b>%s</b>", content)
	})

	// Зачёркнутый: ~~текст~~ или ~текст~
	text = strikeRegex.ReplaceAllStringFunc(text, func(m string) string {
		sub := strikeRegex.FindStringSubmatch(m)
		content := sub[1]
		if content == "" {
			content = sub[2]
		}
		return fmt.Sprintf("<s>%s</s>", content)
	})

	// Курсив: *текст*
	text = italicStarRegex.ReplaceAllStringFunc(text, func(m string) string {
		sub := italicStarRegex.FindStringSubmatch(m)
		return fmt.Sprintf("<i>%s</i>", sub[1])
	})

	// Курсив через подчеркивание: _текст_ (с проверкой границ слов, чтобы не ломать variable_name)
	text = italicUndRegex.ReplaceAllStringFunc(text, func(m string) string {
		sub := italicUndRegex.FindStringSubmatch(m)
		prefix := sub[1]
		content := sub[2]
		suffix := sub[3]
		return fmt.Sprintf("%s<i>%s</i>%s", prefix, content, suffix)
	})

	// 5. Восстановление инлайн-кода
	for i, ic := range inlineCodes {
		placeholder := fmt.Sprintf("\x00IC_%d\x00", i)
		text = strings.ReplaceAll(text, placeholder, ic)
	}

	// 6. Восстановление блоков кода
	for i, cb := range codeBlocks {
		placeholder := fmt.Sprintf("\x00CB_%d\x00", i)
		text = strings.ReplaceAll(text, placeholder, cb)
	}

	// 7. Проверка баланса тегов для исключения ошибок парсера Telegram
	text = EnsureTagsClosed(text)

	return text
}

// EnsureTagsClosed проверяет корректность закрытия разрешённых HTML-тегов в Telegram.
func EnsureTagsClosed(htmlStr string) string {
	allowedTags := map[string]bool{
		"b":          true,
		"i":          true,
		"s":          true,
		"u":          true,
		"code":       true,
		"pre":        true,
		"blockquote": true,
		"a":          true,
	}

	var tagStack []string
	matches := tagRegex.FindAllStringSubmatchIndex(htmlStr, -1)
	if len(matches) == 0 {
		return htmlStr
	}

	for _, idxs := range matches {
		fullMatch := htmlStr[idxs[0]:idxs[1]]
		tagName := strings.ToLower(htmlStr[idxs[2]:idxs[3]])

		if !allowedTags[tagName] {
			continue
		}

		isClosing := strings.HasPrefix(fullMatch, "</")
		if isClosing {
			if len(tagStack) > 0 && tagStack[len(tagStack)-1] == tagName {
				tagStack = tagStack[:len(tagStack)-1]
			} else {
				// Ищем последнее совпадение в стеке
				foundIdx := -1
				for i := len(tagStack) - 1; i >= 0; i-- {
					if tagStack[i] == tagName {
						foundIdx = i
						break
					}
				}
				if foundIdx != -1 {
					tagStack = tagStack[:foundIdx]
				}
			}
		} else {
			tagStack = append(tagStack, tagName)
		}
	}

	// Закрываем оставшиеся незакрытыми теги в обратном порядке
	if len(tagStack) > 0 {
		var bldr strings.Builder
		bldr.WriteString(htmlStr)
		for i := len(tagStack) - 1; i >= 0; i-- {
			bldr.WriteString(fmt.Sprintf("</%s>", tagStack[i]))
		}
		return bldr.String()
	}

	return htmlStr
}

// SplitMarkdown разбивает длинный Markdown-текст на части без разрыва блоков кода.
func SplitMarkdown(text string, maxChunkLen int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxChunkLen <= 0 {
		maxChunkLen = 3500
	}

	runes := []rune(text)
	if len(runes) <= maxChunkLen {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var curChunk strings.Builder
	var inCodeBlock bool
	var codeBlockFence string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		isFence := strings.HasPrefix(trimmed, "```")

		// Если текущая строка превышает maxChunkLen сама по себе (очень длинная строка)
		if len([]rune(line)) > maxChunkLen {
			if curChunk.Len() > 0 {
				if inCodeBlock {
					curChunk.WriteString("\n```")
				}
				chunks = append(chunks, curChunk.String())
				curChunk.Reset()
				if inCodeBlock {
					curChunk.WriteString(codeBlockFence + "\n")
				}
			}

			lineRunes := []rune(line)
			for len(lineRunes) > maxChunkLen {
				sub := string(lineRunes[:maxChunkLen])
				lineRunes = lineRunes[maxChunkLen:]
				if inCodeBlock {
					chunks = append(chunks, sub+"\n```")
				} else {
					chunks = append(chunks, sub)
				}
				if inCodeBlock {
					curChunk.WriteString(codeBlockFence + "\n")
				}
			}
			if len(lineRunes) > 0 {
				curChunk.WriteString(string(lineRunes))
			}
			continue
		}

		// Проверяем, поместится ли строка в текущий чанк
		projectedLen := curChunk.Len() + len(line) + 1
		if inCodeBlock {
			projectedLen += 4 // резерв для закрывающего ```
		}

		if projectedLen > maxChunkLen && curChunk.Len() > 0 {
			if inCodeBlock {
				curChunk.WriteString("\n```")
			}
			chunks = append(chunks, curChunk.String())
			curChunk.Reset()
			if inCodeBlock {
				curChunk.WriteString(codeBlockFence + "\n")
			}
		}

		if curChunk.Len() > 0 {
			curChunk.WriteByte('\n')
		}
		curChunk.WriteString(line)

		if isFence {
			if !inCodeBlock {
				inCodeBlock = true
				codeBlockFence = trimmed
			} else {
				inCodeBlock = false
				codeBlockFence = ""
			}
		}
	}

	if curChunk.Len() > 0 {
		chunks = append(chunks, curChunk.String())
	}

	return chunks
}

// FormatAskQuestionParams форматирует параметры инструмента ask_question в читаемый Markdown.
func FormatAskQuestionParams(params map[string]interface{}) string {
	if params == nil {
		return ""
	}

	if qs, ok := params["questions"].([]interface{}); ok && len(qs) > 0 {
		var bldr strings.Builder
		for i, qItem := range qs {
			qMap, ok := qItem.(map[string]interface{})
			if !ok {
				continue
			}

			qText, _ := qMap["question"].(string)
			if qText == "" {
				continue
			}

			if i > 0 {
				bldr.WriteString("\n\n")
			}
			bldr.WriteString(fmt.Sprintf("**%s**", qText))

			if isMulti, _ := qMap["is_multi_select"].(bool); isMulti {
				bldr.WriteString(" *(можно выбрать несколько)*")
			}

			if opts, ok := qMap["options"].([]interface{}); ok && len(opts) > 0 {
				bldr.WriteString("\n")
				for j, opt := range opts {
					optStr := fmt.Sprintf("%v", opt)
					bldr.WriteString(fmt.Sprintf("\n%d. %s", j+1, optStr))
				}
			}
		}
		if bldr.Len() > 0 {
			return bldr.String()
		}
	}

	// Фолбэк на строковое представление параметров
	if q, ok := params["question"].(string); ok && q != "" {
		return q
	}

	var parts []string
	for k, v := range params {
		parts = append(parts, fmt.Sprintf("**%s**: %v", k, v))
	}
	return strings.Join(parts, "\n")
}

// ExtractAskQuestionOptions безопасно извлекает список вариантов ответа из параметров ask_question.
func ExtractAskQuestionOptions(params map[string]interface{}) []string {
	if params == nil {
		return nil
	}

	var result []string

	// 1. Проверяем params["questions"]
	if qs, ok := params["questions"].([]interface{}); ok && len(qs) > 0 {
		for _, qItem := range qs {
			qMap, ok := qItem.(map[string]interface{})
			if !ok {
				continue
			}
			if opts, ok := qMap["options"].([]interface{}); ok {
				for _, opt := range opts {
					s := strings.TrimSpace(fmt.Sprintf("%v", opt))
					if s != "" {
						result = append(result, s)
					}
				}
			} else if opts, ok := qMap["options"].([]string); ok {
				for _, opt := range opts {
					s := strings.TrimSpace(opt)
					if s != "" {
						result = append(result, s)
					}
				}
			}
		}
	}

	// 2. Фолбэк на прямой params["options"]
	if len(result) == 0 {
		if opts, ok := params["options"].([]interface{}); ok {
			for _, opt := range opts {
				s := strings.TrimSpace(fmt.Sprintf("%v", opt))
				if s != "" {
					result = append(result, s)
				}
			}
		} else if opts, ok := params["options"].([]string); ok {
			for _, opt := range opts {
				s := strings.TrimSpace(opt)
				if s != "" {
					result = append(result, s)
				}
			}
		}
	}

	return result
}

var planVariantRegex = regexp.MustCompile(`(?i)(?:^|\n)\s*(?:#{1,6}\s+|(?:\d+\.|\*|-)\s+)?((?:Вариант|Option|Альтернатива)\s*(?:\d+|[A-Za-zА-Яа-я])\b(?:[^\n]*))`)

// ExtractPlanVariantOptions ищет в тексте плана предложенные альтернативные варианты реализации.
func ExtractPlanVariantOptions(planText string) []string {
	if strings.TrimSpace(planText) == "" {
		return nil
	}

	matches := planVariantRegex.FindAllStringSubmatch(planText, -1)
	if len(matches) < 2 {
		return nil
	}

	var variants []string
	seen := make(map[string]bool)

	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		raw := strings.TrimSpace(m[1])
		raw = strings.Trim(raw, "*_#`~:- ")
		raw = strings.Join(strings.Fields(raw), " ")
		if len([]rune(raw)) > 40 {
			runes := []rune(raw)
			raw = string(runes[:37]) + "..."
		}
		lower := strings.ToLower(raw)
		if raw != "" && !seen[lower] {
			seen[lower] = true
			variants = append(variants, raw)
			if len(variants) >= 5 {
				break
			}
		}
	}

	if len(variants) < 2 {
		return nil
	}
	return variants
}

// ExtractPlanSummary формирует краткое резюме плана для отправки в Telegram-сообщении,
// если полный текст плана слишком велик и прикрепляется файлом.
func ExtractPlanSummary(planText string, maxRunes int) string {
	planText = strings.TrimSpace(planText)
	if planText == "" {
		return ""
	}
	if maxRunes <= 0 {
		maxRunes = 2000
	}

	runes := []rune(planText)
	if len(runes) <= maxRunes {
		return planText
	}

	subRunes := runes[:maxRunes]
	subText := string(subRunes)

	// Ищем логическую границу (конец абзаца, заголовка или списка) в диапазоне от 40% до 100% от maxRunes
	cutIdx := -1
	minBound := len(subText) * 4 / 10

	if idx := strings.LastIndex(subText, "\n\n"); idx > minBound {
		cutIdx = idx
	} else if idx := strings.LastIndex(subText, "\n#"); idx > minBound {
		cutIdx = idx
	} else if idx := strings.LastIndex(subText, "\n"); idx > minBound {
		cutIdx = idx
	} else if idx := strings.LastIndex(subText, " "); idx > minBound {
		cutIdx = idx
	}

	summary := subText
	if cutIdx > 0 {
		summary = strings.TrimSpace(subText[:cutIdx])
	}

	// Закрываем незакрытые блоки кода (```) в резюме, если они были разорваны срезом
	if strings.Count(summary, "```")%2 != 0 {
		summary += "\n```"
	}

	return summary
}

// IsFinalResponseAQuestion определяет, завершился ли финальный ответ агента вопросом пользователю.
// Проверяются только последние строки завершённого ответа, исключая списки задач, блоки кода и заголовки.
func IsFinalResponseAQuestion(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}

	lines := strings.Split(text, "\n")
	var lastLine string
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l != "" {
			lastLine = l
			break
		}
	}

	if lastLine == "" {
		return false
	}

	// Исключаем блоки кода и markdown-заголовки
	if strings.HasPrefix(lastLine, "```") || strings.HasPrefix(lastLine, "#") {
		return false
	}

	lower := strings.ToLower(lastLine)
	if strings.HasSuffix(lastLine, "?") {
		return true
	}

	if strings.Contains(lower, "какой вариант") ||
		strings.Contains(lower, "как поступить") ||
		strings.Contains(lower, "подтвердите выбор") ||
		strings.Contains(lower, "подтвердите, как") ||
		strings.Contains(lower, "выберите вариант") ||
		strings.Contains(lower, "do you want to") ||
		strings.Contains(lower, "what would you like") ||
		strings.Contains(lower, "which option") {
		return true
	}

	return false
}

// ExtractQuestionFromResponse извлекает завершающий блок вопроса из ответа агента.
func ExtractQuestionFromResponse(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	if len([]rune(text)) <= 500 {
		return text
	}

	paragraphs := strings.Split(text, "\n\n")
	for i := len(paragraphs) - 1; i >= 0; i-- {
		p := strings.TrimSpace(paragraphs[i])
		if p != "" {
			return p
		}
	}

	return text
}
