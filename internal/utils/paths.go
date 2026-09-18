package utils

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// validSegmentRegex — символы, допустимые в имени файла или каталога, пришедшем от пользователя.
var validSegmentRegex = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// SanitizeSegment проверяет один сегмент пути, полученный от пользователя.
// Возвращает очищенное имя либо ошибку с объяснением на русском — её можно
// показывать пользователю как есть.
func SanitizeSegment(name string) (string, error) {
	clean := strings.TrimSpace(name)

	if clean == "" {
		return "", fmt.Errorf("имя не может быть пустым")
	}
	if strings.ContainsRune(clean, 0) {
		return "", fmt.Errorf("имя содержит недопустимый символ")
	}
	// Ведущий дефис превратил бы имя в опцию командной строки.
	if strings.HasPrefix(clean, "-") {
		return "", fmt.Errorf("имя не может начинаться с дефиса")
	}
	if clean == "." || clean == ".." {
		return "", fmt.Errorf("недопустимое имя")
	}
	if strings.ContainsAny(clean, "/\\: \t\r\n") {
		return "", fmt.Errorf("имя не должно содержать слэши, двоеточия или пробелы")
	}
	if !validSegmentRegex.MatchString(clean) {
		return "", fmt.Errorf("имя содержит недопустимые символы. Разрешены только буквы, цифры, дефис, подчёркивание и точка")
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
		return "", fmt.Errorf("корневой каталог не задан")
	}

	trimmed := strings.TrimSpace(rel)
	if trimmed == "" {
		return "", fmt.Errorf("имя не может быть пустым")
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("абсолютные пути недопустимы")
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
		return "", fmt.Errorf("путь выходит за пределы разрешённого каталога")
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
