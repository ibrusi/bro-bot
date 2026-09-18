package domain

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"bro-bot/internal/storage"
	"bro-bot/internal/utils"
)

// Ограничения истории разговора: держим контекст полезным, а базу — компактной.
const (
	// MaxChatHistoryTurns — сколько последних реплик храним и проигрываем.
	MaxChatHistoryTurns = 40
	// MaxChatTurnRunes — предельная длина одной сохраняемой реплики.
	MaxChatTurnRunes = 8000
	// MaxChatHistoryChars — суммарный бюджет символов истории, передаваемой агенту.
	MaxChatHistoryChars = 60000
)

// ChatRole — роль реплики в разговоре.
type ChatRole string

const (
	ChatRoleUser      ChatRole = "user"
	ChatRoleAssistant ChatRole = "assistant"
)

// ChatTurn — одна реплика разговора.
type ChatTurn struct {
	Role      ChatRole
	Content   string
	CreatedAt time.Time
}

// ChatSession — разговорная сессия одного проекта.
//
// История разговора хранится у нас и не зависит ни от агента, ни от режима выполнения.
// Идентификаторы сессий самих агентов лежат отдельно для каждой пары «агент/режим»:
// сессия agy в cli не имеет смысла для claude или для api-режима.
type ChatSession struct {
	sync.Mutex
	ID         int
	Project    string
	Model      string
	History    []ChatTurn
	Pending    []string
	Running    bool
	StartedAt  time.Time
	LastUsedAt time.Time

	conversations map[string]string
	cancel        context.CancelFunc
	storage       storage.Storage
}

// ChatConversationKey формирует ключ пары «агент/режим» (например "agy/cli").
func ChatConversationKey(agent, mode string) string {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		agent = "agy"
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "cli"
	}
	return agent + "/" + mode
}

// ConversationIDFor возвращает идентификатор сессии агента для пары «агент/режим».
func (cs *ChatSession) ConversationIDFor(agent, mode string) string {
	cs.Lock()
	defer cs.Unlock()
	return cs.conversations[ChatConversationKey(agent, mode)]
}

// SetConversationID сохраняет идентификатор сессии агента для пары «агент/режим».
func (cs *ChatSession) SetConversationID(agent, mode, conversationID string) {
	if conversationID == "" {
		return
	}
	key := ChatConversationKey(agent, mode)

	cs.Lock()
	if cs.conversations[key] == conversationID {
		cs.Unlock()
		return
	}
	cs.conversations[key] = conversationID
	st := cs.storage
	sessionID := cs.ID
	cs.Unlock()

	if st != nil && sessionID > 0 {
		parts := strings.SplitN(key, "/", 2)
		if err := st.SetChatConversationID(context.Background(), sessionID, parts[0], parts[1], conversationID); err != nil {
			log.Printf("Предупреждение: не удалось сохранить сессию разговора %s: %v", key, err)
		}
	}
}

// NeedsContextBootstrap сообщает, нужно ли подмешать в промпт краткий контекст прошлых реплик.
// Это требуется, когда агент в cli-режиме ещё не имеет своей сессии (например, пользователь
// переключил агента или режим посреди разговора), а история у нас уже есть:
// api-режим получает историю напрямую в запросе и в бутстрапе не нуждается.
func (cs *ChatSession) NeedsContextBootstrap(agent, mode string) bool {
	cs.Lock()
	defer cs.Unlock()
	if strings.EqualFold(mode, "api") {
		return false
	}
	return cs.conversations[ChatConversationKey(agent, mode)] == "" && len(cs.History) > 0
}

// BeginTurn помечает начало хода. Возвращает false, если ход уже выполняется.
func (cs *ChatSession) BeginTurn(cancel context.CancelFunc) bool {
	cs.Lock()
	defer cs.Unlock()
	if cs.Running {
		return false
	}
	cs.Running = true
	cs.cancel = cancel
	cs.StartedAt = time.Now()
	return true
}

