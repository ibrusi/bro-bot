package claude

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bro-bot/internal/i18n"
)

var (
	// reUsageLine разбирает строки лимитов подписки Claude Code вида:
	// "Current session: 0% used · resets Sep 24, 11:10am (UTC)"
	// "Current week (all models): 30% used · resets Sep 27, 12am (UTC)"
	// "Current week (Sonnet only): 10% used · resets Sep 27, 12am (UTC)"
	// "Spend limit: 15% used · resets Oct 1, 12am (UTC)"
	reUsageLine = regexp.MustCompile(`(?i)^([A-Za-z0-9\s\(\)]+?):\s*(\d+(?:\.\d+)?)%\s*used(?:\s*\([^)]*\))?(?:\s*[·\-\*]\s*resets?\s+(.+))?$`)

	// reResetDate разбирает текстовые даты сброса вида:
	// "Sep 24, 11:10am (UTC)", "Sep 27, 12am (UTC)", "11:10am (UTC)", "Oct 1, 2026, 3:00pm (UTC)"
	reResetDate = regexp.MustCompile(`(?i)(?:([A-Za-z]+)\s+(\d{1,2})(?:,?\s+(\d{4}))?,?\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)(?:\s*\(([A-Za-z0-9_\-\+/]+)\))?`)
)

// parseClaudeResetTime парсит строковое время сброса квоты Claude Code в time.Time (RFC3339).
func parseClaudeResetTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	m := reResetDate.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, fmt.Errorf("cannot parse reset date: %q", s)
	}

	monthStr := m[1]
	dayStr := m[2]
	yearStr := m[3]
	hourStr := m[4]
	minStr := m[5]
	ampm := strings.ToLower(m[6])
	tzStr := m[7]

	hour, _ := strconv.Atoi(hourStr)
	minute := 0
	if minStr != "" {
		minute, _ = strconv.Atoi(minStr)
	}

	if ampm == "pm" && hour < 12 {
		hour += 12
	} else if ampm == "am" && hour == 12 {
		hour = 0
	}

	loc := time.UTC
	if tzStr != "" && tzStr != "UTC" && tzStr != "GMT" {
		if l, err := time.LoadLocation(tzStr); err == nil {
			loc = l
		}
	}

	nowInLoc := now.In(loc)
	year := nowInLoc.Year()
	if yearStr != "" {
		year, _ = strconv.Atoi(yearStr)
	}

	month := nowInLoc.Month()
	day := nowInLoc.Day()

	if monthStr != "" {
		tMonth, err := time.Parse("Jan", monthStr)
		if err != nil {
			return time.Time{}, err
		}
		month = tMonth.Month()
	}
	if dayStr != "" {
		day, _ = strconv.Atoi(dayStr)
	}

	parsed := time.Date(year, month, day, hour, minute, 0, 0, loc)
	if monthStr == "" && dayStr == "" {
		if parsed.Before(now.Add(-5 * time.Minute)) {
			parsed = parsed.Add(24 * time.Hour)
		}
	} else if yearStr == "" && parsed.Before(now.Add(-24*time.Hour)) {
		parsed = parsed.AddDate(1, 0, 0)
	}

	return parsed, nil
}

// parseClaudeCLIQuota парсит ответ команды /usage Claude Code (CLI и MCP режимы)
// и преобразует его в структурированный JSON с группами и корзинами (как у agy).
// Если в выводе нет разобранных корзин квоты, возвращает исходный raw и false.
func parseClaudeCLIQuota(raw []byte, lang string, now time.Time) ([]byte, bool) {
	if len(raw) == 0 {
		return raw, false
	}

	// Ответ может быть как JSON-конвертом CLI ({"type":"result","result":"..."}),
	// так и сырым текстом.
	var env struct {
		Result string `json:"result"`
	}
	text := string(raw)
	if err := json.Unmarshal(raw, &env); err == nil && env.Result != "" {
		text = env.Result
	}

	lines := strings.Split(text, "\n")
	var buckets []map[string]interface{}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		m := reUsageLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		title := strings.TrimSpace(m[1])
		usedPct, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}

		remaining := (100.0 - usedPct) / 100.0
		if remaining < 0 {
			remaining = 0
		} else if remaining > 1 {
			remaining = 1
		}

		lowerTitle := strings.ToLower(title)
		window := ""
		id := "claude-" + strings.ToLower(strings.ReplaceAll(title, " ", "-"))

		if strings.Contains(lowerTitle, "session") || strings.Contains(lowerTitle, "5 hour") || strings.Contains(lowerTitle, "five hour") {
			window = "5h"
			id = "claude-5h"
		} else if strings.Contains(lowerTitle, "sonnet") {
			window = "weekly"
			id = "claude-weekly-sonnet"
		} else if strings.Contains(lowerTitle, "week") {
			window = "weekly"
			id = "claude-weekly"
		}

		bucket := map[string]interface{}{
			"id":                 id,
			"name":               title,
			"window":             window,
			"remaining_fraction": remaining,
		}

		resetRaw := strings.TrimSpace(m[3])
		if resetRaw != "" {
			if resetTime, err := parseClaudeResetTime(resetRaw, now); err == nil {
				bucket["reset_time"] = resetTime.Format(time.RFC3339)
			} else {
				bucket["reset_time"] = resetRaw
			}
		}

		buckets = append(buckets, bucket)
	}

	if len(buckets) == 0 {
		return raw, false
	}

	groupName := i18n.T(lang, "usage.claude_subscription")
	if groupName == "usage.claude_subscription" || groupName == "" {
		if strings.HasPrefix(strings.ToLower(lang), "ru") {
			groupName = "Подписка Claude"
		} else {
			groupName = "Claude Subscription"
		}
	}

	payload := map[string]interface{}{
		"status": "SUCCESS",
		"result": text,
		"command": map[string]interface{}{
			"name": "usage",
			"data": map[string]interface{}{
				"groups": []map[string]interface{}{
					{
						"name":    groupName,
						"buckets": buckets,
					},
				},
			},
		},
	}

	out, err := json.Marshal(payload)
	if err != nil {
		return raw, false
	}
	return out, true
}
