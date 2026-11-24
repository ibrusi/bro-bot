package acp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"bro-bot/internal/config"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"
)

const (
	maxMCPHTTPBodyBytes   = 4 << 20 // 4 MiB
	httpReadHeaderTimeout = 5 * time.Second
	httpShutdownTimeout   = 1 * time.Second
)

// MCPEnv описывает переменную окружения для stdio MCP-сервера.
type MCPEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// HTTPHeader описывает заголовок для HTTP MCP-сервера в ACP session/new.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MCPServer описывает MCP-сервер в параметрах session/new и session/resume протокола ACP.
type MCPServer struct {
	Type    string       `json:"type,omitempty"`
	Name    string       `json:"name"`
	Command string       `json:"command,omitempty"`
	Args    []string     `json:"args,omitempty"`
	Env     []MCPEnv     `json:"env,omitempty"`
	URL     string       `json:"url,omitempty"`
	Headers []HTTPHeader `json:"headers,omitempty"`
}

// ToolServer обслуживает инструменты бота для одного хода ACP-агента напрямую
// в памяти процесса по локальному HTTP (127.0.0.1:0) с одноразовым Bearer-токеном.
type ToolServer struct {
	listener net.Listener
	server   *http.Server
	token    string
	url      string
}

// StartToolServer запускает эфемерный HTTP MCP-сервер, маршрутизирующий вызовы
// инструментов агента напрямую в handler без запуска подпроцесса mcp-serve.
func StartToolServer(handler ports.BotToolHandler, token string) (*ToolServer, error) {
	if handler == nil {
		return nil, errors.New("acp toolserver: nil handler")
	}
	tok := strings.TrimSpace(token)
	if tok == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, fmt.Errorf("acp toolserver: generate token: %w", err)
		}
		tok = hex.EncodeToString(raw[:])
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("acp toolserver: listen: %w", err)
	}

	mcpSrv := mcp.NewServer(mcp.NewInProcessTools(handler))
	expectedAuth := "Bearer " + tok

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		gotAuth := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(gotAuth), []byte(expectedAuth)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			// По спецификации MCP Streamable HTTP клиенты шлют DELETE /mcp при закрытии сессии.
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodPost:
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPHTTPBodyBytes))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resp := mcpSrv.HandleLine(r.Context(), body)
		if resp == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp)
	})

	httpSrv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}
	go func() { _ = httpSrv.Serve(ln) }()

	return &ToolServer{
		listener: ln,
		server:   httpSrv,
		token:    tok,
		url:      fmt.Sprintf("http://%s/mcp", ln.Addr().String()),
	}, nil
}

// ServerConfig возвращает описание HTTP MCP-сервера для передачи в session/new.mcpServers.
func (s *ToolServer) ServerConfig() MCPServer {
	return MCPServer{
		Type: "http",
		Name: "bro_bot",
		URL:  s.url,
		Headers: []HTTPHeader{
			{Name: "Authorization", Value: "Bearer " + s.token},
		},
	}
}

// URL возвращает адрес локального HTTP-эндпоинта /mcp.
func (s *ToolServer) URL() string {
	if s == nil {
		return ""
	}
	return s.url
}

// Close выполняет graceful Shutdown с коротким таймаутом и fallback на принудительное закрытие.
func (s *ToolServer) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		return s.server.Close()
	}
	return nil
}

// BroBotStdioServer возвращает резервную stdio-конфигурацию MCP-сервера для случаев,
// когда BotTools не передан (например, в изолированных вызовах без обработчика).
func BroBotStdioServer(channelToken string) MCPServer {
	args := []string{"mcp-serve"}
	if socket := strings.TrimSpace(config.MCPSocketPath); socket != "" {
		args = append(args, "--socket", socket)
	}
	env := []MCPEnv{}
	if tok := strings.TrimSpace(channelToken); tok != "" {
		args = append(args, "--token", tok)
		env = append(env, MCPEnv{Name: mcp.ChannelTokenEnv, Value: tok})
	}
	return MCPServer{
		Name:    "bro_bot",
		Command: mcp.GetBotExecutable(),
		Args:    args,
		Env:     env,
	}
}
