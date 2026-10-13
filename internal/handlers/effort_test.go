package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/effort"
	"bro-bot/internal/ports"
)

// restoreEffortState возвращает глобальные уровень, режим и агента после теста:
// оставленный high или api сломал бы соседние тесты.
func restoreEffortState(t *testing.T) {
	t.Helper()
	prevEffort := config.ProjectState.GetEffort()
	prevMode := config.ProjectState.GetExecutionMode()
	prevFramework, prevName := ActiveAgent()
	t.Cleanup(func() {
		config.ProjectState.SetEffort(prevEffort)
		config.ProjectState.SetExecutionMode(prevMode)
		SetActiveAgent(prevFramework, prevName)
		setAgentRegistry(testAgentRegistry())
	})
}

func runEffortCommand(t *testing.T, mt *mockTransport, args ...string) *mock.SentMessage {
	t.Helper()
	handler, ok := mt.commands["effort"]
	if !ok {
		t.Fatal("команда /effort не зарегистрирована")
	}
	if err := handler(adminSession(mt, &mock.Session{ArgsVal: args})); err != nil {
		t.Fatalf("/effort %v: %v", args, err)
	}
	last := mt.LastSent()
	if last == nil {
		t.Fatalf("/effort %v ничего не ответил", args)
	}
	return last
}

// effortButtons возвращает подписи кнопок по payload.
func effortButtons(t *testing.T, msg *mock.SentMessage) map[string]string {
	t.Helper()
	if msg.Opts == nil || msg.Opts.Keyboard == nil {
		t.Fatal("ожидали клавиатуру выбора уровня")
	}
	buttons := map[string]string{}
	for _, row := range msg.Opts.Keyboard.Rows {
		for _, btn := range row {
			if btn.Action != effortCallbackAction {
				t.Errorf("кнопка %q с чужим действием %q", btn.Text, btn.Action)
			}
			buttons[btn.Payload] = btn.Text
		}
	}
	return buttons
}

func TestEffortDefaultsToAuto(t *testing.T) {
	setupTestApp(t)
	restoreEffortState(t)

	if got := config.ProjectState.GetEffort(); !got.IsAuto() {
		t.Errorf("уровень по умолчанию = %q, ожидали auto", got)
	}
}

func TestEffortCommandShowsLevelsAndKeyboard(t *testing.T) {
	mt := setupTestApp(t)
	restoreEffortState(t)

	msg := runEffortCommand(t, mt)
	if !strings.Contains(msg.Text, "<code>auto</code>") {
		t.Errorf("в ответе нет текущего уровня: %s", msg.Text)
	}

	buttons := effortButtons(t, msg)
	for _, name := range []string{"auto", "low", "medium", "high", "xhigh", "max"} {
		if _, ok := buttons[name]; !ok {
			t.Errorf("нет кнопки %q: %v", name, buttons)
		}
	}
	if buttons["auto"] != "✅ auto" {
		t.Errorf("текущий уровень должен быть отмечен: %q", buttons["auto"])
	}
}

func TestEffortCommandSwitchesAndPersists(t *testing.T) {
	mt := setupTestApp(t)
	restoreEffortState(t)

	msg := runEffortCommand(t, mt, "HIGH")
	if got := config.ProjectState.GetEffort(); got != effort.High {
		t.Fatalf("уровень = %q, ожидали high", got)
	}
	if !strings.Contains(msg.Text, "<code>high</code>") {
		t.Errorf("ответ не подтверждает уровень: %s", msg.Text)
	}

	saved, err := domain.GlobalTaskManager.Storage().GetSetting(context.Background(), effortSettingKey)
	if err != nil || saved != "high" {
		t.Errorf("в SQLite сохранено %q (ошибка %v), ожидали high", saved, err)
	}

	buttons := effortButtons(t, runEffortCommand(t, mt))
	if buttons["high"] != "✅ high" || buttons["auto"] != "auto" {
		t.Errorf("галочка должна переехать на high: %v", buttons)
	}
}

func TestEffortCommandRejectsUnknownLevel(t *testing.T) {
	mt := setupTestApp(t)
	restoreEffortState(t)
	config.ProjectState.SetEffort(effort.Low)

	msg := runEffortCommand(t, mt, "ultra")
	if got := config.ProjectState.GetEffort(); got != effort.Low {
		t.Errorf("незнакомый уровень не должен менять настройку, стало %q", got)
	}
	if !strings.Contains(msg.Text, "ultra") || !strings.Contains(msg.Text, "auto|low|medium|high|xhigh|max") {
		t.Errorf("ответ должен назвать ошибку и варианты: %s", msg.Text)
	}
}

