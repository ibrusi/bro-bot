package utils

import (
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultTelegramMaxChunkLen — безопасный лимит видимого текста (в единицах UTF-16)
	// на одно сообщение Telegram (официальный предел API — 4096 символов после парсинга HTML).
	DefaultTelegramMaxChunkLen = 4000
	// maxRawHTMLChunkBytes — защитный потолок сырого HTML одного чанка на случай
	// аномального количества длинных URL-ссылок в коротком видимом тексте.
	maxRawHTMLChunkBytes = 32768
)

var (
	telegramHTMLTagRegex = regexp.MustCompile(`(?i)^<(/)?([a-z0-9_-]+)((?:\s+[^>]*)?)>`)
	allHTMLTagsRegex     = regexp.MustCompile(`(?i)</?[a-z0-9_-]+(?:\s+[^>]*)?>`)
	htmlEntityRegex      = regexp.MustCompile(`^&(?:[a-zA-Z]+|#\d+|#x[0-9a-fA-F]+);`)
)

type openHTMLTag struct {
	name string
	open string
}

// UTF16Len возвращает длину строки в кодовых единицах UTF-16 (метрика Telegram Bot API).
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func runeUTF16Len(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// TelegramHTMLVisibleLen возвращает видимую длину HTML-сообщения Telegram в UTF-16 единицах
// (HTML-теги не учитываются, сущности вроде &lt;/&amp;/&quot; считаются по декодированному символу).
func TelegramHTMLVisibleLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == '<' {
			if loc := telegramHTMLTagRegex.FindStringIndex(s[i:]); loc != nil {
				i += loc[1]
				continue
			}
		}
		if s[i] == '&' {
			if loc := htmlEntityRegex.FindStringIndex(s[i:]); loc != nil {
				n += UTF16Len(html.UnescapeString(s[i : i+loc[1]]))
				i += loc[1]
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		n += runeUTF16Len(r)
		i += size
	}
	return n
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

func cloneOpenTags(tags []openHTMLTag) []openHTMLTag {
	if len(tags) == 0 {
		return nil
	}
	cp := make([]openHTMLTag, len(tags))
	copy(cp, tags)
	return cp
}

func applyHTMLTag(fullTag string, isClosing bool, tagName string, openTags []openHTMLTag) []openHTMLTag {
	if strings.HasSuffix(fullTag, "/>") {
		return openTags
	}
	if isClosing {
		for k := len(openTags) - 1; k >= 0; k-- {
			if openTags[k].name == tagName {
				return append(openTags[:k], openTags[k+1:]...)
			}
		}
		return openTags
	}
	return append(openTags, openHTMLTag{name: tagName, open: fullTag})
}

// scanLineTags обновляет стек открытых тегов на основе содержимого строки.
func scanLineTags(line string, openTags []openHTMLTag) []openHTMLTag {
	i := 0
	for i < len(line) {
		if line[i] == '<' {
			if loc := telegramHTMLTagRegex.FindStringSubmatchIndex(line[i:]); loc != nil {
				fullTag := line[i : i+loc[1]]
				isClosing := loc[2] != -1
				tagName := strings.ToLower(line[i+loc[4] : i+loc[5]])
				openTags = applyHTMLTag(fullTag, isClosing, tagName, openTags)
				i += loc[1]
				continue
			}
		}
		i++
	}
	return openTags
}

type htmlToken struct {
	text       string
	visibleLen int
	isTag      bool
	isClosing  bool
	tagName    string
	isSpace    bool
}

func tokenizeHTML(s string) []htmlToken {
	var tokens []htmlToken
	for i := 0; i < len(s); {
		if s[i] == '<' {
			if loc := telegramHTMLTagRegex.FindStringSubmatchIndex(s[i:]); loc != nil {
				tokens = append(tokens, htmlToken{
					text:      s[i : i+loc[1]],
					isTag:     true,
					isClosing: loc[2] != -1,
					tagName:   strings.ToLower(s[i+loc[4] : i+loc[5]]),
				})
				i += loc[1]
				continue
			}
		}
		if s[i] == '&' {
			if loc := htmlEntityRegex.FindStringIndex(s[i:]); loc != nil {
				raw := s[i : i+loc[1]]
				tokens = append(tokens, htmlToken{
					text:       raw,
					visibleLen: UTF16Len(html.UnescapeString(raw)),
				})
				i += loc[1]
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		tokens = append(tokens, htmlToken{
			text:       s[i : i+size],
			visibleLen: runeUTF16Len(r),
			isSpace:    r == ' ',
		})
		i += size
	}
	return tokens
}

// SplitTelegramHTML разбивает длинное HTML-сообщение Telegram на части <= maxChunkLen
// видимых UTF-16 символов, сохраняя валидность HTML-тегов и целостность сущностей между частями.
func SplitTelegramHTML(text string, maxChunkLen int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxChunkLen <= 0 {
		maxChunkLen = DefaultTelegramMaxChunkLen
	}

	if TelegramHTMLVisibleLen(text) <= maxChunkLen && len(text) <= maxRawHTMLChunkBytes {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var curChunk strings.Builder
	curVisibleLen := 0
	curHasBody := false
	var openTags []openHTMLTag

	flushChunk := func() {
		if !curHasBody {
			curChunk.Reset()
			curVisibleLen = 0
			return
		}
		curChunk.WriteString(closeOpenTags(openTags))
		chunks = append(chunks, curChunk.String())
		curChunk.Reset()
		curVisibleLen = 0
		curHasBody = false
		if len(openTags) > 0 {
			curChunk.WriteString(reopenTags(openTags))
		}
	}

	for _, line := range lines {
		lineVisibleLen := TelegramHTMLVisibleLen(line)
		rawLineBytes := len(line)

		// Сверхдлинная строка (длиннее maxChunkLen видимых символов или потолка сырого HTML):
		// режем по неделимым HTML-токенам, чтобы не рассекать теги и &entities;.
		if lineVisibleLen > maxChunkLen || rawLineBytes > maxRawHTMLChunkBytes {
			if curHasBody {
				flushChunk()
			}

			tokens := tokenizeHTML(line)
			i := 0
			for i < len(tokens) {
				var seg strings.Builder
				segVisible := 0
				segRaw := curChunk.Len() + len(closeOpenTags(openTags))
				segTags := cloneOpenTags(openTags)
				lastSpaceTok := -1
				lastSpaceVisible := 0
				lastSpaceRaw := 0

				end := i
				for end < len(tokens) {
					tok := tokens[end]
					nextVis := segVisible + tok.visibleLen
					nextRaw := segRaw + len(tok.text) + 12 // небольшой запас на закрытие тега, если он откроется здесь
					if (nextVis > maxChunkLen || nextRaw > maxRawHTMLChunkBytes) && end > i {
						break
					}
					if tok.isTag {
						segTags = applyHTMLTag(tok.text, tok.isClosing, tok.tagName, segTags)
					} else if tok.isSpace && segVisible >= maxChunkLen/2 {
						lastSpaceTok = end
						lastSpaceVisible = segVisible
						lastSpaceRaw = end
						_ = lastSpaceRaw
					}
					seg.WriteString(tok.text)
					segVisible = nextVis
					segRaw += len(tok.text)
					end++
				}

				// Если упёрлись в предел и во второй половине окна был пробел — режем по слову.
				if end < len(tokens) && lastSpaceTok > i {
					seg.Reset()
					segTags = cloneOpenTags(openTags)
					for k := i; k < lastSpaceTok; k++ {
						tok := tokens[k]
						seg.WriteString(tok.text)
						if tok.isTag {
							segTags = applyHTMLTag(tok.text, tok.isClosing, tok.tagName, segTags)
						}
					}
					segVisible = lastSpaceVisible
					end = lastSpaceTok + 1 // пропускаем разделительный пробел
				}

				if end == i {
					tok := tokens[i]
					seg.WriteString(tok.text)
					if tok.isTag {
						segTags = applyHTMLTag(tok.text, tok.isClosing, tok.tagName, segTags)
					}
					segVisible = tok.visibleLen
					end = i + 1
				}

				curChunk.WriteString(seg.String())
				curVisibleLen += segVisible
				curHasBody = curChunk.Len() > 0
				openTags = segTags

				if end < len(tokens) {
					curChunk.WriteString(closeOpenTags(openTags))
					chunks = append(chunks, curChunk.String())
					curChunk.Reset()
					curVisibleLen = 0
					curHasBody = false
					if len(openTags) > 0 {
						curChunk.WriteString(reopenTags(openTags))
					}
				}
				i = end
			}
			continue
		}

		sep := 0
		if curHasBody {
			sep = 1
		}
		projectedVis := curVisibleLen + sep + lineVisibleLen
		projectedRaw := curChunk.Len() + sep + rawLineBytes + len(closeOpenTags(openTags)) + 16
		if curHasBody && (projectedVis > maxChunkLen || projectedRaw > maxRawHTMLChunkBytes) {
			flushChunk()
			sep = 0
		}

		if sep == 1 {
			curChunk.WriteByte('\n')
			curVisibleLen++
		}
		curChunk.WriteString(line)
		curVisibleLen += lineVisibleLen
		curHasBody = true
		openTags = scanLineTags(line, openTags)
	}

	if curHasBody {
		curChunk.WriteString(closeOpenTags(openTags))
		chunks = append(chunks, curChunk.String())
	}

	return chunks
}

// SplitPlainText разбивает обычный текст на части <= maxChunkLen в UTF-16 единицах.
func SplitPlainText(text string, maxChunkLen int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxChunkLen <= 0 {
		maxChunkLen = DefaultTelegramMaxChunkLen
	}

	if UTF16Len(text) <= maxChunkLen {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var curChunk strings.Builder
	curLen := 0

	for _, line := range lines {
		lineLen := UTF16Len(line)
		if lineLen > maxChunkLen {
			if curChunk.Len() > 0 {
				chunks = append(chunks, curChunk.String())
				curChunk.Reset()
				curLen = 0
			}
			lineRunes := []rune(line)
			for len(lineRunes) > 0 {
				if UTF16Len(string(lineRunes)) <= maxChunkLen {
					curChunk.WriteString(string(lineRunes))
					curLen = UTF16Len(string(lineRunes))
					break
				}
				cut := slicePrefixByUTF16(lineRunes, maxChunkLen)
				chunks = append(chunks, string(lineRunes[:cut]))
				lineRunes = lineRunes[cut:]
				if len(lineRunes) > 0 && lineRunes[0] == ' ' {
					lineRunes = lineRunes[1:]
				}
			}
			continue
		}

		sep := 0
		if curChunk.Len() > 0 {
			sep = 1
		}
		if curLen+sep+lineLen > maxChunkLen && curChunk.Len() > 0 {
			chunks = append(chunks, curChunk.String())
			curChunk.Reset()
			curLen = 0
			sep = 0
		}

		if sep == 1 {
			curChunk.WriteByte('\n')
			curLen++
		}
		curChunk.WriteString(line)
		curLen += lineLen
	}

	if curChunk.Len() > 0 {
		chunks = append(chunks, curChunk.String())
	}

	return chunks
}

// slicePrefixByUTF16 возвращает количество ведущих рун из runes, укладывающихся в limit
// кодовых единиц UTF-16. Если во второй половине окна есть пробел — предпочитает разрыв по нему.
func slicePrefixByUTF16(runes []rune, limit int) int {
	if len(runes) == 0 {
		return 0
	}
	if limit <= 0 {
		return 1
	}
	u16 := 0
	lastSpace := -1
	for i, r := range runes {
		w := runeUTF16Len(r)
		if u16+w > limit {
			if lastSpace > 0 {
				return lastSpace
			}
			if i == 0 {
				return 1
			}
			return i
		}
		if r == ' ' && u16 >= limit/2 {
			lastSpace = i
		}
		u16 += w
	}
	return len(runes)
}

// SplitMessageByMode разбивает текст сообщения в зависимости от режима разметки (HTML, Markdown или текст).
func SplitMessageByMode(text string, parseMode string, maxChunkLen int) []string {
	if maxChunkLen <= 0 {
		maxChunkLen = DefaultTelegramMaxChunkLen
	}

	mode := strings.ToUpper(strings.TrimSpace(parseMode))
	switch mode {
	case "HTML":
		if TelegramHTMLVisibleLen(text) <= maxChunkLen && len(text) <= maxRawHTMLChunkBytes {
			return []string{text}
		}
		return SplitTelegramHTML(text, maxChunkLen)
	case "MARKDOWN", "MARKDOWNV2":
		if UTF16Len(text) <= maxChunkLen {
			return []string{text}
		}
		return SplitMarkdown(text, maxChunkLen)
	default:
		if UTF16Len(text) <= maxChunkLen {
			return []string{text}
		}
		return SplitPlainText(text, maxChunkLen)
	}
}
