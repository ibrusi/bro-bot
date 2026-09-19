package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"context"
)

func buildLanguageMarkup() *ports.Keyboard {
	return &ports.Keyboard{
		Rows: [][]ports.Button{
			{
				{Text: "🇷🇺 Русский", Action: "lang_sel", Payload: "ru"},
				{Text: "🇬🇧 English", Action: "lang_sel", Payload: "en"},
			},
		},
	}
}

func handleLanguage(s ports.Session) error {
	lang := config.ProjectState.GetLanguage()
	return s.Send(i18n.T(lang, "CmdLanguageDesc"), ports.RichWith(buildLanguageMarkup()))
}

func onLanguageSel(s ports.Session) error {
	newLang := s.Callback().Payload
	if newLang != "ru" && newLang != "en" {
		newLang = "en"
	}

	config.ProjectState.SetLanguage(newLang)

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "bot_language", newLang)
	}

	_ = s.Respond(i18n.T(newLang, "CmdLanguage"))

	var msgKey string
	if newLang == "ru" {
		msgKey = "LangSetToRU"
	} else {
		msgKey = "LangSetToEN"
	}

	// Update bot commands with language code if transport supports it
	if err := s.Messenger().SetCommands(context.Background(), getDefaultCommands("en"), ""); err != nil {
		// Log silently
	}
	if err := s.Messenger().SetCommands(context.Background(), getDefaultCommands("ru"), "ru"); err != nil {
		// Log silently
	}

	return s.Send(i18n.T(newLang, msgKey), ports.Rich())
}
