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
	PlanMode       bool
}

// GetCurrentAgent возвращает имя активного CLI агента (по умолчанию agy).
func (ps *ProjectState) GetCurrentAgent() string {
	ps.RLock()
	defer ps.RUnlock()
	if ps.CurrentAgent == "" {
		return "agy"
	}
	return ps.CurrentAgent
}

// SetCurrentAgent обновляет имя активного CLI агента.
func (ps *ProjectState) SetCurrentAgent(agent string) {
	ps.Lock()
	defer ps.Unlock()
	ps.CurrentAgent = agent
}

