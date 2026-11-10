package utils

import "testing"

func TestSafeMessageRunesStayUnderTheTelegramLimit(t *testing.T) {
	if SafeMessageRunes != TelegramMessageLimit-MessageRuneBuffer {
		t.Fatalf("SafeMessageRunes = %d, want %d", SafeMessageRunes, TelegramMessageLimit-MessageRuneBuffer)
	}
	if SafeMessageRunes >= TelegramMessageLimit {
		t.Fatalf("SafeMessageRunes %d reached the hard limit %d", SafeMessageRunes, TelegramMessageLimit)
	}
	if SafeMessageRunes <= 3800 {
		t.Fatalf("SafeMessageRunes %d is not longer than the previous chunk", SafeMessageRunes)
	}
}
