package ports

import (
	"context"
	"io"
)

// VoiceMessage — нейтральное представление входящего голосового или аудиосообщения.
type VoiceMessage struct {
	FileID   string
	Duration int
	MIME     string
	Caption  string
	FileName string
}

// FileMessage — нейтральное представление входящего файла, документа или фотографии.
type FileMessage struct {
	FileID   string
	FileName string
	FileSize int64
	MIME     string
	Caption  string
}

// IncomingMessage — нейтральное представление входящего текстового сообщения.
type IncomingMessage struct {
	Chat      ChatID
	SenderID  string
	Text      string
	MessageID MessageID
	ReplyTo   *MessageRef
}

// CallbackQuery — нейтральное представление нажатия инлайн-кнопки.
type CallbackQuery struct {
	ID          string // используется в Messenger.AnswerCallback
	Chat        ChatID
	SenderID    string
	Action      string
	Payload     string
	Message     *MessageRef
	MessageText string // текст сообщения, к которому прикреплена кнопка (для Session.Edit)
}

// Session — один входящий апдейт (сообщение или callback) с удобными методами
// ответа в тот же чат. Нейтральный аналог tele.Context.
type Session interface {
	Chat() ChatID
	SenderID() string
	Text() string
	Args() []string

	// Message возвращает данные входящего сообщения, либо nil, если апдейт — callback.
	Message() *IncomingMessage
	// Callback возвращает данные нажатой кнопки, либо nil, если апдейт — сообщение.
	Callback() *CallbackQuery
	// Voice возвращает данные голосового или аудиосообщения, либо nil, если апдейт не содержит аудио.
	Voice() *VoiceMessage
	// OpenVoice открывает поток чтения аудиофайла для текущего голосового сообщения.
	// Вызывающий обязан закрыть полученный io.ReadCloser.
	OpenVoice(ctx context.Context) (io.ReadCloser, error)
	// File возвращает данные входящего файла, документа или фото, либо nil, если апдейт не содержит файла.
	File() *FileMessage
	// OpenFile открывает поток чтения файла для текущего файлового сообщения.
	// Вызывающий обязан закрыть полученный io.ReadCloser.
	OpenFile(ctx context.Context) (io.ReadCloser, error)

	Messenger() Messenger

	Send(text string, opts *SendOptions) error
	Reply(text string, opts *SendOptions) error
	SendDocument(doc Document) error
	// Edit редактирует сообщение, к которому относится текущий апдейт
	// (для callback — сообщение с нажатой кнопкой).
	Edit(text string, opts *SendOptions) error
	// Respond отвечает на нажатие кнопки всплывающим уведомлением.
	// На апдейтах, не являющихся callback, или в мессенджерах без такой возможности — no-op.
	Respond(text string) error
}

// Handler обрабатывает один входящий апдейт.
type Handler func(s Session) error

// Transport — входящий контракт: регистрация обработчиков команд, текста и
// нажатий кнопок, плюс управление жизненным циклом получения апдейтов.
// Реализует Messenger, поэтому один Transport полностью закрывает нужды
// конкретного мессенджера — и на приём, и на отправку.
type Transport interface {
	Messenger

	// OnCommand регистрирует обработчик команды без ведущего "/", например "status".
	OnCommand(name string, h Handler)
	// OnText регистрирует обработчик произвольного текстового сообщения,
	// не распознанного как команда.
	OnText(h Handler)
	// OnVoice регистрирует обработчик входящих голосовых и аудиосообщений.
	OnVoice(h Handler)
	// OnDocument регистрирует обработчик входящих файлов, документов и фотографий.
	OnDocument(h Handler)
	// OnCallback регистрирует обработчик нажатия кнопки с данным Button.Action.
	OnCallback(action string, h Handler)
	// Use добавляет middleware, оборачивающий все последующие обработчики.
	Use(mw func(Handler) Handler)

	Start(ctx context.Context) error
	Stop()
}
