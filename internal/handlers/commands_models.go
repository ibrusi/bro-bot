package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/models"
	"bro-bot/internal/ports"
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
)

// Команды выбора агента, режима выполнения и модели, статистика токенов.

// handleTokens — обработчик команды /tokens.
func handleTokens(s ports.Session) error {
	msg := domain.GlobalTokenTracker.GetTokensCommandMessage()
	return s.Send(msg, ports.Rich())
}

// handleStats — обработчик команды /stats.
func handleStats(s ports.Session) error {
	msg := domain.GlobalTokenTracker.GetTokensCommandMessage()
	return s.Send(msg, ports.Rich())
}

// handleContext — обработчик команды /context.
func handleContext(s ports.Session) error {
	args := s.Args()
	var target *domain.TaskSession
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		if id, err := strconv.Atoi(first); err == nil {
			target = domain.GlobalTaskManager.GetTask(id)
			if target == nil {
				return s.Send(fmt.Sprintf("❌ Задача #%d не найдена. Список задач: /tasks", id), ports.Rich())
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

	msg := domain.GlobalTokenTracker.GetContextCommandMessage(target, curProj, curMod)
	return s.Send(msg, ports.Rich())
}

// handleModels — обработчик команды /models.
func handleModels(s ports.Session) error {
	args := s.Args()
	forceRefresh := len(args) > 0 && (args[0] == "refresh" || args[0] == "update")
	if forceRefresh {
		if _, err := models.GlobalModelRegistry.RefreshModels(true); err != nil {
			return s.Send(fmt.Sprintf("⚠️ Ошибка синхронизации с %s: %v\nПоказан кэшированный список.", ActiveAgentName(), err), nil)
		}
	}

	config.ProjectState.RLock()
	curModel := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	msg := models.GlobalModelRegistry.FormatModelsMessage(curModel)
	return s.Send(msg, ports.Rich())
}

// handleModel — обработчик команды /model.
func handleModel(s ports.Session) error {
	args := s.Args()
	if len(args) == 0 {
		config.ProjectState.RLock()
		cur := config.ProjectState.CurrentModel
		config.ProjectState.RUnlock()
		return s.Send(fmt.Sprintf("Текущая модель: <code>%s</code>\nИспользование: <code>/model &lt;имя&gt;</code> (например, <code>/model sonnet</code>)", html.EscapeString(cur)), ports.Rich())
	}

	target := strings.TrimSpace(args[0])
	resolved, ok := models.GlobalModelRegistry.ResolveModel(target)
	if !ok {
		return s.Send(fmt.Sprintf("❌ Неизвестная модель: <code>%s</code>. Список: /models", html.EscapeString(target)), ports.Rich())
	}

	config.ProjectState.Lock()
	config.ProjectState.CurrentModel = resolved
	config.ProjectState.Unlock()

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "current_model", resolved)
	}

	return s.Send(fmt.Sprintf("✅ Модель переключена на: <code>%s</code>", html.EscapeString(resolved)), ports.Rich())
}

// handleAgent — обработчик команды /agent.
func handleAgent(s ports.Session) error {
	args := s.Args()
	if len(args) == 0 {
		mode := config.ProjectState.GetExecutionMode()
		return s.Send(fmt.Sprintf("🤖 Текущий агент: <code>%s</code> (режим: <code>%s</code>)\nДоступны: %s",
			html.EscapeString(ActiveAgentName()), html.EscapeString(mode), formatAgentNames()), ports.Rich())
	}

	res, err := SwitchActiveAgent(args[0])
	if err != nil {
		return s.Send(fmt.Sprintf("❌ %s", html.EscapeString(err.Error())), ports.Rich())
	}
	return s.Send(formatAgentSwitch(res), ports.Rich())
}

// handleMode — обработчик команды /mode.
func handleMode(s ports.Session) error {
	args := s.Args()
	if len(args) == 0 {
		curMode := config.ProjectState.GetExecutionMode()
		return s.Send(fmt.Sprintf("⚙️ Текущий режим выполнения агента: <code>%s</code>\nДоступные варианты: <code>cli</code>, <code>api</code>\nПереключение: <code>/mode cli</code> или <code>/mode api</code>", html.EscapeString(curMode)), ports.Rich())
	}

	targetMode := strings.ToLower(strings.TrimSpace(args[0]))
	if targetMode != "cli" && targetMode != "api" {
		return s.Send("❌ Неизвестный режим. Доступны: <code>cli</code>, <code>api</code>", ports.Rich())
	}

	// Сначала собираем адаптер для нового режима (в api — с проверкой ключа),
	// и только если это удалось, переключаем и сохраняем режим.
	res, err := switchActiveAgent(ActiveAgentName(), targetMode)
	if err != nil {
		return s.Send(fmt.Sprintf("⚠️ %s", html.EscapeString(err.Error())), ports.Rich())
	}

	config.ProjectState.SetExecutionMode(targetMode)
	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "execution_mode", targetMode)
	}

	return s.Send(fmt.Sprintf("%s\n✅ Режим выполнения переключен на: <code>%s</code>", formatAgentSwitch(res), html.EscapeString(targetMode)), ports.Rich())
}
