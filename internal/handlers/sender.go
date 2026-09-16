package handlers

import (
	"fmt"
	"bro-bot/internal/utils"

	tele "gopkg.in/telebot.v3"
)

// extractSendOptions извлекает параметры отправки и разделяет их для промежуточных и финального чанков.
func extractSendOptions(opts []interface{}) (tele.ParseMode, *tele.ReplyMarkup, []interface{}, []interface{}) {
	var parseMode tele.ParseMode
	var markup *tele.ReplyMarkup
	var intermediate []interface{}
	var last []interface{}

	for _, opt := range opts {
		switch v := opt.(type) {
		case tele.ParseMode:
			parseMode = v
			intermediate = append(intermediate, v)
			last = append(last, v)
		case *tele.ReplyMarkup:
			markup = v
			last = append(last, v)
		case *tele.SendOptions:
			if v.ParseMode != "" {
				parseMode = v.ParseMode
			}
			if v.ReplyMarkup != nil {
				markup = v.ReplyMarkup
			}
			// Копируем опции без ReplyMarkup для промежуточных частей
			interSO := *v
			interSO.ReplyMarkup = nil
			intermediate = append(intermediate, &interSO)
			last = append(last, v)
		default:
			intermediate = append(intermediate, opt)
			last = append(last, opt)
		}
	}
	return parseMode, markup, intermediate, last
}

// stripParseMode убирает режим разметки из опций отправки (для fallback-отправки plain text).
func stripParseMode(opts []interface{}) []interface{} {
	var res []interface{}
	for _, opt := range opts {
		switch v := opt.(type) {
		case tele.ParseMode:
			continue
		case *tele.SendOptions:
			cloned := *v
			cloned.ParseMode = ""
			res = append(res, &cloned)
		default:
			res = append(res, opt)
		}
	}
	return res
}

// SendSplit отправляет сообщение получателю, автоматически разбивая его на несколько частей,
// если текст превышает лимиты Telegram. Кнопки (ReplyMarkup) прикрепляются к последнему сообщению.
func SendSplit(b *tele.Bot, to tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error) {
	text := ""
	switch v := what.(type) {
	case string:
		text = v
	case fmt.Stringer:
		text = v.String()
	default:
		return b.Send(to, what, opts...)
	}

	parseMode, _, intermediateOpts, lastOpts := extractSendOptions(opts)
	chunks := utils.SplitMessageByMode(text, string(parseMode), 3800)
	if len(chunks) <= 1 {
		msg, err := b.Send(to, what, opts...)
		if err != nil && parseMode != "" {
			plain := utils.StripTelegramHTML(text)
			return b.Send(to, plain, stripParseMode(opts)...)
		}
		return msg, err
	}

	var lastMsg *tele.Message
	for i, chunk := range chunks {
		currentOpts := intermediateOpts
		if i == len(chunks)-1 {
			currentOpts = lastOpts
		}

		msg, err := b.Send(to, chunk, currentOpts...)
		if err != nil && parseMode != "" {
			plain := utils.StripTelegramHTML(chunk)
			msg, err = b.Send(to, plain, stripParseMode(currentOpts)...)
		}
		if err != nil {
			return lastMsg, err
		}
		lastMsg = msg
	}
	return lastMsg, nil
}

// ReplySplit отвечает на сообщение, автоматически разбивая длинный текст на части.
func ReplySplit(b *tele.Bot, to *tele.Message, what interface{}, opts ...interface{}) error {
	if to == nil {
		_, err := SendSplit(b, to.Chat, what, opts...)
		return err
	}

	text := ""
	switch v := what.(type) {
	case string:
		text = v
	case fmt.Stringer:
		text = v.String()
	default:
		_, err := b.Reply(to, what, opts...)
		return err
	}

	parseMode, _, intermediateOpts, lastOpts := extractSendOptions(opts)
	chunks := utils.SplitMessageByMode(text, string(parseMode), 3800)
	if len(chunks) <= 1 {
		_, err := b.Reply(to, what, opts...)
		if err != nil && parseMode != "" {
			plain := utils.StripTelegramHTML(text)
			_, err = b.Reply(to, plain, stripParseMode(opts)...)
		}
		return err
	}

	for i, chunk := range chunks {
		currentOpts := intermediateOpts
		if i == len(chunks)-1 {
			currentOpts = lastOpts
		}

		var err error
		if i == 0 {
			_, err = b.Reply(to, chunk, currentOpts...)
			if err != nil && parseMode != "" {
				plain := utils.StripTelegramHTML(chunk)
				_, err = b.Reply(to, plain, stripParseMode(currentOpts)...)
			}
		} else {
			_, err = b.Send(to.Chat, chunk, currentOpts...)
			if err != nil && parseMode != "" {
				plain := utils.StripTelegramHTML(chunk)
				_, err = b.Send(to.Chat, plain, stripParseMode(currentOpts)...)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// autoSplitContext оборачивает tele.Context, перехватывая вызовы Send и Reply для автоматического сплита.
type autoSplitContext struct {
	tele.Context
	bot *tele.Bot
}

func (sc *autoSplitContext) Send(what interface{}, opts ...interface{}) error {
	_, err := SendSplit(sc.bot, sc.Recipient(), what, opts...)
	return err
}

func (sc *autoSplitContext) Reply(what interface{}, opts ...interface{}) error {
	return ReplySplit(sc.bot, sc.Message(), what, opts...)
}
