package handlers

import (
	"context"
	"errors"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
)

const (
	// maxDeliveredFiles — сколько файлов одного хода уходит в чат.
	maxDeliveredFiles = 5
	// maxCaptionRunes — подпись вложения короче лимита Telegram в 1024 символа.
	maxCaptionRunes = 1000
)

// filePhrases — явная просьба получить ответ файлом, без имени документа.
var filePhrases = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}_])в\s+файл(?:[^\p{L}\p{N}_]|$)`),
	regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}_])файлом(?:[^\p{L}\p{N}_]|$)`),
	regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}_])в\s+виде\s+файла(?:[^\p{L}\p{N}_]|$)`),
	regexp.MustCompile(`(?i)\bto\s+a\s+file\b`),
	regexp.MustCompile(`(?i)\binto\s+a\s+file\b`),
	regexp.MustCompile(`(?i)\bas\s+a\s+file\b`),
}

// toFileRes ловит «to file», но не «to file a bug».
var toFileRes = regexp.MustCompile(`(?i)\bto\s+file\b`)

// saveVerbRes — глагол сохранения. Сам по себе он не включает отдачу файла:
// нужно ещё имя документа. «write unit tests» сюда не попадает.
var saveVerbRes = regexp.MustCompile(`(?i)(?:запиш|сохран|полож|выгруз|пришл|отправ|экспорт|\bwrite\b|\bsave\b|\bexport\b|\bput\b|\bstore\b)`)

// docNameRes вытаскивает имя документа. Исходники (.go и подобные) не считаются
// просьбой «пришли файл»: иначе обычная правка кода разошлёт репозиторий в чат.
var docNameRes = regexp.MustCompile(`(?i)([\p{L}\p{N}_./\\-]+)\.(txt|md|markdown|csv|json|html|pdf|log|yml|yaml|xml|tsv|png|jpg|jpeg|webp|gif)(?:[^\p{L}\p{N}_]|$)`)

var (
	errOutsideProject = errors.New("path is outside the project")
	errNotRegularFile = errors.New("not a regular file")
	errEmptyAnswer    = errors.New("empty answer")
)

type fileTooLargeError struct {
	size int64
}

func (e fileTooLargeError) Error() string { return "file is too large" }

// userWantsFile сообщает, просил ли пользователь записать информацию в файл.
// fileName — имя документа, если оно было в тексте, иначе пустая строка.
func userWantsFile(prompt string) (fileName string, ok bool) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", false
	}
	name := documentBaseName(prompt)
	phrase := mentionsFilePhrase(prompt)
	if phrase || (saveVerbRes.MatchString(prompt) && name != "") {
		return name, true
	}
	return "", false
}

func mentionsFilePhrase(prompt string) bool {
	for _, re := range filePhrases {
		if re.MatchString(prompt) {
			return true
		}
	}
	loc := toFileRes.FindStringIndex(prompt)
	if loc == nil {
		return false
	}
	rest := strings.TrimLeft(prompt[loc[1]:], " \t")
	lower := strings.ToLower(rest)
	// «to file a bug» — канцелярский оборот, не просьба приложить файл.
	if lower == "a" || strings.HasPrefix(lower, "a ") || strings.HasPrefix(lower, "a,") || strings.HasPrefix(lower, "a.") {
		return false
	}
	return true
}

func documentBaseName(prompt string) string {
	m := docNameRes.FindStringSubmatch(prompt)
	if len(m) < 3 {
		return ""
	}
	base := filepath.Base(m[1] + "." + m[2])
	base = strings.Trim(base, ". ")
	if base == "" || base == "." || base == ".." {
		return ""
	}
	return base
}

// observedFile — путь, который агент писал или правил в этом ходе.
type observedFile struct {
	path    string
	written bool
}

// writtenFileCollector собирает пути из шагов stream-json. Читать его можно
// только после того, как сканер хода закончил работу.
type writtenFileCollector struct {
	items []observedFile
}

func (c *writtenFileCollector) Observe(toolName string, info *domain.StreamToolInfo) {
	if c == nil {
		return
	}
	name := strings.TrimSpace(toolName)
	if name == "" && info != nil {
		name = info.Name
	}
	path := toolPath(info)
	if path == "" {
		return
	}
	write := isWriteTool(name)
	if !write && !isEditTool(name) {
		return
	}
	for i := range c.items {
		if c.items[i].path == path {
			if write {
				c.items[i].written = true
			}
			return
		}
	}
	c.items = append(c.items, observedFile{path: path, written: write})
}

func (c *writtenFileCollector) Paths() []observedFile {
	if c == nil {
		return nil
	}
	return append([]observedFile(nil), c.items...)
}

func toolPath(info *domain.StreamToolInfo) string {
	if info == nil || info.Parameters == nil {
		return ""
	}
	for _, key := range []string{"TargetFile", "file_path", "path", "AbsolutePath"} {
		if value, ok := info.Parameters[key].(string); ok {
			if path := strings.TrimSpace(value); path != "" {
				return path
			}
		}
	}
	return ""
}

func isWriteTool(name string) bool {
	switch strings.ToLower(name) {
	case "write", "write_file", "write_to_file", "create_file":
		return true
	default:
		return false
	}
}

func isEditTool(name string) bool {
	switch strings.ToLower(name) {
	case "edit", "edit_file", "replace_file_content":
		return true
	default:
		return false
	}
}

func hasDocumentExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".markdown", ".csv", ".json", ".html", ".pdf", ".log",
		".yml", ".yaml", ".xml", ".tsv", ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	default:
		return false
	}
}

// resolveProjectPath оставляет только путь внутри workDir.
func resolveProjectPath(workDir, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty path")
	}
	root := filepath.Clean(workDir)
	var abs string
	if filepath.IsAbs(raw) {
		abs = filepath.Clean(raw)
	} else {
		abs = filepath.Clean(filepath.Join(root, raw))
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errOutsideProject
	}
	return abs, nil
}

// confineExistingFile проверяет, что файл лежит в проекте, это обычный файл
// и он помещается в лимит Telegram.
func confineExistingFile(workDir, raw string) (string, error) {
	abs, err := resolveProjectPath(workDir, raw)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		if _, err := resolveProjectPath(workDir, target); err != nil {
			return "", err
		}
		abs = target
		info, err = os.Stat(abs)
		if err != nil {
			return "", err
		}
	}
	if !info.Mode().IsRegular() {
		return "", errNotRegularFile
	}
	if info.Size() > maxTelegramFileSize {
		return "", fileTooLargeError{size: info.Size()}
	}
	return abs, nil
}

// deliverRequestedFiles отправляет файл, если пользователь просил записать
// информацию в файл. Обычные правки исходников не прикладываются.
// onSent вызывается для каждого доставленного сообщения (задача регистрирует его у себя).
func deliverRequestedFiles(m ports.Messenger, chat ports.ChatID, workDir, lang string, prompts []string, observed []observedFile, answer string, onSent func(ports.MessageRef)) {
	if m == nil || strings.TrimSpace(workDir) == "" {
		return
	}
	name, ok := userWantsFile(strings.Join(prompts, "\n"))
	if !ok {
		return
	}
	matches := selectDeliverable(workDir, name, observed)
	if len(matches) > 0 {
		for _, path := range matches {
			ref, err := sendProjectFile(m, chat, workDir, lang, path, "")
			if err != nil || onSent == nil {
				continue
			}
			onSent(ref)
		}
		return
	}
	saved, err := materializeAnswerFile(workDir, name, answer)
	if err != nil {
		if errors.Is(err, errEmptyAnswer) {
			return
		}
		_, _ = m.Send(context.Background(), chat, i18n.Tf(lang, "file.save_failed", html.EscapeString(err.Error())), ports.Rich())
		return
	}
	ref, err := sendProjectFile(m, chat, workDir, lang, saved, "")
	if err != nil || onSent == nil {
		return
	}
	onSent(ref)
}

func selectDeliverable(workDir, requested string, observed []observedFile) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, item := range observed {
		base := filepath.Base(item.path)
		if requested != "" {
			if !strings.EqualFold(base, requested) {
				continue
			}
		} else if !item.written || !hasDocumentExt(base) {
			continue
		}
		abs, err := confineExistingFile(workDir, item.path)
		if err != nil {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
		if len(out) == maxDeliveredFiles {
			break
		}
	}
	return out
}

// materializeAnswerFile кладёт текст ответа в uploads/ и никогда не затирает
// исходник проекта: диалог не имеет права писать в дерево исходников.
func materializeAnswerFile(workDir, requestedName, answer string) (string, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", errEmptyAnswer
	}
	info, err := os.Stat(workDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("project directory is missing")
	}
	if int64(len(answer)) > maxTelegramFileSize {
		return "", fileTooLargeError{size: int64(len(answer))}
	}
	name := strings.TrimSpace(requestedName)
	if name == "" {
		name = "answer.md"
	}
	name = utils.SanitizeUploadFileName(name)
	if !hasDocumentExt(name) {
		name += ".md"
	}
	dir := filepath.Join(workDir, uploadsDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	dest := uniqueUploadPath(dir, name)
	rel, err := filepath.Rel(workDir, dest)
	if err != nil || !strings.HasPrefix(rel, uploadsDirName+string(filepath.Separator)) {
		return "", errOutsideProject
	}
	if err := os.WriteFile(dest, []byte(answer), 0644); err != nil {
		return "", err
	}
	return dest, nil
}

func uniqueUploadPath(dir, name string) string {
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		return dest
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		candidate := filepath.Join(dir, stem+"_"+strconv.Itoa(i)+ext)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return filepath.Join(dir, stem+"_extra"+ext)
}

// sendProjectFile читает файл проекта и отправляет его документом.
func sendProjectFile(m ports.Messenger, chat ports.ChatID, workDir, lang, rawPath, caption string) (ports.MessageRef, error) {
	abs, err := confineExistingFile(workDir, rawPath)
	if err != nil {
		notifyFileError(m, chat, lang, rawPath, err)
		return ports.MessageRef{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		notifyFileError(m, chat, lang, rawPath, err)
		return ports.MessageRef{}, err
	}
	name := filepath.Base(abs)
	ref, err := m.SendDocument(context.Background(), chat, ports.Document{
		FileName: name,
		MIME:     mimeByName(name),
		Caption:  clipCaption(caption, i18n.Tf(lang, "file.sent", name)),
		Content:  data,
	})
	if err != nil {
		notifyFileError(m, chat, lang, rawPath, err)
		return ports.MessageRef{}, err
	}
	return ref, nil
}

func clipCaption(caption, fallback string) string {
	caption = strings.TrimSpace(caption)
	if caption == "" {
		caption = fallback
	}
	return utils.TruncateString(caption, maxCaptionRunes)
}

func notifyFileError(m ports.Messenger, chat ports.ChatID, lang, raw string, err error) {
	if m == nil || err == nil {
		return
	}
	var text string
	var tooLarge fileTooLargeError
	switch {
	case errors.Is(err, errOutsideProject):
		text = i18n.Tf(lang, "file.outside_project", html.EscapeString(raw))
	case errors.As(err, &tooLarge):
		text = i18n.Tf(lang, "file.send_too_large", html.EscapeString(filepath.Base(raw)), formatFileSize(tooLarge.size))
	default:
		text = i18n.Tf(lang, "file.send_failed", html.EscapeString(err.Error()))
	}
	_, _ = m.Send(context.Background(), chat, text, ports.Rich())
}

func mimeByName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".log":
		return "text/plain"
	case ".md", ".markdown":
		return "text/markdown"
	case ".csv", ".tsv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".html":
		return "text/html"
	case ".pdf":
		return "application/pdf"
	case ".yml", ".yaml":
		return "application/yaml"
	case ".xml":
		return "application/xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}
