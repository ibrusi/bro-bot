package handlers

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
)

// setupChatTestApp поднимает бота с мок-агентом вместо реального CLI/API.
func setupChatTestApp(t *testing.T, response string) (*mockTransport, *mock.AgentFramework) {
	t.Helper()

	// Start переинициализирует менеджеры задач и разговоров на свежей базе,
	// поэтому каждый тест диалога начинает с чистого состояния.
	mt := setupTestApp(t)

	agent := &mock.AgentFramework{Name: "agy", Response: response, ConversationID: "conv-agy-cli"}
	prevFramework, prevName := ActiveAgent()
	SetActiveAgent(agent, "agy")
	config.ProjectState.SetExecutionMode("cli")
	config.ProjectState.SetInteractionMode(domain.InteractionModeChat)

	t.Cleanup(func() {
		SetActiveAgent(prevFramework, prevName)
		config.ProjectState.SetExecutionMode("cli")
		config.ProjectState.SetInteractionMode(domain.InteractionModeChat)
	})

	return mt, agent
}

// waitFor ждёт выполнения условия, чтобы не зависеть от таймингов фоновой горутины.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

func sendText(t *testing.T, mt *mockTransport, text string) {
	t.Helper()
	if mt.textH == nil {
		t.Fatal("обработчик текста не зарегистрирован")
	}
	sess := adminSession(mt, &mock.Session{TextVal: text})
	if err := mt.textH(sess); err != nil {
		t.Fatalf("обработчик текста вернул ошибку: %v", err)
	}
}

func TestInteractionModeDefaultIsChat(t *testing.T) {
	setupTestApp(t)

	if got := config.ProjectState.GetInteractionMode(); got != domain.InteractionModeChat {
		t.Errorf("режим взаимодействия по умолчанию = %q, ожидали %q", got, domain.InteractionModeChat)
	}
}

func TestChatModeCommandTogglePersists(t *testing.T) {
	mt, _ := setupChatTestApp(t, "ответ")

	handler, ok := mt.commands["chatmode"]
	if !ok {
		t.Fatal("команда /chatmode не зарегистрирована")
	}

	if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{"off"}})); err != nil {
		t.Fatalf("/chatmode off: %v", err)
	}
	if got := config.ProjectState.GetInteractionMode(); got != domain.InteractionModeTask {
		t.Errorf("после /chatmode off режим = %q", got)
	}
	if st := domain.GlobalTaskManager.Storage(); st != nil {
		saved, _ := st.GetSetting(context.Background(), "interaction_mode")
		if saved != domain.InteractionModeTask {
			t.Errorf("в базе сохранено %q, ожидали %q", saved, domain.InteractionModeTask)
		}
	}

	if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{"on"}})); err != nil {
		t.Fatalf("/chatmode on: %v", err)
	}
	if got := config.ProjectState.GetInteractionMode(); got != domain.InteractionModeChat {
		t.Errorf("после /chatmode on режим = %q", got)
	}
}

func TestQuestionAnsweredInChatWithoutCreatingTask(t *testing.T) {
	mt, agent := setupChatTestApp(t, "Роутер разбирает сообщения в OnText.")

	tasksBefore := len(domain.GlobalTaskManager.ListTasks())
	sendText(t, mt, "как работает роутер OnText?")

	waitFor(t, "ответ агента в чате", func() bool {
		for _, text := range mt.AllTexts() {
			if strings.Contains(text, "Роутер разбирает сообщения") {
				return true
			}
		}
		return false
	})

	if got := len(domain.GlobalTaskManager.ListTasks()); got != tasksBefore {
		t.Errorf("вопрос не должен создавать задачу: было %d задач, стало %d", tasksBefore, got)
	}
	if len(agent.Calls()) != 1 {
		t.Fatalf("ожидали один запуск агента, получили %d", len(agent.Calls()))
	}

	for _, text := range mt.AllTexts() {
		if strings.Contains(text, "Задача #") && strings.Contains(text, "завершена") {
			t.Errorf("в диалоге не должно быть карточки завершения задачи: %s", text)
		}
	}
}

func TestChatKeepsContextBetweenMessages(t *testing.T) {
	mt, agent := setupChatTestApp(t, "Я Gemini.")

	sendText(t, mt, "привет, как дела?")
	waitFor(t, "первый ответ", func() bool { return len(agent.Calls()) == 1 })
	waitFor(t, "завершение первого хода", func() bool {
		session := domain.GlobalChatManager.Get("testproj")
		return session != nil && !session.IsRunning()
	})

	sendText(t, mt, "а какая ты модель?")
	waitFor(t, "второй ответ", func() bool { return len(agent.Calls()) == 2 })

	calls := agent.Calls()
	if calls[0].ConversationID != "" {
		t.Errorf("первый ход должен идти без сессии агента, получили %q", calls[0].ConversationID)
	}
	// Второй ход продолжает ту же сессию агента — пользователю не нужен /resume.
	if calls[1].ConversationID != "conv-agy-cli" {
		t.Errorf("второй ход должен продолжать сессию conv-agy-cli, получили %q", calls[1].ConversationID)
	}

	session := domain.GlobalChatManager.Get("testproj")
	if session == nil || session.TurnsCount() != 4 {
		t.Errorf("ожидали 4 реплики в истории, получили %v", session)
	}
}

