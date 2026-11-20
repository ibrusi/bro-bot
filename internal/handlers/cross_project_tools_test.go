package handlers

import (
	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedAgentStep задаёт поведение одного запуска scriptedAgentFramework.
type scriptedAgentStep func(ctx context.Context, args ports.ExecuteArgs) (events string, waitErr error)

// scriptedAgentFramework — лёгкий детерминированный мок-агент, позволяющий
// на каждом шаге инспектировать args (BotInstructions, ChannelToken, BotTools,
// ReadOnly, History, Prompt) и вызывать инструменты бота in-process.
type scriptedAgentFramework struct {
	mu    sync.Mutex
	steps []scriptedAgentStep
	calls []ports.ExecuteArgs
}

type scriptedProcess struct {
	reader  io.Reader
	waitErr error
}

func (p *scriptedProcess) Stdout() io.Reader     { return p.reader }
func (p *scriptedProcess) Stdin() io.WriteCloser { return nopPTYWriteCloser{} }
func (p *scriptedProcess) Wait() error           { return p.waitErr }
func (p *scriptedProcess) Kill() error           { return nil }
func (p *scriptedProcess) Close() error          { return nil }
func (p *scriptedProcess) PID() int              { return 0 }

func (f *scriptedAgentFramework) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	f.mu.Lock()
	idx := len(f.calls)
	f.calls = append(f.calls, args)
	var step scriptedAgentStep
	if idx < len(f.steps) {
		step = f.steps[idx]
	}
	f.mu.Unlock()

	var events string
	var waitErr error
	if step != nil {
		events, waitErr = step(ctx, args)
	}
	if events == "" {
		events = `{"type":"system","session_id":"scripted-conv"}` + "\n" +
			`{"type":"result","session_id":"scripted-conv","status":"SUCCESS"}` + "\n"
	}
	return &scriptedProcess{
		reader:  strings.NewReader(events),
		waitErr: waitErr,
	}, nil
}

func (f *scriptedAgentFramework) Calls() []ports.ExecuteArgs {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ports.ExecuteArgs, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *scriptedAgentFramework) GetModels(_ context.Context) ([]byte, error) {
	return nil, nil
}
func (f *scriptedAgentFramework) GetQuota(_ context.Context, _ string) ([]byte, error) {
	return []byte(`{}`), nil
}
func (f *scriptedAgentFramework) GetQuotaText(_ context.Context, _ string) ([]byte, error) {
	return []byte("quota"), nil
}
func (f *scriptedAgentFramework) GetCredits(_ context.Context) ([]byte, error) {
	return []byte(`{}`), nil
}

func registerScriptedAgent(t *testing.T, name string, fw ports.AgentFramework) {
	t.Helper()
	reg := testAgentRegistry()
	reg.Register(agents.Spec{
		Name:   name,
		NewMCP: func() ports.AgentFramework { return fw },
		NewAPI: func() ports.AgentFramework { return fw },
	})
	setAgentRegistry(reg)
	if _, err := SwitchActiveAgent(name); err != nil {
		t.Fatalf("SwitchActiveAgent(%s): %v", name, err)
	}
}

func setupTwoProjects(t *testing.T) (projectsRoot, dirA, dirB string) {
	t.Helper()
	projectsRoot = t.TempDir()
	dirA = filepath.Join(projectsRoot, "projA")
	dirB = filepath.Join(projectsRoot, "projB")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	config.ProjectsRoot = projectsRoot
	config.ProjectState.Lock()
	config.ProjectState.CurrentProject = "projA"
	config.ProjectState.Unlock()
	return projectsRoot, dirA, dirB
}

