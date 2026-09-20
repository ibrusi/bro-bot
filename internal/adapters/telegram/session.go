package telegram

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"bro-bot/internal/ports"

	tele "gopkg.in/telebot.v3"
)

// session — реализация ports.Session поверх одного tele.Context.
type session struct {
	t *Transport
	c tele.Context
}

func newSession(t *Transport, c tele.Context) *session {
	return &session{t: t, c: c}
}

func (s *session) Chat() ports.ChatID {
	if chat := s.c.Chat(); chat != nil {
		return chatIDFromInt(chat.ID)
	}
	return ""
}

func (s *session) SenderID() string {
	if sender := s.c.Sender(); sender != nil {
		return strconv.FormatInt(sender.ID, 10)
	}
	return ""
}

func (s *session) Text() string {
	return s.c.Text()
}

func (s *session) Args() []string {
	return s.c.Args()
}

// Message возвращает данные входящего текстового сообщения, либо nil для апдейта-callback.
func (s *session) Message() *ports.IncomingMessage {
	if s.c.Callback() != nil {
		return nil
	}
	m := s.c.Message()
	if m == nil {
		return nil
	}
	msg := &ports.IncomingMessage{
		Chat:      s.Chat(),
		SenderID:  s.SenderID(),
		Text:      m.Text,
		MessageID: ports.MessageID(strconv.Itoa(m.ID)),
	}
	if m.ReplyTo != nil {
		ref := msgRef(m.ReplyTo)
		msg.ReplyTo = &ref
	}
	return msg
}

// Callback возвращает данные нажатой кнопки, либо nil для апдейта-сообщения.
func (s *session) Callback() *ports.CallbackQuery {
	cb := s.c.Callback()
	if cb == nil {
		return nil
	}
	q := &ports.CallbackQuery{
		ID:       cb.ID,
		Chat:     s.Chat(),
		SenderID: s.SenderID(),
		Payload:  strings.TrimSpace(cb.Data),
	}
	if cb.Message != nil {
		ref := msgRef(cb.Message)
		q.Message = &ref
		q.MessageText = cb.Message.Text
	}
	return q
}

// Voice возвращает данные голосового или аудиосообщения, либо nil для текстового апдейта или callback.
func (s *session) Voice() *ports.VoiceMessage {
	if s.c.Callback() != nil {
		return nil
	}
	m := s.c.Message()
	if m == nil {
		return nil
	}
	if m.Voice != nil {
		return &ports.VoiceMessage{
			FileID:   m.Voice.FileID,
			Duration: m.Voice.Duration,
			MIME:     m.Voice.MIME,
			Caption:  m.Voice.Caption,
			FileName: "voice.oga",
		}
	}
	if m.Audio != nil {
		fileName := m.Audio.FileName
		if fileName == "" {
			fileName = "audio.mp3"
		}
		return &ports.VoiceMessage{
			FileID:   m.Audio.FileID,
			Duration: m.Audio.Duration,
			MIME:     m.Audio.MIME,
			Caption:  m.Audio.Caption,
			FileName: fileName,
		}
	}
	return nil
}

// OpenVoice открывает поток чтения аудиофайла для текущего голосового сообщения.
func (s *session) OpenVoice(_ context.Context) (io.ReadCloser, error) {
	if s.c.Callback() != nil {
		return nil, errors.New("telegram: callback update has no voice file")
	}
	m := s.c.Message()
	if m == nil {
		return nil, errors.New("telegram: no message in session")
	}
	var file *tele.File
	if m.Voice != nil {
		file = &m.Voice.File
	} else if m.Audio != nil {
		file = &m.Audio.File
	} else {
		return nil, errors.New("telegram: message has no voice or audio payload")
	}
	return s.t.bot.File(file)
}

func (s *session) Messenger() ports.Messenger {
	return s.t
}

func (s *session) Send(text string, opts *ports.SendOptions) error {
	to, err := recipient(s.Chat())
	if err != nil {
		return err
	}
	_, err = sendSplit(s.t.bot, to, text, buildSendParams(opts))
	return err
}

// Reply отправляет ответ в чат сессии со ссылкой (reply_to) на исходное сообщение,
// если оно есть в апдейте.
func (s *session) Reply(text string, opts *ports.SendOptions) error {
	merged := &ports.SendOptions{}
	if opts != nil {
		merged = &ports.SendOptions{Format: opts.Format, Keyboard: opts.Keyboard, Silent: opts.Silent, ReplyTo: opts.ReplyTo}
	}
	if merged.ReplyTo == nil {
		if m := s.c.Message(); m != nil && s.c.Callback() == nil {
			ref := msgRef(m)
			merged.ReplyTo = &ref
		}
	}
	return s.Send(text, merged)
}

// Edit редактирует сообщение, к которому относится текущий апдейт (для callback —
// сообщение с нажатой кнопкой).
func (s *session) Edit(text string, opts *ports.SendOptions) error {
	m := s.c.Message()
	if m == nil {
		return errors.New("telegram: no message to edit")
	}
	ref := msgRef(m)
	ed, err := editable(ref)
	if err != nil {
		return err
	}
	to, err := recipient(ref.Chat)
	if err != nil {
		return err
	}
	_, err = editSplit(s.t.bot, ed, to, text, buildSendParams(opts))
	return err
}

func (s *session) SendDocument(doc ports.Document) error {
	to, err := recipient(s.Chat())
	if err != nil {
		return err
	}
	_, err = s.t.bot.Send(to, toDocument(doc))
	return err
}

// Respond отвечает на нажатие кнопки всплывающим уведомлением. Для апдейтов,
// не являющихся callback, это no-op.
func (s *session) Respond(text string) error {
	cb := s.c.Callback()
	if cb == nil {
		return nil
	}
	return s.t.bot.Respond(cb, &tele.CallbackResponse{Text: text})
}
