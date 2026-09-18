package handlers

import (
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
)

// TestCancelStopsAgentForEveryAgentAndMode — /cancel обязан остановить агента в любом
// из четырёх сочетаний агент × режим. До этой правки домен убивал процесс через
// *exec.Cmd, которого у API-адаптеров нет: задача помечалась отменённой, а запрос к
// модели продолжал выполняться и тратить токены. «Висящий» мок воспроизводит именно
// это: его поток не заканчивается, пока процесс не остановят.
func TestCancelStopsAgentForEveryAgentAndMode(t *testing.T) {
	cases := []struct {
		agent string
		mode  string
	}{
		{"agy", "cli"},
		{"agy", "api"},
		{"claude", "cli"},
		{"claude", "api"},
	}

	for _, tc := range cases {
		t.Run(tc.agent+"/"+tc.mode, func(t *testing.T) {
			mt, agent := setupMatrixApp(t, tc.agent, tc.mode, tc.agent+"-"+tc.mode+"-cancel", "")
			agent.Hang = true

			newHandler := mt.commands["new"]
			if err := newHandler(adminSession(mt, &mock.Session{ArgsVal: []string{"долгая", "задача"}})); err != nil {
				t.Fatalf("/new вернула ошибку: %v", err)
			}

			task := domain.GlobalTaskManager.GetActiveTask()
			if task == nil {
				t.Fatal("после /new нет активной задачи")
			}

			// Пайплайн запускается в фоне: дожидаемся, пока процесс будет привязан к задаче.
			waitFor(t, "привязку процесса к задаче", task.HasLiveProcess)
			if agent.Stopped() {
				t.Fatal("процесс остановился до /cancel — мок не висит")
			}

			cancelHandler := mt.commands["cancel"]
			if err := cancelHandler(adminSession(mt, &mock.Session{})); err != nil {
				t.Fatalf("/cancel вернула ошибку: %v", err)
			}

			// Главное свойство: агент действительно остановлен, а не только помечен.
			waitFor(t, "остановку агента после /cancel", agent.Stopped)
			if !agent.Killed() {
				t.Error("у процесса агента не вызван Kill")
			}
			waitFor(t, "завершение пайплайна", func() bool { return !task.HasLiveProcess() })

			task.Lock()
			status := task.Status
			task.Unlock()
			if status != domain.TaskStatusCancelled {
				t.Errorf("статус задачи = %q, ожидали %q", status, domain.TaskStatusCancelled)
			}
		})
	}
}

// TestCancelBeforeProcessAttached — /cancel мог прийти между запуском процесса и его
// привязкой к задаче; AttachProcess тогда отказывает, и пайплайн гасит процесс сам.
func TestCancelBeforeProcessAttached(t *testing.T) {
	tm := domain.NewTaskManager()
	task := tm.CreateTask("test-proj", "flash", "гонка с отменой", testChatID)

	task.Lock()
	task.Status = domain.TaskStatusRunning
	task.Unlock()

	if _, err := tm.CancelTask(task.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	agent := &mock.AgentFramework{Hang: true}
	proc, err := agent.ExecuteTask(t.Context(), ports.ExecuteArgs{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if task.AttachProcess(proc, nil) {
		t.Fatal("AttachProcess на отменённой задаче должен вернуть false")
	}
	if task.HasLiveProcess() {
		t.Error("у отменённой задачи не должно появиться живого процесса")
	}
	_ = proc.Kill()
}
