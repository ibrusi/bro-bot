package domain

import (
	"strings"
	"unicode"
)

// Intent — намерение пользовательского сообщения в диалоговом режиме.
type Intent int

const (
	// IntentChat — разговор: вопрос, уточнение, обсуждение, короткая реплика.
	IntentChat Intent = iota
	// IntentWork — запрос на изменение кода: такие сообщения предлагаем оформить планом или задачей.
	IntentWork
)

func (i Intent) String() string {
	if i == IntentWork {
		return "work"
	}
	return "chat"
}

// Причины срабатывания правил классификатора (используются в логах и тестах).
const (
	IntentReasonEmpty        = "empty"         // пустое сообщение
	IntentReasonVCS          = "vcs"           // упоминание ветки, коммита, пуша, PR или деплоя
	IntentReasonImperative   = "imperative"    // повелительный глагол в начале сообщения
	IntentReasonQuestion     = "question"      // вопросительная форма
	IntentReasonWorkMention  = "work_mention"  // повелительный глагол в середине сообщения
	IntentReasonDefaultChat  = "default_chat"  // ничего не сработало — считаем разговором
	IntentReasonReadOnlyVerb = "readonly_verb" // «объясни», «покажи» и подобные — это тоже разговор
	IntentReasonChatSave     = "chat_save"     // прямой запрос на скачивание/сохранение данных или создание скрипта в чате
)

// fillerPrefixes — вежливые вступления, которые отбрасываются перед разбором повелительной формы.
var fillerPrefixes = map[string]bool{
	"пожалуйста": true, "плиз": true, "please": true,
	"давай": true, "давайте": true, "слушай": true, "слушайте": true,
	"нужно": true, "надо": true, "требуется": true, "хочу": true, "хотелось": true,
	"можешь": true, "можете": true, "сможешь": true, "сможете": true,
	"ты": true, "бы": true, "мне": true, "нам": true, "тут": true, "там": true, "вот": true,
	"привет": true, "здравствуй": true, "здравствуйте": true, "хай": true,
	"hey": true, "hi": true, "hello": true, "lets": true, "let's": true,
	"can": true, "could": true, "would": true, "you": true, "me": true, "us": true,
}

// workVerbStems — основы повелительных глаголов, означающих изменение кода.
var workVerbStems = []string{
	"сделай", "сделать", "сделаем",
	"исправ", "почин", "поправ", "пофикс",
	"добав", "допиш", "дореализ",
	"реализ", "внедр", "интегрир", "подключ",
	"напиш", "перепиш", "допилива", "допил",
	"отрефактор", "зарефактор", "рефактор",
	"удал", "убер", "убрат", "почист", "вычист",
	"обнов", "апдейт", "апгрейд", "подним",
	"созда", "сгенерир", "нагенерир",
	"оптимизир", "ускор", "переимен", "перенес", "вынес", "замен", "настро",
	"покр", "отлад", "задеплой", "деплой", "смерж",
	"fix", "add", "implement", "refactor", "rewrite", "rework",
	"create", "remove", "delete", "drop", "update", "upgrade", "migrate",
	"optimize", "rename", "extract", "replace", "integrate", "cover",
	"write", "build", "bump", "revert", "cleanup", "deploy",
}

// readOnlyVerbStems — повелительные глаголы, которые не требуют изменений: это разговор.
var readOnlyVerbStems = []string{
	"объясн", "расскаж", "покаж", "поясн", "опиш", "сравн",
	"посмотр", "глянь", "найд", "поищ", "изуч", "разбер", "подскаж",
	"напомн", "перечисл", "оцен", "проанализир", "прокомментир",
	"explain", "show", "describe", "compare", "find", "list", "tell", "summarize",
}

// questionWords — вопросительные слова в начале сообщения.
var questionWords = map[string]bool{
	"что": true, "чем": true, "чего": true, "как": true, "каким": true, "какой": true,
	"какая": true, "какое": true, "какие": true, "почему": true, "отчего": true,
	"зачем": true, "где": true, "куда": true, "откуда": true, "когда": true,
	"кто": true, "кого": true, "чей": true, "сколько": true, "правда": true,
	"стоит": true, "можно": true, "нормально": true, "ок": true,
	"what": true, "how": true, "why": true, "where": true, "when": true, "who": true,
	"which": true, "whose": true, "does": true, "do": true, "did": true, "is": true,
	"are": true, "was": true, "were": true, "should": true, "any": true,
}

