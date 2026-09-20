package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/models"
	"bro-bot/internal/ports"
	"context"
	"html"
	"strconv"
	"strings"
)

// Команды выбора агента, режима выполнения и модели, статистика токенов.

// handleTokens — обработчик команды /tokens.
func handleTokens(s ports.Session) error {
	return s.Send(domain.GlobalTokenTracker.GetTokensCommandMessage(uiLang()), ports.Rich())
}

// handleStats — обработчик команды /stats.
func handleStats(s ports.Session) error {
	return s.Send(domain.GlobalTokenTracker.GetTokensCommandMessage(uiLang()), ports.Rich())
}

// handleContext — обработчик команды /context.
func handleContext(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	var target *domain.TaskSession
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		if id, err := strconv.Atoi(first); err == nil {
			target = domain.GlobalTaskManager.GetTask(id)
			if target == nil {
				return s.Send(i18n.Tf(lang, "task.not_found", id), ports.Rich())
			}
		}
	}

	if target == nil {
		target = domain.GlobalTaskManager.GetActiveTask()
	}

	config.ProjectState.RLock()
	curProj := config.ProjectState.CurrentProject
	curMod := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	msg := domain.GlobalTokenTracker.GetContextCommandMessage(target, curProj, curMod, lang)
	return s.Send(msg, ports.Rich())
}

// handleModels — обработчик команды /models.
func handleModels(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	forceRefresh := len(args) > 0 && (args[0] == "refresh" || args[0] == "update")
	if forceRefresh {
		if _, err := models.GlobalModelRegistry.RefreshModels(true); err != nil {
			return s.Send(i18n.Tf(lang, "models.refresh_failed", ActiveAgentName(), err), nil)
		}
	}

	config.ProjectState.RLock()
	curModel := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	msg := models.GlobalModelRegistry.FormatModelsMessage(curModel, lang)
	return s.Send(msg, ports.Rich())
}

// handleModel — обработчик команды /model.
func handleModel(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) == 0 {
		config.ProjectState.RLock()
		cur := config.ProjectState.CurrentModel
		config.ProjectState.RUnlock()
		return s.Send(i18n.Tf(lang, "models.current", html.EscapeString(cur)), ports.Rich())
	}

	target := strings.TrimSpace(args[0])
	resolved, ok := models.GlobalModelRegistry.ResolveModel(target)
	if !ok {
		return s.Send(i18n.Tf(lang, "models.unknown", html.EscapeString(target)), ports.Rich())
	}

	config.ProjectState.Lock()
	config.ProjectState.CurrentModel = resolved
	config.ProjectState.Unlock()

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "current_model", resolved)
	}

	return s.Send(i18n.Tf(lang, "models.switched", html.EscapeString(resolved)), ports.Rich())
}

// handleAgent — обработчик команды /agent.
func handleAgent(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) == 0 {
		mode := config.ProjectState.GetExecutionMode()
		return s.Send(i18n.Tf(lang, "agent.current",
			html.EscapeString(ActiveAgentName()), html.EscapeString(mode), formatAgentNames()), ports.Rich())
	}

	res, err := SwitchActiveAgent(args[0])
	if err != nil {
		return s.Send("❌ "+html.EscapeString(ErrorText(err, lang)), ports.Rich())
	}
	return s.Send(formatAgentSwitch(res, lang), ports.Rich())
}

// handleMode — обработчик команды /mode.
func handleMode(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) == 0 {
		curMode := config.ProjectState.GetExecutionMode()
		return s.Send(i18n.Tf(lang, "agent.mode_current", html.EscapeString(curMode)), ports.Rich())
	}

	targetMode := strings.ToLower(strings.TrimSpace(args[0]))
	if targetMode != "cli" && targetMode != "api" {
		return s.Send(i18n.T(lang, "agent.mode_unknown"), ports.Rich())
	}

	// Сначала собираем адаптер для нового режима (в api — с проверкой ключа),
	// и только если это удалось, переключаем и сохраняем режим.
	res, err := switchActiveAgent(ActiveAgentName(), targetMode)
	if err != nil {
		return s.Send("⚠️ "+html.EscapeString(ErrorText(err, lang)), ports.Rich())
	}

	config.ProjectState.SetExecutionMode(targetMode)
	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "execution_mode", targetMode)
	}

	return s.Send(i18n.Tf(lang, "agent.mode_switched", formatAgentSwitch(res, lang), html.EscapeString(targetMode)), ports.Rich())
}
