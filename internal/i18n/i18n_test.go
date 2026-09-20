package i18n

import (
	"strings"
	"testing"
)

// TestDefaultLanguageIsEnglish фиксирует язык, на котором бот отвечает до выбора пользователя.
func TestDefaultLanguageIsEnglish(t *testing.T) {
	if Default != "en" {
		t.Fatalf("язык по умолчанию = %q, ожидался %q", Default, "en")
	}
	if Normalize("") != "en" {
		t.Fatalf("Normalize(\"\") = %q, ожидался %q", Normalize(""), "en")
	}
	if Normalize("klingon") != "en" {
		t.Fatalf("неизвестный язык должен откатываться на %q, получили %q", "en", Normalize("klingon"))
	}
}

// TestCatalogsAreComplete — главная проверка при добавлении нового языка: в каждом
// каталоге должны быть все ключи языка по умолчанию, иначе часть интерфейса молча
// останется английской.
func TestCatalogsAreComplete(t *testing.T) {
	want := Keys(Default)
	if len(want) == 0 {
		t.Fatal("каталог языка по умолчанию пуст")
	}

	for _, lang := range Codes() {
		if lang == Default {
			continue
		}
		var missing []string
		for _, key := range want {
			if !Has(lang, key) {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			t.Errorf("в каталоге %q нет переводов для ключей: %s", lang, strings.Join(missing, ", "))
		}
	}
}

// TestCatalogsHaveNoExtraKeys ловит опечатки в ключах: ключ, которого нет в языке
// по умолчанию, не будет использован никогда.
func TestCatalogsHaveNoExtraKeys(t *testing.T) {
	known := make(map[string]bool)
	for _, key := range Keys(Default) {
		known[key] = true
	}

	for _, lang := range Codes() {
		if lang == Default {
			continue
		}
		for _, key := range Keys(lang) {
			if !known[key] {
				t.Errorf("в каталоге %q есть ключ %q, которого нет в языке по умолчанию", lang, key)
			}
		}
	}
}

// TestPlaceholdersMatch: перевод с другим набором подстановок ломает fmt.Sprintf
// в рантайме, поэтому сверяем их количество и порядок с языком по умолчанию.
func TestPlaceholdersMatch(t *testing.T) {
	for _, key := range Keys(Default) {
		want := verbs(T(Default, key))
		for _, lang := range Codes() {
			if lang == Default || !Has(lang, key) {
				continue
			}
			got := verbs(T(lang, key))
			if !equalStrings(want, got) {
				t.Errorf("ключ %q: подстановки в %q = %v, в %q = %v", key, lang, got, Default, want)
			}
		}
	}
}

// TestTaskStatusesAreTranslated проверяет, что у каждого статуса задачи есть перевод.
func TestTaskStatusesAreTranslated(t *testing.T) {
	for _, key := range TaskStatusKeys() {
		for _, lang := range Codes() {
			if !Has(lang, key) {
				t.Errorf("в каталоге %q нет перевода статуса %q", lang, key)
			}
		}
	}
}

// TestLanguagesStartWithDefault фиксирует порядок языков: он определяет порядок
// кнопок в команде /language.
func TestLanguagesStartWithDefault(t *testing.T) {
	langs := Languages()
	if len(langs) < 2 {
		t.Fatalf("ожидали минимум два языка, получили %d", len(langs))
	}
	if langs[0].Code != Default {
		t.Errorf("первым должен идти язык по умолчанию, получили %q", langs[0].Code)
	}
	for _, l := range langs {
		if l.Name == "" || l.Flag == "" {
			t.Errorf("у языка %q не заполнены name/flag", l.Code)
		}
	}
}

func TestNormalizeStripsRegion(t *testing.T) {
	if got := Normalize("RU-ru"); got != "ru" {
		t.Errorf("Normalize(\"RU-ru\") = %q, ожидался %q", got, "ru")
	}
	if got := Normalize("en_US"); got != "en" {
		t.Errorf("Normalize(\"en_US\") = %q, ожидался %q", got, "en")
	}
}

func TestFallbackToDefaultAndKey(t *testing.T) {
	if got := T("ru", "definitely.missing.key"); got != "definitely.missing.key" {
		t.Errorf("отсутствующий ключ должен возвращаться как есть, получили %q", got)
	}
	if got := For("ru").T("status.queued"); got == "status.queued" {
		t.Error("перевод существующего ключа не должен совпадать с ключом")
	}
}

// verbs выделяет глаголы формата (%s, %d, %.1f и т. п.), пропуская экранированный %%.
func verbs(template string) []string {
	var found []string
	for i := 0; i < len(template); i++ {
		if template[i] != '%' {
			continue
		}
		if i+1 < len(template) && template[i+1] == '%' {
			i++
			continue
		}
		j := i + 1
		for j < len(template) && strings.ContainsRune("+-# 0123456789.", rune(template[j])) {
			j++
		}
		if j < len(template) {
			found = append(found, template[i:j+1])
			i = j
		}
	}
	return found
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
