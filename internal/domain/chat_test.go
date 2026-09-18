package domain

import (
	"context"
	"strings"
	"sync"
	"testing"

	"bro-bot/internal/storage"
)

func newChatTestStorage(t *testing.T) storage.Storage {
	t.Helper()
	st, err := storage.NewSQLiteStorage(":memory:")
	if err != nil {
		t.Fatalf("не удалось создать тестовое хранилище: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestChatSessionConversationsPerAgentAndMode(t *testing.T) {
	cm := NewChatManager()
	cm.InitWithStorage(newChatTestStorage(t))

	cs := cm.GetOrCreate("proj", "gemini-3.1-pro-high")
	cs.SetConversationID("agy", "cli", "agy-cli-1")
	cs.SetConversationID("claude", "cli", "claude-cli-1")
	cs.SetConversationID("agy", "api", "agy-api-1")

	if got := cs.ConversationIDFor("agy", "cli"); got != "agy-cli-1" {
		t.Errorf("agy/cli = %q", got)
	}
	if got := cs.ConversationIDFor("claude", "cli"); got != "claude-cli-1" {
		t.Errorf("claude/cli = %q", got)
	}
	if got := cs.ConversationIDFor("agy", "api"); got != "agy-api-1" {
		t.Errorf("agy/api = %q", got)
	}
	// Пара без записи не должна подхватывать чужой идентификатор.
	if got := cs.ConversationIDFor("claude", "api"); got != "" {
		t.Errorf("claude/api должен быть пустым, получили %q", got)
	}
}

func TestChatSessionNeedsContextBootstrap(t *testing.T) {
	cm := NewChatManager()
	cs := cm.GetOrCreate("proj", "model")

	if cs.NeedsContextBootstrap("agy", "cli") {
		t.Error("пустой истории бутстрап не нужен")
	}

	cs.AppendTurn(ChatRoleUser, "привет")
	cs.AppendTurn(ChatRoleAssistant, "привет, чем помочь?")

	if !cs.NeedsContextBootstrap("claude", "cli") {
		t.Error("новому cli-агенту нужен контекст прошлых реплик")
	}
	if cs.NeedsContextBootstrap("agy", "api") {
		t.Error("в api-режиме история передаётся напрямую, бутстрап не нужен")
	}

	cs.SetConversationID("claude", "cli", "claude-conv")
	if cs.NeedsContextBootstrap("claude", "cli") {
		t.Error("у агента появилась своя сессия — бутстрап больше не нужен")
	}
}

func TestChatSessionHistoryTrimByTurns(t *testing.T) {
	cm := NewChatManager()
	cs := cm.GetOrCreate("proj", "model")

	for i := 0; i < MaxChatHistoryTurns+10; i++ {
		cs.AppendTurn(ChatRoleUser, strings.Repeat("a", 5))
	}
	if got := cs.TurnsCount(); got != MaxChatHistoryTurns {
		t.Errorf("история = %d реплик, ожидали %d", got, MaxChatHistoryTurns)
	}
}

func TestChatSessionHistoryForPromptRespectsCharBudget(t *testing.T) {
	cm := NewChatManager()
	cs := cm.GetOrCreate("proj", "model")

	cs.AppendTurn(ChatRoleUser, strings.Repeat("с", 100))
	cs.AppendTurn(ChatRoleAssistant, strings.Repeat("т", 100))
	cs.AppendTurn(ChatRoleUser, strings.Repeat("н", 100))

	history := cs.HistoryForPrompt(10, 250)
	if len(history) != 2 {
		t.Fatalf("в бюджет 250 символов должны поместиться 2 реплики, получили %d", len(history))
	}
	// Остаются самые свежие реплики, в хронологическом порядке.
	if !strings.HasPrefix(history[0].Content, "т") || !strings.HasPrefix(history[1].Content, "н") {
		t.Errorf("ожидали две последние реплики по порядку, получили %q и %q", history[0].Content, history[1].Content)
	}

	if got := len(cs.HistoryForPrompt(2, 0)); got != 2 {
		t.Errorf("лимит по числу реплик не сработал: %d", got)
	}
}

func TestChatSessionTurnIsExclusive(t *testing.T) {
	cm := NewChatManager()
	cs := cm.GetOrCreate("proj", "model")

	if !cs.BeginTurn(func() {}) {
		t.Fatal("первый ход должен стартовать")
	}
	if cs.BeginTurn(func() {}) {
		t.Error("второй ход не должен стартовать параллельно")
	}
	if n := cs.EnqueuePending("второе сообщение"); n != 1 {
		t.Errorf("очередь = %d, ожидали 1", n)
	}
	if n := cs.EnqueuePending("третье сообщение"); n != 2 {
		t.Errorf("очередь = %d, ожидали 2", n)
	}

	pending := cs.DrainPending()
	if len(pending) != 2 || pending[0] != "второе сообщение" {
		t.Errorf("неожиданная очередь: %+v", pending)
	}
	if cs.DrainPending() != nil {
		t.Error("повторный разбор очереди должен быть пустым")
	}

	cs.EndTurn()
	if !cs.BeginTurn(func() {}) {
		t.Error("после завершения хода новый ход должен стартовать")
	}
}

func TestChatSessionCancel(t *testing.T) {
	cm := NewChatManager()
	cs := cm.GetOrCreate("proj", "model")

	if cs.Cancel() {
		t.Error("отменять нечего, когда ход не идёт")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cs.BeginTurn(cancel)
	if !cs.Cancel() {
		t.Fatal("ожидали отмену активного хода")
	}
	select {
	case <-ctx.Done():
	default:
		t.Error("контекст хода должен быть отменён")
	}
	cs.EndTurn()
}

func TestChatSessionConcurrentTurns(t *testing.T) {
	cm := NewChatManager()
	cs := cm.GetOrCreate("proj", "model")

	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cs.BeginTurn(func() {}) {
				mu.Lock()
				started++
				mu.Unlock()
			} else {
				cs.EnqueuePending("сообщение")
			}
		}()
	}
	wg.Wait()

	if started != 1 {
		t.Errorf("параллельно стартовало %d ходов, ожидали 1", started)
	}
	if got := len(cs.DrainPending()); got != 19 {
		t.Errorf("в очередь попало %d сообщений, ожидали 19", got)
	}
}

func TestChatManagerResetStartsCleanSession(t *testing.T) {
	cm := NewChatManager()
	cm.InitWithStorage(newChatTestStorage(t))

	first := cm.GetOrCreate("proj", "model")
	first.AppendTurn(ChatRoleUser, "первый разговор")
	first.SetConversationID("agy", "cli", "conv-1")

	second := cm.Reset("proj", "")
	if second.ID == first.ID {
		t.Error("после сброса ожидали новую сессию")
	}
	if second.TurnsCount() != 0 {
		t.Error("новая сессия должна быть пустой")
	}
	if second.ConversationIDFor("agy", "cli") != "" {
		t.Error("новая сессия не должна наследовать идентификатор сессии агента")
	}
	if second.Model != "model" {
		t.Errorf("модель должна сохраниться: %q", second.Model)
	}
}

func TestChatManagerPersistsAndReloads(t *testing.T) {
	st := newChatTestStorage(t)

	cm := NewChatManager()
	cm.InitWithStorage(st)
	cs := cm.GetOrCreate("proj", "gemini-3.1-pro-high")
	cs.AppendTurn(ChatRoleUser, "как работает роутер?")
	cs.AppendTurn(ChatRoleAssistant, "он разбирает сообщения")
	cs.SetConversationID("agy", "cli", "conv-cli")
	cs.SetConversationID("claude", "api", "conv-api")

	// Перезапуск бота: новый менеджер поверх того же хранилища.
	restored := NewChatManager()
	restored.InitWithStorage(st)

	session := restored.Get("proj")
	if session == nil {
		t.Fatal("разговор не восстановился")
	}
	if session.TurnsCount() != 2 {
		t.Errorf("восстановлено %d реплик, ожидали 2", session.TurnsCount())
	}
	if session.ConversationIDFor("agy", "cli") != "conv-cli" {
		t.Errorf("не восстановлен id сессии agy/cli: %q", session.ConversationIDFor("agy", "cli"))
	}
	if session.ConversationIDFor("claude", "api") != "conv-api" {
		t.Errorf("не восстановлен id сессии claude/api: %q", session.ConversationIDFor("claude", "api"))
	}
	if session.History[0].Role != ChatRoleUser || session.History[1].Role != ChatRoleAssistant {
		t.Errorf("нарушены роли реплик: %+v", session.History)
	}
}

func TestChatManagerSessionsAreIsolatedPerProject(t *testing.T) {
	cm := NewChatManager()
	cm.InitWithStorage(newChatTestStorage(t))

	a := cm.GetOrCreate("proj-a", "model")
	b := cm.GetOrCreate("proj-b", "model")
	if a.ID == b.ID {
		t.Fatal("у проектов должны быть разные сессии")
	}

	a.AppendTurn(ChatRoleUser, "вопрос про проект A")
	if b.TurnsCount() != 0 {
		t.Error("история проектов не должна смешиваться")
	}
	if cm.Get("proj-a").TurnsCount() != 1 {
		t.Error("ожидали одну реплику в разговоре проекта A")
	}
}

func TestChatConversationKeyNormalizes(t *testing.T) {
	if got := ChatConversationKey(" AGY ", " CLI "); got != "agy/cli" {
		t.Errorf("ChatConversationKey = %q", got)
	}
	if got := ChatConversationKey("", ""); got != "agy/cli" {
		t.Errorf("значения по умолчанию = %q", got)
	}
}
