package handlers

import (
	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"context"
	"fmt"
	"html"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func collectAllButtons(kb *ports.Keyboard) []ports.Button {
	if kb == nil {
		return nil
	}
	var out []ports.Button
	for _, row := range kb.Rows {
		out = append(out, row...)
	}
	return out
}

func rowLengths(rows [][]ports.Button) []int {
	lengths := make([]int, len(rows))
	for i, r := range rows {
		lengths[i] = len(r)
	}
	return lengths
}

func TestQuestionUI_ShortSingleSelect(t *testing.T) {
	task := &domain.TaskSession{
		ID:      7,
		Project: "demo",
		PendingQuestions: []domain.QuestionItem{
			{
				Question: "Удалить временный файл?",
				Options:  []string{"Да", "Нет"},
			},
		},
	}

	msg := formatTaskQuestionMessage(task.Snapshot(), false, "ru")
	if !strings.Contains(msg, "<b>1.</b> Да\n<b>2.</b> Нет") {
		t.Fatalf("expected full numbered list in message, got:\n%s", msg)
	}

	kb := buildQuestionMarkup(task, "ru")
	if kb == nil || len(kb.Rows) < 2 {
		t.Fatalf("expected at least 2 button rows, got %+v", kb)
	}
	firstRow := kb.Rows[0]
	if len(firstRow) != 2 {
		t.Fatalf("expected 2 short buttons in one row, got %d: %+v", len(firstRow), firstRow)
	}
	if firstRow[0].Text != "1. Да" || firstRow[1].Text != "2. Нет" {
		t.Errorf("unexpected short button labels: %+v", firstRow)
	}
	for _, b := range firstRow {
		if strings.Contains(b.Text, "...") {
			t.Errorf("button text must not be truncated: %q", b.Text)
		}
		if b.Action != "q_choice" {
			t.Errorf("expected Action q_choice, got %q", b.Action)
		}
	}
}

func TestQuestionUI_LongSingleSelectAndEscaping(t *testing.T) {
	opt1 := "Использовать существующую таблицу migrations_v2 с сохранением обратной совместимости <legacy> & индексов"
	opt2 := "Создать новую схему schema_v3 и перенести исторические записи отдельным фоновым воркером"
	opt3 := "Пропустить миграцию и оставить текущую структуру базы без изменений"

	task := &domain.TaskSession{
		ID:      12,
		Project: "core",
		PendingQuestions: []domain.QuestionItem{
			{
				Question: "Какой способ миграции выбрать?",
				Options:  []string{opt1, opt2, opt3},
			},
		},
	}

	msg := formatTaskQuestionMessage(task.Snapshot(), true, "ru")
	if !strings.Contains(msg, "<b>1.</b> "+html.EscapeString(opt1)) {
		t.Errorf("expected full escaped opt1 in message, got:\n%s", msg)
	}
	if !strings.Contains(msg, "<b>2.</b> "+html.EscapeString(opt2)) {
		t.Errorf("expected full escaped opt2 in message, got:\n%s", msg)
	}
	if !strings.Contains(msg, "<b>3.</b> "+html.EscapeString(opt3)) {
		t.Errorf("expected full escaped opt3 in message, got:\n%s", msg)
	}

	kb := buildQuestionMarkup(task, "ru")
	if kb == nil || len(kb.Rows) != 2 {
		t.Fatalf("expected 1 option row + 1 control row, got %+v", kb)
	}
	optRow := kb.Rows[0]
	if len(optRow) != 3 {
		t.Fatalf("expected 3 numeric buttons in row 0, got %d", len(optRow))
	}
	for i, b := range optRow {
		wantText := strconv.Itoa(i + 1)
		wantPayload := fmt.Sprintf("12:0:%d", i)
		if b.Text != wantText || b.Action != "q_choice" || b.Payload != wantPayload {
			t.Errorf("button %d = %+v, want text=%q action=q_choice payload=%q", i, b, wantText, wantPayload)
		}
	}
}

func TestQuestionUI_MixedAndMultilineSwitchesToNumeric(t *testing.T) {
	t.Run("Mixed short and long option uses numeric buttons for whole question", func(t *testing.T) {
		task := &domain.TaskSession{
			ID:      3,
			Project: "app",
			PendingQuestions: []domain.QuestionItem{
				{
					Question: "Продолжить?",
					Options:  []string{"Да", "Сначала пересобрать весь кэш и проверить миграции"},
				},
			},
		}
		kb := buildQuestionMarkup(task, "ru")
		if len(kb.Rows[0]) != 2 || kb.Rows[0][0].Text != "1" || kb.Rows[0][1].Text != "2" {
			t.Fatalf("expected numeric buttons [1, 2], got %+v", kb.Rows[0])
		}
	})

	t.Run("Multiline option forces numeric buttons and indents continuation", func(t *testing.T) {
		task := &domain.TaskSession{
			ID:      4,
			Project: "app",
			PendingQuestions: []domain.QuestionItem{
				{
					Question: "Выберите формат",
					Options:  []string{"Строка 1\nСтрока 2", "Кратко"},
				},
			},
		}
		msg := formatTaskQuestionMessage(task.Snapshot(), false, "ru")
		if !strings.Contains(msg, "<b>1.</b> Строка 1\n   Строка 2") {
			t.Errorf("expected indented multiline option in message, got:\n%s", msg)
		}
		kb := buildQuestionMarkup(task, "ru")
		if kb.Rows[0][0].Text != "1" || kb.Rows[0][1].Text != "2" {
			t.Errorf("expected numeric buttons for multiline option, got %+v", kb.Rows[0])
		}
	})
}

func TestQuestionUI_MultiSelectSyncAndSubmit(t *testing.T) {
	t.Run("Short multi-select options show full text and synchronized checkboxes", func(t *testing.T) {
		task := &domain.TaskSession{
			ID:      9,
			Project: "api",
			PendingQuestions: []domain.QuestionItem{
				{
					Question:        "Какие модули?",
					Options:         []string{"API", "UI", "Worker"},
					MultiSelect:     true,
					SelectedIndices: []int{1},
				},
			},
		}

		msg := formatTaskQuestionMessage(task.Snapshot(), false, "ru")
		if !strings.Contains(msg, "⬜ <b>1.</b> API\n☑️ <b>2.</b> UI\n⬜ <b>3.</b> Worker") {
			t.Errorf("expected synchronized checkboxes in message, got:\n%s", msg)
		}

		kb := buildQuestionMarkup(task, "ru")
		btns := collectAllButtons(kb)
		var toggles []string
		var submitText string
		for _, b := range btns {
			if b.Action == "q_toggle" {
				toggles = append(toggles, b.Text)
			}
			if b.Action == "q_submit" {
				submitText = b.Text
			}
		}
		wantToggles := []string{"⬜ 1. API", "☑️ 2. UI", "⬜ 3. Worker"}
		if !reflect.DeepEqual(toggles, wantToggles) {
			t.Errorf("toggles = %v, want %v", toggles, wantToggles)
		}
		if submitText != i18n.Tf("ru", "btn.q_submit_count", 1) {
			t.Errorf("submit button = %q, want %q", submitText, i18n.Tf("ru", "btn.q_submit_count", 1))
		}
	})

	t.Run("Long multi-select uses compact numbered toggles and submits multi_option_chosen_edit", func(t *testing.T) {
		mt := setupTestApp(t)
		optA := "Полный аудит безопасности HTTP-эндпоинтов <v1>"
		optB := "Нагрузочное тестирование WebSocket & gRPC соединений"
		optC := "Проверка миграций SQLite без простоя сервиса"

		task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "m", "agy", "audit", testChatID, false)
		task.BeginPipeline()
		task.Update(func(ts *domain.TaskSession) {
			ts.Status = domain.TaskStatusWaitingInput
			ts.PendingQuestions = []domain.QuestionItem{{
				Question:        "Какие проверки запустить?",
				Options:         []string{optA, optB, optC},
				MultiSelect:     true,
				SelectedIndices: []int{0, 2},
			}}
		})
		taskID := task.Snapshot().ID

		kb := buildQuestionMarkup(task, "ru")
		if len(kb.Rows[0]) != 3 {
			t.Fatalf("expected 3 compact toggles in first row, got %d", len(kb.Rows[0]))
		}
		wantRow := []string{"☑️ 1", "⬜ 2", "☑️ 3"}
		for i, want := range wantRow {
			if kb.Rows[0][i].Text != want {
				t.Errorf("toggle[%d].Text = %q, want %q", i, kb.Rows[0][i].Text, want)
			}
		}
		if kb.Rows[1][0].Action != "q_submit" || kb.Rows[1][0].Text != i18n.Tf("ru", "btn.q_submit_count", 2) {
			t.Errorf("expected submit button with count 2, got %+v", kb.Rows[1][0])
		}

		// Нажатие Submit редактирует сообщение с task.multi_option_chosen_edit и экранирует MessageText.
		rawMsgText := "Вопрос <v1> & опции"
		cbMock := &mock.Session{
			CB: &ports.CallbackQuery{
				ID:          "cb-sub",
				Action:      "q_submit",
				Payload:     fmt.Sprintf("%d:0", taskID),
				Message:     &ports.MessageRef{Chat: testChatID, ID: "m1"},
				MessageText: rawMsgText,
			},
		}
		if err := onQuestionSubmit(adminSession(mt, cbMock)); err != nil {
			t.Fatalf("onQuestionSubmit: %v", err)
		}
		if len(cbMock.Edits) != 1 {
			t.Fatalf("expected 1 edit, got %d", len(cbMock.Edits))
		}
		edited := cbMock.Edits[0]
		if strings.Contains(edited, "Выбран вариант 1") {
			t.Errorf("multi-select edit must not claim 'Выбран вариант 1', got:\n%s", edited)
		}
		if !strings.Contains(edited, html.EscapeString(rawMsgText)) {
			t.Errorf("expected escaped original MessageText in edit, got:\n%s", edited)
		}
		if !strings.Contains(edited, html.EscapeString(optA+", "+optC)) {
			t.Errorf("expected full escaped selected options in edit, got:\n%s", edited)
		}
	})
}

