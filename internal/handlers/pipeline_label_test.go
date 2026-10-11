package handlers

import (
	"bro-bot/internal/effort"
	"testing"
)

func TestFormatRunLabel(t *testing.T) {
	cases := []struct {
		name  string
		model string
		agent string
		level effort.Level
		want  string
	}{
		{"explicit level", "claude-opus-5-5", "claude", effort.High, "claude-opus-5-5 · claude · effort: high"},
		{"auto level", "gemini-3", "agy", effort.Auto, "gemini-3 · agy · effort: auto"},
		{"escapes html", "<m>", "a&b", effort.Max, "&lt;m&gt; · a&amp;b · effort: max"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatRunLabel(tc.model, tc.agent, tc.level); got != tc.want {
				t.Errorf("formatRunLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}