func TestCrossProjectChannelTokenAndBotTools(t *testing.T) {
	mt := setupTestApp(t)
	_, dirA, dirB := setupTwoProjects(t)

	if err := os.MkdirAll(filepath.Join(dirB, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(dirB, "docs", "report.md")
	if err := os.WriteFile(reportPath, []byte("# Analysis\nDetails"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(filepath.Dir(dirA), "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outsideFile, filepath.Join(dirB, "escape_link.txt"))

	taskB := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("projB", "mock-model", "claude", "Audit projB", testChatID, false)
	taskBID := taskB.Snapshot().ID

	ch := newTaskChannel(mt.Messenger, testChatID, taskB, dirB, config.ProjectsRoot, "en")
	token := generateChannelToken()
	unbind := mcpChannels.bindWithToken(100, token, ch)
	defer unbind()

	ctx := context.Background()

	// Вызов с оторванного PID 99999 (parent=1), но с валидным ChannelToken: должен попасть в projB.
	res := mcpChannels.handle(ctx, 99999, mcp.BridgeCall{
		Tool:         mcp.ToolSendFile,
		ChannelToken: token,
		Arguments: map[string]interface{}{
			"path":    "docs/report.md",
			"caption": "Audit report",
		},
	})
	if res.IsError {
		t.Fatalf("send_file failed: %s", res.Text)
	}
	if len(mt.Messenger.Documents) != 1 || mt.Messenger.Documents[0].Doc.FileName != "report.md" {
		t.Fatalf("expected report.md document upload, got %+v", mt.Messenger.Documents)
	}

	// Защита от path traversal и выхода по symlink из workDir projB.
	for _, badPath := range []string{"../secret.txt", "escape_link.txt"} {
		badRes := mcpChannels.handle(ctx, 99999, mcp.BridgeCall{
			Tool:         mcp.ToolSendFile,
			ChannelToken: token,
			Arguments:    map[string]interface{}{"path": badPath},
		})
		if !badRes.IsError {
			t.Errorf("expected error for escaping path %q, got success: %s", badPath, badRes.Text)
		}
	}

	// get_task_context
	ctxRes := mcpChannels.handle(ctx, 99999, mcp.BridgeCall{
		Tool:         mcp.ToolGetTaskContext,
		ChannelToken: token,
		Arguments:    map[string]interface{}{},
	})
	if ctxRes.IsError || !strings.Contains(ctxRes.Text, `"project": "projB"`) || !strings.Contains(ctxRes.Text, "Audit projB") {
		t.Errorf("unexpected get_task_context response (err=%v): %s", ctxRes.IsError, ctxRes.Text)
	}

	// list_tasks
	tasksRes := mcpChannels.handle(ctx, 99999, mcp.BridgeCall{
		Tool:         mcp.ToolListTasks,
		ChannelToken: token,
		Arguments:    map[string]interface{}{"project": "projB", "limit": float64(5)},
	})
	if tasksRes.IsError || !strings.Contains(tasksRes.Text, fmt.Sprintf(`"id": %d`, taskBID)) {
		t.Errorf("unexpected list_tasks response (err=%v): %s", tasksRes.IsError, tasksRes.Text)
	}

	// list_projects
	projRes := mcpChannels.handle(ctx, 99999, mcp.BridgeCall{
		Tool:         mcp.ToolListProjects,
		ChannelToken: token,
	})
	var projJSON map[string]interface{}
	if projRes.IsError || json.Unmarshal([]byte(projRes.Text), &projJSON) != nil {
		t.Fatalf("list_projects invalid response (err=%v): %s", projRes.IsError, projRes.Text)
	}
	if projJSON["active_project"] != "projA" {
		t.Errorf("active_project = %v, want projA", projJSON["active_project"])
	}

	// telegram_send_message: silent=true и длинный текст > 4000 рун
	longMsg := strings.Repeat("Секция анализа кода. ", 250) // ~5250 runes
	msgRes := mcpChannels.handle(ctx, 99999, mcp.BridgeCall{
		Tool:         mcp.ToolSendMessage,
		ChannelToken: token,
		Arguments: map[string]interface{}{
			"message": longMsg,
			"silent":  true,
		},
	})
	if msgRes.IsError {
		t.Fatalf("long silent telegram_send_message failed: %s", msgRes.Text)
	}
	if len(mt.Messenger.Sent) < 2 {
		t.Errorf("expected split long message into >=2 messages, got %d", len(mt.Messenger.Sent))
	}
	for i, s := range mt.Messenger.Sent {
		if s.Opts == nil || !s.Opts.Silent {
			t.Errorf("sent[%d] expected Silent=true, got %+v", i, s.Opts)
		}
	}
}

func TestMultiQuestionBatchAccumulationAndWizard(t *testing.T) {
	mt := setupTestApp(t)
	projectsRoot, _, dirB := setupTwoProjects(t)

	fw := &scriptedAgentFramework{
		steps: []scriptedAgentStep{
			func(ctx context.Context, args ports.ExecuteArgs) (string, error) {
				// Первый вызов — пакет из 2 вопросов (первый с мультивыбором)
				r1 := args.BotTools.CallBotTool(ctx, mcp.ToolAskUser, map[string]interface{}{
					"questions": []interface{}{
						map[string]interface{}{
							"question":        "Какие компоненты обновить?",
							"options":         []interface{}{"API", "Worker", "UI"},
							"is_multi_select": true,
						},
						map[string]interface{}{
							"question": "Какая БД?",
							"options":  []interface{}{"SQLite", "Postgres"},
						},
					},
				})
				if r1.IsError || !r1.EndTurn {
					return "", fmt.Errorf("unexpected ask 1 result: %+v", r1)
				}
				// Второй вызов ask_user в том же шаге — накапливается 3-й вопрос
				r2 := args.BotTools.CallBotTool(ctx, mcp.ToolAskUser, map[string]interface{}{
					"question": "Ветка?",
					"options":  []interface{}{"feat/a", "feat/b"},
				})
				if r2.IsError || !r2.EndTurn {
					return "", fmt.Errorf("unexpected ask 2 result: %+v", r2)
				}
				return `{"type":"system","session_id":"conv-mq"}` + "\n" +
					`{"type":"result","session_id":"conv-mq","status":"SUCCESS"}` + "\n", nil
			},
			func(_ context.Context, _ ports.ExecuteArgs) (string, error) {
				return `{"type":"system","session_id":"conv-mq"}` + "\n" +
					`{"type":"result","session_id":"conv-mq","status":"SUCCESS","response":"Done\nPR_URL: https://github.com/org/repo/pull/10"}` + "\n", nil
			},
		},
	}
	registerScriptedAgent(t, "scripted-mq", fw)

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("projB", "mock-model", "scripted-mq", "multi-q task", testChatID, false)
	task.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusRunning })
	taskID := task.Snapshot().ID

	done := make(chan struct{})
	go func() {
		defer close(done)
		runAgentTaskPipeline(mt.Messenger, testChatID, task, dirB, projectsRoot)
	}()

	waitFor(t, "task waits with 3 pending questions", func() bool {
		v := task.Snapshot()
		return v.Status == domain.TaskStatusWaitingInput && v.HasActivePipeline && len(v.PendingQuestions) == 3
	})

	snap := task.Snapshot()
	if !snap.PendingQuestions[0].MultiSelect || snap.CurrentQuestionIndex != 0 {
		t.Fatalf("unexpected initial PendingQuestions state: %+v", snap.PendingQuestions)
	}

	// Выбираем варианты 0 и 2 на Q0, нажимаем Submit
	_ = onQuestionToggle(adminSession(mt, &mock.Session{CB: &ports.CallbackQuery{ID: "c1", Action: "q_toggle", Payload: fmt.Sprintf("%d:0:0", taskID), Message: &ports.MessageRef{Chat: testChatID, ID: "qmsg"}, MessageText: "Q1"}}))
	_ = onQuestionToggle(adminSession(mt, &mock.Session{CB: &ports.CallbackQuery{ID: "c2", Action: "q_toggle", Payload: fmt.Sprintf("%d:0:2", taskID), Message: &ports.MessageRef{Chat: testChatID, ID: "qmsg"}, MessageText: "Q1"}}))
	_ = onQuestionSubmit(adminSession(mt, &mock.Session{CB: &ports.CallbackQuery{ID: "c3", Action: "q_submit", Payload: fmt.Sprintf("%d:0", taskID), Message: &ports.MessageRef{Chat: testChatID, ID: "qmsg"}, MessageText: "Q1"}}))

	if v := task.Snapshot(); v.CurrentQuestionIndex != 1 {
		t.Fatalf("expected CurrentQuestionIndex=1 after submitting Q0, got %d", v.CurrentQuestionIndex)
	}

	// Проверяем кнопку Назад (q_back)
	_ = onQuestionBack(adminSession(mt, &mock.Session{CB: &ports.CallbackQuery{ID: "c4", Action: "q_back", Payload: fmt.Sprintf("%d", taskID), Message: &ports.MessageRef{Chat: testChatID, ID: "qmsg"}, MessageText: "Q2"}}))
	if v := task.Snapshot(); v.CurrentQuestionIndex != 0 {
		t.Fatalf("expected CurrentQuestionIndex=0 after q_back, got %d", v.CurrentQuestionIndex)
	}

	// Повторно отправляем Q0 -> Q1 -> Q2
	_ = onQuestionSubmit(adminSession(mt, &mock.Session{CB: &ports.CallbackQuery{ID: "c5", Action: "q_submit", Payload: fmt.Sprintf("%d:0", taskID), Message: &ports.MessageRef{Chat: testChatID, ID: "qmsg"}, MessageText: "Q1"}}))
	_ = onQuestionChoice(adminSession(mt, &mock.Session{CB: &ports.CallbackQuery{ID: "c6", Action: "q_choice", Payload: fmt.Sprintf("%d:1:1", taskID), Message: &ports.MessageRef{Chat: testChatID, ID: "qmsg"}, MessageText: "Q2"}}))

	if v := task.Snapshot(); v.CurrentQuestionIndex != 2 {
		t.Fatalf("expected CurrentQuestionIndex=2, got %d", v.CurrentQuestionIndex)
	}

	// Завершаем последний вопрос
	_ = onQuestionChoice(adminSession(mt, &mock.Session{CB: &ports.CallbackQuery{ID: "c7", Action: "q_choice", Payload: fmt.Sprintf("%d:2:0", taskID), Message: &ports.MessageRef{Chat: testChatID, ID: "qmsg"}, MessageText: "Q3"}}))

	<-done

	finalSnap := task.Snapshot()
	if finalSnap.Status != domain.TaskStatusCompleted {
		t.Fatalf("expected TaskStatusCompleted, got %s", finalSnap.Status)
	}
	if len(finalSnap.PendingQuestions) != 0 {
		t.Errorf("expected PendingQuestions cleared after completion, got %+v", finalSnap.PendingQuestions)
	}
	calls := fw.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if !strings.Contains(calls[1].Prompt, "API, UI") || !strings.Contains(calls[1].Prompt, "Postgres") || !strings.Contains(calls[1].Prompt, "feat/a") {
		t.Errorf("expected combined answers in step 2 prompt, got:\n%s", calls[1].Prompt)
	}
}

func TestMultiRoundQAInPlanRevisionAndAfterPR(t *testing.T) {
	t.Run("Plan multi-round QA resets output and preserves ReadOnly", func(t *testing.T) {
		mt := setupTestApp(t)
		projectsRoot, _, dirB := setupTwoProjects(t)

		fw := &scriptedAgentFramework{
			steps: []scriptedAgentStep{
				func(ctx context.Context, args ports.ExecuteArgs) (string, error) {
					args.BotTools.CallBotTool(ctx, mcp.ToolAskUser, map[string]interface{}{"question": "Auth provider?"})
					return `{"type":"system","session_id":"plan-1"}` + "\n" +
						`{"type":"assistant","session_id":"plan-1","message":{"role":"assistant","content":[{"type":"text","text":"Thinking before Q1"}]}}` + "\n" +
						`{"type":"result","session_id":"plan-1","status":"SUCCESS"}` + "\n", nil
				},
				func(ctx context.Context, args ports.ExecuteArgs) (string, error) {
					args.BotTools.CallBotTool(ctx, mcp.ToolAskUser, map[string]interface{}{"question": "Cache backend?"})
					return `{"type":"system","session_id":"plan-1"}` + "\n" +
						`{"type":"assistant","session_id":"plan-1","message":{"role":"assistant","content":[{"type":"text","text":"Thinking before Q2"}]}}` + "\n" +
						`{"type":"result","session_id":"plan-1","status":"SUCCESS"}` + "\n", nil
				},
				func(_ context.Context, _ ports.ExecuteArgs) (string, error) {
					return `{"type":"system","session_id":"plan-1"}` + "\n" +
						`{"type":"assistant","session_id":"plan-1","message":{"role":"assistant","content":[{"type":"text","text":"# Final Plan\n1. OAuth + Redis"}]}}` + "\n" +
						`{"type":"result","session_id":"plan-1","status":"SUCCESS"}` + "\n", nil
				},
			},
		}
		registerScriptedAgent(t, "scripted-plan", fw)

		task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("projB", "mock-model", "scripted-plan", "Add auth", testChatID, true)
		task.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusPlanning })

		done := make(chan struct{})
		go func() {
			defer close(done)
			runAgentTaskPipeline(mt.Messenger, testChatID, task, dirB, projectsRoot)
		}()

		waitFor(t, "round 1 question", func() bool {
			v := task.Snapshot()
			return v.Status == domain.TaskStatusWaitingInput && v.LastQuestion == "Auth provider?"
		})
		_ = handleAddFollowupToTask(adminSession(mt, &mock.Session{}), task.Snapshot().ID, "OAuth2")

		waitFor(t, "round 2 question", func() bool {
			v := task.Snapshot()
			return v.Status == domain.TaskStatusWaitingInput && v.LastQuestion == "Cache backend?"
		})
		_ = handleAddFollowupToTask(adminSession(mt, &mock.Session{}), task.Snapshot().ID, "Redis")

		<-done

		snap := task.Snapshot()
		if snap.Status != domain.TaskStatusWaitingApproval {
			t.Fatalf("expected TaskStatusWaitingApproval, got %s", snap.Status)
		}
		if snap.Plan != "# Final Plan\n1. OAuth + Redis" {
			t.Errorf("expected clean final plan without previous Q&A output bleed, got %q", snap.Plan)
		}
		for i, c := range fw.Calls() {
			if !c.ReadOnly {
				t.Errorf("planning call %d expected ReadOnly=true", i)
			}
			if c.BotInstructions == "" {
				t.Errorf("planning call %d missing BotInstructions", i)
			}
		}
	})

	t.Run("Execution step asks question even when prior step had PR_URL", func(t *testing.T) {
		mt := setupTestApp(t)
		_, _, dirB := setupTwoProjects(t)

		fw := &scriptedAgentFramework{
			steps: []scriptedAgentStep{
				func(ctx context.Context, args ports.ExecuteArgs) (string, error) {
					args.BotTools.CallBotTool(ctx, mcp.ToolAskUser, map[string]interface{}{"question": "Force push PR branch?"})
					return `{"type":"system","session_id":"exec-pr"}` + "\n" +
						`{"type":"result","session_id":"exec-pr","status":"SUCCESS","response":"Working on existing PR"}` + "\n", nil
				},
			},
		}
		registerScriptedAgent(t, "scripted-pr", fw)

		task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("projB", "mock-model", "scripted-pr", "initial", testChatID, false)
		task.Update(func(ts *domain.TaskSession) {
			ts.Status = domain.TaskStatusRunning
			ts.LastPRURL = "https://github.com/org/repo/pull/99"
		})

		res := executeStepForTask(mt.Messenger, testChatID, task, dirB, "add test", "mock-model", "en")
		if res.Outcome != StepOutcomeWaitingInput {
			t.Fatalf("expected StepOutcomeWaitingInput even with historical LastPRURL, got %v", res.Outcome)
		}
	})

	t.Run("Plan revision question resume sends raw answer without double plan_revise_prompt", func(t *testing.T) {
		mt := setupTestApp(t)
		projectsRoot, _, dirB := setupTwoProjects(t)

		fw := &scriptedAgentFramework{
			steps: []scriptedAgentStep{
				func(ctx context.Context, args ports.ExecuteArgs) (string, error) {
					args.BotTools.CallBotTool(ctx, mcp.ToolAskUser, map[string]interface{}{"question": "Which exporter?"})
					return `{"type":"system","session_id":"rev-conv"}` + "\n" +
						`{"type":"result","session_id":"rev-conv","status":"SUCCESS"}` + "\n", nil
				},
				func(_ context.Context, _ ports.ExecuteArgs) (string, error) {
					return `{"type":"system","session_id":"rev-conv"}` + "\n" +
						`{"type":"assistant","session_id":"rev-conv","message":{"role":"assistant","content":[{"type":"text","text":"# Updated Plan"}]}}` + "\n" +
						`{"type":"result","session_id":"rev-conv","status":"SUCCESS"}` + "\n", nil
				},
			},
		}
		registerScriptedAgent(t, "scripted-rev", fw)

		task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("projB", "mock-model", "scripted-rev", "Metrics task", testChatID, true)
		task.Update(func(ts *domain.TaskSession) {
			ts.Status = domain.TaskStatusWaitingApproval
			ts.Plan = "Initial plan"
			ts.ConversationID = "rev-conv"
			ts.CurrentPrompt = "Add Prometheus metrics"
			ts.PendingPlanRevision = true
		})

		done := make(chan struct{})
		go func() {
			defer close(done)
			runAgentTaskPipeline(mt.Messenger, testChatID, task, dirB, projectsRoot)
		}()

		waitFor(t, "plan revision waiting for answer", func() bool {
			return task.Snapshot().Status == domain.TaskStatusWaitingInput
		})

		// Имитируем паузу и возобновление (перезапуск runAgentTaskPipeline после рестарта/таймаута)
		task.PauseTask()
		<-done

		task.Update(func(ts *domain.TaskSession) {
			ts.Status = domain.TaskStatusPlanning
			ts.CurrentPrompt = "Prometheus"
			ts.ClearPendingQuestionLocked()
		})
		done2 := make(chan struct{})
		go func() {
			defer close(done2)
			runAgentTaskPipeline(mt.Messenger, testChatID, task, dirB, projectsRoot)
		}()
		<-done2

		calls := fw.Calls()
		if len(calls) != 2 {
			t.Fatalf("expected 2 calls, got %d", len(calls))
		}
		if !strings.Contains(calls[0].Prompt, "Add Prometheus metrics") || !strings.Contains(calls[0].Prompt, "Initial plan") {
			t.Errorf("expected call 0 to have plan_revise_prompt, got: %s", calls[0].Prompt)
		}
		if calls[1].Prompt != "Prometheus" {
			t.Errorf("expected resumed revision reply to be raw %q, got %q", "Prometheus", calls[1].Prompt)
		}
	})

	t.Run("API mode preserves History and SystemPrompt across QA steps", func(t *testing.T) {
		mt := setupTestApp(t)
		projectsRoot, _, dirB := setupTwoProjects(t)
		_ = os.WriteFile(filepath.Join(dirB, "AGENTS.md"), []byte("PROJECT_RULES_B"), 0o644)

		prevMode := config.ProjectState.GetExecutionMode()
		config.ProjectState.SetExecutionMode("api")
		t.Cleanup(func() { config.ProjectState.SetExecutionMode(prevMode) })

		fw := &scriptedAgentFramework{
			steps: []scriptedAgentStep{
				func(ctx context.Context, args ports.ExecuteArgs) (string, error) {
					args.BotTools.CallBotTool(ctx, mcp.ToolAskUser, map[string]interface{}{"question": "Pick target?"})
					return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Pick target?"}]}}` + "\n" +
						`{"type":"result","status":"SUCCESS"}` + "\n", nil
				},
				func(_ context.Context, _ ports.ExecuteArgs) (string, error) {
					return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Finished\nPR_URL: https://github.com/org/repo/pull/77"}]}}` + "\n" +
						`{"type":"result","status":"SUCCESS"}` + "\n", nil
				},
			},
		}
		registerScriptedAgent(t, "scripted-api", fw)

		task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("projB", "mock-model", "scripted-api", "API task", testChatID, false)
		task.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusRunning })

		done := make(chan struct{})
		go func() {
			defer close(done)
			runAgentTaskPipeline(mt.Messenger, testChatID, task, dirB, projectsRoot)
		}()

		waitFor(t, "api step waits for answer", func() bool {
			return task.Snapshot().Status == domain.TaskStatusWaitingInput
		})
		_ = handleAddFollowupToTask(adminSession(mt, &mock.Session{}), task.Snapshot().ID, "Target X")
		<-done

		calls := fw.Calls()
		if len(calls) != 2 {
			t.Fatalf("expected 2 api calls, got %d", len(calls))
		}
		if calls[1].SystemPrompt != "PROJECT_RULES_B" || calls[1].BotInstructions == "" {
			t.Errorf("expected SystemPrompt & BotInstructions on API step 2, got sys=%q bot=%q", calls[1].SystemPrompt, calls[1].BotInstructions)
		}
		if len(calls[1].History) < 2 || calls[1].History[0].Content != "API task" || calls[1].History[1].Content != "Pick target?" {
			t.Errorf("expected step 1 turn in step 2 API History, got %+v", calls[1].History)
		}
	})
}

