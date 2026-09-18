package handlers

import (
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
)

// runUsage выполняет /usage для указанной пары «агент + режим» и возвращает
// итоговый текст, который увидел пользователь.
func runUsage(t *testing.T, agentName, mode string) string {
	t.Helper()

	mt, _ := setupMatrixApp(t, agentName, mode, agentName+"-usage-conv", "ответ")

	handler, ok := mt.commands["usage"]
	if !ok {
		t.Fatal("команда /usage не зарегистрирована")
	}
	if err := handler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/usage вернула ошибку: %v", err)
	}

	texts := mt.AllTexts()
	if len(texts) == 0 {
		t.Fatal("/usage ничего не отправила")
	}
	return texts[len(texts)-1]
}

// TestUsageNeverLeaksServiceJSON — регрессия на утечку служебного конверта:
// заглушка квоты разбиралась, но групп в ней нет, и пользователь видел
// <pre>{"status":"SUCCESS",...}</pre> вместо человеческой сводки.
func TestUsageNeverLeaksServiceJSON(t *testing.T) {
	cases := []struct {
		agent string
		mode  string
	}{
		{"agy", "cli"},
		{"agy", "api"},
		{"claude", "cli"},
		{"claude", "api"},
	}

	for _, tc := range cases {
		t.Run(tc.agent+"/"+tc.mode, func(t *testing.T) {
			out := runUsage(t, tc.agent, tc.mode)

			for _, forbidden := range []string{"SUCCESS", "<pre>", `{"status"`} {
				if strings.Contains(out, forbidden) {
					t.Errorf("в ответе /usage есть служебный %q: %s", forbidden, out)
				}
			}
			// Человеческая сводка агента доходит до пользователя.
			if !strings.Contains(out, "mock quota") {
				t.Errorf("в ответе нет текстовой сводки агента: %s", out)
			}
		})
	}
}

// TestUsageHeaderAndFooterMatchMode — в api-режиме источник лимитов подписывается
// как API, а не как CLI-продукт, и подпись не обещает окна подписки.
func TestUsageHeaderAndFooterMatchMode(t *testing.T) {
	cases := []struct {
		agent      string
		mode       string
		wantHeader string
		wantFooter string
		notFooter  string
	}{
		{"claude", "cli", "Claude Code", "5-часового окна", "/tokens"},
		{"claude", "api", "Claude API", "/tokens", "5-часового окна"},
		{"agy", "cli", "Google Antigravity", "5-часового окна", "/tokens"},
		{"agy", "api", "Gemini API", "/tokens", "5-часового окна"},
	}

	for _, tc := range cases {
		t.Run(tc.agent+"/"+tc.mode, func(t *testing.T) {
			out := runUsage(t, tc.agent, tc.mode)

			if !strings.Contains(out, tc.wantHeader) {
				t.Errorf("в шапке нет %q: %s", tc.wantHeader, out)
			}
			if !strings.Contains(out, tc.wantFooter) {
				t.Errorf("в подписи нет %q: %s", tc.wantFooter, out)
			}
			if strings.Contains(out, tc.notFooter) {
				t.Errorf("в подписи есть лишнее %q: %s", tc.notFooter, out)
			}
		})
	}
}

func TestUsageSourceTitle(t *testing.T) {
	cases := []struct {
		agent string
		mode  string
		want  string
	}{
		{"claude", "api", "Claude API"},
		{"claude", "cli", "Claude Code"},
		{"agy", "api", "Gemini API"},
		{"agy", "cli", "Google Antigravity"},
		{"", "api", "API агента"},
		{"неизвестный", "cli", "CLI агента"},
	}
	for _, tc := range cases {
		if got := usageSourceTitle(tc.agent, tc.mode); got != tc.want {
			t.Errorf("usageSourceTitle(%q, %q) = %q, ожидали %q", tc.agent, tc.mode, got, tc.want)
		}
	}
}
