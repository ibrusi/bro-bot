package telegram

import (
	"testing"

	"tg-agent-bot/internal/ports"

	tele "gopkg.in/telebot.v3"
)

func TestParseChatID(t *testing.T) {
	id, err := parseChatID(ports.ChatID("12345"))
	if err != nil || id != 12345 {
		t.Fatalf("expected 12345, nil, got %d, %v", id, err)
	}

	if _, err := parseChatID(ports.ChatID("not-a-number")); err == nil {
		t.Fatalf("expected error for non-numeric chat id")
	}
}

func TestChatIDFromInt(t *testing.T) {
	if got := chatIDFromInt(9876); got != ports.ChatID("9876") {
		t.Errorf("expected \"9876\", got %q", got)
	}
}

func TestToParseMode(t *testing.T) {
	if got := toParseMode(ports.FormatRich); got != tele.ModeHTML {
		t.Errorf("expected ModeHTML for FormatRich, got %q", got)
	}
	if got := toParseMode(ports.FormatPlain); got != "" {
		t.Errorf("expected empty ParseMode for FormatPlain, got %q", got)
	}
}

func TestToReplyMarkup(t *testing.T) {
	if got := toReplyMarkup(nil); got != nil {
		t.Errorf("expected nil markup for nil keyboard, got %+v", got)
	}
	if got := toReplyMarkup(&ports.Keyboard{}); got != nil {
		t.Errorf("expected nil markup for empty keyboard, got %+v", got)
	}

	kb := &ports.Keyboard{Rows: [][]ports.Button{
		{
			{Text: "Callback", Action: "my_action", Payload: "42"},
			{Text: "Link", URL: "https://example.com"},
		},
	}}
	markup := toReplyMarkup(kb)
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("expected 1 row with 2 buttons, got %+v", markup)
	}
	cb := markup.InlineKeyboard[0][0]
	if cb.Unique != "my_action" || cb.Data != "42" {
		t.Errorf("expected callback button with unique=my_action data=42, got %+v", cb)
	}
	link := markup.InlineKeyboard[0][1]
	if link.URL != "https://example.com" {
		t.Errorf("expected URL button, got %+v", link)
	}
}

func TestToBotCommands(t *testing.T) {
	cmds := toBotCommands([]ports.BotCommand{
		{Name: "status", Description: "show status"},
	})
	if len(cmds) != 1 || cmds[0].Text != "status" || cmds[0].Description != "show status" {
		t.Errorf("unexpected conversion result: %+v", cmds)
	}
}

func TestBuildSendParams(t *testing.T) {
	if p := buildSendParams(nil); p.parseMode != "" || p.markup != nil || p.silent || p.replyTo != nil {
		t.Errorf("expected zero-value sendParams for nil opts, got %+v", p)
	}

	ref := ports.MessageRef{Chat: "1", ID: "77"}
	opts := &ports.SendOptions{
		Format:  ports.FormatRich,
		Silent:  true,
		ReplyTo: &ref,
	}
	p := buildSendParams(opts)
	if p.parseMode != tele.ModeHTML {
		t.Errorf("expected ModeHTML, got %q", p.parseMode)
	}
	if !p.silent {
		t.Errorf("expected silent=true")
	}
	if p.replyTo == nil || p.replyTo.ID != 77 {
		t.Errorf("expected replyTo.ID=77, got %+v", p.replyTo)
	}
}

func TestMsgRefRoundTrip(t *testing.T) {
	msg := &tele.Message{ID: 55, Chat: &tele.Chat{ID: 999}}
	ref := msgRef(msg)
	if ref.Chat != "999" || ref.ID != "55" {
		t.Errorf("unexpected MessageRef: %+v", ref)
	}

	if got := msgRef(nil); got != (ports.MessageRef{}) {
		t.Errorf("expected zero MessageRef for nil message, got %+v", got)
	}
}