func TestCrossProjectChatCardImplicitTextAndFileRouting(t *testing.T) {
	t.Run("onChatPlan and onChatTask preserve suggestion project and agent", func(t *testing.T) {
		mt := setupTestApp(t)
		_, _, _ = setupTwoProjects(t)
		registerScriptedAgent(t, "mock-a", &scriptedAgentFramework{})

		planRef := ports.MessageRef{Chat: testChatID, ID: "sug-plan"}
		rememberSuggestion(planRef, workSuggestion{Project: "projB", Agent: "mock-a", Text: "Refactor auth", CreatedAt: time.Now()})
		if err := onChatPlan(adminSession(mt, &mock.Session{
			CB: &ports.CallbackQuery{ID: "cb1", Action: "chat_plan", Message: &planRef},
		})); err != nil {
			t.Fatalf("onChatPlan: %v", err)
		}

		tasks := domain.GlobalTaskManager.ListTasksByProject("projB", 10)
		if len(tasks) != 1 || !tasks[0].Snapshot().RequiresPlan || tasks[0].Snapshot().Project != "projB" {
			t.Fatalf("expected planning task in projB, got %+v", tasks)
		}

		taskRef := ports.MessageRef{Chat: testChatID, ID: "sug-task"}
		rememberSuggestion(taskRef, workSuggestion{Project: "projB", Agent: "mock-a", Text: "Fix typo", CreatedAt: time.Now()})
		if err := onChatTask(adminSession(mt, &mock.Session{
			CB: &ports.CallbackQuery{ID: "cb2", Action: "chat_task", Message: &taskRef},
		})); err != nil {
			t.Fatalf("onChatTask: %v", err)
		}
		tasksAfter := domain.GlobalTaskManager.ListTasksByProject("projB", 10)
		if len(tasksAfter) != 2 {
			t.Fatalf("expected 2 tasks in projB, got %d", len(tasksAfter))
		}
	})

	t.Run("Implicit plain text answers waiting task in projB while projA is running and focused", func(t *testing.T) {
		mt := setupTestApp(t)
		_, _, _ = setupTwoProjects(t)

		taskA := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("projA", "m", "agy", "Run A", testChatID, false)
		taskA.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusRunning })
		_, _ = domain.GlobalTaskManager.SetActiveTask(taskA.Snapshot().ID)

		taskB := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("projB", "m", "agy", "Wait B", testChatID, false)
		taskB.BeginPipeline()
		taskB.Update(func(ts *domain.TaskSession) {
			ts.Status = domain.TaskStatusWaitingInput
			ts.LastQuestion = "Confirm migration?"
			ts.QuestionAskedAt = time.Now()
		})

		sess := adminSession(mt, &mock.Session{TextVal: "Yes migrate projB"})
		if err := handleText(sess); err != nil {
			t.Fatalf("handleText: %v", err)
		}

		if len(taskA.Snapshot().PendingFollowups) != 0 {
			t.Errorf("running projA task should NOT steal reply meant for waiting projB task")
		}
		select {
		case ans := <-taskB.AnswerChannel():
			if ans != "Yes migrate projB" {
				t.Errorf("taskB got answer %q, want %q", ans, "Yes migrate projB")
			}
		case <-time.After(time.Second):
			t.Fatal("taskB did not receive answer on AnswerChannel")
		}
	})

	t.Run("File upload with caption /plan projB saves to projB uploads and starts plan task", func(t *testing.T) {
		mt := setupTestApp(t)
		_, dirA, dirB := setupTwoProjects(t)
		registerScriptedAgent(t, "scripted-file", &scriptedAgentFramework{})

		content := []byte("spec line 1\nspec line 2\n")
		sess := adminSession(mt, &mock.Session{
			FileVal: &ports.FileMessage{
				FileID:   "f-spec",
				FileName: "spec.txt",
				FileSize: int64(len(content)),
				Caption:  "/plan projB review this spec",
			},
			FileReader: io.NopCloser(bytes.NewReader(content)),
		})
		if err := handleDocument(sess); err != nil {
			t.Fatalf("handleDocument: %v", err)
		}

		if _, err := os.Stat(filepath.Join(dirB, "uploads", "spec.txt")); err != nil {
			t.Errorf("expected spec.txt in projB/uploads, got err: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dirA, "uploads", "spec.txt")); !os.IsNotExist(err) {
			t.Errorf("spec.txt must NOT be saved in projA/uploads")
		}
		tasksB := domain.GlobalTaskManager.ListTasksByProject("projB", 5)
		if len(tasksB) != 1 || !tasksB[0].Snapshot().RequiresPlan {
			t.Fatalf("expected planning task created in projB, got %+v", tasksB)
		}
		if !strings.Contains(tasksB[0].Snapshot().InitialPrompt, "review this spec") || !strings.Contains(tasksB[0].Snapshot().InitialPrompt, "uploads/spec.txt") {
			t.Errorf("unexpected task prompt: %q", tasksB[0].Snapshot().InitialPrompt)
		}
	})
}