func TestQuestionUI_ChunkButtonsBalanced(t *testing.T) {
	makeBtns := func(n int) []ports.Button {
		out := make([]ports.Button, n)
		for i := 0; i < n; i++ {
			out[i] = ports.Button{Text: strconv.Itoa(i + 1)}
		}
		return out
	}

	singleCases := map[int][]int{
		1:  {1},
		2:  {2},
		3:  {3},
		4:  {4},
		5:  {5},
		6:  {3, 3},
		7:  {4, 3},
		8:  {4, 4},
		9:  {5, 4},
		10: {5, 5},
	}
	for n, want := range singleCases {
		got := rowLengths(chunkButtonsBalanced(makeBtns(n), maxSingleDigitButtonsPerRow))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("chunkButtonsBalanced(%d, 5) = %v, want %v", n, got, want)
		}
	}

	multiCases := map[int][]int{
		1:  {1},
		2:  {2},
		3:  {3},
		4:  {4},
		5:  {3, 2},
		6:  {3, 3},
		7:  {4, 3},
		8:  {4, 4},
		9:  {3, 3, 3},
		10: {4, 3, 3},
	}
	for n, want := range multiCases {
		got := rowLengths(chunkButtonsBalanced(makeBtns(n), maxMultiDigitButtonsPerRow))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("chunkButtonsBalanced(%d, 4) = %v, want %v", n, got, want)
		}
	}
}

