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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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

// Command собирает команду запуска скрипта по абсолютному пути path. Путь уже
// должен быть проверен вызывающим (utils.SafeJoin), здесь решается только способ
// запуска. Рабочий каталог — каталог скрипта, чтобы относительные пути в нём
// не зависели от того, откуда запущен бот.
func Command(ctx context.Context, path string) (*exec.Cmd, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRunnable
	}

	var cmd *exec.Cmd
	if isExecutable(info) {
		cmd = exec.CommandContext(ctx, path)
	} else {
		interpreter, ok := interpreters[strings.ToLower(filepath.Ext(path))]
		if !ok {
			return nil, ErrNotRunnable
		}
		cmd = exec.CommandContext(ctx, interpreter, path)
	}
	cmd.Dir = filepath.Dir(path)
	return cmd, nil
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

// runnable сообщает, сможет ли Command запустить файл. os.Stat идёт по
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
