package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
)

// agyBackgroundTerminatedOutput — реальная строка agy из логов задачи, у которой print-режим
// завершился раньше долгого `make test`, запущенного агентом в фоне.
const agyBackgroundTerminatedOutput = "terminating 1 background task(s) on exit"

// interruptedStep — запуск агента, который ушёл в фон и был оборван при выходе.
var interruptedStep = mock.Step{
	Response:    "Запустил тесты, ожидаю завершения...",
	ExtraEvents: []string{"root agent idle; waiting up to 5s for 1 background task(s)", agyBackgroundTerminatedOutput},
}

// setupIncompletePipeline поднимает тестового бота с мок-агентом, работающим по сценарию,
// и выставляет лимит автопродолжений на время теста.
func setupIncompletePipeline(t *testing.T, autoContinueMax int, steps ...mock.Step) (*mockTransport, *mock.AgentFramework, string) {
	t.Helper()
	mt := setupTestApp(t)

	projectsRoot := t.TempDir()
	workDir := filepath.Join(projectsRoot, "testproj")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	config.ProjectsRoot = projectsRoot

	prevMax := config.AutoContinueMax
	config.AutoContinueMax = autoContinueMax
	t.Cleanup(func() { config.AutoContinueMax = prevMax })

	framework := &mock.AgentFramework{
		Name:           "mock-agent",
		ConversationID: "session-incomplete",
		Steps:          steps,
	}
	reg := testAgentRegistry()
	reg.Register(agents.Spec{
		Name:   "mock-agent",
		NewCLI: func() ports.AgentFramework { return framework },
		NewMCP: func() ports.AgentFramework { return framework },
		NewAPI: func() ports.AgentFramework { return framework },
	})
	setAgentRegistry(reg)
	if _, err := SwitchActiveAgent("mock-agent"); err != nil {
		t.Fatalf("switch active agent: %v", err)
	}
	return mt, framework, workDir
}

func sentTextsContain(mt *mockTransport, substr string) bool {
	for _, text := range mt.AllTexts() {
		if strings.Contains(text, substr) {
			return true
		}
	}
	return false
}

func TestPipeline_AutoContinuesInterruptedExecutionStep(t *testing.T) {
	const prURL = "https://github.com/org/repo/pull/7"
	mt, framework, workDir := setupIncompletePipeline(t, 2,
		interruptedStep,
		mock.Step{Response: "Тесты прошли, PR создан.\nPR_URL: " + prURL},
	)

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "mock-agent", "исправить баг", testChatID, false)
	runAgentTaskPipeline(mt.Messenger, testChatID, task, workDir, config.ProjectsRoot)

	lang := i18n.Active()
	calls := framework.Calls()
	if len(calls) != 2 {
		t.Fatalf("ожидалось 2 запуска агента (исходный и автопродолжение), получено %d", len(calls))
	}
	if calls[1].ConversationID != "session-incomplete" {
		t.Errorf("автопродолжение должно идти в той же сессии, ConversationID = %q", calls[1].ConversationID)
	}
	if want := i18n.T(lang, "pipeline.auto_continue_prompt"); calls[1].Prompt != want {
		t.Errorf("промпт автопродолжения = %q, ожидалось %q", calls[1].Prompt, want)
	}

	view := task.Snapshot()
	if view.Status != domain.TaskStatusCompleted {
		t.Errorf("статус = %s, ожидалось %s", view.Status, domain.TaskStatusCompleted)
	}
	if view.LastPRURL != prURL {
		t.Errorf("LastPRURL = %q, ожидалось %q", view.LastPRURL, prURL)
	}
	if notice := i18n.Tf(lang, "pipeline.auto_continue_notice", view.ID, 1, 2); !sentTextsContain(mt, notice) {
		t.Errorf("не отправлено уведомление об автопродолжении %q", notice)
	}
}

func TestPipeline_InterruptedStepIsNotCompletedWhenAutoContinueDisabled(t *testing.T) {
	mt, framework, workDir := setupIncompletePipeline(t, 0, interruptedStep)

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "mock-agent", "исправить баг", testChatID, false)
	runAgentTaskPipeline(mt.Messenger, testChatID, task, workDir, config.ProjectsRoot)

	if n := len(framework.Calls()); n != 1 {
		t.Fatalf("при AUTO_CONTINUE_MAX=0 ожидался 1 запуск агента, получено %d", n)
	}
	assertPausedAsIncomplete(t, mt, task)
}

func TestPipeline_AutoContinueStopsAtLimit(t *testing.T) {
	mt, framework, workDir := setupIncompletePipeline(t, 2, interruptedStep)

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "mock-agent", "исправить баг", testChatID, false)
	runAgentTaskPipeline(mt.Messenger, testChatID, task, workDir, config.ProjectsRoot)

	if n := len(framework.Calls()); n != 3 {
		t.Fatalf("ожидалось 3 запуска агента (исходный и 2 автопродолжения), получено %d", n)
	}
	assertPausedAsIncomplete(t, mt, task)
}