func TestQuestionUI_WizardAndAllTextMode(t *testing.T) {
	mt := setupTestApp(t)
	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "m", "agy", "wizard", testChatID, false)
	task.Update(func(ts *domain.TaskSession) {
		ts.Status = domain.TaskStatusWaitingInput
		ts.PendingQuestions = []domain.QuestionItem{
			{
				Question: "Где хранить сессии?",
				Options: []string{
					"Встроенная SQLite база данных с WAL-режимом",
					"Внешний PostgreSQL кластер с пулом соединений",
				},
			},
			{
				Question: "Включить логирование?",
				Options:  []string{"Да", "Нет"},
			},
		}
		ts.CurrentQuestionIndex = 0
	})
	taskID := task.Snapshot().ID

	msg := formatTaskQuestionMessage(task.Snapshot(), false, "ru")
	if !strings.Contains(msg, "<b>1.</b> Встроенная SQLite база данных с WAL-режимом") ||
		!strings.Contains(msg, "<b>2.</b> Внешний PostgreSQL кластер с пулом соединений") {
		t.Errorf("wizard step 1 missing full option list:\n%s", msg)
	}

	// Переключаемся в режим "Ответить на все текстом" (q_all_text)
	allSess := &mock.Session{CB: &ports.CallbackQuery{ID: "cb-all", Action: "q_all_text", Payload: strconv.Itoa(taskID)}}
	if err := onQuestionAllText(adminSession(mt, allSess)); err != nil {
		t.Fatalf("onQuestionAllText: %v", err)
	}
	if len(allSess.Edits) != 1 {
		t.Fatalf("expected 1 edit after q_all_text, got %d", len(allSess.Edits))
	}
	allMsg := allSess.Edits[0]
	if !strings.Contains(allMsg, "   1) Встроенная SQLite база данных с WAL-режимом") ||
		!strings.Contains(allMsg, "   2) Внешний PostgreSQL кластер с пулом соединений") {
		t.Errorf("all-text prompt missing full numbered options:\n%s", allMsg)
	}

	// Возвращаемся кнопкой q_back к шагу 1
	backSess := &mock.Session{CB: &ports.CallbackQuery{ID: "cb-back", Action: "q_back", Payload: strconv.Itoa(taskID)}}
	if err := onQuestionBack(adminSession(mt, backSess)); err != nil {
		t.Fatalf("onQuestionBack: %v", err)
	}
	if task.Snapshot().QuestionAcceptAllTxt {
		t.Errorf("expected QuestionAcceptAllTxt=false after q_back")
	}
}

