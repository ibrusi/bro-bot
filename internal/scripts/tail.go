package scripts

import (
	"sync"
	"unicode/utf8"
)

// TailBuffer — потокобезопасный io.Writer, который хранит только последние limit
// байт записанного. Вывод долгого скрипта не ограничен, а показать в чате можно
// лишь его хвост, поэтому держать всё в памяти незачем.
type TailBuffer struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
}

// NewTailBuffer создаёт буфер, хранящий не больше limit байт.
func NewTailBuffer(limit int) *TailBuffer {
	return &TailBuffer{limit: limit}
}

// Write добавляет p и отбрасывает начало, вышедшее за limit. Граница среза
// сдвигается к началу UTF-8 символа, чтобы хвост не начинался с обрывка.
func (b *TailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf = append(b.buf, p...)
	if excess := len(b.buf) - b.limit; excess > 0 {
		for excess < len(b.buf) && !utf8.RuneStart(b.buf[excess]) {
			excess++
		}
		b.buf = append(b.buf[:0], b.buf[excess:]...)
		b.truncated = true
	}
	return len(p), nil
}

// Snapshot возвращает накопленный хвост и признак того, что начало вывода отброшено.
func (b *TailBuffer) Snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf), b.truncated
}
