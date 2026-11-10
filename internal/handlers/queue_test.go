package handlers

import (
	"strconv"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
)

func TestQueueShowClearAndBtwWhileChatIsRunning(t *testing.T) {
	mt := setupTestApp(t)

	session := domain.GlobalChatManager.GetOrCreate("testproj", "model")
	if !session.BeginTurn(func() {}) {
		t.Fatal("ход чата должен стартовать")
	}
	t.Cleanup(session.EndTurn)
	session.EnqueuePending("later")

	task := domain.GlobalTaskManager.CreateTask("testproj", "model", "do the work", testChatID)
	task.Update(func(ts *domain.TaskSession) {
		ts.Status = domain.TaskStatusRunning
		ts.PendingFollowups = []string{"keep this"}
	})

	queue := mt.commands["queue"]
	if queue == nil || mt.commands["clearqueue"] == nil || mt.commands["btw"] == nil || mt.commands["flush"] == nil || mt.commands["sessions"] == nil {
		t.Fatal("команды очереди не зарегистрированы")
	}
	if err := queue(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/queue: %v", err)
	}
	shown := mt.LastSent()
	if shown == nil || !strings.Contains(shown.Text, "later") || !strings.Contains(shown.Text, "keep this") {
		t.Fatalf("очередь не показала оба списка: %#v", shown)
	}

	session.EnqueuePending("<b>tag</b>")
	if err := queue(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/queue html: %v", err)
	}
	if text := mt.LastSent().Text; !strings.Contains(text, "&lt;b&gt;tag&lt;/b&gt;") {
		t.Fatalf("текст очереди не экранирован: %s", text)
	}

	if err := mt.commands["btw"](adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/btw без текста: %v", err)
	}
	if text := mt.LastSent().Text; !strings.Contains(text, "/btw") {
		t.Fatalf("ожидали подсказку формата, получили %s", text)
	}

	if err := mt.commands["btw"](adminSession(mt, &mock.Session{ArgsVal: []string{"first"}})); err != nil {
		t.Fatalf("/btw: %v", err)
	}
	pending := session.PendingTexts()
	if len(pending) == 0 || pending[0] != "first" {
		t.Fatalf("реплика должна встать в начало чата, очередь = %#v", pending)
	}
	if followups := task.Snapshot().PendingFollowups; len(followups) != 1 || followups[0] != "keep this" {
		t.Fatalf("пока идёт чат, задача не должна меняться: %#v", followups)
	}
	if !session.IsRunning() {
		t.Fatal("/btw не должен останавливать ход")
	}

	if err := mt.commands["clearqueue"](adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/clearqueue: %v", err)
	}
	if session.PendingTexts() != nil {
		t.Fatalf("очередь чата не очищена: %#v", session.PendingTexts())
	}
	if followups := task.Snapshot().PendingFollowups; len(followups) != 0 {
		t.Fatalf("дополнения задачи не очищены: %#v", followups)
	}
	if !session.IsRunning() {
		t.Fatal("/clearqueue не должен останавливать ход")
	}

	if err := mt.commands["flush"](adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/flush пустой: %v", err)
	}
	if text := mt.LastSent().Text; !strings.Contains(text, "Nothing is waiting") {
		t.Fatalf("пустой /flush: %s", text)
	}
}

func TestBtwPrependsCurrentProjectTaskOnly(t *testing.T) {
	mt := setupTestApp(t)

	current := domain.GlobalTaskManager.CreateTask("testproj", "model", "current", testChatID)
	current.Update(func(ts *domain.TaskSession) {
		ts.Status = domain.TaskStatusRunning
		ts.PendingFollowups = []string{"second"}
	})
	other := domain.GlobalTaskManager.CreateTask("otherproj", "model", "other", testChatID)
	other.Update(func(ts *domain.TaskSession) {
		ts.Status = domain.TaskStatusRunning
		ts.PendingFollowups = []string{"leave-me"}
	})
	_, _ = domain.GlobalTaskManager.SetActiveTask(current.Snapshot().ID)

	if err := mt.commands["btw"](adminSession(mt, &mock.Session{ArgsVal: []string{"first"}})); err != nil {
		t.Fatalf("/btw: %v", err)
	}
	got := current.Snapshot().PendingFollowups
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("дополнения текущей задачи = %#v", got)
	}
	if otherGot := other.Snapshot().PendingFollowups; len(otherGot) != 1 || otherGot[0] != "leave-me" {
		t.Fatalf("чужой проект изменился: %#v", otherGot)
	}
	if text := mt.LastSent().Text; !strings.Contains(text, "front of task") {
		t.Fatalf("ожидали подтверждение для задачи, получили %s", text)
	}
}

func TestBtwWaitingInputUsesTheAnswerPath(t *testing.T) {
	mt, _ := setupChatTestApp(t, "done")

	task := domain.GlobalTaskManager.CreateTask("testproj", "model", "ask me", testChatID)
	task.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusWaitingInput })

	if err := mt.commands["btw"](adminSession(mt, &mock.Session{ArgsVal: []string{"the", "second", "way"}})); err != nil {
		t.Fatalf("/btw: %v", err)
	}
	if !textsContain(mt.AllTexts(), "resumed with the answer", "the second way") {
		t.Fatalf("ответ должен уйти в задачу, а не в тихую очередь: %#v", mt.AllTexts())
	}
}

