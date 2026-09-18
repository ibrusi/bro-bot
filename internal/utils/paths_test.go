package utils

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeSegment(t *testing.T) {
	valid := []string{"myrepo", "bro-bot", "deploy_v2", "restart.sh", "a"}
	for _, name := range valid {
		got, err := SanitizeSegment(name)
		if err != nil {
			t.Errorf("SanitizeSegment(%q) вернул ошибку: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("SanitizeSegment(%q) = %q", name, got)
		}
	}

	invalid := map[string]string{
		"пустое":         "",
		"только пробелы": "   ",
		"точка":          ".",
		"две точки":      "..",
		"ведущий дефис":  "-rf",
		"слэш":           "a/b",
		"обратный слэш":  `a\b`,
		"двоеточие":      "host:path",
		"пробел внутри":  "my repo",
		"нулевой байт":   "repo\x00",
		"кириллица":      "проект",
		"звёздочка":      "repo*",
		"перевод строки": "repo\nls",
	}
	for what, name := range invalid {
		if _, err := SanitizeSegment(name); err == nil {
			t.Errorf("SanitizeSegment(%q) (%s) должен был вернуть ошибку", name, what)
		}
	}
}

func TestSafeJoinStaysInsideRoot(t *testing.T) {
	root := "/opt/scripts"

	allowed := map[string]string{
		"restart.sh":        "/opt/scripts/restart.sh",
		"deploy/restart.sh": "/opt/scripts/deploy/restart.sh",
		"a/b/c.sh":          "/opt/scripts/a/b/c.sh",
	}
	for rel, want := range allowed {
		got, err := SafeJoin(root, rel)
		if err != nil {
			t.Errorf("SafeJoin(%q, %q) вернул ошибку: %v", root, rel, err)
			continue
		}
		if got != want {
			t.Errorf("SafeJoin(%q, %q) = %q, ожидали %q", root, rel, got, want)
		}
	}

	// Каталог-сосед — та самая дыра, которую давала проверка префикса без разделителя:
	// filepath.Clean("/opt/scripts/../scripts-evil/x") = "/opt/scripts-evil/x",
	// и strings.HasPrefix(..., "/opt/scripts") вернул бы true.
	rejected := map[string]string{
		"выход вверх":          "../../etc/passwd",
		"каталог-сосед":        "../scripts-evil/payload.sh",
		"две точки в середине": "deploy/../../etc/passwd",
		"абсолютный путь":      "/etc/passwd",
		"ведущий дефис":        "-rf",
		"пустое":               "",
		"только слэш":          "/",
		"пустой сегмент":       "deploy//restart.sh",
		"текущий каталог":      "./restart.sh",
	}
	for what, rel := range rejected {
		got, err := SafeJoin(root, rel)
		if err == nil {
			t.Errorf("SafeJoin(%q, %q) (%s) должен был вернуть ошибку, получили %q", root, rel, what, got)
		}
	}
}

// TestSafeJoinSegmentRejectsNesting — имена проектов плоские: "a/b" проектом не является,
// хотя для скриптов вложенный путь допустим.
func TestSafeJoinSegmentRejectsNesting(t *testing.T) {
	root := "/home/deploy/projects"

	got, err := SafeJoinSegment(root, "myrepo")
	if err != nil {
		t.Fatalf("SafeJoinSegment: %v", err)
	}
	if got != "/home/deploy/projects/myrepo" {
		t.Errorf("SafeJoinSegment = %q", got)
	}

	for _, name := range []string{"a/b", "../escape", "deploy/restart.sh", ""} {
		if _, err := SafeJoinSegment(root, name); err == nil {
			t.Errorf("SafeJoinSegment(%q) должен был вернуть ошибку", name)
		}
	}

	// Тот же вложенный путь через SafeJoin проходит — разница намеренная.
	if _, err := SafeJoin(root, "a/b"); err != nil {
		t.Errorf("SafeJoin должен допускать вложенность: %v", err)
	}
}

func TestSafeJoinRequiresRoot(t *testing.T) {
	if _, err := SafeJoin("", "repo"); err == nil {
		t.Error("без корневого каталога ожидали ошибку")
	}
}

// TestSafeJoinRelativeRoot — корень может быть относительным (в тестах это t.TempDir()
// или каталог проекта), результат всё равно обязан оставаться внутри него.
func TestSafeJoinRelativeRoot(t *testing.T) {
	root := t.TempDir()

	got, err := SafeJoin(root, "myrepo")
	if err != nil {
		t.Fatalf("SafeJoin: %v", err)
	}
	if want := filepath.Join(root, "myrepo"); got != want {
		t.Errorf("SafeJoin = %q, ожидали %q", got, want)
	}
	if _, err := SafeJoin(root, "../escape"); err == nil {
		t.Error("выход за пределы временного каталога должен отклоняться")
	}
}

func TestRedactURLCredentials(t *testing.T) {
	cases := map[string]string{
		// Токен в HTTPS-ссылке — ровно то, что бот предлагает в подсказке к /clone.
		"https://ghp_SecretToken123@github.com/owner/repo.git": "https://***@github.com/owner/repo.git",
		"https://user:p@ssw0rd@gitlab.com/o/r.git":             "https://***@gitlab.com/o/r.git",
		// Без учётных данных ссылка не меняется.
		"https://github.com/owner/repo.git": "https://github.com/owner/repo.git",
		"http://example.com/r.git":          "http://example.com/r.git",
		// SCP-форма: git@ — это имя пользователя транспорта, не секрет.
		"git@github.com:owner/repo.git":       "git@github.com:owner/repo.git",
		"ssh://git@github.com/owner/repo.git": "ssh://***@github.com/owner/repo.git",
		// Мусор не должен ронять функцию.
		"":          "",
		"не ссылка": "не ссылка",
	}

	for raw, want := range cases {
		if got := RedactURLCredentials(raw); got != want {
			t.Errorf("RedactURLCredentials(%q) = %q, ожидали %q", raw, got, want)
		}
	}
}

// TestRedactURLCredentialsInText — git цитирует адрес репозитория в сообщениях об
// ошибках, поэтому чистить приходится не только отдельную ссылку, но и вывод команды.
func TestRedactURLCredentialsInText(t *testing.T) {
	out := "remote: Invalid username or password.\n" +
		"fatal: Authentication failed for 'https://ghp_SecretToken123@github.com/owner/repo.git/'\n"

	got := RedactURLCredentials(out)

	if strings.Contains(got, "ghp_SecretToken123") {
		t.Errorf("токен остался в выводе git: %s", got)
	}
	if !strings.Contains(got, "https://***@github.com/owner/repo.git/") {
		t.Errorf("адрес должен остаться читаемым: %s", got)
	}
	if !strings.Contains(got, "remote: Invalid username or password.") {
		t.Errorf("остальной текст не должен меняться: %s", got)
	}
}

// TestRedactURLCredentialsNeverLeaksToken — главное свойство: что бы ни пришло,
// секрет из userinfo не должен остаться в результате.
func TestRedactURLCredentialsNeverLeaksToken(t *testing.T) {
	const token = "ghp_SecretToken123"
	inputs := []string{
		"https://" + token + "@github.com/o/r.git",
		"https://user:" + token + "@github.com/o/r.git",
		"https://" + token + "@github.com:8443/o/r.git",
		"https://" + token + "@github.com",
		"https://" + token + "@github.com/o/r.git?ref=main",
		"https://" + token + "@[::1]/o/r.git",
	}
	for _, in := range inputs {
		if got := RedactURLCredentials(in); strings.Contains(got, token) {
			t.Errorf("токен утёк: RedactURLCredentials(%q) = %q", in, got)
		}
	}
}