func TestMultilineCommandsPreservedEndToEnd(t *testing.T) {
	mt := setupTestApp(t)
	_, _, _ = setupTwoProjects(t)
	fw := &scriptedAgentFramework{}
	registerScriptedAgent(t, "scripted-ml", fw)

	newMsg := "/new projB First line\n  Indented line\nLast line"
	if err := handleNew(adminSession(mt, &mock.Session{
		TextVal: newMsg,
		ArgsVal: strings.Fields("projB First line Indented line Last line"),
	})); err != nil {
		t.Fatalf("handleNew: %v", err)
	}

	planMsg := "/plan projB Step one\nStep two\nStep three"
	if err := handlePlan(adminSession(mt, &mock.Session{
		TextVal: planMsg,
		ArgsVal: strings.Fields("projB Step one Step two Step three"),
	})); err != nil {
		t.Fatalf("handlePlan: %v", err)
	}

	tasksB := domain.GlobalTaskManager.ListTasksByProject("projB", 10)
	if len(tasksB) < 2 {
		t.Fatalf("expected 2 tasks in projB, got %d", len(tasksB))
	}
	var sawNewPrompt, sawPlanPrompt bool
	var addTargetID int
	for _, tk := range tasksB {
		v := tk.Snapshot()
		if v.InitialPrompt == "First line\n  Indented line\nLast line" {
			sawNewPrompt = true
			addTargetID = v.ID
		}
		if v.InitialPrompt == "Step one\nStep two\nStep three" {
			sawPlanPrompt = true
		}
	}
	if !sawNewPrompt || !sawPlanPrompt {
		t.Errorf("multiline prompts lost newlines: new=%v plan=%v", sawNewPrompt, sawPlanPrompt)
	}

	// /add <id> multiline
	addMsg := fmt.Sprintf("/add %d Fix A\nFix B", addTargetID)
	if err := handleAdd(adminSession(mt, &mock.Session{
		TextVal: addMsg,
		ArgsVal: strings.Fields(fmt.Sprintf("%d Fix A Fix B", addTargetID)),
	})); err != nil {
		t.Fatalf("handleAdd: %v", err)
	}
	if fups := domain.GlobalTaskManager.GetTask(addTargetID).Snapshot().PendingFollowups; len(fups) == 0 || fups[0] != "Fix A\nFix B" {
		t.Errorf("expected multiline followup preserved, got %v", fups)
	}

	// /resume <id> multiline
	pauseTask := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("projB", "m", "scripted-ml", "p", testChatID, false)
	pauseTask.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusPaused })
	resumeID := pauseTask.Snapshot().ID
	resumeMsg := fmt.Sprintf("/resume %d Answer line 1\nAnswer line 2", resumeID)
	if err := handleResume(adminSession(mt, &mock.Session{
		TextVal: resumeMsg,
		ArgsVal: strings.Fields(fmt.Sprintf("%d Answer line 1 Answer line 2", resumeID)),
	})); err != nil {
		t.Fatalf("handleResume: %v", err)
	}
	if cur := domain.GlobalTaskManager.GetTask(resumeID).Snapshot().CurrentPrompt; cur != "Answer line 1\nAnswer line 2" {
		t.Errorf("expected multiline resume answer preserved, got %q", cur)
	}

	// /chat multiline
	if err := handleChat(adminSession(mt, &mock.Session{
		TextVal: "/chat Question line 1\nQuestion line 2",
		ArgsVal: strings.Fields("Question line 1 Question line 2"),
	})); err != nil {
		t.Fatalf("handleChat: %v", err)
	}
	waitFor(t, "chat turn recorded multiline question", func() bool {
		for _, c := range fw.Calls() {
			if strings.Contains(c.Prompt, "Question line 1\nQuestion line 2") {
				return true
			}
		}
		return false
	})
}
