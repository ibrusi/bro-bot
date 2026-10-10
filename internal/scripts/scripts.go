// Package scripts находит и запускает пользовательские скрипты из SCRIPTS_DIR.
//
// Скрипт запускается одним из двух способов:
//   - исполняемый файл (бит x) — напрямую, интерпретатор выбирает его shebang;
//   - неисполняемый файл — интерпретатором, который сопоставлен его расширению.
//
// Так в каталог можно положить и chmod +x скрипт на любом языке, и обычный
// .py или .sh файл без прав на исполнение.
package scripts

import (
	"bro-bot/internal/utils"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ErrNotRunnable — файл не исполняемый, и для его расширения нет интерпретатора.
var ErrNotRunnable = errors.New("scripts: file is not executable and its extension has no interpreter")

// interpreters сопоставляет расширение неисполняемого скрипта с командой запуска.
var interpreters = map[string]string{
	".sh":   "bash",
	".bash": "bash",
	".py":   "python3",
	".js":   "node",
	".rb":   "ruby",
	".pl":   "perl",
	".php":  "php",
}

const (
	// killGrace — сколько ждать завершения после SIGTERM, прежде чем добить группу
	// процессов SIGKILL. Скрипту хватает этого, чтобы убрать за собой.
	killGrace = 5 * time.Second
	// pipeWaitDelay ограничивает ожидание закрытия вывода после выхода скрипта:
	// фоновый внук, унаследовавший stdout, иначе держал бы Run бесконечно.
	pipeWaitDelay = 2 * time.Second
)

// Extensions возвращает отсортированный список расширений, которые запускаются
// без бита исполнения.
func Extensions() []string {
	exts := make([]string, 0, len(interpreters))
	for ext := range interpreters {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	return exts
}

// Script — подготовленный к запуску скрипт. Run вызывается не больше одного раза.
type Script struct {
	cmd *exec.Cmd
}

// Prepare выбирает способ запуска скрипта по абсолютному пути path и передаёт ему
// аргументы args. Путь уже должен быть проверен вызывающим (utils.SafeJoin).
// Аргументы уходят в exec напрямую, без оболочки, поэтому их содержимое не
// интерпретируется как команды.
func Prepare(path string, args []string) (*Script, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRunnable
	}

	var cmd *exec.Cmd
	if isExecutable(info) {
		cmd = exec.Command(path, args...)
	} else {
		interpreter, ok := interpreters[strings.ToLower(filepath.Ext(path))]
		if !ok {
			return nil, ErrNotRunnable
		}
		cmd = exec.Command(interpreter, append([]string{path}, args...)...)
	}
	// Рабочий каталог — каталог скрипта, чтобы относительные пути в нём не
	// зависели от того, откуда запущен бот.
	cmd.Dir = filepath.Dir(path)
	// Своя группа процессов: остановка должна достать и дочерние процессы скрипта.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = pipeWaitDelay
	return &Script{cmd: cmd}, nil
}

// Run запускает скрипт, пишет объединённый stdout и stderr в out и ждёт завершения.
// Отмена ctx останавливает скрипт: всей группе процессов уходит SIGTERM, а если
// она не завершилась за killGrace — SIGKILL. После отмены возвращается ctx.Err().
func (s *Script) Run(ctx context.Context, out io.Writer) error {
	s.cmd.Stdout = out
	s.cmd.Stderr = out
	if err := s.cmd.Start(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}

	pgid := -s.cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)

	grace := time.NewTimer(killGrace)
	defer grace.Stop()
	select {
	case <-done:
		// Лидер вышел, но его потомки могли пережить SIGTERM — добиваем группу.
		_ = syscall.Kill(pgid, syscall.SIGKILL)
	case <-grace.C:
		_ = syscall.Kill(pgid, syscall.SIGKILL)
		<-done
	}
	return ctx.Err()
}

// List возвращает отсортированные пути скриптов относительно dir — ровно в том
// виде, в каком их принимает /script. Скрытые файлы и каталоги пропускаются,
// как и файлы, которые нельзя запустить или нельзя передать команде из-за
// недопустимых символов в имени.
func List(dir string) ([]string, error) {
	var names []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == dir {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, err := utils.SafeJoin(dir, rel); err != nil {
			return nil
		}
		if runnable(path) {
			names = append(names, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// runnable сообщает, сможет ли Prepare запустить файл. os.Stat идёт по
// симлинкам, поэтому ссылка на скрипт тоже считается скриптом.
func runnable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if isExecutable(info) {
		return true
	}
	_, ok := interpreters[strings.ToLower(filepath.Ext(path))]
	return ok
}

func isExecutable(info fs.FileInfo) bool {
	return info.Mode().Perm()&0o111 != 0
}
