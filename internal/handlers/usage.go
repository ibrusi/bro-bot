package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"strings"
	"sync"
	"time"
)

// Команда /usage: лимиты и квоты аккаунта агента.

// handleUsage — общий обработчик нескольких команд.
func handleUsage(s ports.Session) error {
	m := s.Messenger()
	chat := s.Chat()
	// Адаптер снимаем один раз здесь: горутины ниже не должны читать активного агента,
	// пока пользователь может переключить его командой /agent или /mode.
	framework, agentName := ActiveAgent()
	execMode := config.ProjectState.GetExecutionMode()
	loadingAgent := agentName
	if loadingAgent == "" {
		loadingAgent = "агента"
	}
	if framework == nil {
		return s.Send("❌ Агент не сконфигурирован. Переключите агента: /agent", ports.Rich())
	}
	statusRef, _ := m.Send(context.Background(), chat, fmt.Sprintf("⏳ <i>Запрашиваю актуальные лимиты и квоты из %s...</i>", html.EscapeString(loadingAgent)), ports.Rich())

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var (
		quotaResp   AgyQuotaResponse
		creditsResp AgyCreditsResponse
		quotaRaw    string
		quotaText   string
		quotaParsed bool
		quotaErr    error
		creditsErr  error
		wg          sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		out, err := framework.GetQuota(ctx)
		cleanOut := utils.AnsiRegex.ReplaceAllString(string(out), "")
		quotaRaw = strings.TrimSpace(cleanOut)
		if err != nil {
			quotaErr = err
		} else if jsonErr := json.Unmarshal([]byte(cleanOut), &quotaResp); jsonErr != nil {
			quotaErr = jsonErr
		} else {
			quotaParsed = true
		}

		// Структурных данных нет — берём человекочитаемую сводку, иначе
		// пользователь увидит служебный JSON вместо ответа.
		if len(quotaResp.Command.Data.Groups) > 0 {
			return
		}
		// Конверт CLI уже содержит текст для человека: второй запуск процесса
		// ради GetQuotaText не нужен.
		if text := strings.TrimSpace(quotaResp.Result); text != "" {
			quotaText = text
			return
		}
		textOut, textErr := framework.GetQuotaText(ctx)
		if textErr != nil {
			return
		}
		quotaText = strings.TrimSpace(utils.AnsiRegex.ReplaceAllString(string(textOut), ""))
	}()

	go func() {
		defer wg.Done()
		out, err := framework.GetCredits(ctx)
		cleanOut := utils.AnsiRegex.ReplaceAllString(string(out), "")
		if err != nil {
			creditsErr = err
			return
		}
		_ = json.Unmarshal([]byte(cleanOut), &creditsResp)
	}()

	wg.Wait()

	config.Session.Lock()
	lastModel := config.Session.LastModelUsed
	config.Session.Unlock()

	config.ProjectState.RLock()
	activeModel := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	if lastModel == "" {
		lastModel = activeModel
	}

	lastTokens := domain.GlobalTokenTracker.FormatShortLastTask()
	if lastTokens == "" {
		config.Session.Lock()
		lastTokens = config.Session.LastTokensUsed
		config.Session.Unlock()
		if lastTokens == "" {
			lastTokens = "нет данных (запустите хотя бы одну задачу)"
		}
	}

	var bldr strings.Builder
	bldr.WriteString(fmt.Sprintf("📊 <b>Лимиты и квоты аккаунта (%s)</b>\n\n",
		html.EscapeString(usageSourceTitle(agentName, execMode))))

	if quotaErr == nil && len(quotaResp.Command.Data.Groups) > 0 {
		for _, g := range quotaResp.Command.Data.Groups {
			bldr.WriteString(fmt.Sprintf("🔹 <b>%s</b>\n", html.EscapeString(g.Name)))
			if g.Description != "" {
				bldr.WriteString(fmt.Sprintf("<i>%s</i>\n", html.EscapeString(g.Description)))
			}
			for _, b := range g.Buckets {
				bucketLabel := formatBucketName(b.Name, b.Window)
				if b.RemainingFraction != nil {
					frac := *b.RemainingFraction
					pct := frac * 100
					bar := renderProgressBar(frac, 10)
					emoji := quotaStatusEmoji(frac)
					bldr.WriteString(fmt.Sprintf("• <b>%s:</b> %.1f%% %s\n", html.EscapeString(bucketLabel), pct, emoji))
					bldr.WriteString(fmt.Sprintf("  <code>[%s]</code> %.1f%%\n", bar, pct))
				} else {
					bldr.WriteString(fmt.Sprintf("• <b>%s:</b> <i>доступно</i>\n", html.EscapeString(bucketLabel)))
				}
				if b.ResetTime != "" {
					resetInfo := formatResetDuration(b.ResetTime)
					bldr.WriteString(fmt.Sprintf("  ⏳ <i>Сброс: %s</i>\n", html.EscapeString(resetInfo)))
				}
			}
			bldr.WriteString("\n")
		}
	} else if quotaText != "" {
		// Обычный текст, а не <pre> с JSON: это сводка для человека.
		bldr.WriteString(html.EscapeString(quotaText))
		bldr.WriteString("\n\n")
	} else if quotaParsed {
		// Ответ разобран, но данных в нём нет — показываем пояснение, если оно есть,
		// и ни при каких условиях не печатаем служебный конверт.
		if descr := strings.TrimSpace(firstNonEmpty(quotaResp.Command.Data.Description, quotaResp.Response, quotaResp.Result)); descr != "" {
			bldr.WriteString(fmt.Sprintf("<i>%s</i>\n\n", html.EscapeString(descr)))
		}
	} else if quotaRaw != "" {
		// Запасная ветка для CLI: вывод терминала показываем как есть.
		bldr.WriteString(fmt.Sprintf("<b>%s:</b>\n<pre>%s</pre>\n\n",
			html.EscapeString(fmt.Sprintf("Ответ %s", loadingAgent)), html.EscapeString(quotaRaw)))
	} else if quotaErr != nil {
		bldr.WriteString(fmt.Sprintf("⚠️ <i>Не удалось получить актуальные лимиты из %s: %s</i>\n\n", html.EscapeString(loadingAgent), html.EscapeString(quotaErr.Error())))
	}

	if creditsErr == nil && creditsResp.Command.Name == "credits" {
		bldr.WriteString(fmt.Sprintf("💳 <b>Дополнительные кредиты:</b> <code>%.0f</code>\n\n", creditsResp.Command.Data.RemainingCredits))
	}

	bldr.WriteString("⚙️ <b>Сессия и модель:</b>\n")
	bldr.WriteString(fmt.Sprintf("• Выбранная модель: <code>%s</code>\n", html.EscapeString(activeModel)))
	bldr.WriteString(fmt.Sprintf("• Модель в сессии: <code>%s</code>\n", html.EscapeString(lastModel)))
	bldr.WriteString(fmt.Sprintf("• Использовано токенов: <code>%s</code>\n\n", html.EscapeString(lastTokens)))

	bldr.WriteString(usageFooter(execMode))

	resultMsg := bldr.String()
	if statusRef.ID != "" {
		if editErr := m.Edit(context.Background(), statusRef, resultMsg, ports.Rich()); editErr == nil {
			return nil
		}
	}
	return s.Send(resultMsg, ports.Rich())
}

