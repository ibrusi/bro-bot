package ports

import "context"

// ChatID — идентификатор чата/канала в конкретном мессенджере (строковое представление).
type ChatID string

// MessageID — идентификатор отправленного сообщения в конкретном мессенджере.
type MessageID string

// MessageRef однозначно ссылается на ранее отправленное сообщение (нужен для Edit).
type MessageRef struct {
	Chat ChatID
	ID   MessageID
}

// Format задаёт способ разметки текста сообщения.
type Format string

const (
	// FormatPlain — обычный текст без разметки.
	FormatPlain Format = ""
	// FormatRich — канонический внутренний rich-формат бота: подмножество HTML
	// (теги <b>, <i>, <code>, <pre>, <a href="...">). Адаптер Telegram отправляет
	// его как есть (tele.ModeHTML); адаптеры других мессенджеров обязаны сами
	// конвертировать это подмножество в свой формат разметки (например, mrkdwn у Slack).
	FormatRich Format = "rich"
)

// Button описывает одну инлайн-кнопку. Ровно одно из полей Action или URL должно быть задано:
// Action — для кнопки обратного вызова (обрабатывается через Transport.OnCallback),
// URL — для кнопки-ссылки, которую мессенджер открывает напрямую, без апдейта в бота.
type Button struct {
	Text    string
	Action  string // идентификатор обработчика, регистрируемого через Transport.OnCallback
	Payload string // произвольные данные, переданные в CallbackQuery.Payload
	URL     string // если задано, кнопка-ссылка вместо кнопки обратного вызова
}

// Keyboard — набор рядов инлайн-кнопок.
type Keyboard struct {
	Rows [][]Button
}

// SendOptions управляет форматированием и поведением при отправке/редактировании сообщения.
type SendOptions struct {
	Format   Format
	Keyboard *Keyboard
	Silent   bool
	ReplyTo  *MessageRef
}

// Rich возвращает SendOptions с включённым каноническим rich-форматом.
func Rich() *SendOptions {
	return &SendOptions{Format: FormatRich}
}

// RichWith возвращает SendOptions с rich-форматом и клавиатурой.
func RichWith(kb *Keyboard) *SendOptions {
	return &SendOptions{Format: FormatRich, Keyboard: kb}
}

// Document описывает файл, отправляемый как вложение.
type Document struct {
	FileName string
	MIME     string
	Caption  string
	Content  []byte
}

// BotCommand описывает команду для меню подсказок мессенджера (если он его поддерживает).
type BotCommand struct {
	Name        string
	Description string
}

// Capabilities сообщает, какие возможности поддерживает конкретный адаптер мессенджера,
// чтобы вызывающий код мог деградировать функциональность там, где она недоступна.
type Capabilities struct {
	EditMessages    bool
	InlineButtons   bool
	Documents       bool
	CallbackAnswer  bool
	MaxMessageRunes int
	RichFormat      Format
}

// Messenger — исходящий контракт: отправка, редактирование и служебные вызовы
// в сторону конкретного мессенджера. Реализация сама разбивает длинные сообщения
// на части (по Capabilities().MaxMessageRunes) и по возможности откатывается на
// простой текст, если разметка отклонена мессенджером.
type Messenger interface {
	Send(ctx context.Context, chat ChatID, text string, opts *SendOptions) (MessageRef, error)
	Edit(ctx context.Context, ref MessageRef, text string, opts *SendOptions) error
	SendDocument(ctx context.Context, chat ChatID, doc Document) (MessageRef, error)
	AnswerCallback(ctx context.Context, callbackID, text string) error
	SetCommands(ctx context.Context, cmds []BotCommand) error
	Capabilities() Capabilities
}
