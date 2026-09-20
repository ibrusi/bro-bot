package cliproc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"
)

type errReader struct {
	data string
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

func TestIsPTYEOF(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"io.EOF", io.EOF, true},
		{"syscall.EIO", syscall.EIO, true},
		{
			"os.PathError EIO",
			&os.PathError{Op: "read", Path: "/dev/ptmx", Err: syscall.EIO},
			true,
		},
		{
			"wrapped path error",
			fmt.Errorf("wrapper: %w", &os.PathError{Op: "read", Path: "/dev/ptmx", Err: syscall.EIO}),
			true,
		},
		{
			"string match read /dev/ptmx input/output error",
			errors.New("read /dev/ptmx: input/output error"),
			true,
		},
		{
			"regular io error",
			errors.New("connection reset by peer"),
			false,
		},
		{
			"permission denied",
			os.ErrPermission,
			false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsPTYEOF(tc.err)
			if got != tc.expected {
				t.Errorf("IsPTYEOF(%v) = %v, expected %v", tc.err, got, tc.expected)
			}
		})
	}
}

func TestPTYReader_TranslatesEIO(t *testing.T) {
	eio := &os.PathError{Op: "read", Path: "/dev/ptmx", Err: syscall.EIO}
	inner := &errReader{
		data: "hello world\n",
		err:  eio,
	}

	reader := NewPTYReader(inner)
	buf, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("io.ReadAll returned unexpected error: %v", err)
	}
	if string(buf) != "hello world\n" {
		t.Errorf("got %q, want 'hello world\\n'", string(buf))
	}
}

func TestPTYReader_PassesThroughOtherErrors(t *testing.T) {
	customErr := errors.New("critical disk failure")
	inner := &errReader{
		data: "some data",
		err:  customErr,
	}

	reader := NewPTYReader(inner)
	_, err := io.ReadAll(reader)
	if !errors.Is(err, customErr) {
		t.Errorf("expected customErr, got %v", err)
	}
}
