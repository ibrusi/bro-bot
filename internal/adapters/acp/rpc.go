package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// RPCError представляет ошибку JSON-RPC 2.0 протокола ACP.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return "acp error"
	}
	return fmt.Sprintf("acp %d: %s", e.Code, e.Message)
}

// Message представляет JSON-RPC 2.0 сообщение поверх построчного stdio.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// AuthMethod описывает способ аутентификации из ответа initialize.
type AuthMethod struct {
	ID string `json:"id"`
}

// InitializeResult описывает ответ ACP-агента на запрос initialize.
type InitializeResult struct {
	ProtocolVersion   int          `json:"protocolVersion"`
	AuthMethods       []AuthMethod `json:"authMethods"`
	AgentCapabilities struct {
		LoadSession         bool `json:"loadSession"`
		SessionCapabilities struct {
			Resume json.RawMessage `json:"resume"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
}

func idKey(raw json.RawMessage) string {
	return string(bytes.TrimSpace(raw))
}

func hasID(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) > 0 && !bytes.Equal(s, []byte("null"))
}

// Conn читает поток ACP-агента в одной горутине и сопоставляет ответы с запросами.
type Conn struct {
	r *bufio.Reader
	w io.Writer

	writeMu sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[string]chan Message

	// OnRequest обрабатывает входящий RPC-запрос агента к клиенту (например, session/request_permission).
	OnRequest func(Message)
	// OnNotify получает уведомления агента (например, session/update).
	OnNotify func(Message)
}

// NewConn создаёт JSON-RPC соединение поверх потоков stdio процесса агента.
func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{
		r:       bufio.NewReaderSize(r, 64*1024),
		w:       w,
		pending: make(map[string]chan Message),
	}
}

// ReadLoop читает stdout до EOF или ошибки. По завершении будит все ожидающие запросы.
func (c *Conn) ReadLoop() error {
	defer c.failPending(io.EOF)
	for {
		line, err := c.r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var msg Message
			if jsonErr := json.Unmarshal(line, &msg); jsonErr == nil {
				c.dispatch(msg)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (c *Conn) dispatch(msg Message) {
	switch {
	case msg.Method != "" && hasID(msg.ID):
		if c.OnRequest != nil {
			go c.OnRequest(msg)
		}
	case msg.Method != "":
		if c.OnNotify != nil {
			c.OnNotify(msg)
		}
	case hasID(msg.ID):
		c.mu.Lock()
		ch := c.pending[idKey(msg.ID)]
		delete(c.pending, idKey(msg.ID))
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

func (c *Conn) failPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, ch := range c.pending {
		ch <- Message{Error: &RPCError{Code: -1, Message: err.Error()}}
		delete(c.pending, key)
	}
}

func (c *Conn) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// Call выполняет JSON-RPC запрос и ждёт ответ с тем же идентификатором.
func (c *Conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.next++
	idNum := c.next
	idRaw, err := json.Marshal(idNum)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	ch := make(chan Message, 1)
	c.pending[idKey(idRaw)] = ch
	c.mu.Unlock()

	req := Message{JSONRPC: "2.0", ID: idRaw, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			c.drop(idRaw)
			return nil, err
		}
		req.Params = raw
	}
	if err := c.writeJSON(req); err != nil {
		c.drop(idRaw)
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.drop(idRaw)
		return nil, ctx.Err()
	case msg := <-ch:
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	}
}

func (c *Conn) drop(id json.RawMessage) {
	c.mu.Lock()
	delete(c.pending, idKey(id))
	c.mu.Unlock()
}

// Notify отправляет JSON-RPC уведомление без ID.
func (c *Conn) Notify(method string, params any) error {
	note := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method, Params: params}
	return c.writeJSON(note)
}

// Reply отправляет успешный ответ на входящий запрос агента.
func (c *Conn) Reply(id json.RawMessage, result any) error {
	res := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result}
	return c.writeJSON(res)
}

// ReplyError отправляет ошибку JSON-RPC в ответ на входящий запрос агента.
func (c *Conn) ReplyError(id json.RawMessage, code int, message string) error {
	res := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *RPCError       `json:"error"`
	}{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: message}}
	return c.writeJSON(res)
}
