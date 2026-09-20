package telegram

import (
	"bytes"
	"fmt"
	"strconv"

	"bro-bot/internal/ports"

	tele "gopkg.in/telebot.v3"
)

// parseChatID разбирает строковый ports.ChatID в числовой Telegram chat ID.
func parseChatID(chat ports.ChatID) (int64, error) {
	id, err := strconv.ParseInt(string(chat), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("telegram: invalid chat id %q: %w", chat, err)
	}
	return id, nil
}

// chatIDFromInt конвертирует числовой Telegram chat ID в ports.ChatID.
func chatIDFromInt(id int64) ports.ChatID {
	return ports.ChatID(strconv.FormatInt(id, 10))
}

// recipient возвращает tele.Recipient для отправки в чат.
func recipient(chat ports.ChatID) (tele.Recipient, error) {
	id, err := parseChatID(chat)
	if err != nil {
		return nil, err
	}
	return tele.ChatID(id), nil
}

// editable возвращает tele.Editable для редактирования ранее отправленного сообщения.
func editable(ref ports.MessageRef) (tele.Editable, error) {
	chatID, err := parseChatID(ref.Chat)
	if err != nil {
		return nil, err
	}
	return tele.StoredMessage{MessageID: string(ref.ID), ChatID: chatID}, nil
}

// msgRef строит ports.MessageRef из отправленного telebot-сообщения.
func msgRef(msg *tele.Message) ports.MessageRef {
	if msg == nil {
		return ports.MessageRef{}
	}
	chatID := int64(0)
	if msg.Chat != nil {
		chatID = msg.Chat.ID
	}
	return ports.MessageRef{
		Chat: chatIDFromInt(chatID),
		ID:   ports.MessageID(strconv.Itoa(msg.ID)),
	}
}

// toParseMode конвертирует канонический ports.Format в tele.ParseMode.
// FormatRich — единственное поддерживаемое здесь rich-подмножество (b/i/code/pre/a) — отправляется как есть в tele.ModeHTML.
func toParseMode(f ports.Format) tele.ParseMode {
	if f == ports.FormatRich {
		return tele.ModeHTML
	}
	return ""
}

// toReplyMarkup строит инлайн-клавиатуру Telegram из нейтрального ports.Keyboard.
func toReplyMarkup(kb *ports.Keyboard) *tele.ReplyMarkup {
	if kb == nil || len(kb.Rows) == 0 {
		return nil
	}
	markup := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, row := range kb.Rows {
		var btns []tele.Btn
		for _, btn := range row {
			if btn.URL != "" {
				btns = append(btns, markup.URL(btn.Text, btn.URL))
			} else {
				btns = append(btns, markup.Data(btn.Text, btn.Action, btn.Payload))
			}
		}
		rows = append(rows, markup.Row(btns...))
	}
	markup.Inline(rows...)
	return markup
}

// toDocument конвертирует нейтральный ports.Document в *tele.Document.
func toDocument(doc ports.Document) *tele.Document {
	return &tele.Document{
		File:     tele.FromReader(bytes.NewReader(doc.Content)),
		FileName: doc.FileName,
		MIME:     doc.MIME,
		Caption:  doc.Caption,
	}
}

// toBotCommands конвертирует список ports.BotCommand в формат telebot.
func toBotCommands(cmds []ports.BotCommand) []tele.Command {
	res := make([]tele.Command, 0, len(cmds))
	for _, c := range cmds {
		res = append(res, tele.Command{Text: c.Name, Description: c.Description})
	}
	return res
}

// buildSendParams извлекает telegram-specific параметры отправки из нейтрального ports.SendOptions.
func buildSendParams(opts *ports.SendOptions) sendParams {
	if opts == nil {
		return sendParams{}
	}
	return sendParams{
		parseMode: toParseMode(opts.Format),
		markup:    toReplyMarkup(opts.Keyboard),
		silent:    opts.Silent,
		replyTo:   replyToMessage(opts.ReplyTo),
	}
}

// replyToMessage строит минимальный *tele.Message, достаточный для reply_to_message_id
// (telebot использует из него только поле ID).
func replyToMessage(ref *ports.MessageRef) *tele.Message {
	if ref == nil {
		return nil
	}
	id, err := strconv.Atoi(string(ref.ID))
	if err != nil {
		return nil
	}
	return &tele.Message{ID: id}
}
