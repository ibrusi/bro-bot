package handlers

import (
	"context"

	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
)

// languageButtonsPerRow — сколько кнопок выбора языка помещается в один ряд.
const languageButtonsPerRow = 2

// buildLanguageMarkup собирает клавиатуру выбора языка по списку загруженных
// каталогов: новый язык появляется в ней сам, как только рядом ляжет его файл
// локали. Активный язык помечен галочкой.
func buildLanguageMarkup(current string) *ports.Keyboard {
	languages := i18n.Languages()
	rows := make([][]ports.Button, 0, (len(languages)+languageButtonsPerRow-1)/languageButtonsPerRow)

	var row []ports.Button
	for _, lang := range languages {
		label := lang.Label()
		if lang.Code == current {
			label = "✅ " + label
		}
		row = append(row, ports.Button{Text: label, Action: "lang_sel", Payload: lang.Code})
		if len(row) == languageButtonsPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	return &ports.Keyboard{Rows: rows}
}

// handleLanguage — обработчик команды /language.
func handleLanguage(s ports.Session) error {
	lang := config.ProjectState.GetLanguage()
	return s.Send(i18n.T(lang, "lang.choose"), ports.RichWith(buildLanguageMarkup(lang)))
}

// onLanguageSel — обработчик кнопки lang_sel.
func onLanguageSel(s ports.Session) error {
	newLang := config.ProjectState.SetLanguage(s.Callback().Payload)

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "bot_language", newLang)
	}

	_ = s.Respond(i18n.T(newLang, "lang.title"))

	// Меню подсказок мессенджера перерегистрируем целиком: описания команд
	// хранятся у него по коду языка, и после появления нового каталога в меню
	// должен появиться и он.
	registerBotCommands(s.Messenger())

	return s.Send(i18n.T(newLang, "lang.changed"), ports.Rich())
}
