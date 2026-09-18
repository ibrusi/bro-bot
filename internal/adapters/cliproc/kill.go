// Package cliproc содержит общие операции над процессами CLI-агентов.
package cliproc

import (
	"os/exec"
	"syscall"
)

// KillGroup шлёт SIGKILL всей группе процессов команды, чтобы вместе с агентом
// умерли и его дочерние процессы (git, node и т. п.), иначе они остаются сиротами.
//
// Рассчитан на команды, запущенные через pty.Start: он выставляет Setsid, поэтому
// агент становится лидером своей группы, и её идентификатор равен PID. Если группы
// нет (команда запущена иначе), откатывается на завершение одного процесса.
func KillGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// PID возвращает идентификатор процесса команды или 0, если он ещё не запущен.
func PID(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}
