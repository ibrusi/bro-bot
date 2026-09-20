package handlers

import (
	"context"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
)

// restoreLanguage возвращает язык интерфейса после теста: он глобальный,
// и оставленный «русский» сломал бы соседние тесты.
func restoreLanguage(t *testing.T) {
	t.Helper()
	previous := config.ProjectState.GetLanguage()
	t.Cleanup(func() { config.ProjectState.SetLanguage(previous) })
}

// TestDefaultLanguageIsEnglish — бот без выбранного языка отвечает по-английски.
func TestDefaultLanguageIsEnglish(t *testing.T) {
	mt := setupTestApp(t)
	restoreLanguage(t)

	if got := config.ProjectState.GetLanguage(); got != i18n.Default {
		t.Fatalf("язык по умолчанию = %q, ожидали %q", got, i18n.Default)
	}

	startHandler, ok := mt.commands["start"]
	if !ok {
		t.Fatal("команда /start не зарегистрирована")
	}
	if err := startHandler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/start: %v", err)
	}

	last := mt.LastSent()
	if last == nil {
		t.Fatal("ожидали приветствие от /start")
	}
	if !strings.Contains(last.Text, "Worker agent is ready") {
		t.Errorf("приветствие не на английском: %s", last.Text)
	}
}

// TestLanguageCommandListsEveryCatalog — клавиатура выбора строится по каталогам,
// поэтому новый язык появляется в ней без правок кода.
func TestLanguageCommandListsEveryCatalog(t *testing.T) {
	mt := setupTestApp(t)
	restoreLanguage(t)

	handler, ok := mt.commands["language"]
	if !ok {
		t.Fatal("команда /language не зарегистрирована")
	}
	if err := handler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/language: %v", err)
	}

	last := mt.LastSent()
	if last == nil || last.Opts == nil || last.Opts.Keyboard == nil {
		t.Fatal("ожидали сообщение с клавиатурой выбора языка")
	}

	payloads := map[string]string{}
	for _, row := range last.Opts.Keyboard.Rows {
		for _, btn := range row {
			payloads[btn.Payload] = btn.Text
		}
	}

	for _, lang := range i18n.Languages() {
		text, ok := payloads[lang.Code]
		if !ok {
			t.Errorf("в клавиатуре нет кнопки для языка %q", lang.Code)
			continue
		}
		if !strings.Contains(text, lang.Name) {
			t.Errorf("кнопка языка %q подписана как %q", lang.Code, text)
		}
	}

	if marked := payloads[i18n.Default]; !strings.HasPrefix(marked, "✅") {
		t.Errorf("активный язык должен быть отмечен, получили %q", marked)
	}
}

// TestLanguageSwitchAppliesAndPersists — выбор языка меняет ответы бота и
// переживает перезапуск, потому что сохраняется в базе.
func TestLanguageSwitchAppliesAndPersists(t *testing.T) {
	mt := setupTestApp(t)
	restoreLanguage(t)

	selectLanguage(t, mt, "ru")

	if got := config.ProjectState.GetLanguage(); got != "ru" {
		t.Fatalf("после выбора язык = %q, ожидали %q", got, "ru")
	}

	last := mt.LastSent()
	if last == nil || !strings.Contains(last.Text, "изменён на русский") {
		t.Errorf("ожидали подтверждение на русском, получили %v", last)
	}

	st := domain.GlobalTaskManager.Storage()
	if st == nil {
		t.Fatal("ожидали хранилище у менеджера задач")
	}
	saved, err := st.GetSetting(context.Background(), "bot_language")
	if err != nil {
		t.Fatalf("чтение сохранённого языка: %v", err)
	}
	if saved != "ru" {
		t.Errorf("в базе сохранено %q, ожидали %q", saved, "ru")
	}

	// Ответы бота идут на выбранном языке.
	tasksHandler, ok := mt.commands["tasks"]
	if !ok {
		t.Fatal("команда /tasks не зарегистрирована")
	}
	if err := tasksHandler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/tasks: %v", err)
	}
	if text := mt.LastSent(); text == nil || !strings.Contains(text.Text, "Список задач") {
		t.Errorf("ответ /tasks не на русском: %v", text)
	}
}

// TestUnknownLanguageFallsBackToDefault — код языка приходит из кнопки и из базы,
// поэтому незнакомое значение не должно оставлять бота без языка.
func TestUnknownLanguageFallsBackToDefault(t *testing.T) {
	mt := setupTestApp(t)
	restoreLanguage(t)

	selectLanguage(t, mt, "klingon")

	if got := config.ProjectState.GetLanguage(); got != i18n.Default {
		t.Errorf("незнакомый язык должен откатываться на %q, получили %q", i18n.Default, got)
	}
}

// TestCommandMenuRegisteredForEveryLanguage — меню подсказок публикуется и для
// набора по умолчанию, и для каждого каталога.
func TestCommandMenuRegisteredForEveryLanguage(t *testing.T) {
	mt := setupTestApp(t)

	registered := map[string]bool{}
	for _, call := range mt.Messenger.CommandCalls() {
		registered[call.LanguageCode] = true
	}

	if !registered[""] {
		t.Error("меню по умолчанию не зарегистрировано")
	}
	for _, lang := range i18n.Codes() {
		if !registered[lang] {
			t.Errorf("меню для языка %q не зарегистрировано", lang)
		}
	}
}

// selectLanguage нажимает кнопку выбора языка.
func selectLanguage(t *testing.T, mt *mockTransport, code string) {
	t.Helper()

	handler, ok := mt.callbacks["lang_sel"]
	if !ok {
		t.Fatal("обработчик lang_sel не зарегистрирован")
	}
	sess := adminSession(mt, &mock.Session{
		CB: &ports.CallbackQuery{ID: "lang-cb", Action: "lang_sel", Payload: code},
	})
	if err := handler(sess); err != nil {
		t.Fatalf("выбор языка %q: %v", code, err)
	}
}