// vcsMarkers — фразы про ветки, коммиты, пуши, PR и деплой: это всегда работа.
var vcsMarkers = []string{
	"создай ветку", "создать ветку", "заведи ветку", "новую ветку",
	"сделай коммит", "закоммить", "сделай commit",
	"запушь", "запуш", "сделай пуш", "push it",
	"открой pr", "открой пр", "создай pr", "сделай pr", "открой пулл", "открой pull",
	"open a pr", "open pr", "create a pr", "make a commit", "commit and push",
	"выкати релиз", "сделай релиз", "задеплой", "выкати на прод",
}

// ClassifyMessage определяет, является ли сообщение запросом на изменение кода.
func ClassifyMessage(text string) Intent {
	intent, _ := ClassifyMessageReason(text)
	return intent
}

// ClassifyMessageReason дополнительно возвращает сработавшее правило — удобно для логов и тестов.
//
// Порядок правил (первое сработавшее побеждает):
//  1. Упоминание веток/коммитов/PR/деплоя — всегда работа.
//  2. Повелительный глагол изменения в НАЧАЛЕ сообщения — самый чёткий сигнал работы
//     («Проверь и поправь» тоже сюда попадёт по правилу 4).
//  3. Вопросительная форма — разговор, даже если внутри есть «добавить»
//     («как добавить кэш?» — это вопрос, а не задача).
//  4. Повелительный глагол изменения в любом месте сообщения — работа.
//  5. Иначе — разговор.
//
// Асимметрия намеренная: ложное «работа» стоит пользователю одного нажатия кнопки
// «Ответить в чате», а ложное «разговор» не стоит ничего — бот просто ответит.
func ClassifyMessageReason(text string) (Intent, string) {
	normalized := normalizeIntentText(text)
	if normalized == "" {
		return IntentChat, IntentReasonEmpty
	}

	for _, marker := range vcsMarkers {
		if strings.Contains(normalized, marker) {
			return IntentWork, IntentReasonVCS
		}
	}

	tokens := intentTokens(normalized)
	if isChatSaveIntent(text, normalized, tokens) {
		return IntentChat, IntentReasonChatSave
	}
	meaningful := dropFillerPrefix(tokens)

	if len(meaningful) > 0 && hasStem(meaningful[0], workVerbStems) {
		return IntentWork, IntentReasonImperative
	}

	if strings.Contains(text, "?") {
		return IntentChat, IntentReasonQuestion
	}
	if len(meaningful) > 0 && questionWords[meaningful[0]] {
		return IntentChat, IntentReasonQuestion
	}
	if len(meaningful) > 0 && hasStem(meaningful[0], readOnlyVerbStems) {
		return IntentChat, IntentReasonReadOnlyVerb
	}

	for _, token := range meaningful {
		if hasStem(token, workVerbStems) {
			return IntentWork, IntentReasonWorkMention
		}
	}

	return IntentChat, IntentReasonDefaultChat
}

// normalizeIntentText приводит текст к нижнему регистру, заменяет «ё» на «е» и схлопывает пробелы.
func normalizeIntentText(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	lower = strings.ReplaceAll(lower, "ё", "е")
	return strings.Join(strings.Fields(lower), " ")
}

// intentTokens разбивает нормализованный текст на слова (буквы, цифры и символы кода).
func intentTokens(normalized string) []string {
	return strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '\''
	})
}

// dropFillerPrefix отбрасывает вежливые вступления в начале сообщения.
func dropFillerPrefix(tokens []string) []string {
	idx := 0
	for idx < len(tokens) && fillerPrefixes[tokens[idx]] {
		idx++
	}
	if idx == len(tokens) {
		return tokens
	}
	return tokens[idx:]
}

// hasStem проверяет, начинается ли слово с одной из основ.
func hasStem(token string, stems []string) bool {
	for _, stem := range stems {
		if strings.HasPrefix(token, stem) {
			return true
		}
	}
	return false
}

