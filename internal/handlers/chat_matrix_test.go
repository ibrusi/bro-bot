package handlers

import (
	"strings"
	"testing"

	"bro-bot/internal/adapters/agy"
	"bro-bot/internal/adapters/claude"
	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
)

// setupMatrixApp поднимает бота с мок-агентом для конкретной пары «агент + режим».
func setupMatrixApp(t *testing.T, agentName, mode, convID, response string) (*mockTransport, *mock.AgentFramework) {
	t.Helper()

	mt, agent := setupChatTestApp(t, response)
	agent.Name = agentName
	agent.ConversationID = convID

	SetActiveAgent(agent, agentName)
	config.ProjectState.SetCurrentAgent(agentName)
	config.ProjectState.SetExecutionMode(mode)

	return mt, agent
}

// TestChatWorksForEveryAgentAndMode проверяет диалог во всех четырёх сочетаниях
// агента (agy, claude) и режима выполнения (cli, api).
func TestChatWorksForEveryAgentAndMode(t *testing.T) {
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
			convID := tc.agent + "-" + tc.mode + "-conv"
			mt, agent := setupMatrixApp(t, tc.agent, tc.mode, convID, "Ответ агента в диалоге.")

			sendText(t, mt, "как устроен роутер сообщений?")
			waitFor(t, "ответ агента", func() bool { return len(agent.Calls()) == 1 })
			waitFor(t, "завершение хода", func() bool {
				session := domain.GlobalChatManager.Get("testproj")
				return session != nil && !session.IsRunning()
			})

			// 1. Ответ дошёл до пользователя и задача при этом не создавалась.
			answered := false
			for _, text := range mt.AllTexts() {
				if strings.Contains(text, "Ответ агента в диалоге") {
					answered = true
				}
			}
			if !answered {
				t.Fatalf("ответ агента не отправлен пользователю: %v", mt.AllTexts())
			}
			if got := len(domain.GlobalTaskManager.ListTasks()); got != 0 {
				t.Errorf("диалог не должен создавать задачи, создано %d", got)
			}

			first := agent.Calls()[0]
			if first.ConversationID != "" {
				t.Errorf("первый ход должен идти без сессии агента, получили %q", first.ConversationID)
			}
			if first.WorkDir == "" {
				t.Error("агент должен запускаться в каталоге проекта")
			}

			// 2. Память режима: в api историю и преамбулу передаём сами, в cli их держит агент.
			if tc.mode == "api" {
				if first.SystemPrompt == "" || (!strings.Contains(first.SystemPrompt, "РЕЖИМ ДИАЛОГА") && !strings.Contains(first.SystemPrompt, "CHAT MODE")) {
					t.Error("в api-режиме преамбула диалога должна уходить системным полем")
				}
				if first.Prompt != "как устроен роутер сообщений?" {
					t.Errorf("в api-режиме промпт не должен обрастать преамбулой: %q", first.Prompt)
				}
			} else {
				if first.SystemPrompt != "" {
					t.Error("в cli-режиме системное поле не используется")
				}
				if !strings.Contains(first.Prompt, "РЕЖИМ ДИАЛОГА") && !strings.Contains(first.Prompt, "CHAT MODE") {
					t.Errorf("в cli-режиме преамбула должна быть в промпте: %q", first.Prompt)
				}
			}

			// 3. Идентификатор сессии агента сохранён именно для своей пары агент/режим.
			session := domain.GlobalChatManager.Get("testproj")
			if got := session.ConversationIDFor(tc.agent, tc.mode); got != convID {
				t.Errorf("сессия %s/%s = %q, ожидали %q", tc.agent, tc.mode, got, convID)
			}

			// 4. Второй ход продолжает тот же разговор.
			sendText(t, mt, "а если сообщение — вопрос?")
			waitFor(t, "второй ответ", func() bool { return len(agent.Calls()) == 2 })

			second := agent.Calls()[1]
			if second.ConversationID != convID {
				t.Errorf("второй ход должен продолжать сессию %q, получили %q", convID, second.ConversationID)
			}
			if tc.mode == "api" {
				if len(second.History) == 0 {
					t.Error("в api-режиме второй ход должен получать историю диалога")
				} else if second.History[0].Content != "как устроен роутер сообщений?" {
					t.Errorf("первая реплика истории = %q", second.History[0].Content)
				}
			} else if len(second.History) != 0 {
				t.Error("в cli-режиме история передаваться не должна — её хранит сам агент")
			}
		})
	}
}

