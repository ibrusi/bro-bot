package utils

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"bro-bot/internal/i18n"
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

// tableColumnAlign задаёт выравнивание содержимого ячеек в колонке таблицы.
type tableColumnAlign int

const (
	alignLeft tableColumnAlign = iota
	alignCenter
	alignRight
)

// runeVisualWidth возвращает визуальную ширину символа в моноширинном шрифте (0, 1 или 2).
func runeVisualWidth(r rune) int {
	if r < 32 || (r >= 0x7f && r < 0xa0) {
		return 0
	}
	if r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff || (r >= 0xfe00 && r <= 0xfe0f) {
		return 0
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r) {
		return 0
	}
	if (r >= 0x1F300 && r <= 0x1FAFF) || // Emojis, Pictographs
		(r >= 0x2600 && r <= 0x27BF) ||   // Dingbats (❌ 0x274c, ✅ 0x2705)
		(r >= 0x2B50 && r <= 0x2B55) ||   // Звёзды и символы
		(r >= 0x1F1E6 && r <= 0x1F1FF) || // Флаги
		(r >= 0x2300 && r <= 0x23FF) ||   // Часы, таймеры
		(r >= 0x2E80 && r <= 0x9FFF) ||   // CJK Ideographs
		(r >= 0xF900 && r <= 0xFAFF) ||   // CJK Compatibility
		(r >= 0xFF01 && r <= 0xFF60) ||   // Полноширинные формы
		(r >= 0xFFE0 && r <= 0xFFE6) {
		return 2
	}
	return 1
}

// stringVisualWidth возвращает экранную ширину строки в моноширинном шрифте с учётом широких символов и эмодзи.
func stringVisualWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeVisualWidth(r)
	}
	return w
}

// cleanTableCell очищает ячейку от избыточного Markdown-форматирования для моноширинного вывода.
func cleanTableCell(cell string) string {
	s := strings.TrimSpace(cell)
	if s == "" {
		return ""
	}

	// Снятие инлайн-кода: `code` -> code внутри ячейки
	if strings.Contains(s, "`") {
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			if s[i] == '`' {
				end := strings.IndexByte(s[i+1:], '`')
				if end != -1 {
					b.WriteString(s[i+1 : i+1+end])
					i += 1 + end
					continue
				}
			}
			b.WriteByte(s[i])
		}
		s = b.String()
	}

	// Снятие жирного начертания: **text** -> text или __text__ -> text
	s = boldRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := boldRegex.FindStringSubmatch(m)
		if len(sub) > 1 && sub[1] != "" {
			return sub[1]
		}
		if len(sub) > 2 && sub[2] != "" {
			return sub[2]
		}
		return m
	})

	// Снятие курсива: *text* -> text
	s = italicStarRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := italicStarRegex.FindStringSubmatch(m)
		if len(sub) > 1 {
			return sub[1]
		}
		return m
	})

	// Снятие зачёркивания: ~~text~~ -> text
	s = strikeRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := strikeRegex.FindStringSubmatch(m)
		if len(sub) > 1 && sub[1] != "" {
			return sub[1]
		}
		if len(sub) > 2 && sub[2] != "" {
			return sub[2]
		}
		return m
	})

	return strings.TrimSpace(s)
}

// splitTableRowRaw разбивает строку таблицы на ячейки без очистки форматирования.
func splitTableRowRaw(line string) []string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "|") {
		line = line[1:]
	}
	if strings.HasSuffix(line, "|") {
		line = line[:len(line)-1]
	}

	var cells []string
	var cur strings.Builder
	escaped := false

	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			cur.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(ch)
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

// splitTableRow разбивает строку таблицы на ячейки с очисткой форматирования.
func splitTableRow(line string) []string {
	raw := splitTableRowRaw(line)
	cells := make([]string, len(raw))
	for i, r := range raw {
		cells[i] = cleanTableCell(r)
	}
	return cells
}

// isDelimiterCell проверяет, является ли ячейка разделителем GFM (например, ---, :---, :---:).
func isDelimiterCell(c string) bool {
	c = strings.TrimSpace(c)
	if len(c) == 0 {
		return false
	}
	hasHyphen := false
	for _, r := range c {
		if r == '-' {
			hasHyphen = true
		} else if r != ':' && !unicode.IsSpace(r) {
			return false
		}
	}
	return hasHyphen
}

