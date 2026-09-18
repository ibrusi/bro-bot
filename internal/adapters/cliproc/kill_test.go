package cliproc

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// processGone сообщает, что процесса больше нет: сигнал 0 не доставляется либо
// процесс уже зомби и ждёт лишь, когда его подберёт init.
func processGone(pid int) bool {
	if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
		return true
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	// Поле состояния идёт после закрывающей скобки имени процесса.
	if idx := strings.LastIndex(string(data), ")"); idx != -1 && idx+2 < len(data) {
		return data[idx+2] == 'Z'
	}
	return false
}

// TestKillGroupTerminatesChildren — главное свойство: вместе с лидером группы умирают
// и его дети. Раньше Kill CLI-адаптеров бил только по лидеру, и внуки (git, node)
// оставались сиротами.
func TestKillGroupTerminatesChildren(t *testing.T) {
	// Дочерний sleep печатает свой PID, чтобы после Kill проверить именно его.
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $!; wait")
	// Та же настройка, что делает pty.Start в адаптерах.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("запуск sh: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("не прочитали PID дочернего процесса: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("PID дочернего процесса не число: %q", line)
	}
	if processGone(childPID) {
		t.Fatalf("дочерний процесс %d не запустился", childPID)
	}

	if err := KillGroup(cmd); err != nil {
		t.Fatalf("KillGroup: %v", err)
	}
	_ = cmd.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for !processGone(childPID) {
		if time.Now().After(deadline) {
			t.Fatalf("дочерний процесс %d пережил убийство группы", childPID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestKillGroupWithoutProcess(t *testing.T) {
	if err := KillGroup(nil); err != nil {
		t.Errorf("nil-команда: %v", err)
	}
	if err := KillGroup(exec.Command("true")); err != nil {
		t.Errorf("незапущенная команда: %v", err)
	}
	if got := PID(nil); got != 0 {
		t.Errorf("PID(nil) = %d", got)
	}
	if got := PID(exec.Command("true")); got != 0 {
		t.Errorf("PID незапущенной команды = %d", got)
	}
}
