package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
)

func restoreTimeoutConfig(t *testing.T) {
	t.Helper()
	prevChat := config.ChatTimeout
	prevStep := config.StepTimeout
	t.Cleanup(func() {
		config.ChatTimeout = prevChat
		config.StepTimeout = prevStep
	})
}

func TestTimeoutDefaultsAndOverrides(t *testing.T) {
	restoreTimeoutConfig(t)

	config.ChatTimeout = 0
	config.StepTimeout = 0
	if got := chatTimeout(); got != time.Hour {
		t.Errorf("chatTimeout() with zero config = %v, want %v", got, time.Hour)
	}
	if got := stepTimeout(); got != time.Hour {
		t.Errorf("stepTimeout() with zero config = %v, want %v", got, time.Hour)
	}

	config.ChatTimeout = 45 * time.Minute
	config.StepTimeout = 90 * time.Minute
	if got := chatTimeout(); got != 45*time.Minute {
		t.Errorf("chatTimeout() override = %v, want %v", got, 45*time.Minute)
	}
	if got := stepTimeout(); got != 90*time.Minute {
		t.Errorf("stepTimeout() override = %v, want %v", got, 90*time.Minute)
	}
}

func TestTimeoutReachesExecuteArgs(t *testing.T) {
	mt := setupTestApp(t)
	restoreEffortState(t)
	restoreTimeoutConfig(t)

	projDir := filepath.Join(config.ProjectsRoot, "testproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	framework := &mock.AgentFramework{Name: "timeout-mock", ConversationID: "conv-1", Response: "ok"}
	reg := testAgentRegistry()
	reg.Register(agents.Spec{
		Name:   "timeout-mock",
		NewMCP: func() ports.AgentFramework { return framework },
		NewAPI: func() ports.AgentFramework { return framework },
	})
	setAgentRegistry(reg)
	if _, err := SwitchActiveAgent("timeout-mock"); err != nil {
		t.Fatalf("SwitchActiveAgent: %v", err)
	}

	// 1. Нулевые значения конфигурации падают обратно на часовой таймаут по умолчанию.
	config.StepTimeout = 0
	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "timeout-mock", "задача", testChatID, false)
	if res := executeStepForTask(mt.Messenger, testChatID, task, projDir, "шаг", "mock-model", "ru"); res.Outcome != StepOutcomeSuccess {
		t.Fatalf("шаг задачи (default): %v", res.Error)
	}
	if last := framework.LastCall(); last == nil || last.Timeout != time.Hour {
		t.Fatalf("шаг задачи получил Timeout=%v, ожидали %v", last, time.Hour)
	}

	// 2. Переопределённый STEP_TIMEOUT доходит до ExecuteArgs.
	config.StepTimeout = 90 * time.Minute
	task2 := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "timeout-mock", "задача 2", testChatID, false)
	if res := executeStepForTask(mt.Messenger, testChatID, task2, projDir, "шаг 2", "mock-model", "ru"); res.Outcome != StepOutcomeSuccess {
		t.Fatalf("шаг задачи (override): %v", res.Error)
	}
	if last := framework.LastCall(); last == nil || last.Timeout != 90*time.Minute {
		t.Fatalf("шаг задачи получил Timeout=%v, ожидали %v", last, 90*time.Minute)
	}

	session := domain.GlobalChatManager.GetOrCreate("testproj", "mock-model")
	setup := chatTurnSetup{
		Project:   "testproj",
		Agent:     "timeout-mock",
		Mode:      "mcp",
		Model:     "mock-model",
		Framework: framework,
	}

	// 3. В диалоге при нулевом ChatTimeout передаётся часовой таймаут по умолчанию.
	config.ChatTimeout = 0
	runChatTurn(context.Background(), mt.Messenger, testChatID, session, setup, "вопрос 1", "")
	if last := framework.LastCall(); last == nil || last.Timeout != time.Hour {
		t.Fatalf("ход диалога получил Timeout=%v, ожидали %v", last, time.Hour)
	}

	// 4. Переопределённый CHAT_TIMEOUT доходит до ExecuteArgs.
	config.ChatTimeout = 45 * time.Minute
	runChatTurn(context.Background(), mt.Messenger, testChatID, session, setup, "вопрос 2", "")
	if last := framework.LastCall(); last == nil || last.Timeout != 45*time.Minute {
		t.Fatalf("ход диалога получил Timeout=%v, ожидали %v", last, 45*time.Minute)
	}
}

func TestChatHandlesAgyPrintTimeoutBanner(t *testing.T) {
	mt := setupTestApp(t)
	restoreTimeoutConfig(t)

	streamEvents := `{"type":"system","session_id":"conv-timeout"}` + "\n" +
		`[agy] print timeout after 1h0m0s with turn in progress; returning partial output` + "\n"

	proc := &mockPTYProcess{
		reader: strings.NewReader(streamEvents),
	}
	fw := &mockPTYAgentFramework{proc: proc}

	config.ChatTimeout = time.Hour
	session := domain.GlobalChatManager.GetOrCreate("testproj", "mock-model")
	setup := chatTurnSetup{
		Project:   "testproj",
		Agent:     "agy",
		Mode:      "mcp",
		Model:     "mock-model",
		Framework: fw,
	}

	runChatTurn(context.Background(), mt.Messenger, testChatID, session, setup, "долгий вопрос", "")

	lang := i18n.Active()
	wantText := i18n.Tf(lang, "chat.answer_timeout", domain.FormatDuration(time.Hour, lang))

	found := false
	for _, text := range mt.AllTexts() {
		if strings.Contains(text, wantText) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ожидали сообщение о таймауте %q, получили %v", wantText, mt.AllTexts())
	}

	if got := session.ConversationIDFor("agy", "mcp"); got != "conv-timeout" {
		t.Errorf("ConversationIDFor(agy, mcp) = %q, ожидали conv-timeout", got)
	}
	if got := session.TurnsCount(); got != 0 {
		t.Errorf("TurnsCount() = %d, ожидали 0 для оборванного по таймауту хода", got)
	}
}
