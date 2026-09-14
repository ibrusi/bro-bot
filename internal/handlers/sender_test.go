package handlers

import (
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestExtractSendOptions(t *testing.T) {
	menu := &tele.ReplyMarkup{}
	btn := menu.Data("Test", "test_btn")
	menu.Inline(menu.Row(btn))

	opts := []interface{}{
		tele.ModeHTML,
		menu,
		tele.Silent,
	}

	parseMode, markup, intermediate, last := extractSendOptions(opts)

	if parseMode != tele.ModeHTML {
		t.Errorf("expected parseMode ModeHTML, got %v", parseMode)
	}
	if markup != menu {
		t.Errorf("expected markup to be extracted")
	}

	// Intermediate options must NOT contain ReplyMarkup
	for _, opt := range intermediate {
		if _, ok := opt.(*tele.ReplyMarkup); ok {
			t.Errorf("intermediate options must not contain ReplyMarkup")
		}
	}

	// Last options MUST contain ReplyMarkup
	hasMarkup := false
	for _, opt := range last {
		if _, ok := opt.(*tele.ReplyMarkup); ok {
			hasMarkup = true
			break
		}
	}
	if !hasMarkup {
		t.Errorf("last options must contain ReplyMarkup")
	}
}

func TestExtractSendOptions_WithSendOptionsStruct(t *testing.T) {
	menu := &tele.ReplyMarkup{}
	so := &tele.SendOptions{
		ParseMode:   tele.ModeHTML,
		ReplyMarkup: menu,
	}

	opts := []interface{}{so}
	parseMode, markup, intermediate, last := extractSendOptions(opts)

	if parseMode != tele.ModeHTML {
		t.Errorf("expected parseMode ModeHTML, got %v", parseMode)
	}
	if markup != menu {
		t.Errorf("expected markup to be extracted")
	}

	if len(intermediate) != 1 {
		t.Fatalf("expected 1 intermediate option, got %d", len(intermediate))
	}
	interSO, ok := intermediate[0].(*tele.SendOptions)
	if !ok {
		t.Fatalf("expected *tele.SendOptions in intermediate, got %T", intermediate[0])
	}
	if interSO.ReplyMarkup != nil {
		t.Errorf("intermediate SendOptions must have nil ReplyMarkup")
	}

	lastSO, ok := last[0].(*tele.SendOptions)
	if !ok || lastSO.ReplyMarkup == nil {
		t.Errorf("last SendOptions must preserve ReplyMarkup")
	}
}

func TestStripParseMode(t *testing.T) {
	opts := []interface{}{
		tele.ModeHTML,
		tele.Silent,
		&tele.SendOptions{ParseMode: tele.ModeHTML, DisableNotification: true},
	}

	stripped := stripParseMode(opts)
	for _, opt := range stripped {
		if pm, ok := opt.(tele.ParseMode); ok && pm != "" {
			t.Errorf("unexpected parseMode in stripped: %v", pm)
		}
		if so, ok := opt.(*tele.SendOptions); ok && so.ParseMode != "" {
			t.Errorf("unexpected parseMode in stripped SendOptions: %v", so.ParseMode)
		}
	}
}