func TestWorkRequestSendsSuggestionCard(t *testing.T) {
	mt, agent := setupChatTestApp(t, "не должно вызываться")

	tasksBefore := len(domain.GlobalTaskManager.ListTasks())
	sendText(t, mt, "добавь таймаут для диалога")

	last := mt.LastSent()
	if last == nil {
		t.Fatal("ожидали карточку выбора")
	}
	if !strings.Contains(last.Text, "request to change code") {
		t.Errorf("неожиданный текст карточки: %s", last.Text)
	}
	if last.Opts == nil || last.Opts.Keyboard == nil {
		t.Fatal("ожидали кнопки в карточке")
	}

	var actions []string
	for _, row := range last.Opts.Keyboard.Rows {
		for _, btn := range row {
			actions = append(actions, btn.Action)
		}
	}
	want := []string{"chat_answer", "chat_plan", "chat_task"}
	if len(actions) != len(want) {
		t.Fatalf("ожидали %d кнопки, получили %v", len(want), actions)
	}
	for i, action := range want {
		if actions[i] != action {
			t.Errorf("кнопка %d = %q, ожидали %q", i, actions[i], action)
		}
	}

	if len(agent.Calls()) != 0 {
		t.Error("до нажатия кнопки агент запускаться не должен")
	}
	if got := len(domain.GlobalTaskManager.ListTasks()); got != tasksBefore {
		t.Errorf("до нажатия кнопки задача создаваться не должна: было %d, стало %d", tasksBefore, got)
	}
}

func TestChatSuggestionCallbacksCreateTasks(t *testing.T) {
	cases := []struct {
		action      string
		wantPlan    bool
		messageText string
	}{
		{action: "chat_plan", wantPlan: true, messageText: "исправь падающий тест"},
		{action: "chat_task", wantPlan: false, messageText: "обнови зависимости"},
	}

	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			mt, _ := setupChatTestApp(t, "ответ")

			sendText(t, mt, tc.messageText)
			card := mt.LastSent()
			if card == nil || card.Opts == nil || card.Opts.Keyboard == nil {
				t.Fatal("ожидали карточку выбора")
			}

			// Идентификатор карточки — номер последнего отправленного сообщения.
			msgID := ports.MessageID(strconv.Itoa(mt.SentCount()))
			handler, ok := mt.callbacks[tc.action]
			if !ok {
				t.Fatalf("callback %s не зарегистрирован", tc.action)
			}

			tasksBefore := len(domain.GlobalTaskManager.ListTasks())
			sess := &mock.Session{
				M:      mt.Messenger,
				ChatID: testChatID,
				Sender: string(testChatID),
				CB: &ports.CallbackQuery{
					ID:      "cb-1",
					Chat:    testChatID,
					Action:  tc.action,
					Message: &ports.MessageRef{Chat: testChatID, ID: msgID},
				},
			}
			if err := handler(sess); err != nil {
				t.Fatalf("callback %s вернул ошибку: %v", tc.action, err)
			}

			tasks := domain.GlobalTaskManager.ListTasks()
			if len(tasks) != tasksBefore+1 {
				t.Fatalf("ожидали новую задачу: было %d, стало %d", tasksBefore, len(tasks))
			}
			created := tasks[len(tasks)-1]
			createdView := created.Snapshot()
			requiresPlan := createdView.RequiresPlan
			prompt := createdView.InitialPrompt

			if requiresPlan != tc.wantPlan {
				t.Errorf("RequiresPlan = %v, ожидали %v", requiresPlan, tc.wantPlan)
			}
			if prompt != tc.messageText {
				t.Errorf("текст задачи = %q, ожидали %q", prompt, tc.messageText)
			}
		})
	}
}

func TestChatSuggestionExpired(t *testing.T) {
	mt, _ := setupChatTestApp(t, "ответ")

	handler := mt.callbacks["chat_answer"]
	sess := &mock.Session{
		M:      mt.Messenger,
		ChatID: testChatID,
		Sender: string(testChatID),
		CB: &ports.CallbackQuery{
			ID:      "cb-x",
			Chat:    testChatID,
			Action:  "chat_answer",
			Message: &ports.MessageRef{Chat: testChatID, ID: "999999"},
		},
	}
	if err := handler(sess); err != nil {
		t.Fatalf("callback вернул ошибку: %v", err)
	}

	last := mt.LastSent()
	if last == nil || !strings.Contains(last.Text, "has expired") {
		t.Errorf("ожидали сообщение об устаревшей карточке, получили %v", last)
	}
}