// isValidTableStart проверяет, образуют ли две последовательные строки корректный заголовок и разделитель таблицы.
func isValidTableStart(headerLine, delimLine string) bool {
	hCells := splitTableRowRaw(headerLine)
	dCells := splitTableRowRaw(delimLine)
	if len(hCells) == 0 || len(dCells) == 0 {
		return false
	}
	if len(hCells) != len(dCells) {
		return false
	}
	if len(hCells) == 1 && !strings.HasPrefix(strings.TrimSpace(headerLine), "|") {
		return false
	}
	for _, c := range dCells {
		if !isDelimiterCell(c) {
			return false
		}
	}
	return true
}

// parseColumnAlign определяет выравнивание по содержимому ячейки строки-разделителя.
func parseColumnAlign(c string) tableColumnAlign {
	c = strings.TrimSpace(c)
	if strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":") {
		return alignCenter
	}
	if strings.HasSuffix(c, ":") {
		return alignRight
	}
	return alignLeft
}

// padTableCell выравнивает ячейку пробелами с учётом визуальной ширины и режима выравнивания.
func padTableCell(content string, targetWidth int, align tableColumnAlign) string {
	curWidth := stringVisualWidth(content)
	if curWidth >= targetWidth {
		return content
	}
	diff := targetWidth - curWidth
	switch align {
	case alignRight:
		return strings.Repeat(" ", diff) + content
	case alignCenter:
		left := diff / 2
		right := diff - left
		return strings.Repeat(" ", left) + content + strings.Repeat(" ", right)
	default: // alignLeft
		return content + strings.Repeat(" ", diff)
	}
}

