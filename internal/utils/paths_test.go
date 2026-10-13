package utils

import (
	"os"
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

func TestSanitizeUploadFileName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"normal.txt", "normal.txt"},
		{"../../etc/passwd", "passwd"},
		{"..\\windows\\system32\\calc.exe", "calc.exe"},
		{"-flag.txt", "flag.txt"},
		{"---important.pdf", "important.pdf"},
		{"документ_2026.docx", "документ_2026.docx"},
		{"отчёт о расходах.xlsx", "отчёт о расходах.xlsx"},
		{"hello:world*test?.log", "hello_world_test_.log"},
		{"", "upload.bin"},
		{"   ", "upload.bin"},
		{".", "upload.bin"},
		{"..", "upload.bin"},
		{"...", "upload.bin"},
		{"/root/path/", "upload.bin"},
	}

	for _, tc := range cases {
		got := SanitizeUploadFileName(tc.input)
		if got != tc.want {
			t.Errorf("SanitizeUploadFileName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFilesEqual(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "f1.txt")
	file2 := filepath.Join(tmpDir, "f2.txt")
	file3 := filepath.Join(tmpDir, "f3.txt")
	fileEmpty1 := filepath.Join(tmpDir, "empty1.txt")
	fileEmpty2 := filepath.Join(tmpDir, "empty2.txt")

	contentA := strings.Repeat("Hello World! 1234567890\n", 4000) // ~100KB
	contentB := contentA
	contentC := contentA + "diff"

	if err := os.WriteFile(file1, []byte(contentA), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte(contentB), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file3, []byte(contentC), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileEmpty1, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileEmpty2, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Identical large files
	eq, err := FilesEqual(file1, file2)
	if err != nil || !eq {
		t.Errorf("FilesEqual(f1, f2) = %v, err: %v, expected true", eq, err)
	}

	// Different sizes
	eq, err = FilesEqual(file1, file3)
	if err != nil || eq {
		t.Errorf("FilesEqual(f1, f3) = %v, err: %v, expected false", eq, err)
	}

	// Same size but different content
	file4 := filepath.Join(tmpDir, "f4.txt")
	contentD := contentA[:len(contentA)-5] + "XXXXX"
	if err := os.WriteFile(file4, []byte(contentD), 0644); err != nil {
		t.Fatal(err)
	}
	eq, err = FilesEqual(file1, file4)
	if err != nil || eq {
		t.Errorf("FilesEqual(f1, f4) = %v, err: %v, expected false", eq, err)
	}

	// Empty files
	eq, err = FilesEqual(fileEmpty1, fileEmpty2)
	if err != nil || !eq {
		t.Errorf("FilesEqual(empty1, empty2) = %v, err: %v, expected true", eq, err)
	}

	// Empty vs non-empty
	eq, err = FilesEqual(fileEmpty1, file1)
	if err != nil || eq {
		t.Errorf("FilesEqual(empty1, f1) = %v, err: %v, expected false", eq, err)
	}

	// Non-existent file
	_, err = FilesEqual(file1, filepath.Join(tmpDir, "nonexistent.txt"))
	if err == nil {
		t.Errorf("expected error for non-existent file")
	}
}

func TestResolveUploadDestination(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. New file
	tmp1 := filepath.Join(tmpDir, "tmp1.txt")
	if err := os.WriteFile(tmp1, []byte("first content"), 0644); err != nil {
		t.Fatal(err)
	}
	final1, isReused, err := ResolveUploadDestination(tmpDir, "doc.txt", tmp1)
	if err != nil {
		t.Fatal(err)
	}
	if isReused {
		t.Errorf("expected isReused=false for new file")
	}
	if final1 != filepath.Join(tmpDir, "doc.txt") {
		t.Errorf("expected path %s, got %s", filepath.Join(tmpDir, "doc.txt"), final1)
	}
	if _, err := os.Stat(tmp1); !os.IsNotExist(err) {
		t.Errorf("tmp file should be moved/renamed")
	}

	// 2. Identical file upload (should be reused without duplicates)
	tmp2 := filepath.Join(tmpDir, "tmp2.txt")
	if err := os.WriteFile(tmp2, []byte("first content"), 0644); err != nil {
		t.Fatal(err)
	}
	final2, isReused, err := ResolveUploadDestination(tmpDir, "doc.txt", tmp2)
	if err != nil {
		t.Fatal(err)
	}
	if !isReused {
		t.Errorf("expected isReused=true for identical file")
	}
	if final2 != filepath.Join(tmpDir, "doc.txt") {
		t.Errorf("expected path %s, got %s", filepath.Join(tmpDir, "doc.txt"), final2)
	}
	if _, err := os.Stat(tmp2); !os.IsNotExist(err) {
		t.Errorf("tmp file should be deleted")
	}

	// 3. Different content with same filename (should save as doc_1.txt)
	tmp3 := filepath.Join(tmpDir, "tmp3.txt")
	if err := os.WriteFile(tmp3, []byte("second different content"), 0644); err != nil {
		t.Fatal(err)
	}
	final3, isReused, err := ResolveUploadDestination(tmpDir, "doc.txt", tmp3)
	if err != nil {
		t.Fatal(err)
	}
	if isReused {
		t.Errorf("expected isReused=false for modified file")
	}
	if final3 != filepath.Join(tmpDir, "doc_1.txt") {
		t.Errorf("expected path %s, got %s", filepath.Join(tmpDir, "doc_1.txt"), final3)
	}

	// 4. Identical to doc_1.txt (should reuse doc_1.txt!)
	tmp4 := filepath.Join(tmpDir, "tmp4.txt")
	if err := os.WriteFile(tmp4, []byte("second different content"), 0644); err != nil {
		t.Fatal(err)
	}
	final4, isReused, err := ResolveUploadDestination(tmpDir, "doc.txt", tmp4)
	if err != nil {
		t.Fatal(err)
	}
	if !isReused {
		t.Errorf("expected isReused=true when matching doc_1.txt")
	}
	if final4 != filepath.Join(tmpDir, "doc_1.txt") {
		t.Errorf("expected path %s, got %s", filepath.Join(tmpDir, "doc_1.txt"), final4)
	}
}