func TestChatQueuesMessageWhileAnswering(t *testing.T) {
	mt, _ := setupChatTestApp(t, "ответ")

	session := domain.GlobalChatManager.GetOrCreate("testproj", "model")
	if !session.BeginTurn(func() {}) {
		t.Fatal("не удалось занять сессию")
	}
	defer session.EndTurn()

	sendText(t, mt, "и ещё один вопрос")

	last := mt.LastSent()
	if last == nil || !strings.Contains(last.Text, "queued:") {
		t.Fatalf("ожидали сообщение об очереди, получили %v", last)
	}
	if got := len(session.DrainPending()); got != 1 {
		t.Errorf("в очереди %d сообщений, ожидали 1", got)
	}
}

func TestPausedTaskWithoutFreshQuestionDoesNotHijackChat(t *testing.T) {
	mt, agent := setupChatTestApp(t, "отвечаю в чате")

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "model", "agy", "старая задача", testChatID, false)
	task.Update(func(t *domain.TaskSession) {
		t.Status = domain.TaskStatusPaused
	})
	if _, err := domain.GlobalTaskManager.SetActiveTask(task.Snapshot().ID); err != nil {
		t.Fatalf("не удалось сделать задачу активной: %v", err)
	}

	sendText(t, mt, "расскажи, что делает роутер")

	waitFor(t, "запуск агента для диалога", func() bool { return len(agent.Calls()) == 1 })

	view := task.Snapshot()
	followups := len(view.PendingFollowups)
	if followups != 0 {
		t.Errorf("сообщение не должно попадать в приостановленную задачу без свежего вопроса (дополнений: %d)", followups)
	}
}

func TestPausedTaskWithFreshQuestionGetsAnswer(t *testing.T) {
	mt, agent := setupChatTestApp(t, "не должно вызываться")

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "model", "agy", "задача с вопросом", testChatID, false)
	task.Update(func(t *domain.TaskSession) {
		t.Status = domain.TaskStatusPaused
		t.LastQuestion = "Какой вариант выбрать?"
		t.QuestionAskedAt = time.Now()
	})
	if _, err := domain.GlobalTaskManager.SetActiveTask(task.Snapshot().ID); err != nil {
		t.Fatalf("не удалось сделать задачу активной: %v", err)
	}

	sendText(t, mt, "выбирай первый вариант")

	if len(agent.Calls()) != 0 {
		t.Error("ответ на вопрос задачи не должен уходить в диалог")
	}

	found := false
	for _, text := range mt.AllTexts() {
		if strings.Contains(text, "#"+strconv.Itoa(task.Snapshot().ID)) {
			found = true
			break
		}
	}
	if !found {
		t.Error("ожидали, что ответ будет передан задаче")
	}
}

func TestChatNewResetsConversation(t *testing.T) {
	mt, agent := setupChatTestApp(t, "ответ агента")

	sendText(t, mt, "первый вопрос?")
	waitFor(t, "первый ответ", func() bool { return len(agent.Calls()) == 1 })
	waitFor(t, "завершение хода", func() bool {
		session := domain.GlobalChatManager.Get("testproj")
		return session != nil && !session.IsRunning()
	})

	handler := mt.commands["chat"]
	if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{"new"}})); err != nil {
		t.Fatalf("/chat new: %v", err)
	}

	session := domain.GlobalChatManager.Get("testproj")
	if session == nil {
		t.Fatal("после сброса ожидали новую сессию")
	}
	if session.TurnsCount() != 0 || session.ConversationIDFor("agy", "cli") != "" {
		t.Errorf("после /chat new контекст должен быть пустым: реплик %d, сессия %q",
			session.TurnsCount(), session.ConversationIDFor("agy", "cli"))
	}
}

func TestChatStatusShowsAgentAndMode(t *testing.T) {
	mt, _ := setupChatTestApp(t, "ответ")

	handler := mt.commands["chat"]
	if err := handler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/chat: %v", err)
	}

	last := mt.LastSent()
	if last == nil {
		t.Fatal("ожидали карточку статуса диалога")
	}
	if !strings.Contains(last.Text, "agy") || !strings.Contains(last.Text, "cli") {
		t.Errorf("статус должен показывать агента и режим: %s", last.Text)
	}
	if !strings.Contains(last.Text, "testproj") {
		t.Errorf("статус должен показывать проект: %s", last.Text)
	}
}

func TestChatCommandsRegisteredInMenu(t *testing.T) {
	setupTestApp(t)

	cmds := getDefaultCommands("ru")
	found := map[string]bool{}
	for _, cmd := range cmds {
		found[cmd.Name] = true
	}
	for _, name := range []string{"chat", "chatmode"} {
		if !found[name] {
			t.Errorf("команда /%s отсутствует в меню", name)
		}
	}
}