// formatMarkdownTable форматирует Markdown-таблицу в аккуратный моноширинный блок <pre>.
func formatMarkdownTable(headerLine, delimLine string, dataLines []string) string {
	headers := splitTableRow(headerLine)
	delims := splitTableRowRaw(delimLine)

	numCols := len(headers)
	if len(delims) > numCols {
		numCols = len(delims)
	}

	var rows [][]string
	for _, dLine := range dataLines {
		dCells := splitTableRow(dLine)
		if len(dCells) > numCols {
			numCols = len(dCells)
		}
		rows = append(rows, dCells)
	}

	aligns := make([]tableColumnAlign, numCols)
	for i := 0; i < numCols; i++ {
		if i < len(delims) {
			aligns[i] = parseColumnAlign(delims[i])
		} else {
			aligns[i] = alignLeft
		}
	}

	colWidths := make([]int, numCols)
	for i := 0; i < numCols; i++ {
		colWidths[i] = 3 // минимальная ширина под "---"
		if i < len(headers) {
			if w := stringVisualWidth(headers[i]); w > colWidths[i] {
				colWidths[i] = w
			}
		}
		for _, row := range rows {
			if i < len(row) {
				if w := stringVisualWidth(row[i]); w > colWidths[i] {
					colWidths[i] = w
				}
			}
		}
	}

	var b strings.Builder

	// Строка заголовка
	b.WriteString("|")
	for i := 0; i < numCols; i++ {
		val := ""
		if i < len(headers) {
			val = headers[i]
		}
		b.WriteString(" " + padTableCell(val, colWidths[i], aligns[i]) + " |")
	}
	b.WriteString("\n")

	// Строка-разделитель
	b.WriteString("|")
	for i := 0; i < numCols; i++ {
		w := colWidths[i]
		switch aligns[i] {
		case alignCenter:
			if w >= 2 {
				b.WriteString(" :" + strings.Repeat("-", w-2) + ": |")
			} else {
				b.WriteString(" " + strings.Repeat("-", w) + " |")
			}
		case alignRight:
			if w >= 1 {
				b.WriteString(" " + strings.Repeat("-", w-1) + ": |")
			} else {
				b.WriteString(" " + strings.Repeat("-", w) + " |")
			}
		default: // alignLeft
			b.WriteString(" " + strings.Repeat("-", w) + " |")
		}
	}

	// Строки данных
	for _, row := range rows {
		b.WriteString("\n|")
		for i := 0; i < numCols; i++ {
			val := ""
			if i < len(row) {
				val = row[i]
			}
			b.WriteString(" " + padTableCell(val, colWidths[i], aligns[i]) + " |")
		}
	}

	return fmt.Sprintf("<pre>%s</pre>", escapeHTML(b.String()))
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

	// 1.5. Извлечение и форматирование Markdown-таблиц
	var tableBlocks []string
	var linesWithoutTables []string
	lineIdx := 0
	for lineIdx < len(processedLines) {
		line := processedLines[lineIdx]

		// Проверяем, может ли текущая строка быть началом таблицы (заголовок + строка разделителя)
		if strings.Contains(line, "|") && lineIdx+1 < len(processedLines) && isValidTableStart(line, processedLines[lineIdx+1]) {
			headerLine := line
			delimLine := processedLines[lineIdx+1]
			lineIdx += 2

			var dataLines []string
			for lineIdx < len(processedLines) {
				cur := processedLines[lineIdx]
				curTrimmed := strings.TrimSpace(cur)
				// Пустая строка, другой блочный плейсхолдер или разделитель hr завершают таблицу
				if curTrimmed == "" || !strings.Contains(cur, "|") || strings.HasPrefix(curTrimmed, "\x00CB_") || hrRegex.MatchString(curTrimmed) {
					break
				}
				dataLines = append(dataLines, cur)
				lineIdx++
			}

			htmlTable := formatMarkdownTable(headerLine, delimLine, dataLines)
			placeholder := fmt.Sprintf("\x00TB_%d\x00", len(tableBlocks))
			tableBlocks = append(tableBlocks, htmlTable)
			linesWithoutTables = append(linesWithoutTables, placeholder)
			continue
		}

		linesWithoutTables = append(linesWithoutTables, line)
		lineIdx++
	}
	processedLines = linesWithoutTables

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

	// 6.5. Восстановление блоков таблиц
	for i, tb := range tableBlocks {
		placeholder := fmt.Sprintf("\x00TB_%d\x00", i)
		text = strings.ReplaceAll(text, placeholder, tb)
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

// FormatAskQuestionParams форматирует параметры инструмента ask_question в читаемый
// Markdown на языке интерфейса lang.
func FormatAskQuestionParams(params map[string]interface{}, lang string) string {
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
				bldr.WriteString(i18n.T(lang, "markdown.multi_select"))
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

var promptWrapperTagRegex = regexp.MustCompile(`(?s)^\s*<(?:USER_REQUEST|SYSTEM_PROMPT|ADDITIONAL_METADATA|USER_SETTINGS_CHANGE)>[\s\S]*?</(?:USER_REQUEST|SYSTEM_PROMPT|ADDITIONAL_METADATA|USER_SETTINGS_CHANGE)>\s*`)
var leadingDividerRegex = regexp.MustCompile(`^(?:---|===|\*\*\*)\s*`)

var planningPromptEchoMarkers = []string{
	"внимание: сейчас выполняется этап планирования",
	"внимание: это этап планирования",
	"attention: the planning stage is in progress",
	"attention: the planning stage",
	"не создавай git-ветку",
	"do not create a git branch",
	"инструкции проекта (agents.md)",
	"project instructions (agents.md)",
	"задача пользователя:",
	"user task:",
	"твоя цель сейчас:",
	"your goal right now:",
	"выведи итоговый план",
	"output the resulting plan",
	"утверждённый план реализации:",
	"approved implementation plan:",
}

func isPromptListOrQuoteLine(l string) bool {
	if l == "" || strings.HasPrefix(l, ">") || strings.HasPrefix(l, "-") || strings.HasPrefix(l, "*") || l == "---" || l == "===" {
		return true
	}
	dot := strings.Index(l, ".")
	if dot > 0 && dot <= 3 {
		_, err := strconv.Atoi(l[:dot])
		return err == nil
	}
	return false
}

// SanitizePlanText очищает текст плана от эха системных промптов (тегов <USER_REQUEST>,
// преамбул AGENTS.md, системных инструкций этапа планирования и запретов на создание веток).
func SanitizePlanText(planText string) string {
	s := strings.TrimSpace(planText)
	if s == "" {
		return ""
	}

	for {
		loc := promptWrapperTagRegex.FindStringIndex(s)
		if loc != nil && loc[0] == 0 {
			s = strings.TrimSpace(s[loc[1]:])
		} else {
			break
		}
	}

	lines := strings.Split(s, "\n")
	idx := 0
	inEcho := false
	for i, line := range lines {
		l := strings.TrimSpace(line)
		lower := strings.ToLower(l)

		hasMarker := false
		for _, marker := range planningPromptEchoMarkers {
			if strings.Contains(lower, marker) {
				hasMarker = true
				break
			}
		}

		if hasMarker {
			inEcho = true
			idx = i + 1
		} else if inEcho && isPromptListOrQuoteLine(l) {
			idx = i + 1
		} else if inEcho {
			break
		} else if !inEcho && l == "" {
			idx = i + 1
		} else {
			break
		}
	}

	rest := strings.TrimSpace(strings.Join(lines[idx:], "\n"))
	rest = strings.TrimSpace(leadingDividerRegex.ReplaceAllString(rest, ""))
	return rest
}
