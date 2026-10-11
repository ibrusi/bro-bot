package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Мост между `bot mcp-serve` и работающим ботом.
//
// MCP-сервер запускает сам агент (claude, agy) как свой дочерний процесс, поэтому
// у сервера нет ни мессенджера, ни задач — всё это живёт в процессе бота. Сервер
// пересылает каждый вызов инструмента боту через Unix-сокет и отдаёт агенту
// настоящий результат. Формат — один JSON-объект запроса и один JSON-объект ответа
// на соединение.
//
// Задачу по вызову бот определяет сам: ядро сообщает PID процесса на той стороне
// сокета (SO_PEERCRED), и бот поднимается по его предкам до процесса агента,
// которого запустил. Подделать чужую задачу из вызова поэтому нельзя.

// BridgeCall — вызов инструмента, пересылаемый боту.
type BridgeCall struct {
	Tool      string                 `json:"tool"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// BridgeResult — результат вызова, который получит агент.
type BridgeResult struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
}

// BridgeHandler обрабатывает вызов процесса peerPID.
type BridgeHandler func(ctx context.Context, peerPID int, call BridgeCall) BridgeResult

const (
	// bridgeIOTimeout ограничивает обмен по одному соединению. Инструменты моста
	// не ждут человека, поэтому времени с запасом хватает на любой вызов.
	bridgeIOTimeout = 30 * time.Second
	// bridgeMaxMessage — предел размера одного сообщения в байтах.
	bridgeMaxMessage = 1 << 20
)

// ErrBridgeInUse — сокет уже слушает другой живой процесс бота.
var ErrBridgeInUse = errors.New("mcp bridge: socket is already in use by another process")

// BridgeServer принимает вызовы от процессов `bot mcp-serve`.
type BridgeServer struct {
	ln      *net.UnixListener
	handler BridgeHandler
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// ListenBridge начинает слушать сокет path. Каталог сокета создаётся с правами
// 0700: подключиться может только владелец, независимо от umask, с которой
// создан сам сокет. Проверка UID собеседника — второй рубеж той же защиты.
func ListenBridge(path string, handler BridgeHandler) (*BridgeServer, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mcp bridge: create socket dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mcp bridge: restrict socket dir: %w", err)
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}

	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("mcp bridge: listen: %w", err)
	}
	ln.SetUnlinkOnClose(true)

	ctx, cancel := context.WithCancel(context.Background())
	s := &BridgeServer{ln: ln, handler: handler, ctx: ctx, cancel: cancel}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Close перестаёт принимать вызовы, дожидается текущих и удаляет сокет.
func (s *BridgeServer) Close() error {
	s.cancel()
	err := s.ln.Close()
	s.wg.Wait()
	return err
}

func (s *BridgeServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *BridgeServer) handle(conn *net.UnixConn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(bridgeIOTimeout))

	pid, uid, err := peerCredentials(conn)
	if err != nil {
		writeBridgeResult(conn, BridgeResult{Text: "bro-bot: cannot identify the caller: " + err.Error(), IsError: true})
		return
	}
	if uid != os.Getuid() {
		writeBridgeResult(conn, BridgeResult{Text: "bro-bot: caller belongs to another user", IsError: true})
		return
	}

	var call BridgeCall
	if err := json.NewDecoder(io.LimitReader(conn, bridgeMaxMessage)).Decode(&call); err != nil {
		writeBridgeResult(conn, BridgeResult{Text: "bro-bot: malformed request: " + err.Error(), IsError: true})
		return
	}
	writeBridgeResult(conn, s.handler(s.ctx, pid, call))
}

func writeBridgeResult(w io.Writer, res BridgeResult) {
	_ = json.NewEncoder(w).Encode(res)
}

// CallBridge отправляет вызов боту по сокету path и возвращает его результат.
func CallBridge(ctx context.Context, path string, call BridgeCall) (BridgeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, bridgeIOTimeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return BridgeResult{}, fmt.Errorf("mcp bridge: bro-bot is not reachable at %s: %w", path, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if err := json.NewEncoder(conn).Encode(call); err != nil {
		return BridgeResult{}, fmt.Errorf("mcp bridge: send: %w", err)
	}
	var res BridgeResult
	if err := json.NewDecoder(io.LimitReader(conn, bridgeMaxMessage)).Decode(&res); err != nil {
		return BridgeResult{}, fmt.Errorf("mcp bridge: receive: %w", err)
	}
	return res, nil
}

// removeStaleSocket убирает сокет, оставшийся от упавшего процесса. Живой сокет
// означает второй экземпляр бота — его не трогаем. Не-сокет на этом месте тоже
// не удаляем: это чужой файл.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mcp bridge: stat socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("mcp bridge: %s exists and is not a socket", path)
	}
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = conn.Close()
		return ErrBridgeInUse
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("mcp bridge: remove stale socket: %w", err)
	}
	return nil
}
