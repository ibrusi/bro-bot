package domain

import (
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type AgentSession struct {
	sync.Mutex
	Cmd              *exec.Cmd
	Stdin            io.WriteCloser
	IsRunning        bool
	Waiting          bool
	StartedAt        time.Time
	CurrentPrompt    string
	CurrentProject   string
	RecentLogs       []string
	PendingFollowups []string
	LastPRURL        string
	FullOutput       strings.Builder
	LastModelUsed    string
	LastTokensUsed   string
}

type ProjectState struct {
	sync.RWMutex
	CurrentProject string
	CurrentModel   string
	CurrentAgent   string
	ExecutionMode  string // "cli" или "api"
	PlanMode       bool
}

// GetCurrentAgent возвращает имя активного агента (по умолчанию agy).
func (ps *ProjectState) GetCurrentAgent() string {
	ps.RLock()
	defer ps.RUnlock()
	if ps.CurrentAgent == "" {
		return "agy"
	}
	return ps.CurrentAgent
}

// SetCurrentAgent обновляет имя активного агента.
func (ps *ProjectState) SetCurrentAgent(agent string) {
	ps.Lock()
	defer ps.Unlock()
	ps.CurrentAgent = agent
}

// GetExecutionMode возвращает текущий режим выполнения ("cli" или "api", по умолчанию "cli").
func (ps *ProjectState) GetExecutionMode() string {
	ps.RLock()
	defer ps.RUnlock()
	if ps.ExecutionMode == "" {
		return "cli"
	}
	return ps.ExecutionMode
}

// SetExecutionMode обновляет режим выполнения.
func (ps *ProjectState) SetExecutionMode(mode string) {
	ps.Lock()
	defer ps.Unlock()
	if strings.ToLower(mode) == "api" {
		ps.ExecutionMode = "api"
	} else {
		ps.ExecutionMode = "cli"
	}
}