func renderProgressBar(fraction float64, totalBlocks int) string {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(math.Round(fraction * float64(totalBlocks)))
	if filled > totalBlocks {
		filled = totalBlocks
	}
	empty := totalBlocks - filled
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

func quotaStatusEmoji(fraction float64) string {
	switch {
	case fraction >= 0.5:
		return "🟢"
	case fraction >= 0.2:
		return "🟡"
	default:
		return "🔴"
	}
}

// usageFooter подбирает подпись под режим: окна 5 часов и недели — это семантика
// подписки CLI, к ключу API она не относится.
func usageFooter(execMode string) string {
	if strings.EqualFold(execMode, "api") {
		return "💡 <i>Лимиты ключа API восполняются непрерывно. Расход токенов ботом: /tokens.</i>"
	}
	return "💡 <i>Лимиты 5-часового окна и недели сглаживают общую нагрузку и обновляются автоматически.</i>"
}

// firstNonEmpty возвращает первое непустое значение после обрезки пробелов.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func formatBucketName(name, window string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "week") || window == "weekly":
		return "Недельный лимит"
	case strings.Contains(lower, "five hour") || strings.Contains(lower, "5 hour") || window == "5h":
		return "5-часовой лимит"
	default:
		return name
	}
}

func formatResetDuration(resetTimeStr string) string {
	if resetTimeStr == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, resetTimeStr)
	if err != nil {
		return resetTimeStr
	}
	remaining := time.Until(t)
	formattedTime := t.UTC().Format("02.01 15:04 UTC")
	if remaining <= 0 {
		return fmt.Sprintf("сейчас (%s)", formattedTime)
	}

	var parts []string
	days := int(remaining.Hours()) / 24
	hours := int(remaining.Hours()) % 24
	mins := int(remaining.Minutes()) % 60

	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d д.", days))
	}
	if hours > 0 || (days > 0 && mins > 0) {
		parts = append(parts, fmt.Sprintf("%d ч.", hours))
	}
	if days == 0 && mins > 0 {
		parts = append(parts, fmt.Sprintf("%d мин.", mins))
	}
	if len(parts) == 0 {
		parts = append(parts, "< 1 мин.")
	}

	return fmt.Sprintf("через %s (%s)", strings.Join(parts, " "), formattedTime)
}

type AgyQuotaResponse struct {
	Status   string `json:"status"`
	Response string `json:"response"`
	// Result — человекочитаемый текст из конверта CLI Claude Code
	// ({"type":"result","result":"…"}): его достаточно, чтобы не запускать второй процесс.
	Result  string `json:"result"`
	Command struct {
		Name string `json:"name"`
		Data struct {
			Description string          `json:"description"`
			Groups      []AgyQuotaGroup `json:"groups"`
		} `json:"data"`
	} `json:"command"`
}

type AgyQuotaGroup struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Buckets     []AgyQuotaBucket `json:"buckets"`
}

type AgyQuotaBucket struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remaining_fraction"`
	ResetTime         string   `json:"reset_time"`
}

type AgyCreditsResponse struct {
	Command struct {
		Name string `json:"name"`
		Data struct {
			RemainingCredits float64 `json:"remaining_credits"`
			UpgradeURI       string  `json:"upgrade_uri"`
		} `json:"data"`
	} `json:"command"`
}
