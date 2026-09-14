package utils

import (
	"regexp"
	"strings"
)

var (
	telegramHTMLTagRegex = regexp.MustCompile(`(?i)^<(/)?([a-z0-9_-]+)((?:\s+[^>]*)?)>`)
	allHTMLTagsRegex     = regexp.MustCompile(`(?i)</?[a-z0-9_-]+(?:\s+[^>]*)?>`)
)

type openHTMLTag struct {
	name string
	open string
}

// StripTelegramHTML удаляет HTML-теги и декодирует базовые сущности для fallback-отправки.
func StripTelegramHTML(s string) string {
	clean := allHTMLTagsRegex.ReplaceAllString(s, "")
	clean = strings.ReplaceAll(clean, "&lt;", "<")
	clean = strings.ReplaceAll(clean, "&gt;", ">")
	clean = strings.ReplaceAll(clean, "&quot;", "\"")
	clean = strings.ReplaceAll(clean, "&amp;", "&")
	return clean
}

// closeOpenTags генерирует строку закрывающих тегов в обратном порядке стека.
func closeOpenTags(tags []openHTMLTag) string {
	if len(tags) == 0 {
		return ""
	}
	var bldr strings.Builder
	for i := len(tags) - 1; i >= 0; i-- {
		bldr.WriteString("</")
		bldr.WriteString(tags[i].name)
		bldr.WriteString(">")
	}
	return bldr.String()
}

// reopenTags генерирует строку открывающих тегов в прямом порядке стека.
func reopenTags(tags []openHTMLTag) string {
	if len(tags) == 0 {
		return ""
	}
	var bldr strings.Builder
	for _, t := range tags {
		bldr.WriteString(t.open)
	}
	return bldr.String()
}

// tagsExtraLen вычисляет длину (в рунах) закрывающих тегов для текущего стека.
func tagsExtraLen(tags []openHTMLTag) int {
	// </name> = 3 + len(name)
	extra := 0
	for _, t := range tags {
		extra += 3 + len([]rune(t.name))
	}
	return extra
}

// scanLineTags обновляет стек открытых тегов на основе содержимого строки.
func scanLineTags(line string, openTags []openHTMLTag) []openHTMLTag {
	i := 0
	for i < len(line) {
		if line[i] == '<' {
			loc := telegramHTMLTagRegex.FindStringSubmatchIndex(line[i:])
			if loc != nil {
				fullTag := line[i : i+loc[1]]
				isClosing := loc[2] != -1
				tagName := strings.ToLower(line[i+loc[4] : i+loc[5]])

				// Проверяем самозакрывающийся тег
				isSelfClosing := strings.HasSuffix(fullTag, "/>")

				if !isSelfClosing {
					if isClosing {
						// Удаляем последний тег с таким именем
						for k := len(openTags) - 1; k >= 0; k-- {
							if openTags[k].name == tagName {
								openTags = append(openTags[:k], openTags[k+1:]...)
								break
							}
						}
					} else {
						// Открывающий тег
						openTags = append(openTags, openHTMLTag{
							name: tagName,
							open: fullTag,
						})
					}
				}
				i += loc[1]
				continue
			}
		}
		i++
	}
	return openTags
}

