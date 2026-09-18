package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	// Hang — процесс отдаёт событие system и затем «работает» бесконечно: поток
	// не завершается, пока его не остановят через Kill или отмену контекста. Нужен
	// для проверки /cancel: обычный мок заканчивается раньше, чем его успеют отменить.
	Hang bool

	calls   []ports.ExecuteArgs
	killed  bool
	stopped bool
}

// ExecuteTask имитирует запуск агента и возвращает процесс с готовым потоком событий.
func (f *AgentFramework) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	startErr := f.StartErr
	hang := f.Hang
	f.mu.Unlock()

	if startErr != nil {
		return nil, startErr
	}
	if hang {
		return f.newHangingProcess(ctx), nil
	}
	return &AgentProcess{reader: strings.NewReader(f.buildStream()), waitErr: f.WaitErr}, nil
}

// Killed сообщает, вызывали ли у последнего процесса Kill.
func (f *AgentFramework) Killed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

// Stopped сообщает, завершился ли «висящий» процесс — через Kill или отмену контекста.
func (f *AgentFramework) Stopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// newHangingProcess собирает процесс, который пишет событие system и затем держит поток
// открытым до Kill или отмены ctx — так же ведёт себя настоящий API-стрим.
func (f *AgentFramework) newHangingProcess(ctx context.Context) *AgentProcess {
	f.mu.Lock()
	convID := f.ConversationID
	f.mu.Unlock()
	if convID == "" {
		convID = "mock-conversation"
	}

	pr, pw := io.Pipe()
	killCh := make(chan struct{})
	done := make(chan struct{})

	var once sync.Once
	kill := func() {
		once.Do(func() {
			f.mu.Lock()
			f.killed = true
			f.mu.Unlock()
			close(killCh)
		})
	}

	go func() {
		defer close(done)
		var bldr strings.Builder
		writeEvent(&bldr, map[string]interface{}{"type": "system", "session_id": convID})
		_, _ = io.WriteString(pw, bldr.String())

		select {
		case <-killCh:
		case <-ctx.Done():
		}

		f.mu.Lock()
		f.stopped = true
		f.mu.Unlock()
		_ = pw.Close()
	}()

	return &AgentProcess{
		reader:  pr,
		waitErr: f.WaitErr,
		kill:    kill,
		done:    done,
	}
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
// У «висящего» процесса kill и done заполнены, у обычного — nil.
type AgentProcess struct {
	reader  io.Reader
	waitErr error
	kill    func()
	done    chan struct{}
}

func (p *AgentProcess) Stdout() io.Reader { return p.reader }

func (p *AgentProcess) Stdin() io.WriteCloser { return nopWriteCloser{} }

// Wait у висящего процесса дожидается его остановки, как настоящий Wait.
func (p *AgentProcess) Wait() error {
	if p.done != nil {
		<-p.done
	}
	return p.waitErr
}

func (p *AgentProcess) Kill() error {
	if p.kill != nil {
		p.kill()
	}
	return nil
}

// Close ничего не делает: висящий процесс останавливается только через Kill или
// отмену контекста, иначе Killed() перестал бы отличать /cancel от обычного завершения.
func (p *AgentProcess) Close() error { return nil }

// PID — у мок-процесса нет процесса в ОС.
func (p *AgentProcess) PID() int { return 0 }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(b []byte) (int, error) { return len(b), nil }

func (nopWriteCloser) Close() error { return nil }
