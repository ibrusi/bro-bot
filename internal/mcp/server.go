package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

const (
	CurrentProtocolVersion = "2024-11-05"
	ServerName             = "bro-bot"
	ServerVersion          = "1.0.0"
)

// Server реализует сервер протокола Model Context Protocol (JSON-RPC 2.0).
type Server struct {
	mu          sync.Mutex
	tools       *ToolRegistry
	initialized bool
}

// NewServer создает новый экземпляр MCP-сервера.
func NewServer(tools *ToolRegistry) *Server {
	if tools == nil {
		tools = NewToolRegistry()
	}
	return &Server{
		tools: tools,
	}
}

// HandleLine обрабатывает одну входящую строку JSON-RPC запроса и возвращает строку ответа (или nil, если ответ не требуется).
func (s *Server) HandleLine(ctx context.Context, line []byte) []byte {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}

	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		errResp := Response{
			JSONRPC: "2.0",
			Error: &RPCError{
				Code:    ParseError,
				Message: fmt.Sprintf("parse error: %v", err),
			},
		}
		res, _ := json.Marshal(errResp)
		return res
	}

	// Обработка нотификаций (запросы без ID)
	if req.ID == nil {
		s.handleNotification(req)
		return nil
	}

	resp := s.handleRequest(ctx, req)
	out, err := json.Marshal(resp)
	if err != nil {
		return nil
	}
	return out
}

func (s *Server) handleNotification(req Request) {
	switch req.Method {
	case "notifications/initialized":
		s.mu.Lock()
		s.initialized = true
		s.mu.Unlock()
	default:
		// Неизвестные нотификации не вызывают ошибку по спецификации JSON-RPC
	}
}

func (s *Server) handleRequest(ctx context.Context, req Request) Response {
	resp := Response{
		JSONRPC: "2.0",
		ID:      req.ID,
	}

	switch req.Method {
	case "initialize":
		s.mu.Lock()
		s.initialized = true
		s.mu.Unlock()

		resp.Result = InitializeResult{
			ProtocolVersion: CurrentProtocolVersion,
			Capabilities: ServerCapabilities{
				Tools: &ToolsCapability{ListChanged: false},
				Experimental: map[string]interface{}{
					"claude/channel": map[string]interface{}{},
				},
			},
			ServerInfo: ServerInfo{
				Name:    ServerName,
				Version: ServerVersion,
			},
		}

	case "ping":
		resp.Result = map[string]interface{}{}

	case "tools/list":
		resp.Result = ToolsListResult{
			Tools: s.tools.List(),
		}

	case "tools/call":
		var params CallToolParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				resp.Error = &RPCError{
					Code:    InvalidParams,
					Message: fmt.Sprintf("invalid params: %v", err),
				}
				return resp
			}
		}

		resText, err := s.tools.Call(ctx, params.Name, params.Arguments)
		if err != nil {
			resp.Result = CallToolResult{
				Content: []ContentItem{
					{Type: "text", Text: err.Error()},
				},
				IsError: true,
			}
		} else {
			resp.Result = CallToolResult{
				Content: []ContentItem{
					{Type: "text", Text: resText},
				},
				IsError: false,
			}
		}

	default:
		resp.Error = &RPCError{
			Code:    MethodNotFound,
			Message: fmt.Sprintf("method %q not found", req.Method),
		}
	}

	return resp
}

// Run читает поток JSON-RPC запросов из in и пишет ответы в out.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		resp := s.HandleLine(ctx, line)
		if resp != nil {
			if _, wErr := out.Write(append(resp, '\n')); wErr != nil {
				return wErr
			}
		}
	}
}

// RunStdioServer запускает MCP-сервер на стандартных потоках ввода/вывода процесса.
func RunStdioServer() error {
	// Перенаправляем стандартный логгер в Stderr, чтобы не засорять Stdout JSON-RPC протокол
	log.SetOutput(os.Stderr)
	srv := NewServer(nil)
	return srv.Run(context.Background(), os.Stdin, os.Stdout)
}
