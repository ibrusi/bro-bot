package cliproc

import (
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
)

// PTYReader оборачивает чтение из псевдотерминала и транслирует системную ошибку
// EIO (input/output error на Linux при закрытии slave-стороны процесса) в io.EOF.
type PTYReader struct {
	r   io.Reader
	eof bool
}

// NewPTYReader создаёт новый читатель с фильтрацией признака конца терминала.
func NewPTYReader(r io.Reader) *PTYReader {
	return &PTYReader{r: r}
}

// Read выполняет чтение и подменяет EIO на EOF.
func (pr *PTYReader) Read(p []byte) (int, error) {
	if pr.eof {
		return 0, io.EOF
	}
	n, err := pr.r.Read(p)
	if err != nil && IsPTYEOF(err) {
		pr.eof = true
		return n, io.EOF
	}
	return n, err
}

// IsPTYEOF проверяет, является ли ошибка индикатором закрытия псевдотерминала (EOF).
// На Linux закрытие slave-стороны дочерним процессом приводит к возврату syscall.EIO
// при чтении из master-дескриптора (/dev/ptmx). Для сканеров и потоков это нормальный EOF.
func IsPTYEOF(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		if pathErr.Err == syscall.EIO || errors.Is(pathErr.Err, syscall.EIO) {
			return true
		}
	}
	if errors.Is(err, syscall.EIO) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "input/output error") && (strings.Contains(s, "ptmx") || strings.Contains(s, "pts") || strings.Contains(s, "pty"))
}