func TestPipeline_AutoContinuesInterruptedPlanningStep(t *testing.T) {
	const plan = "# План\n1. Исправить обработку шага"
	mt, framework, workDir := setupIncompletePipeline(t, 2,
		mock.Step{Response: "Черновик: запускаю тесты в фоне", ExtraEvents: interruptedStep.ExtraEvents},
		mock.Step{Response: plan},
	)

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "mock-agent", "исправить баг", testChatID, true)
	runAgentTaskPipeline(mt.Messenger, testChatID, task, workDir, config.ProjectsRoot)

	calls := framework.Calls()
	if len(calls) != 2 {
		t.Fatalf("ожидалось 2 запуска агента, получено %d", len(calls))
	}
	if want := i18n.T(i18n.Active(), "pipeline.auto_continue_plan_prompt"); calls[1].Prompt != want {
		t.Errorf("промпт автопродолжения планирования = %q, ожидалось %q", calls[1].Prompt, want)
	}

	view := task.Snapshot()
	if view.Status != domain.TaskStatusWaitingApproval {
		t.Errorf("статус = %s, ожидалось %s", view.Status, domain.TaskStatusWaitingApproval)
	}
	if view.Plan != plan {
		t.Errorf("в план на утверждение попали обрывки оборванного хода: %q", view.Plan)
	}
}

// agyBackgroundIdleOutput — строка agy о том, что агент закончил ход, а фоновые задачи ещё идут.
const agyBackgroundIdleOutput = "root agent idle; waiting up to 5s for 1 background task(s)"

func TestPipeline_IdleWithoutTerminationLineIsNotCompleted(t *testing.T) {
	// Строка terminating не дошла (оборван PTY или сменился формат CLI): обрыв всё равно виден
	// по idle, после которого агент так и не проснулся.
	// Как у agy: сначала финальный ответ агента, затем строка idle и выход без terminating.
	finalAnswer := `{"type":"assistant","session_id":"session-incomplete","message":{"role":"assistant",` +
		`"content":[{"type":"text","text":"Запустил make test, жду результата."}]}}`
	mt, framework, workDir := setupIncompletePipeline(t, 0,
		mock.Step{ExtraEvents: []string{finalAnswer, agyBackgroundIdleOutput}},
	)

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "mock-agent", "исправить баг", testChatID, false)
	runAgentTaskPipeline(mt.Messenger, testChatID, task, workDir, config.ProjectsRoot)

	if n := len(framework.Calls()); n != 1 {
		t.Fatalf("ожидался 1 запуск агента, получено %d", n)
	}
	assertPausedAsIncomplete(t, mt, task)
}

func TestPipeline_IdleFollowedByAgentActivityCompletesNormally(t *testing.T) {
	// Фоновая задача успела за отведённые секунды, агент проснулся и продолжил работу.
	toolCall := `{"type":"assistant","session_id":"session-incomplete","message":{"role":"assistant",` +
		`"content":[{"type":"tool_use","id":"t1","name":"run_command","input":{"command":"git status"}}]}}`
	mt, framework, workDir := setupIncompletePipeline(t, 2,
		mock.Step{Response: "Готово, изменения внесены.", ExtraEvents: []string{agyBackgroundIdleOutput, toolCall}},
	)

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "mock-agent", "исправить баг", testChatID, false)
	runAgentTaskPipeline(mt.Messenger, testChatID, task, workDir, config.ProjectsRoot)

	if n := len(framework.Calls()); n != 1 {
		t.Fatalf("проснувшийся агент не должен перезапускаться, запусков %d", n)
	}
	if status := task.Snapshot().Status; status != domain.TaskStatusCompleted {
		t.Errorf("статус = %s, ожидалось %s", status, domain.TaskStatusCompleted)
	}
}

func assertPausedAsIncomplete(t *testing.T, mt *mockTransport, task *domain.TaskSession) {
	t.Helper()
	lang := i18n.Active()
	view := task.Snapshot()
	if view.Status != domain.TaskStatusPaused {
		t.Errorf("статус = %s, ожидалось %s", view.Status, domain.TaskStatusPaused)
	}
	incomplete := i18n.Tf(lang, "pipeline.incomplete_message", view.ID, "testproj",
		i18n.T(lang, "pipeline.phase_running"), i18n.T(lang, "btn.resume"), view.ID)
	if !sentTextsContain(mt, incomplete) {
		t.Errorf("не отправлено сообщение о незавершённой задаче %q", incomplete)
	}
	completed := i18n.Tf(lang, "pipeline.completed", view.ID, "testproj")
	if sentTextsContain(mt, completed) {
		t.Errorf("оборванная задача не должна объявляться завершённой")
	}
}
