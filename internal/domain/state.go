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
	PlanMode       bool
}