func TestFlushIdleChatAndDoesNotResumePausedTask(t *testing.T) {
	mt, agent := setupChatTestApp(t, "ok")

	session := domain.GlobalChatManager.GetOrCreate("testproj", "model")
	session.EnqueuePending("one")
	session.EnqueuePending("two")

	task := domain.GlobalTaskManager.CreateTask("testproj", "model", "paused work", testChatID)
	task.Update(func(ts *domain.TaskSession) {
		ts.Status = domain.TaskStatusPaused
		ts.PendingFollowups = []string{"still queued"}
	})

	if err := mt.commands["flush"](adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/flush: %v", err)
	}
	if !textsContain(mt.AllTexts(), "Sending 2", "/resume") {
		t.Fatalf("flush должен и отправить чат, и указать на /resume: %#v", mt.AllTexts())
	}

	waitFor(t, "ход по очереди чата", func() bool { return len(agent.Calls()) == 1 })
	prompt := agent.Calls()[0].Prompt
	if !strings.Contains(prompt, "one") || !strings.Contains(prompt, "two") {
		t.Fatalf("промпт не содержит очередь: %s", prompt)
	}
	if task.Snapshot().Status != domain.TaskStatusPaused {
		t.Fatalf("пауза не должна сниматься, статус %s", task.Snapshot().Status)
	}
	if followups := task.Snapshot().PendingFollowups; len(followups) != 1 || followups[0] != "still queued" {
		t.Fatalf("дополнения паузы изменились: %#v", followups)
	}
}

func TestFlushWhileChatIsRunningDoesNotStartAnotherTurn(t *testing.T) {
	mt, agent := setupChatTestApp(t, "ok")

	session := domain.GlobalChatManager.GetOrCreate("testproj", "model")
	if !session.BeginTurn(func() {}) {
		t.Fatal("ход должен быть занят")
	}
	t.Cleanup(session.EndTurn)
	session.EnqueuePending("after this")

	if err := mt.commands["flush"](adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/flush: %v", err)
	}
	if text := mt.LastSent().Text; !strings.Contains(text, "after the current answer") {
		t.Fatalf("ожидали отложить очередь, получили %s", text)
	}
	if len(agent.Calls()) != 0 {
		t.Fatal("/flush не должен запускать второй ход")
	}
	if pending := session.PendingTexts(); len(pending) != 1 || pending[0] != "after this" {
		t.Fatalf("очередь не должна забираться, пока ход идёт: %#v", pending)
	}
}

func TestSessionsListsStoredStateAndNewChat(t *testing.T) {
	mt := setupTestApp(t)

	session := domain.GlobalChatManager.GetOrCreate("testproj", "model")
	session.SetConversationID("agy", "mcp", "agy-session")
	session.SetConversationID("grok", "acp", "grok-session")

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "model", "claude", "rename the handler", testChatID, false)
	domain.GlobalTaskManager.SetTaskConversationID(task.Snapshot().ID, "claude-session")

	if err := mt.commands["sessions"](adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/sessions: %v", err)
	}
	last := mt.LastSent()
	if last == nil {
		t.Fatal("ожидали список сессий")
	}
	for _, part := range []string{"agy-session", "grok-session", "claude-session", "rename the handler", "claude"} {
		if !strings.Contains(last.Text, part) {
			t.Errorf("в списке нет %q: %s", part, last.Text)
		}
	}
	if last.Opts == nil || last.Opts.Keyboard == nil {
		t.Fatal("ожидали кнопки")
	}
	rows := last.Opts.Keyboard.Rows
	if len(rows) < 2 || rows[0][0].Action != "chat_new" {
		t.Fatalf("первая кнопка должна начинать новый чат: %#v", rows)
	}
	wantPayload := strconv.Itoa(task.Snapshot().ID)
	var taskButton ports.Button
	foundTaskButton := false
	for _, row := range rows {
		if len(row) > 0 && row[0].Action == "task_sel" {
			taskButton = row[0]
			foundTaskButton = true
		}
	}
	if !foundTaskButton || taskButton.Payload != wantPayload {
		t.Fatalf("кнопка задачи = %#v, ожидали payload %s", taskButton, wantPayload)
	}

	newChat := mt.callbacks["chat_new"]
	if newChat == nil {
		t.Fatal("кнопка chat_new не зарегистрирована")
	}
	if err := newChat(adminSession(mt, &mock.Session{CB: &ports.CallbackQuery{}})); err != nil {
		t.Fatalf("chat_new: %v", err)
	}
	fresh := domain.GlobalChatManager.Get("testproj")
	if fresh == nil || fresh.ConversationIDFor("agy", "mcp") != "" || fresh.ConversationIDFor("grok", "acp") != "" {
		t.Fatal("новый чат должен забыть старые сессии агентов")
	}
}

func TestQueueRequiresProject(t *testing.T) {
	mt := setupTestApp(t)

	config.ProjectState.Lock()
	prev := config.ProjectState.CurrentProject
	config.ProjectState.CurrentProject = ""
	config.ProjectState.Unlock()
	t.Cleanup(func() {
		config.ProjectState.Lock()
		config.ProjectState.CurrentProject = prev
		config.ProjectState.Unlock()
	})

	if err := mt.commands["queue"](adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/queue: %v", err)
	}
	if text := mt.LastSent().Text; !strings.Contains(text, "/project") {
		t.Fatalf("без проекта ждём просьбу выбрать его: %s", text)
	}
}

func textsContain(texts []string, parts ...string) bool {
	for _, text := range texts {
		ok := true
		for _, part := range parts {
			if !strings.Contains(text, part) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
