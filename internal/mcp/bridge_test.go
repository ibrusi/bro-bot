package mcp

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortSocketPath возвращает путь сокета в новом временном каталоге. Каталог
// создаётся через os.MkdirTemp, а не t.TempDir: длина пути Unix-сокета ограничена
// ~108 байтами, а t.TempDir включает в путь имя теста.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "sock", "bridge.sock")
}

func TestBridgeRoundTripReportsCallerPID(t *testing.T) {
	socket := shortSocketPath(t)
	var gotPID int
	bridge, err := ListenBridge(socket, func(_ context.Context, pid int, call BridgeCall) BridgeResult {
		gotPID = pid
		return BridgeResult{Text: "ok:" + call.Tool + ":" + call.Arguments["status"].(string)}
	})
	if err != nil {
		t.Fatalf("ListenBridge: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })

	res, err := CallBridge(context.Background(), socket, BridgeCall{
		Tool:      ToolReportProgress,
		Arguments: map[string]interface{}{"status": "сборка"},
	})
	if err != nil {
		t.Fatalf("CallBridge: %v", err)
	}
	if res.IsError || res.Text != "ok:report_progress:сборка" {
		t.Errorf("result = %+v", res)
	}
	// Клиент в том же процессе: ядро должно сообщить PID теста.
	if gotPID != os.Getpid() {
		t.Errorf("peer PID = %d, want %d", gotPID, os.Getpid())
	}

	info, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket dir perm = %o, want 700", perm)
	}
}

func TestBridgeRejectsMalformedRequest(t *testing.T) {
	socket := shortSocketPath(t)
	bridge, err := ListenBridge(socket, func(context.Context, int, BridgeCall) BridgeResult {
		t.Error("handler must not be called for a malformed request")
		return BridgeResult{}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("{not json}\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), `"is_error":true`) {
		t.Errorf("response = %s", buf[:n])
	}
}

func TestListenBridgeReplacesStaleSocketButNotLiveOne(t *testing.T) {
	socket := shortSocketPath(t)
	handler := func(context.Context, int, BridgeCall) BridgeResult { return BridgeResult{Text: "ok"} }

	first, err := ListenBridge(socket, handler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ListenBridge(socket, handler); !errors.Is(err, ErrBridgeInUse) {
		t.Errorf("second listener on a live socket: %v, want ErrBridgeInUse", err)
	}
	_ = first.Close()

	// Сокет, оставшийся от упавшего процесса: файл есть, никто не слушает.
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()

	second, err := ListenBridge(socket, handler)
	if err != nil {
		t.Fatalf("stale socket was not replaced: %v", err)
	}
	_ = second.Close()
}

func TestListenBridgeKeepsForeignFile(t *testing.T) {
	socket := shortSocketPath(t)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenBridge(socket, nil); err == nil {
		t.Fatal("ListenBridge must refuse to replace a regular file")
	}
	if data, _ := os.ReadFile(socket); string(data) != "data" {
		t.Error("foreign file was modified")
	}
}
