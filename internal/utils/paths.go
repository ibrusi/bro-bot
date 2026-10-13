package utils

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Ошибки проверки пути. Текст здесь служебный и одинаковый для всех языков:
// его читают логи и errors.Is, а пользователю показывается перевод, который
// слой представления подбирает по этим значениям.
var (
	ErrNameEmpty       = errors.New("path: name must not be empty")
	ErrNameControlChar = errors.New("path: name contains an invalid character")
	ErrNameLeadingDash = errors.New("path: name must not start with a dash")
	ErrNameReserved    = errors.New("path: reserved name")
	ErrNameSeparators  = errors.New("path: name must not contain slashes, colons or spaces")
	ErrNameCharset     = errors.New("path: name contains characters outside the allowed set")
	ErrRootMissing     = errors.New("path: root directory is not set")
	ErrPathAbsolute    = errors.New("path: absolute paths are not allowed")
	ErrPathEscape      = errors.New("path: resolved path leaves the allowed directory")
)

// validSegmentRegex — символы, допустимые в имени файла или каталога, пришедшем от пользователя.
var validSegmentRegex = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// SanitizeSegment проверяет один сегмент пути, полученный от пользователя.
// Возвращает очищенное имя либо одну из сентинел-ошибок пакета: показать её
// пользователю на его языке — задача слоя представления.
func SanitizeSegment(name string) (string, error) {
	clean := strings.TrimSpace(name)

	if clean == "" {
		return "", ErrNameEmpty
	}
	if strings.ContainsRune(clean, 0) {
		return "", ErrNameControlChar
	}
	// Ведущий дефис превратил бы имя в опцию командной строки.
	if strings.HasPrefix(clean, "-") {
		return "", ErrNameLeadingDash
	}
	if clean == "." || clean == ".." {
		return "", ErrNameReserved
	}
	if strings.ContainsAny(clean, "/\\: \t\r\n") {
		return "", ErrNameSeparators
	}
	if !validSegmentRegex.MatchString(clean) {
		return "", ErrNameCharset
	}
	return clean, nil
}

// SafeJoin присоединяет относительный путь rel к корню root так, чтобы результат
// гарантированно остался внутри root. Каждый сегмент rel проверяется через
// SanitizeSegment, поэтому "..", ведущие дефисы и обратные слэши не проходят.
//
// Вложенные пути допустимы: SafeJoin("/opt/scripts", "deploy/restart.sh") вернёт
// "/opt/scripts/deploy/restart.sh".
func SafeJoin(root, rel string) (string, error) {
	cleanRoot := filepath.Clean(strings.TrimSpace(root))
	if cleanRoot == "" || cleanRoot == "." {
		return "", ErrRootMissing
	}

	trimmed := strings.TrimSpace(rel)
	if trimmed == "" {
		return "", ErrNameEmpty
	}
	if filepath.IsAbs(trimmed) {
		return "", ErrPathAbsolute
	}

	segments := strings.Split(trimmed, "/")
	cleaned := make([]string, 0, len(segments))
	for _, segment := range segments {
		safe, err := SanitizeSegment(segment)
		if err != nil {
			return "", err
		}
		cleaned = append(cleaned, safe)
	}

	joined := filepath.Clean(filepath.Join(append([]string{cleanRoot}, cleaned...)...))

	// Разделитель в префиксе обязателен: без него каталог-сосед вида
	// "/opt/scripts-evil" прошёл бы проверку на корень "/opt/scripts".
	if !strings.HasPrefix(joined, cleanRoot+string(filepath.Separator)) {
		return "", ErrPathEscape
	}
	return joined, nil
}

