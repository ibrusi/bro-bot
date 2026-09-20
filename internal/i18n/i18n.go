// Package i18n хранит каталоги сообщений бота и отдаёт перевод по коду языка.
//
// Каталог каждого языка лежит в отдельном файле locales/<код>.json и вшивается
// в бинарник через go:embed. Чтобы добавить новый язык, достаточно положить туда
// ещё один файл: код бота при этом не меняется — список языков, клавиатура команды
// /language и валидация сохранённой настройки строятся по содержимому каталога.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed locales/*.json
var localesFS embed.FS

// Default — язык, на котором бот отвечает, пока пользователь не выбрал другой.
const Default = "en"

// Language описывает язык интерфейса: код, самоназвание и флаг для кнопки выбора.
type Language struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Flag string `json:"flag"`
}

// Label — подпись языка на кнопке выбора: «🇬🇧 English».
func (l Language) Label() string {
	if l.Flag == "" {
		return l.Name
	}
	return l.Flag + " " + l.Name
}

// catalog — разобранный файл локали.
type catalog struct {
	Meta     Language          `json:"meta"`
	Messages map[string]string `json:"messages"`
}

// catalogs заполняется один раз в init и дальше только читается, поэтому
// синхронизация не нужна: горутины обработчиков видят уже готовую карту.
var (
	catalogs  = map[string]catalog{}
	languages []Language
)

func init() {
	entries, err := localesFS.ReadDir("locales")
	if err != nil {
		panic(fmt.Sprintf("i18n: не удалось прочитать каталог локалей: %v", err))
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := entry.Name()
		raw, err := localesFS.ReadFile(path.Join("locales", name))
		if err != nil {
			panic(fmt.Sprintf("i18n: не удалось прочитать локаль %s: %v", name, err))
		}

		var cat catalog
		if err := json.Unmarshal(raw, &cat); err != nil {
			panic(fmt.Sprintf("i18n: не удалось разобрать локаль %s: %v", name, err))
		}

		code := normalizeCode(cat.Meta.Code)
		fileCode := strings.TrimSuffix(name, ".json")
		if code == "" {
			panic(fmt.Sprintf("i18n: в локали %s не задан meta.code", name))
		}
		if code != fileCode {
			panic(fmt.Sprintf("i18n: локаль %s объявляет код %q — имя файла и код должны совпадать", name, cat.Meta.Code))
		}
		if _, exists := catalogs[code]; exists {
			panic(fmt.Sprintf("i18n: язык %q объявлен дважды", code))
		}

		cat.Meta.Code = code
		catalogs[code] = cat
		languages = append(languages, cat.Meta)
	}

	if _, ok := catalogs[Default]; !ok {
		panic(fmt.Sprintf("i18n: отсутствует каталог языка по умолчанию %q", Default))
	}

	// Порядок языков не зависит от порядка чтения каталога: язык по умолчанию
	// первый, остальные — по коду, чтобы кнопки /language не прыгали между запусками.
	sort.Slice(languages, func(i, j int) bool {
		if (languages[i].Code == Default) != (languages[j].Code == Default) {
			return languages[i].Code == Default
		}
		return languages[i].Code < languages[j].Code
	})
}

// normalizeCode приводит код языка к каноническому виду: нижний регистр без региона
// («ru-RU» → «ru»), потому что каталоги заведены на язык, а не на локаль.
func normalizeCode(code string) string {
	clean := strings.ToLower(strings.TrimSpace(code))
	clean = strings.ReplaceAll(clean, "_", "-")
	if idx := strings.Index(clean, "-"); idx > 0 {
		clean = clean[:idx]
	}
	return clean
}

// Languages возвращает все загруженные языки: первым язык по умолчанию, дальше по коду.
func Languages() []Language {
	return append([]Language(nil), languages...)
}

// Codes возвращает коды загруженных языков в том же порядке, что и Languages.
func Codes() []string {
	codes := make([]string, 0, len(languages))
	for _, l := range languages {
		codes = append(codes, l.Code)
	}
	return codes
}

// Lookup возвращает описание языка по коду.
func Lookup(code string) (Language, bool) {
	cat, ok := catalogs[normalizeCode(code)]
	return cat.Meta, ok
}

// Supported сообщает, есть ли каталог для такого языка.
func Supported(code string) bool {
	_, ok := catalogs[normalizeCode(code)]
	return ok
}

// Normalize приводит код языка к поддерживаемому: пустой и незнакомый становятся
// языком по умолчанию.
func Normalize(code string) string {
	clean := normalizeCode(code)
	if _, ok := catalogs[clean]; ok {
		return clean
	}
	return Default
}

// T возвращает сообщение по ключу. Если в выбранном языке ключа нет, берётся
// перевод из языка по умолчанию, а если нет и там — сам ключ: незакрытый перевод
// должен быть заметен, а не выглядеть как пустая строка.
func T(lang, key string) string {
	if cat, ok := catalogs[normalizeCode(lang)]; ok {
		if msg, ok := cat.Messages[key]; ok {
			return msg
		}
	}
	if msg, ok := catalogs[Default].Messages[key]; ok {
		return msg
	}
	return key
}

// Tf подставляет аргументы в шаблон сообщения.
func Tf(lang, key string, args ...any) string {
	return fmt.Sprintf(T(lang, key), args...)
}

// Printer — переводчик, привязанный к одному языку. Избавляет вызывающий код от
// того, чтобы таскать код языка в каждый вызов.
type Printer struct {
	lang string
}

// For возвращает переводчик для языка.
func For(lang string) Printer {
	return Printer{lang: Normalize(lang)}
}

// Lang — код языка этого переводчика.
func (p Printer) Lang() string {
	if p.lang == "" {
		return Default
	}
	return p.lang
}

// T возвращает сообщение по ключу.
func (p Printer) T(key string) string {
	return T(p.Lang(), key)
}

// Tf подставляет аргументы в шаблон сообщения.
func (p Printer) Tf(key string, args ...any) string {
	return Tf(p.Lang(), key, args...)
}

// Keys возвращает отсортированный список ключей каталога языка.
func Keys(lang string) []string {
	cat, ok := catalogs[normalizeCode(lang)]
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(cat.Messages))
	for key := range cat.Messages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Has сообщает, есть ли в каталоге языка собственный перевод ключа (без отката
// на язык по умолчанию).
func Has(lang, key string) bool {
	cat, ok := catalogs[normalizeCode(lang)]
	if !ok {
		return false
	}
	_, ok = cat.Messages[key]
	return ok
}