// EndTurn снимает отметку выполнения хода.
func (cs *ChatSession) EndTurn() {
	cs.Lock()
	defer cs.Unlock()
	cs.Running = false
	cs.cancel = nil
	cs.LastUsedAt = time.Now()
}

// SetTurnCancel заменяет функцию отмены текущего хода (например, при разборе очереди сообщений).
func (cs *ChatSession) SetTurnCancel(cancel context.CancelFunc) {
	cs.Lock()
	defer cs.Unlock()
	if cs.Running {
		cs.cancel = cancel
	}
}

// IsRunning сообщает, выполняется ли сейчас ход разговора.
func (cs *ChatSession) IsRunning() bool {
	cs.Lock()
	defer cs.Unlock()
	return cs.Running
}

// Cancel прерывает текущий ход. Возвращает false, если прерывать нечего.
func (cs *ChatSession) Cancel() bool {
	cs.Lock()
	cancel := cs.cancel
	cs.Pending = nil
	cs.Unlock()

	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// EnqueuePending откладывает сообщение до конца текущего хода и возвращает длину очереди.
func (cs *ChatSession) EnqueuePending(text string) int {
	cs.Lock()
	defer cs.Unlock()
	cs.Pending = append(cs.Pending, text)
	return len(cs.Pending)
}

// DrainPending забирает отложенные сообщения.
func (cs *ChatSession) DrainPending() []string {
	cs.Lock()
	defer cs.Unlock()
	if len(cs.Pending) == 0 {
		return nil
	}
	pending := cs.Pending
	cs.Pending = nil
	return pending
}

// AppendTurn добавляет реплику в историю, обрезает её по лимитам и сохраняет в хранилище.
func (cs *ChatSession) AppendTurn(role ChatRole, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	content = utils.TruncateString(content, MaxChatTurnRunes)

	cs.Lock()
	cs.History = append(cs.History, ChatTurn{Role: role, Content: content, CreatedAt: time.Now()})
	if len(cs.History) > MaxChatHistoryTurns {
		cs.History = append([]ChatTurn(nil), cs.History[len(cs.History)-MaxChatHistoryTurns:]...)
	}
	st := cs.storage
	sessionID := cs.ID
	cs.LastUsedAt = time.Now()
	cs.Unlock()

	if st == nil || sessionID <= 0 {
		return
	}
	ctx := context.Background()
	if err := st.AppendChatMessage(ctx, sessionID, string(role), content); err != nil {
		log.Printf("Предупреждение: не удалось сохранить реплику разговора: %v", err)
		return
	}
	if err := st.TrimChatMessages(ctx, sessionID, MaxChatHistoryTurns); err != nil {
		log.Printf("Предупреждение: не удалось обрезать историю разговора: %v", err)
	}
}

// HistoryForPrompt возвращает последние реплики разговора в пределах лимитов по числу и символам
// (от свежих к старым, результат — в хронологическом порядке).
func (cs *ChatSession) HistoryForPrompt(maxTurns, maxChars int) []ChatTurn {
	cs.Lock()
	defer cs.Unlock()

	if maxTurns <= 0 {
		maxTurns = MaxChatHistoryTurns
	}
	if maxChars <= 0 {
		maxChars = MaxChatHistoryChars
	}

	start := len(cs.History)
	budget := maxChars
	for i := len(cs.History) - 1; i >= 0 && len(cs.History)-i <= maxTurns; i-- {
		cost := len([]rune(cs.History[i].Content))
		if budget-cost < 0 {
			break
		}
		budget -= cost
		start = i
	}

	if start >= len(cs.History) {
		return nil
	}
	return append([]ChatTurn(nil), cs.History[start:]...)
}

// TurnsCount возвращает число реплик в истории разговора.
func (cs *ChatSession) TurnsCount() int {
	cs.Lock()
	defer cs.Unlock()
	return len(cs.History)
}

// ChatManager хранит по одной активной разговорной сессии на проект.
type ChatManager struct {
	sync.RWMutex
	sessions map[string]*ChatSession
	storage  storage.Storage
}

// GlobalChatManager — общий менеджер разговоров бота.
var GlobalChatManager = NewChatManager()

// NewChatManager создаёт менеджер разговоров без постоянного хранилища (в памяти).
func NewChatManager() *ChatManager {
	return &ChatManager{sessions: make(map[string]*ChatSession)}
}

// InitWithStorage подключает хранилище и восстанавливает активные разговоры.
func (cm *ChatManager) InitWithStorage(s storage.Storage) {
	cm.Lock()
	defer cm.Unlock()

	cm.storage = s
	cm.sessions = make(map[string]*ChatSession)
	if s == nil {
		return
	}

	ctx := context.Background()
	records, err := s.ListActiveChatSessions(ctx)
	if err != nil {
		log.Printf("Предупреждение: не удалось загрузить разговоры из SQLite: %v", err)
		return
	}

	for _, rec := range records {
		session := chatRecordToSession(rec, s)
		messages, err := s.GetChatMessages(ctx, rec.ID, MaxChatHistoryTurns)
		if err != nil {
			log.Printf("Предупреждение: не удалось загрузить историю разговора проекта %s: %v", rec.Project, err)
		}
		for _, msg := range messages {
			session.History = append(session.History, ChatTurn{
				Role:      ChatRole(msg.Role),
				Content:   msg.Content,
				CreatedAt: msg.CreatedAt,
			})
		}
		cm.sessions[rec.Project] = session
	}

	if len(cm.sessions) > 0 {
		log.Printf("Восстановлено разговорных сессий из SQLite: %d", len(cm.sessions))
	}
}

func chatRecordToSession(rec *storage.ChatSessionRecord, s storage.Storage) *ChatSession {
	conversations := make(map[string]string, len(rec.Conversations))
	for k, v := range rec.Conversations {
		conversations[k] = v
	}
	return &ChatSession{
		ID:            rec.ID,
		Project:       rec.Project,
		Model:         rec.Model,
		LastUsedAt:    rec.UpdatedAt,
		conversations: conversations,
		storage:       s,
	}
}

// Get возвращает активный разговор проекта или nil.
func (cm *ChatManager) Get(project string) *ChatSession {
	cm.RLock()
	defer cm.RUnlock()
	return cm.sessions[project]
}

// GetOrCreate возвращает активный разговор проекта, создавая его при необходимости.
func (cm *ChatManager) GetOrCreate(project, model string) *ChatSession {
	cm.Lock()
	defer cm.Unlock()

	if session, ok := cm.sessions[project]; ok {
		if model != "" {
			session.Lock()
			session.Model = model
			session.Unlock()
		}
		return session
	}
	return cm.createLocked(project, model)
}

// Reset завершает текущий разговор проекта и начинает новый с чистого листа.
func (cm *ChatManager) Reset(project, model string) *ChatSession {
	cm.Lock()
	defer cm.Unlock()

	if existing, ok := cm.sessions[project]; ok && model == "" {
		existing.Lock()
		model = existing.Model
		existing.Unlock()
	}
	delete(cm.sessions, project)

	if cm.storage != nil {
		if err := cm.storage.DeactivateChatSessions(context.Background(), project); err != nil {
			log.Printf("Предупреждение: не удалось закрыть разговор проекта %s: %v", project, err)
		}
	}
	return cm.createLocked(project, model)
}

// createLocked создаёт новую сессию. Вызывается под удерживаемой блокировкой менеджера.
func (cm *ChatManager) createLocked(project, model string) *ChatSession {
	session := &ChatSession{
		Project:       project,
		Model:         model,
		conversations: make(map[string]string),
		storage:       cm.storage,
		LastUsedAt:    time.Now(),
	}

	if cm.storage != nil {
		id, err := cm.storage.CreateChatSession(context.Background(), project, model)
		if err != nil {
			log.Printf("Предупреждение: не удалось создать разговор проекта %s: %v", project, err)
		} else {
			session.ID = id
		}
	}

	cm.sessions[project] = session
	return session
}
