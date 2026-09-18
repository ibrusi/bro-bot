package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"bro-bot/internal/ports"
)

// AgentFramework — мок агента для тестов. Записывает полученные аргументы запуска
// и отдаёт заранее заданный поток событий stream-json, одинаковый для всех агентов и режимов.
type AgentFramework struct {
	mu sync.Mutex

	// Name — имя агента, попадает в ошибки и удобно для отладки тестов.
	Name string
	// ConversationID — идентификатор сессии, который мок сообщает в событии system.
	ConversationID string
	// Response — текст ответа агента.
	Response string
	// ExtraEvents — дополнительные строки stream-json, отдаваемые перед финальным результатом.
	ExtraEvents []string
	// ModelsOutput — ответ GetModels в формате "id Отображаемое имя".
	ModelsOutput string
	// StartErr — ошибка запуска процесса агента.
	StartErr error
	// WaitErr — ошибка завершения процесса агента.
	WaitErr error

	calls []ports.ExecuteArgs
}

// ExecuteTask имитирует запуск агента и возвращает процесс с готовым потоком событий.
func (f *AgentFramework) ExecuteTask(_ context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	startErr := f.StartErr
	f.mu.Unlock()

	if startErr != nil {
		return nil, startErr
	}
	return &AgentProcess{reader: strings.NewReader(f.buildStream()), waitErr: f.WaitErr}, nil
}

// Calls возвращает аргументы всех запусков агента.
func (f *AgentFramework) Calls() []ports.ExecuteArgs {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ports.ExecuteArgs(nil), f.calls...)
}

// LastCall возвращает аргументы последнего запуска агента или nil.
func (f *AgentFramework) LastCall() *ports.ExecuteArgs {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	last := f.calls[len(f.calls)-1]
	return &last
}

// Reset очищает историю вызовов.
func (f *AgentFramework) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *AgentFramework) buildStream() string {
	f.mu.Lock()
	convID := f.ConversationID
	response := f.Response
	extra := append([]string(nil), f.ExtraEvents...)
	f.mu.Unlock()

	if convID == "" {
		convID = "mock-conversation"
	}

	var bldr strings.Builder
	writeEvent(&bldr, map[string]interface{}{"type": "system", "session_id": convID})
	for _, line := range extra {
		bldr.WriteString(line)
		bldr.WriteString("\n")
	}
	if response != "" {
		writeEvent(&bldr, map[string]interface{}{
			"type":       "assistant",
			"session_id": convID,
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": []map[string]interface{}{{"type": "text", "text": response}},
			},
		})
	}
	writeEvent(&bldr, map[string]interface{}{
		"type":       "result",
		"session_id": convID,
		"is_error":   false,
		"result":     response,
	})
	return bldr.String()
}

func writeEvent(bldr *strings.Builder, evt map[string]interface{}) {
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	bldr.Write(data)
	bldr.WriteString("\n")
}

func (f *AgentFramework) GetModels(_ context.Context) ([]byte, error) {
	if f.ModelsOutput == "" {
		return []byte("mock-model Mock Model\n"), nil
	}
	return []byte(f.ModelsOutput), nil
}

func (f *AgentFramework) GetQuota(_ context.Context) ([]byte, error) {
	return []byte(`{"status":"SUCCESS","response":"mock quota"}`), nil
}

func (f *AgentFramework) GetQuotaText(_ context.Context) ([]byte, error) {
	return []byte(fmt.Sprintf("mock quota (%s)", f.Name)), nil
}

func (f *AgentFramework) GetCredits(_ context.Context) ([]byte, error) {
	return []byte(`{}`), nil
}

// AgentProcess — процесс мок-агента: читает заранее подготовленный поток событий.
type AgentProcess struct {
	reader  io.Reader
	waitErr error
}

func (p *AgentProcess) Stdout() io.Reader { return p.reader }

func (p *AgentProcess) Stdin() io.WriteCloser { return nopWriteCloser{} }

func (p *AgentProcess) Wait() error { return p.waitErr }

func (p *AgentProcess) Kill() error { return nil }

func (p *AgentProcess) Close() error { return nil }

func (p *AgentProcess) GetCmd() *exec.Cmd { return nil }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(b []byte) (int, error) { return len(b), nil }

func (nopWriteCloser) Close() error { return nil }
