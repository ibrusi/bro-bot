package utils

// Telegram считает длину сообщения в UTF-16 и не принимает больше 4096 единиц.
// SafeMessageRunes оставляет буфер: закрывающие HTML-теги на границе чанка и
// эмодзи, которые в UTF-16 занимают два юнита, не должны упираться в лимит.
const (
	TelegramMessageLimit = 4096
	MessageRuneBuffer    = 128
	SafeMessageRunes     = TelegramMessageLimit - MessageRuneBuffer
)
