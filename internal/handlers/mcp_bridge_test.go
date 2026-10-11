package handlers

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/agents"
	"bro-bot/internal/domain"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"
)

// fakeChannel записывает, что агент сделал через инструменты бота.
type fakeChannel struct {
	messages  []string
	progress  []string
	questions []string
	options   [][]string
	err       error
}

func (f *fakeChannel) SendMessage(text string) error {
	f.messages = append(f.messages, text)
	return f.err
}

func (f *fakeChannel) ReportProgress(status string) { f.progress = append(f.progress, status) }

func (f *fakeChannel) AskUser(question string, options []string) error {
	f.questions = append(f.questions, question)
	f.options = append(f.options, options)
	return f.err
}

// processTree — PPID по PID для подмены /proc в тестах.
func processTree(parents map[int]int) func(int) (int, error) {
	return func(pid int) (int, error) {
		if parent, ok := parents[pid]; ok {
			return parent, nil
		}
		return 0, errors.New("no such process")
	}
}

func call(tool string, args map[string]interface{}) mcp.BridgeCall {
	return mcp.BridgeCall{Tool: tool, Arguments: args}
}

// TestAgentChannelsFindAgentByAncestor — MCP-сервер — потомок агента (иногда через
// обёртку), и вызов должен найти канал по цепочке предков.
func TestAgentChannelsFindAgentByAncestor(t *testing.T) {
	channels := newAgentChannels(processTree(map[int]int{300: 200, 200: 100, 100: 1, 500: 1}))
	ch := &fakeChannel{}
	unbind := channels.bind(100, ch)

	res := channels.handle(context.Background(), 300, call(mcp.ToolSendMessage, map[string]interface{}{"message": "привет"}))
	if res.IsError || len(ch.messages) != 1 || ch.messages[0] != "привет" {
		t.Fatalf("result = %+v, messages = %v", res, ch.messages)
	}

	if res := channels.handle(context.Background(), 500, call(mcp.ToolSendMessage, map[string]interface{}{"message": "x"})); !res.IsError {
		t.Error("a process outside the agent tree must not reach the channel")
	}

	unbind()
	if res := channels.handle(context.Background(), 300, call(mcp.ToolSendMessage, map[string]interface{}{"message": "x"})); !res.IsError {
		t.Error("an unbound agent must not reach the channel")
	}
}

// TestAgentChannelsUnbindKeepsNewerBinding — отвязка старого шага не должна снять
// привязку нового, если ОС успела переиспользовать PID.
func TestAgentChannelsUnbindKeepsNewerBinding(t *testing.T) {
	channels := newAgentChannels(processTree(nil))
	old, newer := &fakeChannel{}, &fakeChannel{}
	unbindOld := channels.bind(100, old)
	channels.bind(100, newer)
	unbindOld()

	if ch, ok := channels.lookup(100); !ok || ch != newer {
		t.Errorf("lookup = %v, %v; want the newer channel", ch, ok)
	}
	if unbind := channels.bind(0, &fakeChannel{}); unbind == nil {
		t.Error("bind without PID must return a no-op unbind")
	}
}

func TestAgentChannelsTools(t *testing.T) {
	channels := newAgentChannels(processTree(nil))
	ch := &fakeChannel{}
	channels.bind(100, ch)
	ctx := context.Background()

	res := channels.handle(ctx, 100, call(mcp.ToolAskUser, map[string]interface{}{
		"question": " Какую ветку взять? ",
		"options":  []interface{}{"main", "", 42, "dev"},
	}))
	if res.IsError || !strings.Contains(res.Text, "End your turn") {
		t.Errorf("ask_user result = %+v", res)
	}
	if ch.questions[0] != "Какую ветку взять?" || !reflect.DeepEqual(ch.options[0], []string{"main", "dev"}) {
		t.Errorf("question = %q, options = %v", ch.questions[0], ch.options[0])
	}

	channels.handle(ctx, 100, call(mcp.ToolReportProgress, map[string]interface{}{"status": "тесты", "percent": float64(40)}))
	channels.handle(ctx, 100, call(mcp.ToolReportProgress, map[string]interface{}{"status": "сборка", "percent": float64(400)}))
	if !reflect.DeepEqual(ch.progress, []string{"тесты (40%)", "сборка"}) {
		t.Errorf("progress = %v", ch.progress)
	}

	for _, bad := range []mcp.BridgeCall{
		call(mcp.ToolSendMessage, map[string]interface{}{"message": "  "}),
		call(mcp.ToolAskUser, map[string]interface{}{}),
		call(mcp.ToolReportProgress, map[string]interface{}{"status": 7}),
		call("rm_rf", nil),
	} {
		if res := channels.handle(ctx, 100, bad); !res.IsError {
			t.Errorf("%+v must be rejected, got %+v", bad, res)
		}
	}

	ch.err = errors.New("telegram is down")
	if res := channels.handle(ctx, 100, call(mcp.ToolSendMessage, map[string]interface{}{"message": "x"})); !res.IsError || !strings.Contains(res.Text, "telegram is down") {
		t.Errorf("delivery error must reach the agent, got %+v", res)
	}
}