// urlCredentialsRegex находит учётные данные в ссылке: схему, всё до "@" в пределах
// authority и сам "@". Часть [^\s/]* жадная, поэтому доходит до последнего "@" —
// пароль может содержать "@" и сам.
var urlCredentialsRegex = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^\s/]*@`)

// SafeJoinSegment — как SafeJoin, но имя обязано быть одним сегментом, без вложенности.
// Подходит для сущностей с плоскими именами: проекты лежат непосредственно в
// PROJECTS_ROOT, и "a/b" проектом не является.
func SafeJoinSegment(root, name string) (string, error) {
	segment, err := SanitizeSegment(name)
	if err != nil {
		return "", err
	}
	return SafeJoin(root, segment)
}

// RedactURLCredentials заменяет учётные данные в ссылках на ***, оставляя адрес
// читаемым. Нужна там, где URL репозитория уходит в чат или в лог: в HTTPS-ссылку
// часто встраивают токен доступа, и он не должен оседать в истории переписки.
//
// Работает и на отдельной ссылке, и на свободном тексте (git цитирует адрес в своих
// сообщениях об ошибках), потому что ищет именно пару «схема + userinfo + @».
//
// SCP-форма (git@github.com:owner/repo.git) не трогается: там имя пользователя
// транспорта, а не секрет. Ссылка без учётных данных возвращается без изменений.
//
// url.Parse здесь не годится: url.URL.String() percent-кодирует маскировку
// (*** превращается в %2A%2A%2A), а ссылку ещё показывать человеку.
func RedactURLCredentials(raw string) string {
	return urlCredentialsRegex.ReplaceAllString(raw, "${1}***@")
}

// SanitizeUploadFileName очищает имя файла от путей и опасных символов,
// поддерживая Unicode (включая кириллицу), цифры, дефисы, точки и подчёркивания.
func SanitizeUploadFileName(rawName string) string {
	clean := strings.TrimSpace(rawName)
	if clean == "" || strings.HasSuffix(clean, "/") || strings.HasSuffix(clean, "\\") {
		return "upload.bin"
	}
	clean = strings.ReplaceAll(clean, "\\", "/")
	base := filepath.Base(filepath.Clean(clean))
	base = strings.Trim(base, ". \t\r\n")
	base = strings.TrimLeft(base, "-") // защита от параметров командных строк

	var bldr strings.Builder
	for _, r := range base {
		if r == 0 || r < 32 || r == 127 {
			continue
		}
		if r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' || r == '/' || r == '\\' {
			bldr.WriteRune('_')
			continue
		}
		bldr.WriteRune(r)
	}
	res := bldr.String()
	if res == "" || res == "." || res == ".." {
		return "upload.bin"
	}
	return res
}

// FilesEqual сравнивает два файла побайтово блоками по 64 КБ.
// Перед чтением содержимого сравнивает их размеры: если размеры не совпадают,
// чтение содержимого даже не начинается.
func FilesEqual(path1, path2 string) (bool, error) {
	fi1, err := os.Stat(path1)
	if err != nil {
		return false, err
	}
	fi2, err := os.Stat(path2)
	if err != nil {
		return false, err
	}
	if fi1.Size() != fi2.Size() {
		return false, nil
	}
	if fi1.Size() == 0 {
		return true, nil
	}

	f1, err := os.Open(path1)
	if err != nil {
		return false, err
	}
	defer f1.Close()

	f2, err := os.Open(path2)
	if err != nil {
		return false, err
	}
	defer f2.Close()

	const bufSize = 64 * 1024
	buf1 := make([]byte, bufSize)
	buf2 := make([]byte, bufSize)

	for {
		n1, err1 := f1.Read(buf1)
		n2, err2 := f2.Read(buf2)

		if n1 != n2 || !bytes.Equal(buf1[:n1], buf2[:n2]) {
			return false, nil
		}

		if err1 != nil || err2 != nil {
			if err1 == io.EOF && err2 == io.EOF {
				return true, nil
			}
			if err1 == io.EOF || err2 == io.EOF {
				return false, nil
			}
			if err1 != nil {
				return false, err1
			}
			return false, err2
		}
	}
}

// GenerateUniqueFilePath проверяет наличие файла в dir и при коллизии возвращает путь с суффиксом _1, _2...
func GenerateUniqueFilePath(dir, fileName string) string {
	target := filepath.Join(dir, fileName)
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return target
	}

	ext := filepath.Ext(fileName)
	nameWithoutExt := strings.TrimSuffix(fileName, ext)

	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s_%d%s", nameWithoutExt, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s_%d%s", nameWithoutExt, time.Now().UnixNano(), ext))
}

// ResolveUploadDestination определяет финальный путь для загруженного файла.
// Если файл с таким именем уже существует, выполняется побайтовое сравнение:
// - если содержимое идентично, временный файл удаляется, возвращается существующий путь и isReused = true;
// - если содержимое отличается, ищется уникальное имя (суффикс _1, _2...) и временный файл переименовывается в него.
func ResolveUploadDestination(targetDir, fileName, tempPath string) (finalPath string, isReused bool, err error) {
	primaryPath := filepath.Join(targetDir, fileName)
	if _, err := os.Stat(primaryPath); os.IsNotExist(err) {
		if err := os.Rename(tempPath, primaryPath); err != nil {
			return "", false, err
		}
		return primaryPath, false, nil
	}

	// Файл с таким именем уже существует — проверяем на идентичность
	equal, err := FilesEqual(primaryPath, tempPath)
	if err == nil && equal {
		_ = os.Remove(tempPath)
		return primaryPath, true, nil
	}

	// Имя занято файлом с другим содержимым: проверяем существующие варианты _1, _2...
	ext := filepath.Ext(fileName)
	baseName := strings.TrimSuffix(fileName, ext)
	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(targetDir, fmt.Sprintf("%s_%d%s", baseName, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			if err := os.Rename(tempPath, candidate); err != nil {
				return "", false, err
			}
			return candidate, false, nil
		}
		// Если вариант существует, проверяем, не совпадает ли он с загруженным
		if eq, err := FilesEqual(candidate, tempPath); err == nil && eq {
			_ = os.Remove(tempPath)
			return candidate, true, nil
		}
	}

	fallbackPath := filepath.Join(targetDir, fmt.Sprintf("%s_%d%s", baseName, time.Now().UnixNano(), ext))
	if err := os.Rename(tempPath, fallbackPath); err != nil {
		return "", false, err
	}
	return fallbackPath, false, nil
}
