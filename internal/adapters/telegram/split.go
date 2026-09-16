package telegram

import (
	"bro-bot/internal/utils"

	tele "gopkg.in/telebot.v3"
)

// maxChunkRunes — безопасный размер одного чанка при разбиении длинных сообщений.
// Меньше официального лимита Telegram в 4096 символов, чтобы оставить запас на
// закрывающие HTML-теги, которые может добавить SplitTelegramHTML на границе чанка.
const maxChunkRunes = 3800

// sendParams — telegram-specific параметры одной отправки/редактирования,
// извлечённые из ports.SendOptions.
type sendParams struct {
	parseMode tele.ParseMode
	markup    *tele.ReplyMarkup
	silent    bool
	replyTo   *tele.Message
}

// teleOptions строит *tele.SendOptions. withMarkup управляет тем, прикрепляется ли
// клавиатура (она должна оказаться только на последнем чанке многочастного сообщения),
// withReplyTo — тем, сохраняется ли reply (только на первом чанке).
func (p sendParams) teleOptions(withMarkup, withReplyTo bool) *tele.SendOptions {
	so := &tele.SendOptions{
		ParseMode:           p.parseMode,
		DisableNotification: p.silent,
	}
	if withMarkup {
		so.ReplyMarkup = p.markup
	}
	if withReplyTo {
		so.ReplyTo = p.replyTo
	}
	return so
}

// sendSplit отправляет текст получателю, при необходимости разбивая его на несколько
// сообщений по maxChunkRunes. Клавиатура прикрепляется только к последнему сообщению.
// Если Telegram отклоняет разметку (например, из-за незакрытого тега), делается
// повторная попытка отправки того же чанка обычным текстом.
func sendSplit(b *tele.Bot, to tele.Recipient, text string, p sendParams) (*tele.Message, error) {
	chunks := utils.SplitMessageByMode(text, string(p.parseMode), maxChunkRunes)
	if len(chunks) == 0 {
		chunks = []string{""}
	}

	var lastMsg *tele.Message
	for i, chunk := range chunks {
		isLast := i == len(chunks)-1
		opts := p.teleOptions(isLast, i == 0)

		msg, err := b.Send(to, chunk, opts)
		if err != nil && p.parseMode != "" {
			plain := utils.StripTelegramHTML(chunk)
			plainOpts := *opts
			plainOpts.ParseMode = ""
			msg, err = b.Send(to, plain, &plainOpts)
		}
		if err != nil {
			return lastMsg, err
		}
		lastMsg = msg
	}
	return lastMsg, nil
}

// editSplit редактирует ранее отправленное сообщение. Telegram не позволяет сообщению
// "перерасти" за пределы лимита на месте, поэтому если новый текст не помещается в один
// чанк, первый чанк редактируется в исходном сообщении, а остальные досылаются новыми
// сообщениями — клавиатура остаётся на последнем из них.
func editSplit(b *tele.Bot, ref tele.Editable, to tele.Recipient, text string, p sendParams) (*tele.Message, error) {
	chunks := utils.SplitMessageByMode(text, string(p.parseMode), maxChunkRunes)
	if len(chunks) == 0 {
		chunks = []string{""}
	}

	onlyChunk := len(chunks) == 1
	opts := p.teleOptions(onlyChunk, false)
	msg, err := b.Edit(ref, chunks[0], opts)
	if err != nil && p.parseMode != "" {
		plain := utils.StripTelegramHTML(chunks[0])
		plainOpts := *opts
		plainOpts.ParseMode = ""
		msg, err = b.Edit(ref, plain, &plainOpts)
	}
	if err != nil {
		return msg, err
	}

	lastMsg := msg
	for i := 1; i < len(chunks); i++ {
		isLast := i == len(chunks)-1
		sendOpts := p.teleOptions(isLast, false)

		m, sendErr := b.Send(to, chunks[i], sendOpts)
		if sendErr != nil && p.parseMode != "" {
			plain := utils.StripTelegramHTML(chunks[i])
			plainOpts := *sendOpts
			plainOpts.ParseMode = ""
			m, sendErr = b.Send(to, plain, &plainOpts)
		}
		if sendErr != nil {
			return lastMsg, sendErr
		}
		lastMsg = m
	}
	return lastMsg, nil
}