// TestStepHandlesMCPToolCalls — агент посреди шага задачи вызывает инструменты бота:
// сообщение уходит в чат, прогресс — в лог задачи, а вопрос ставит задачу в ожидание
// ответа с кнопками, как ask_question.
func TestStepHandlesMCPToolCalls(t *testing.T) {
	mt := setupTestApp(t)

	const agentPID = 987654
	var during []mcp.BridgeResult
	framework := &mock.AgentFramework{
		Name:       "mcp-agent",
		Response:   "Жду ответа пользователя.",
		ProcessPID: agentPID,
		OnStream: func(pid int) {
			ctx := context.Background()
			during = append(during,
				mcpChannels.handle(ctx, pid, call(mcp.ToolSendMessage, map[string]interface{}{"message": "Начинаю **миграцию**"})),
				mcpChannels.handle(ctx, pid, call(mcp.ToolReportProgress, map[string]interface{}{"status": "Running tests", "percent": float64(40)})),
				mcpChannels.handle(ctx, pid, call(mcp.ToolAskUser, map[string]interface{}{
					"question": "Удалить старую таблицу?",
					"options":  []interface{}{"Да", "Нет"},
				})),
			)
		},
	}
	reg := testAgentRegistry()
	reg.Register(agents.Spec{
		Name:   "mcp-agent",
		NewCLI: func() ports.AgentFramework { return framework },
		NewMCP: func() ports.AgentFramework { return framework },
		NewAPI: func() ports.AgentFramework { return framework },
	})
	setAgentRegistry(reg)
	if _, err := SwitchActiveAgent("mcp-agent"); err != nil {
		t.Fatal(err)
	}

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "mcp-agent", "migrate", testChatID, false)
	task.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusRunning })

	res := executeStepForTask(mt.Messenger, testChatID, task, t.TempDir(), "migrate", "mock-model", "en")

	for i, r := range during {
		if r.IsError {
			t.Errorf("tool call %d failed: %s", i, r.Text)
		}
	}
	if res.Outcome != StepOutcomeWaitingInput {
		t.Fatalf("outcome = %v, want waiting for input", res.Outcome)
	}

	view := task.Snapshot()
	if view.Status != domain.TaskStatusWaitingInput || view.LastQuestion != "Удалить старую таблицу?" {
		t.Errorf("status = %s, question = %q", view.Status, view.LastQuestion)
	}
	if !reflect.DeepEqual(view.QuestionOptions, []string{"Да", "Нет"}) {
		t.Errorf("options = %v", view.QuestionOptions)
	}
	if !containsLog(view.RecentLogs, "Running tests (40%)") {
		t.Errorf("progress is missing in task logs: %v", view.RecentLogs)
	}

	var sawMessage, sawQuestion bool
	for _, sent := range mt.Messenger.Sent {
		if strings.Contains(sent.Text, "<b>миграцию</b>") {
			sawMessage = true
		}
		if strings.Contains(sent.Text, "Удалить старую таблицу?") && sent.Opts != nil && sent.Opts.Keyboard != nil {
			sawQuestion = true
		}
	}
	if !sawMessage {
		t.Error("telegram_send_message did not reach the chat")
	}
	if !sawQuestion {
		t.Error("the question was not shown with answer buttons")
	}

	// После шага процесс агента отвязан: поздний вызов не должен попасть в задачу.
	if late := mcpChannels.handle(context.Background(), agentPID, call(mcp.ToolSendMessage, map[string]interface{}{"message": "late"})); !late.IsError {
		t.Error("a finished step must not accept tool calls")
	}
}

func containsLog(logs []string, want string) bool {
	for _, line := range logs {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}
