// Package telegram реализует ports.Transport (и, тем самым, ports.Messenger) поверх
// gopkg.in/telebot.v3. Это единственный пакет в проекте, которому разрешено
// импортировать telebot напрямую — весь остальной код общается с мессенджером
// только через internal/ports.
package telegram

import (
	"context"
	"fmt"
	"time"

	"bro-bot/internal/ports"

	tele "gopkg.in/telebot.v3"
)

// Config — параметры подключения к Telegram Bot API.
type Config struct {
	Token       string
	PollTimeout time.Duration
}

// Transport — реализация ports.Transport для Telegram.
type Transport struct {
	bot *tele.Bot
	mws []func(ports.Handler) ports.Handler
}

// New создаёт транспорт и авторизует бота в Telegram (без запуска получения апдейтов —
// для этого нужно вызвать Start).
func New(cfg Config) (*Transport, error) {
	pollTimeout := cfg.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = 10 * time.Second
	}
	b, err := tele.NewBot(tele.Settings{
		Token:  cfg.Token,
		Poller: &tele.LongPoller{Timeout: pollTimeout},
	})
	if err != nil {
		return nil, fmt.Errorf("ошибка инициализации telegram-бота: %w", err)
	}
	return &Transport{bot: b}, nil
}

// wrap оборачивает обработчик всеми зарегистрированными middleware — в том порядке,
// в котором они были добавлены через Use (первый добавленный выполняется первым).
func (t *Transport) wrap(h ports.Handler) ports.Handler {
	for i := len(t.mws) - 1; i >= 0; i-- {
		h = t.mws[i](h)
	}
	return h
}

func (t *Transport) Use(mw func(ports.Handler) ports.Handler) {
	t.mws = append(t.mws, mw)
}

func (t *Transport) OnCommand(name string, h ports.Handler) {
	t.bot.Handle("/"+name, func(c tele.Context) error {
		return t.wrap(h)(newSession(t, c))
	})
}

func (t *Transport) OnText(h ports.Handler) {
	t.bot.Handle(tele.OnText, func(c tele.Context) error {
		return t.wrap(h)(newSession(t, c))
	})
}

func (t *Transport) OnCallback(action string, h ports.Handler) {
	btn := tele.Btn{Unique: action}
	t.bot.Handle(&btn, func(c tele.Context) error {
		return t.wrap(h)(newSession(t, c))
	})
}

func (t *Transport) Start(ctx context.Context) error {
	if ctx != nil {
		go func() {
			<-ctx.Done()
			t.bot.Stop()
		}()
	}
	t.bot.Start()
	return nil
}

func (t *Transport) Stop() {
	t.bot.Stop()
}

func (t *Transport) Send(_ context.Context, chat ports.ChatID, text string, opts *ports.SendOptions) (ports.MessageRef, error) {
	to, err := recipient(chat)
	if err != nil {
		return ports.MessageRef{}, err
	}
	msg, err := sendSplit(t.bot, to, text, buildSendParams(opts))
	if err != nil {
		return ports.MessageRef{}, err
	}
	return msgRef(msg), nil
}

func (t *Transport) Edit(_ context.Context, ref ports.MessageRef, text string, opts *ports.SendOptions) error {
	ed, err := editable(ref)
	if err != nil {
		return err
	}
	to, err := recipient(ref.Chat)
	if err != nil {
		return err
	}
	_, err = editSplit(t.bot, ed, to, text, buildSendParams(opts))
	return err
}

func (t *Transport) SendDocument(_ context.Context, chat ports.ChatID, doc ports.Document) (ports.MessageRef, error) {
	to, err := recipient(chat)
	if err != nil {
		return ports.MessageRef{}, err
	}
	msg, err := t.bot.Send(to, toDocument(doc))
	if err != nil {
		return ports.MessageRef{}, err
	}
	return msgRef(msg), nil
}

func (t *Transport) AnswerCallback(_ context.Context, callbackID, text string) error {
	if callbackID == "" {
		return nil
	}
	return t.bot.Respond(&tele.Callback{ID: callbackID}, &tele.CallbackResponse{Text: text})
}

func (t *Transport) SetCommands(_ context.Context, cmds []ports.BotCommand) error {
	return t.bot.SetCommands(toBotCommands(cmds))
}

func (t *Transport) Capabilities() ports.Capabilities {
	return ports.Capabilities{
		EditMessages:    true,
		InlineButtons:   true,
		Documents:       true,
		CallbackAnswer:  true,
		MaxMessageRunes: 4096,
		RichFormat:      ports.FormatRich,
	}
}