func TestEffortCallbackSwitchesLevel(t *testing.T) {
	mt := setupTestApp(t)
	restoreEffortState(t)

	cb, ok := mt.callbacks[effortCallbackAction]
	if !ok {
		t.Fatalf("кнопка %s не зарегистрирована", effortCallbackAction)
	}
	sess := adminSession(mt, &mock.Session{CB: &ports.CallbackQuery{Payload: "max"}})
	if err := cb(sess); err != nil {
		t.Fatalf("effort_sel: %v", err)
	}

	if got := config.ProjectState.GetEffort(); got != effort.Max {
		t.Errorf("уровень = %q, ожидали max", got)
	}
}

// Адаптер, который уровень не применяет (agy в режиме api), должен быть назван
// в ответе, а остальные — нет.
func TestEffortWarnsWhenAdapterIgnoresIt(t *testing.T) {
	mt := setupTestApp(t)
	restoreEffortState(t)
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	config.ProjectState.SetExecutionMode(agents.ModeAPI)
	if _, err := SwitchActiveAgent("agy"); err != nil {
		t.Fatalf("SwitchActiveAgent(agy): %v", err)
	}
	if msg := runEffortCommand(t, mt, "high"); !strings.Contains(msg.Text, "⚠️") {
		t.Errorf("ожидали предупреждение для agy в api: %s", msg.Text)
	}

	if _, err := SwitchActiveAgent("claude"); err != nil {
		t.Fatalf("SwitchActiveAgent(claude): %v", err)
	}
	if msg := runEffortCommand(t, mt, "high"); strings.Contains(msg.Text, "⚠️") {
		t.Errorf("claude в api уровень применяет, предупреждение лишнее: %s", msg.Text)
	}
}

func TestRestoreEffort(t *testing.T) {
	restoreEffortState(t)

	cases := []struct {
		saved string
		want  effort.Level
	}{
		{"xhigh", effort.XHigh},
		{"auto", effort.Auto},
		{"", effort.Auto},
		{"мусор", effort.Auto},
	}
	for _, tc := range cases {
		config.ProjectState.SetEffort(effort.Max)
		restoreEffort(tc.saved)
		if got := config.ProjectState.GetEffort(); got != tc.want {
			t.Errorf("restoreEffort(%q) = %q, ожидали %q", tc.saved, got, tc.want)
		}
	}
}

// Уровень доходит до агента и в задаче, и в диалоге.
func TestEffortReachesExecuteArgs(t *testing.T) {
	mt := setupTestApp(t)
	restoreEffortState(t)

	projDir := filepath.Join(config.ProjectsRoot, "testproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	framework := &mock.AgentFramework{Name: "effort-mock", ConversationID: "conv-1", Response: "ok"}
	reg := testAgentRegistry()
	reg.Register(agents.Spec{
		Name:   "effort-mock",
		NewMCP: func() ports.AgentFramework { return framework },
		NewAPI: func() ports.AgentFramework { return framework },
	})
	setAgentRegistry(reg)
	if _, err := SwitchActiveAgent("effort-mock"); err != nil {
		t.Fatalf("SwitchActiveAgent: %v", err)
	}

	config.ProjectState.SetEffort(effort.XHigh)
	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "effort-mock", "задача", testChatID, false)
	if res := executeStepForTask(mt.Messenger, testChatID, task, projDir, "шаг", "mock-model", "ru"); res.Outcome != StepOutcomeSuccess {
		t.Fatalf("шаг задачи: %v", res.Error)
	}
	if last := framework.LastCall(); last == nil || last.Effort != effort.XHigh {
		t.Fatalf("шаг задачи получил уровень %+v, ожидали xhigh", last)
	}

	config.ProjectState.SetEffort(effort.Low)
	session := domain.GlobalChatManager.GetOrCreate("testproj", "mock-model")
	setup := chatTurnSetup{
		Project:   "testproj",
		Agent:     "effort-mock",
		Mode:      "mcp",
		Model:     "mock-model",
		Effort:    config.ProjectState.GetEffort(),
		Framework: framework,
	}
	runChatTurn(context.Background(), mt.Messenger, testChatID, session, setup, "вопрос", "")
	if last := framework.LastCall(); last == nil || last.Effort != effort.Low {
		t.Fatalf("ход диалога получил уровень %+v, ожидали low", last)
	}
}