// SplitTelegramHTML разбивает длинное HTML-сообщение Telegram на части <= maxChunkLen,
// сохраняя валидность HTML-тегов между частями.
func SplitTelegramHTML(text string, maxChunkLen int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxChunkLen <= 0 {
		maxChunkLen = 3800
	}

	runes := []rune(text)
	if len(runes) <= maxChunkLen {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var curChunk strings.Builder
	var openTags []openHTMLTag

	for _, line := range lines {
		lineRunes := []rune(line)

		// Если сама строка больше maxChunkLen (одна гигантская строка)
		if len(lineRunes) > maxChunkLen {
			if curChunk.Len() > 0 {
				curChunk.WriteString(closeOpenTags(openTags))
				chunks = append(chunks, curChunk.String())
				curChunk.Reset()
				curChunk.WriteString(reopenTags(openTags))
			}

			// Разбиваем длинную строку по частям с учётом тегов
			for len(lineRunes) > 0 {
				safeLimit := maxChunkLen - tagsExtraLen(openTags)
				if safeLimit < 100 {
					safeLimit = 100
				}

				if len(lineRunes) <= safeLimit {
					openTags = scanLineTags(string(lineRunes), openTags)
					curChunk.WriteString(string(lineRunes))
					break
				}

				// Ищем пробел для аккуратного переноса
				splitAt := safeLimit
				for j := safeLimit - 1; j >= safeLimit/2; j-- {
					if lineRunes[j] == ' ' {
						splitAt = j
						break
					}
				}

				subStr := string(lineRunes[:splitAt])
				openTags = scanLineTags(subStr, openTags)
				curChunk.WriteString(subStr)
				curChunk.WriteString(closeOpenTags(openTags))
				chunks = append(chunks, curChunk.String())
				curChunk.Reset()
				curChunk.WriteString(reopenTags(openTags))

				lineRunes = lineRunes[splitAt:]
				if len(lineRunes) > 0 && lineRunes[0] == ' ' {
					lineRunes = lineRunes[1:]
				}
			}
			continue
		}

		// Вычисляем размер текущего чанка с новой строкой и закрывающими тегами
		lineTagsAfter := scanLineTags(line, append([]openHTMLTag(nil), openTags...))
		neededClosingLen := tagsExtraLen(lineTagsAfter)

		currentRuneCount := len([]rune(curChunk.String()))
		projected := currentRuneCount + len(lineRunes) + 1 + neededClosingLen

		if projected > maxChunkLen && currentRuneCount > 0 {
			curChunk.WriteString(closeOpenTags(openTags))
			chunks = append(chunks, curChunk.String())
			curChunk.Reset()
			curChunk.WriteString(reopenTags(openTags))
		}

		if curChunk.Len() > 0 && curChunk.String() != reopenTags(openTags) {
			curChunk.WriteByte('\n')
		}
		curChunk.WriteString(line)
		openTags = lineTagsAfter
	}

	if curChunk.Len() > 0 {
		curChunk.WriteString(closeOpenTags(openTags))
		chunks = append(chunks, curChunk.String())
	}

	return chunks
}

// SplitPlainText разбивает обычный текст на части <= maxChunkLen.
func SplitPlainText(text string, maxChunkLen int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxChunkLen <= 0 {
		maxChunkLen = 3800
	}

	runes := []rune(text)
	if len(runes) <= maxChunkLen {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var curChunk strings.Builder

	for _, line := range lines {
		lineRunes := []rune(line)
		if len(lineRunes) > maxChunkLen {
			if curChunk.Len() > 0 {
				chunks = append(chunks, curChunk.String())
				curChunk.Reset()
			}
			for len(lineRunes) > maxChunkLen {
				splitAt := maxChunkLen
				for j := maxChunkLen - 1; j >= maxChunkLen/2; j-- {
					if lineRunes[j] == ' ' {
						splitAt = j
						break
					}
				}
				chunks = append(chunks, string(lineRunes[:splitAt]))
				lineRunes = lineRunes[splitAt:]
				if len(lineRunes) > 0 && lineRunes[0] == ' ' {
					lineRunes = lineRunes[1:]
				}
			}
			if len(lineRunes) > 0 {
				curChunk.WriteString(string(lineRunes))
			}
			continue
		}

		projected := len([]rune(curChunk.String())) + len(lineRunes) + 1
		if projected > maxChunkLen && curChunk.Len() > 0 {
			chunks = append(chunks, curChunk.String())
			curChunk.Reset()
		}

		if curChunk.Len() > 0 {
			curChunk.WriteByte('\n')
		}
		curChunk.WriteString(line)
	}

	if curChunk.Len() > 0 {
		chunks = append(chunks, curChunk.String())
	}

	return chunks
}

// SplitMessageByMode разбивает текст сообщения в зависимости от режима разметки (HTML, Markdown или текст).
func SplitMessageByMode(text string, parseMode string, maxChunkLen int) []string {
	if maxChunkLen <= 0 {
		maxChunkLen = 3800
	}
	runes := []rune(text)
	if len(runes) <= maxChunkLen {
		return []string{text}
	}

	mode := strings.ToUpper(strings.TrimSpace(parseMode))
	switch mode {
	case "HTML":
		return SplitTelegramHTML(text, maxChunkLen)
	case "MARKDOWN", "MARKDOWNV2":
		return SplitMarkdown(text, maxChunkLen)
	default:
		return SplitPlainText(text, maxChunkLen)
	}
}