// codeWorkExclusionStems — признаки инженерной задачи по изменению кодовой базы,
// которые не должны перехватываться как быстрая запись/сохранение данных в чате.
var codeWorkExclusionStems = []string{
	"рефактор", "отрефактор", "зарефактор",
	"исправ", "почин", "поправ", "пофикс", "баг",
	"реализ", "внедр", "интегрир", "мигр",
	"тест", "зависимост", "удал", "убер", "убрат", "переимен", "вынес",
	"коммит", "ветк", "релиз", "депло",
	"refactor", "fix", "bug", "implement", "integrate", "migrate", "test", "delete", "remove", "drop", "rename",
	"commit", "branch", "release", "deploy",
}

// chatSaveVerbStems — глаголы загрузки/выгрузки/сохранения данных.
var chatSaveVerbStems = []string{
	"скача", "загруз", "подгруз", "выгруз", "сохран", "запиш", "дамп", "экспортир",
	"download", "fetch", "export", "save", "dump",
}

// chatSaveTargetStems — объекты данных/файлов для сохранения в чате.
var chatSaveTargetStems = []string{
	"данн", "файл", "датасет", "кеш", "кэш", "таблиц", "отчет", "диск", "папк", "каталог",
	"csv", "json", "parquet", "sqlite", "tsv", "xlsx", "свеч", "котировк",
	"data", "dataset", "cache", "file", "table", "report", "disk", "folder", "dir",
}

// scriptCreationVerbStems — глаголы создания, применимые к отдельным скриптам.
var scriptCreationVerbStems = []string{
	"созда", "напиш", "сгенерир", "нагенерир", "сдела", "сохран", "обнов",
	"create", "write", "generate", "save", "make", "update",
}

func isChatSaveIntent(rawText, normalized string, tokens []string) bool {
	hasExplicitPath, nonPathText := splitPathAndNonPath(rawText)
	nonPathTokens := intentTokens(normalizeIntentText(nonPathText))
	for _, tok := range nonPathTokens {
		if hasStem(tok, codeWorkExclusionStems) {
			return false
		}
	}

	hasSaveVerb := false
	hasSaveTarget := false
	hasScriptVerb := false
	hasScriptTarget := false

	for _, tok := range tokens {
		if hasStem(tok, chatSaveVerbStems) {
			hasSaveVerb = true
		}
		if hasStem(tok, chatSaveTargetStems) {
			hasSaveTarget = true
		}
		if hasStem(tok, scriptCreationVerbStems) {
			hasScriptVerb = true
		}
		if hasStem(tok, []string{"скрипт", "script"}) {
			hasScriptTarget = true
		}
	}
	if strings.Contains(normalized, "scripts/") || strings.Contains(normalized, ".py") || strings.Contains(normalized, ".sh") {
		hasScriptTarget = true
	}

	if hasSaveVerb && (hasSaveTarget || hasExplicitPath || hasScriptTarget) {
		return true
	}
	if hasScriptVerb && hasScriptTarget {
		return true
	}
	return false
}

var chatSavePathExts = []string{".py", ".sh", ".csv", ".json", ".parquet", ".db", ".sqlite", ".tsv", ".xlsx", ".txt", ".md", ".yaml", ".yml"}

// containsFilePath проверяет, содержит ли текст путь на диске (отсекая слэш-команды вида /chat или /plan).
func containsFilePath(rawText string) bool {
	hasExplicitPath, _ := splitPathAndNonPath(rawText)
	return hasExplicitPath
}

func splitPathAndNonPath(rawText string) (hasExplicitPath bool, nonPathText string) {
	fields := strings.Fields(rawText)
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		if isFilePathField(field) {
			hasExplicitPath = true
			continue
		}
		kept = append(kept, field)
	}
	return hasExplicitPath, strings.Join(kept, " ")
}

func isFilePathField(field string) bool {
	tok := strings.Trim(field, `"'()[]{}<>.,:;!?`+"`")
	if idx := strings.IndexByte(tok, '='); idx > 0 {
		if isFilePathToken(tok[idx+1:]) {
			return true
		}
	}
	return isFilePathToken(tok)
}

func isFilePathToken(tok string) bool {
	if len(tok) < 3 || !strings.Contains(tok, "/") {
		return false
	}
	lower := strings.ToLower(tok)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return false
	}
	if strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "~/") || strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../") {
		rest := strings.TrimLeft(tok, "~./")
		return strings.Contains(rest, "/") || strings.Contains(rest, ".")
	}
	if strings.HasSuffix(tok, "/") {
		return true
	}
	for _, ext := range chatSavePathExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
