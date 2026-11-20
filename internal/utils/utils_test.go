package utils

import "testing"

func TestTruncateStringIsRuneSafe(t *testing.T) {
	// Кириллица занимает два байта на символ: байтовый срез порвал бы руну.
	got := TruncateString("привет, мир", 8)
	if got != "приве..." {
		t.Errorf("TruncateString = %q", got)
	}
	if got := TruncateString("короткая", 20); got != "короткая" {
		t.Errorf("строка короче лимита должна остаться как есть: %q", got)
	}
}

func TestTruncateWithNote(t *testing.T) {
	const note = "\n... (вывод обрезан)"
	long := "абвгдеёжзийклмнопрстуфхцчшщъыьэюя"

	got := TruncateWithNote(long, 5, note)
	if got != "абвгд"+note {
		t.Errorf("TruncateWithNote = %q", got)
	}
	if got := TruncateWithNote("коротко", 100, note); got != "коротко" {
		t.Errorf("без обрезки пометки быть не должно: %q", got)
	}
}

func TestFormatCount(t *testing.T) {
	cases := map[int64]string{
		0:         "0",
		900:       "900",
		1_000:     "1K",
		65_536:    "66K",
		200_000:   "200K",
		1_048_576: "1.0M",
		2_500_000: "2.5M",
	}
	for value, want := range cases {
		if got := FormatCount(value); got != want {
			t.Errorf("FormatCount(%d) = %q, ожидали %q", value, got, want)
		}
	}
}

func TestExtractCommandPayloadMultiline(t *testing.T) {
	payload, ok := ExtractCommandPayload("/new@bro_bot projB\nLine 1\n  Line 2\n\n```\ncode()\n```")
	if !ok {
		t.Fatal("expected ok=true for slash command")
	}
	want := "projB\nLine 1\n  Line 2\n\n```\ncode()\n```"
	if payload != want {
		t.Errorf("payload = %q, want %q", payload, want)
	}

	emptyPayload, ok := ExtractCommandPayload("/plan")
	if !ok || emptyPayload != "" {
		t.Errorf("expected (\"\", true) for bare command, got (%q, %v)", emptyPayload, ok)
	}

	if _, ok := ExtractCommandPayload("plain text"); ok {
		t.Error("expected ok=false for non-command text")
	}
}