// TestChatSurvivesAgentSwitch проверяет, что переключение агента посреди разговора
// не теряет нить: новый агент получает контекст прошлых реплик и свой идентификатор сессии.
func TestChatSurvivesAgentSwitch(t *testing.T) {
	mt, agyAgent := setupMatrixApp(t, "agy", "cli", "agy-conv", "Отвечает agy.")

	sendText(t, mt, "расскажи про адаптеры")
	waitFor(t, "ответ agy", func() bool { return len(agyAgent.Calls()) == 1 })
	waitFor(t, "завершение хода", func() bool {
		session := domain.GlobalChatManager.Get("testproj")
		return session != nil && !session.IsRunning()
	})

	// Пользователь переключился на другого агента.
	claudeAgent := &mock.AgentFramework{Name: "claude", Response: "Отвечает claude.", ConversationID: "claude-conv"}
	SetActiveAgent(claudeAgent, "claude")
	config.ProjectState.SetCurrentAgent("claude")

	sendText(t, mt, "а что по памяти диалога?")
	waitFor(t, "ответ claude", func() bool { return len(claudeAgent.Calls()) == 1 })

	call := claudeAgent.Calls()[0]
	if call.ConversationID != "" {
		t.Errorf("у нового агента своя сессия, ожидали пустой идентификатор, получили %q", call.ConversationID)
	}
	if !strings.Contains(call.Prompt, "КОНТЕКСТ ПРЕДЫДУЩЕГО РАЗГОВОРА") {
		t.Errorf("новому агенту должен передаваться контекст прошлых реплик: %q", call.Prompt)
	}
	if !strings.Contains(call.Prompt, "расскажи про адаптеры") {
		t.Errorf("в контексте должна быть прошлая реплика пользователя: %q", call.Prompt)
	}

	session := domain.GlobalChatManager.Get("testproj")
	if session.ConversationIDFor("agy", "cli") != "agy-conv" {
		t.Error("сессия agy должна сохраниться после переключения агента")
	}
	if session.ConversationIDFor("claude", "cli") != "claude-conv" {
		t.Error("для claude должен появиться собственный идентификатор сессии")
	}
}

// TestChatSurvivesModeSwitch проверяет переключение cli → api посреди разговора:
// api-адаптер получает историю диалога из базы бота.
func TestChatSurvivesModeSwitch(t *testing.T) {
	mt, agent := setupMatrixApp(t, "agy", "cli", "agy-cli-conv", "Ответ в cli.")

	sendText(t, mt, "как работает разговорный режим?")
	waitFor(t, "ответ в cli", func() bool { return len(agent.Calls()) == 1 })
	waitFor(t, "завершение хода", func() bool {
		session := domain.GlobalChatManager.Get("testproj")
		return session != nil && !session.IsRunning()
	})

	config.ProjectState.SetExecutionMode("api")
	agent.ConversationID = "agy-api-conv"

	sendText(t, mt, "а в api-режиме?")
	waitFor(t, "ответ в api", func() bool { return len(agent.Calls()) == 2 })

	call := agent.Calls()[1]
	if call.ConversationID != "" {
		t.Errorf("в новом режиме своя сессия, ожидали пустой идентификатор, получили %q", call.ConversationID)
	}
	if len(call.History) == 0 {
		t.Fatal("после переключения в api агент должен получить историю диалога")
	}
	if call.History[0].Content != "как работает разговорный режим?" {
		t.Errorf("первая реплика истории = %q", call.History[0].Content)
	}
	if call.SystemPrompt == "" {
		t.Error("в api-режиме должна передаваться преамбула диалога")
	}

	session := domain.GlobalChatManager.Get("testproj")
	if session.ConversationIDFor("agy", "cli") != "agy-cli-conv" {
		t.Error("сессия cli должна сохраниться")
	}
	if session.ConversationIDFor("agy", "api") != "agy-api-conv" {
		t.Error("для api-режима должен появиться отдельный идентификатор сессии")
	}
}

// TestAgentFrameworkForRespectsExecutionMode — регрессия: раньше для неактивного агента
// всегда создавался CLI-адаптер, даже когда включён режим api.
func TestAgentFrameworkForRespectsExecutionMode(t *testing.T) {
	setupTestApp(t)

	prevFramework, prevName := ActiveAgent()
	// Пустой адаптер заставляет agentFrameworkFor собирать его заново — это и проверяем.
	SetActiveAgent(nil, "agy")
	t.Cleanup(func() {
		SetActiveAgent(prevFramework, prevName)
		config.ProjectState.SetExecutionMode("cli")
	})

	t.Setenv("GEMINI_API_KEY", "test-gemini-key")
	t.Setenv("ANTHROPIC_API_KEY", "test-anthropic-key")

	config.ProjectState.SetExecutionMode("cli")
	if framework, err := agentFrameworkFor("claude"); err != nil {
		t.Fatalf("claude/cli: %v", err)
	} else if _, ok := framework.(*claude.ClaudeAdapter); !ok {
		t.Errorf("claude/cli: получили %T", framework)
	}

	config.ProjectState.SetExecutionMode("api")
	if framework, err := agentFrameworkFor("claude"); err != nil {
		t.Fatalf("claude/api: %v", err)
	} else if _, ok := framework.(*claude.ClaudeAPIAdapter); !ok {
		t.Errorf("claude/api: получили %T, ожидали API-адаптер", framework)
	}
	if framework, err := agentFrameworkFor("agy"); err != nil {
		t.Fatalf("agy/api: %v", err)
	} else if _, ok := framework.(*agy.AgyAPIAdapter); !ok {
		t.Errorf("agy/api: получили %T, ожидали API-адаптер", framework)
	}
}

// TestAgentFrameworkForRequiresAPIKey — без ключа api-режим сообщает об этом понятной ошибкой.
func TestAgentFrameworkForRequiresAPIKey(t *testing.T) {
	setupTestApp(t)

	prevFramework, prevName := ActiveAgent()
	SetActiveAgent(nil, "agy")
	t.Cleanup(func() {
		SetActiveAgent(prevFramework, prevName)
		config.ProjectState.SetExecutionMode("cli")
	})

	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_API_KEY", "")
	config.ProjectState.SetExecutionMode("api")

	if _, err := agentFrameworkFor("agy"); err == nil {
		t.Error("ожидали ошибку об отсутствующем GEMINI_API_KEY")
	}
	if _, err := agentFrameworkFor("claude"); err == nil {
		t.Error("ожидали ошибку об отсутствующем ANTHROPIC_API_KEY")
	}
}