func TestQuestionUI_MCPAndChatNo64RuneTruncation(t *testing.T) {
	longOpt1 := strings.Repeat("Длинный вариант выбора конфигурации сервера с подробным описанием деталей ", 2) // > 140 runes
	longOpt2 := strings.Repeat("Альтернативный вариант архитектуры с использованием отдельной очереди событий ", 2)

	items := normalizeAskQuestionItems(map[string]interface{}{
		"question": "Какую архитектуру применить?",
		"options":  []interface{}{longOpt1, longOpt2},
	})
	if len(items) != 1 || len(items[0].Options) != 2 {
		t.Fatalf("unexpected items: %+v", items)
	}
	if items[0].Options[0] != strings.TrimSpace(longOpt1) {
		t.Errorf("option 1 was truncated or altered:\ngot:  %q\nwant: %q", items[0].Options[0], strings.TrimSpace(longOpt1))
	}

	// Проверяем верхний предел maxQuestionOptionRunes (500) и maxQuestionOptions (10)
	over500 := strings.Repeat("a", 550)
	many := make([]string, 12)
	for i := range many {
		many[i] = fmt.Sprintf("opt-%d", i)
	}
	many[0] = over500
	norm := normalizeQuestionOptions(many)
	if len(norm) != maxQuestionOptions {
		t.Errorf("len(norm) = %d, want %d", len(norm), maxQuestionOptions)
	}
	if len([]rune(norm[0])) != maxQuestionOptionRunes {
		t.Errorf("len(norm[0]) = %d runes, want %d", len([]rune(norm[0])), maxQuestionOptionRunes)
	}

	// Проверяем chatChannel.AskQuestions: полный список с нумерацией без обрезки 64 симв.
	m := mock.New()
	ch := &chatChannel{m: m, chat: testChatID, project: "p", lang: "ru"}
	if err := ch.AskQuestions([]utils.ParsedQuestion{
		{Question: "Выбрать подход?", Options: []string{strings.TrimSpace(longOpt1), strings.TrimSpace(longOpt2)}},
	}); err != nil {
		t.Fatalf("chatChannel.AskQuestions: %v", err)
	}
	last := m.LastSent()
	if last == nil || !strings.Contains(last.Text, "<b>1.</b> "+strings.TrimSpace(longOpt1)) {
		t.Fatalf("chatChannel.AskQuestions truncated or omitted numbered options:\n%v", last)
	}

	// Проверяем end-to-end: нажатие кнопки длинного варианта доставляет агенту полный не обрезанный текст.
	mt := setupTestApp(t)
	projectsRoot, _, dirB := setupTwoProjects(t)
	fw := &scriptedAgentFramework{
		steps: []scriptedAgentStep{
			func(ctx context.Context, args ports.ExecuteArgs) (string, error) {
				args.BotTools.CallBotTool(ctx, mcp.ToolAskUser, map[string]interface{}{
					"question": "Выберите план действий",
					"options":  []interface{}{longOpt1, longOpt2},
				})
				return `{"type":"system","session_id":"conv-long"}` + "\n" +
					`{"type":"result","session_id":"conv-long","status":"SUCCESS"}` + "\n", nil
			},
			func(_ context.Context, _ ports.ExecuteArgs) (string, error) {
				return `{"type":"system","session_id":"conv-long"}` + "\n" +
					`{"type":"result","session_id":"conv-long","status":"SUCCESS","response":"Done\nPR_URL: https://github.com/org/repo/pull/55"}` + "\n", nil
			},
		},
	}
	registerScriptedAgent(t, "scripted-long", fw)

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("projB", "mock-model", "scripted-long", "long opt test", testChatID, false)
	task.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusRunning })
	taskID := task.Snapshot().ID

	done := make(chan struct{})
	go func() {
		defer close(done)
		runAgentTaskPipeline(mt.Messenger, testChatID, task, dirB, projectsRoot)
	}()

	waitFor(t, "waiting for long option answer", func() bool {
		return task.Snapshot().Status == domain.TaskStatusWaitingInput && sentTextsContain(mt, strings.TrimSpace(longOpt1))
	})

	// Убеждаемся, что отправлено сообщение с полным текстом варианта и цифровой кнопкой "1"
	lastSent := mt.Messenger.LastSent()
	if lastSent == nil || !strings.Contains(lastSent.Text, strings.TrimSpace(longOpt1)) ||
		lastSent.Opts == nil || lastSent.Opts.Keyboard == nil ||
		len(lastSent.Opts.Keyboard.Rows) == 0 || lastSent.Opts.Keyboard.Rows[0][0].Text != "1" {
		t.Fatalf("expected sent message with full longOpt1 and numeric button '1', got %+v", lastSent)
	}

	// Нажимаем кнопку "1"
	_ = onQuestionChoice(adminSession(mt, &mock.Session{
		CB: &ports.CallbackQuery{
			ID:          "cb-long",
			Action:      "q_choice",
			Payload:     fmt.Sprintf("%d:0:0", taskID),
			Message:     &ports.MessageRef{Chat: testChatID, ID: "qmsg"},
			MessageText: "Выберите план действий <tag> & details",
		},
	}))

	<-done

	calls := fw.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[1].Prompt != strings.TrimSpace(longOpt1) {
		t.Errorf("agent received truncated option:\ngot:  %q\nwant: %q", calls[1].Prompt, strings.TrimSpace(longOpt1))
	}
}

