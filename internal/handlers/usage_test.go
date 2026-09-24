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
		{"claude", "cli", "Claude Code", "5-hour and weekly windows", "/tokens"},
		{"claude", "api", "Claude API", "/tokens", "5-hour and weekly windows"},
		{"agy", "cli", "Google Antigravity", "5-hour and weekly windows", "/tokens"},
		{"agy", "api", "Gemini API", "/tokens", "5-hour and weekly windows"},
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
		if got := usageSourceTitle(tc.agent, tc.mode, "ru"); got != tc.want {
			t.Errorf("usageSourceTitle(%q, %q) = %q, ожидали %q", tc.agent, tc.mode, got, tc.want)
		}
	}
}

// TestUsageUsesCLIResultWithoutSecondProcess — конверт CLI Claude Code уже несёт текст
// в поле result; второй запуск процесса ради GetQuotaText — лишняя секунда ожидания.
func TestUsageUsesCLIResultWithoutSecondProcess(t *testing.T) {
	mt, agent := setupMatrixApp(t, "claude", "cli", "claude-cli-usage", "ответ")
	// Снято с настоящего `claude --output-format json -p /usage` (лишние поля опущены).
	agent.QuotaOutput = `{"type":"result","subtype":"success","is_error":false,` +
		`"result":"Total cost:            $0.0000\nUsage:                 0 input, 0 output",` +
		`"local_command":"usage","session_id":"x"}`

	handler := mt.commands["usage"]
	if err := handler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/usage: %v", err)
	}

	texts := mt.AllTexts()
	out := texts[len(texts)-1]
	if !strings.Contains(out, "Total cost") {
		t.Errorf("текст из result не дошёл до пользователя: %s", out)
	}
	if strings.Contains(out, "<pre>") || strings.Contains(out, `"type"`) {
		t.Errorf("в ответ попал служебный конверт: %s", out)
	}
	if calls := agent.QuotaTextCalls(); calls != 0 {
		t.Errorf("GetQuotaText вызван %d раз — второй процесс не нужен, текст уже есть", calls)
	}
}

// TestUsageStillAsksTextWhenEnvelopeHasNone — у API-адаптеров конверт без текста,
// и сводка по-прежнему берётся из GetQuotaText.
func TestUsageStillAsksTextWhenEnvelopeHasNone(t *testing.T) {
	mt, agent := setupMatrixApp(t, "agy", "api", "agy-api-usage", "ответ")

	handler := mt.commands["usage"]
	if err := handler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/usage: %v", err)
	}
	if calls := agent.QuotaTextCalls(); calls != 1 {
		t.Errorf("GetQuotaText должен быть вызван один раз, вызван %d", calls)
	}
}

// TestUsageRendersClaudeSubscriptionWithProgressBars проверяет красивую отрисовку /usage для Claude Code
// (включая режим MCP) со шкалами прогресса, эмодзи статуса и временем сброса.
func TestUsageRendersClaudeSubscriptionWithProgressBars(t *testing.T) {
	mt, agent := setupMatrixApp(t, "claude", "mcp", "claude-mcp-usage", "ответ")
	agent.QuotaOutput = `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[` +
		`{"name":"Подписка Claude","buckets":[` +
		`{"id":"claude-5h","name":"Current session","window":"5h","remaining_fraction":1.0,"reset_time":"2026-09-24T11:10:00Z"},` +
		`{"id":"claude-weekly","name":"Current week (all models)","window":"weekly","remaining_fraction":0.7,"reset_time":"2026-09-27T00:00:00Z"}` +
		`]}]}}}`

	handler := mt.commands["usage"]
	if err := handler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/usage: %v", err)
	}

	texts := mt.AllTexts()
	out := texts[len(texts)-1]

	for _, want := range []string{
		"100.0% 🟢",
		"[██████████]",
		"70.0% 🟢",
		"[███████░░░]",
		"24.09 11:10 UTC",
		"27.09 00:00 UTC",
		"Claude Code (MCP)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в красивом выводе /usage нет %q:\n%s", want, out)
		}
	}
}

