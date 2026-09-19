// Package mock содержит потокобезопасную in-memory реализацию ports.Messenger и
// ports.Session для юнит-тестов хендлеров — без обращения к реальному API мессенджера.
package mock

import (
	"context"
	"strconv"
	"sync"

	"bro-bot/internal/ports"
)

// SentMessage — одна запись из истории Messenger.Send.
type SentMessage struct {
	Chat ports.ChatID
	Text string
	Opts *ports.SendOptions
}

// EditedMessage — одна запись из истории Messenger.Edit.
type EditedMessage struct {
	Ref  ports.MessageRef
	Text string
	Opts *ports.SendOptions
}

// SentDocument — одна запись из истории Messenger.SendDocument.
type SentDocument struct {
	Chat ports.ChatID
	Doc  ports.Document
}

// AnsweredCallback — одна запись из истории Messenger.AnswerCallback.
type AnsweredCallback struct {
	CallbackID string
	Text       string
}

// Messenger — ничего никуда не отправляет, только запоминает вызовы для проверки в тестах.
type Messenger struct {
	mu     sync.Mutex
	nextID int

	Sent      []SentMessage
	Edited    []EditedMessage
	Documents []SentDocument
	Answered  []AnsweredCallback
	Commands  []ports.BotCommand
	Caps      ports.Capabilities
}

// New создаёт Messenger с возможностями "как у Telegram" по умолчанию —
// этого достаточно для большинства тестов хендлеров. Поле Caps можно
// переопределить перед использованием, чтобы проверить деградацию функциональности.
func New() *Messenger {
	return &Messenger{
		Caps: ports.Capabilities{
			EditMessages:    true,
			InlineButtons:   true,
			Documents:       true,
			CallbackAnswer:  true,
			MaxMessageRunes: 4096,
			RichFormat:      ports.FormatRich,
		},
	}
}

func (m *Messenger) Send(_ context.Context, chat ports.ChatID, text string, opts *ports.SendOptions) (ports.MessageRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	m.Sent = append(m.Sent, SentMessage{Chat: chat, Text: text, Opts: opts})
	return ports.MessageRef{Chat: chat, ID: ports.MessageID(strconv.Itoa(m.nextID))}, nil
}

func (m *Messenger) Edit(_ context.Context, ref ports.MessageRef, text string, opts *ports.SendOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Edited = append(m.Edited, EditedMessage{Ref: ref, Text: text, Opts: opts})
	return nil
}

func (m *Messenger) SendDocument(_ context.Context, chat ports.ChatID, doc ports.Document) (ports.MessageRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	m.Documents = append(m.Documents, SentDocument{Chat: chat, Doc: doc})
	return ports.MessageRef{Chat: chat, ID: ports.MessageID(strconv.Itoa(m.nextID))}, nil
}

func (m *Messenger) AnswerCallback(_ context.Context, callbackID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Answered = append(m.Answered, AnsweredCallback{CallbackID: callbackID, Text: text})
	return nil
}

func (m *Messenger) SetCommands(_ context.Context, cmds []ports.BotCommand, languageCode string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Commands = cmds
	return nil
}

func (m *Messenger) Capabilities() ports.Capabilities {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Caps
}

// LastSent возвращает последнее отправленное сообщение, либо nil, если ничего не отправлялось.
func (m *Messenger) LastSent() *SentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Sent) == 0 {
		return nil
	}
	last := m.Sent[len(m.Sent)-1]
	return &last
}

// Session — реализация ports.Session поверх Messenger для одного апдейта.
// Поля заполняются тестом напрямую перед передачей хендлеру.
type Session struct {
	M       *Messenger
	ChatID  ports.ChatID
	Sender  string
	TextVal string
	ArgsVal []string
	Msg     *ports.IncomingMessage
	CB      *ports.CallbackQuery

	mu        sync.Mutex
	Responses []string
	Edits     []string
}

func (s *Session) Chat() ports.ChatID              { return s.ChatID }
func (s *Session) SenderID() string                { return s.Sender }
func (s *Session) Text() string                    { return s.TextVal }
func (s *Session) Args() []string                  { return s.ArgsVal }
func (s *Session) Message() *ports.IncomingMessage { return s.Msg }
func (s *Session) Callback() *ports.CallbackQuery  { return s.CB }
func (s *Session) Messenger() ports.Messenger      { return s.M }

func (s *Session) Send(text string, opts *ports.SendOptions) error {
	_, err := s.M.Send(context.Background(), s.ChatID, text, opts)
	return err
}

func (s *Session) Reply(text string, opts *ports.SendOptions) error {
	return s.Send(text, opts)
}

func (s *Session) SendDocument(doc ports.Document) error {
	_, err := s.M.SendDocument(context.Background(), s.ChatID, doc)
	return err
}

// Edit в этом моке не адресует конкретное сообщение (в отличие от реальных адаптеров) —
// он просто запоминает текст в Edits для проверки в тестах.
func (s *Session) Edit(text string, _ *ports.SendOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Edits = append(s.Edits, text)
	return nil
}

func (s *Session) Respond(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Responses = append(s.Responses, text)
	return nil
}

// LastEdited возвращает последнее изменённое сообщение, либо nil.
func (m *Messenger) LastEdited() *EditedMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Edited) == 0 {
		return nil
	}
	last := m.Edited[len(m.Edited)-1]
	return &last
}

// AllTexts возвращает тексты всех отправленных и изменённых сообщений.
// Безопасен для чтения, пока обработчик пишет сообщения из другой горутины.
func (m *Messenger) AllTexts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	texts := make([]string, 0, len(m.Sent)+len(m.Edited))
	for _, msg := range m.Sent {
		texts = append(texts, msg.Text)
	}
	for _, msg := range m.Edited {
		texts = append(texts, msg.Text)
	}
	return texts
}

// SentCount возвращает число отправленных сообщений.
func (m *Messenger) SentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Sent)
}