func TestQuestionUI_PlanApprovalVariants(t *testing.T) {
	t.Run("Short plan variants keep full labels on buttons", func(t *testing.T) {
		variants := []string{"Вариант 1", "Вариант 2"}
		kb := buildPlanApprovalMarkup(42, variants, "ru")
		if len(kb.Rows[0]) != 2 {
			t.Fatalf("expected 2 short variant buttons in one row, got %d", len(kb.Rows[0]))
		}
		if kb.Rows[0][0].Text != "✅ Вариант 1" || kb.Rows[0][1].Text != "✅ Вариант 2" {
			t.Errorf("unexpected short variant buttons: %+v", kb.Rows[0])
		}
	})

	t.Run("Long plan (>2600 runes) with long variants shows full variant list and numeric buttons", func(t *testing.T) {
		mt := setupTestApp(t)
		v1 := "Вариант 1: Перевести хранилище сессий на SQLite WAL с отдельным пулом соединений"
		v2 := "Вариант 2: Использовать Redis Streams для буферизации сообщений и повторов"
		longBody := strings.Repeat("Подробный шаг реализации с техническими деталями. ", 70) // > 2600 runes
		planText := fmt.Sprintf("## План\n%s\n\n### Альтернативы\n%s\n\n%s\n", longBody, v1, v2)

		task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "m", "agy", "arch", testChatID, true)
		task.Update(func(ts *domain.TaskSession) {
			ts.Status = domain.TaskStatusWaitingApproval
			ts.Plan = planText
		})

		sendPlanForApproval(mt.Messenger, testChatID, task, "ru")
		sent := mt.Messenger.LastSent()
		if sent == nil {
			t.Fatal("expected sent plan approval message")
		}
		if !strings.Contains(sent.Text, "🔀 <b>Варианты реализации:</b>") ||
			!strings.Contains(sent.Text, "<b>1.</b> "+v1) ||
			!strings.Contains(sent.Text, "<b>2.</b> "+v2) {
			t.Errorf("expected full variants section in long plan message, got:\n%s", sent.Text)
		}
		if sent.Opts == nil || sent.Opts.Keyboard == nil || len(sent.Opts.Keyboard.Rows) == 0 {
			t.Fatal("expected keyboard on plan approval message")
		}
		firstRow := sent.Opts.Keyboard.Rows[0]
		if len(firstRow) != 2 || firstRow[0].Text != "✅ 1" || firstRow[1].Text != "✅ 2" {
			t.Errorf("expected compact numeric variant buttons [✅ 1, ✅ 2], got %+v", firstRow)
		}
		if firstRow[0].Action != "plan_appr_var" {
			t.Errorf("expected action plan_appr_var, got %q", firstRow[0].Action)
		}
	})
}

func TestQuestionUI_PlainNumericReplyNotSubstituted(t *testing.T) {
	mt := setupTestApp(t)
	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "m", "agy", "retries", testChatID, false)
	task.BeginPipeline()
	task.Update(func(ts *domain.TaskSession) {
		ts.Status = domain.TaskStatusWaitingInput
		ts.PendingQuestions = []domain.QuestionItem{
			{
				Question: "Сколько попыток сделать?",
				Options:  []string{"5 попыток по таймауту", "10 попыток с экспоненциальной задержкой"},
			},
		}
		ts.LastQuestion = "Сколько попыток сделать?"
		ts.QuestionOptions = ts.PendingQuestions[0].Options
	})

	if err := handleAddFollowupToTask(adminSession(mt, &mock.Session{}), task.Snapshot().ID, "2"); err != nil {
		t.Fatalf("handleAddFollowupToTask: %v", err)
	}

	got := <-task.AnswerChannel()
	if got != "2" {
		t.Errorf("plain numeric reply must pass verbatim as %q, got %q", "2", got)
	}
}

func TestQuestionUI_CallbackEditEscapesHTMLAndResumeLegacy(t *testing.T) {
	mt := setupTestApp(t)
	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "m", "agy", "legacy", testChatID, false)
	task.BeginPipeline()
	task.Update(func(ts *domain.TaskSession) {
		ts.Status = domain.TaskStatusWaitingInput
		ts.LastQuestion = "Выбрать режим?"
		ts.QuestionOptions = []string{"Вариант <A> & B", "Вариант C"}
	})
	taskID := task.Snapshot().ID

	// /resume без аргументов должен вывести полный вопрос и варианты для legacy QuestionOptions
	if err := handleResume(adminSession(mt, &mock.Session{TextVal: "/resume " + strconv.Itoa(taskID), ArgsVal: []string{strconv.Itoa(taskID)}})); err != nil {
		t.Fatalf("handleResume: %v", err)
	}
	resumedMsg := mt.Messenger.LastSent()
	if resumedMsg == nil || !strings.Contains(resumedMsg.Text, "<b>1.</b> "+html.EscapeString("Вариант <A> & B")) {
		t.Errorf("expected legacy QuestionOptions in /resume output, got: %+v", resumedMsg)
	}

	// onQuestionPause экранирует cb.MessageText
	pauseSess := &mock.Session{
		CB: &ports.CallbackQuery{
			ID:          "cb-pause",
			Action:      "q_pause",
			Payload:     strconv.Itoa(taskID),
			Message:     &ports.MessageRef{Chat: testChatID, ID: "qm"},
			MessageText: "Вопрос с <tag> & символом",
		},
	}
	if err := onQuestionPause(adminSession(mt, pauseSess)); err != nil {
		t.Fatalf("onQuestionPause: %v", err)
	}
	if len(pauseSess.Edits) != 1 || !strings.Contains(pauseSess.Edits[0], html.EscapeString("Вопрос с <tag> & символом")) {
		t.Errorf("expected HTML-escaped MessageText in pause edit, got %v", pauseSess.Edits)
	}

	// onQuestionChoice экранирует cb.MessageText
	task2 := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "m", "agy", "escape", testChatID, false)
	task2.BeginPipeline()
	task2.Update(func(ts *domain.TaskSession) {
		ts.Status = domain.TaskStatusWaitingInput
		ts.LastQuestion = "Q?"
		ts.QuestionOptions = []string{"Opt <1> & 2"}
	})
	choiceSess := &mock.Session{
		CB: &ports.CallbackQuery{
			ID:          "cb-ch",
			Action:      "q_choice",
			Payload:     fmt.Sprintf("%d:0:0", task2.Snapshot().ID),
			Message:     &ports.MessageRef{Chat: testChatID, ID: "qm2"},
			MessageText: "Текст с <script> & спецсимволами",
		},
	}
	if err := onQuestionChoice(adminSession(mt, choiceSess)); err != nil {
		t.Fatalf("onQuestionChoice: %v", err)
	}
	if len(choiceSess.Edits) != 1 || !strings.Contains(choiceSess.Edits[0], html.EscapeString("Текст с <script> & спецсимволами")) {
		t.Errorf("expected HTML-escaped MessageText in choice edit, got %v", choiceSess.Edits)
	}
	_ = config.ProjectsRoot
}
