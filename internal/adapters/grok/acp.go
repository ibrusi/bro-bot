package grok

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// json-rpc поверх stdin/stdout процесса `grok agent stdio`.
// Каждое сообщение — одна строка JSON.

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return "acp error"
	}
	return fmt.Sprintf("acp %d: %s", e.Code, e.Message)
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func idKey(raw json.RawMessage) string {
	return string(bytes.TrimSpace(raw))
}

func hasID(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) > 0 && !bytes.Equal(s, []byte("null"))
}

// rpcConn читает поток агента в одной горутине и сопоставляет ответы с запросами.
type rpcConn struct {
	r *bufio.Reader
	w io.Writer

	writeMu sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[string]chan rpcMessage

	// onRequest обрабатывает запрос агента к клиенту (права, файлы).
	// Вызывается вне блокировок, ответ пишется через reply.
	onRequest func(rpcMessage)
	// onNotify получает уведомления агента (session/update).
	onNotify func(rpcMessage)
}

func newRPC(r io.Reader, w io.Writer) *rpcConn {
	return &rpcConn{
		r:       bufio.NewReaderSize(r, 64*1024),
		w:       w,
		pending: make(map[string]chan rpcMessage),
	}
}

// readLoop читает stdout до EOF или ошибки. По завершении будит ожидающие запросы.
func (c *rpcConn) readLoop() error {
	defer c.failPending(io.EOF)
	for {
		line, err := c.r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var msg rpcMessage
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

func (c *rpcConn) dispatch(msg rpcMessage) {
	switch {
	case msg.Method != "" && hasID(msg.ID):
		if c.onRequest != nil {
			go c.onRequest(msg)
		}
	case msg.Method != "":
		if c.onNotify != nil {
			c.onNotify(msg)
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

func (c *rpcConn) failPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, ch := range c.pending {
		ch <- rpcMessage{Error: &rpcError{Code: -1, Message: err.Error()}}
		delete(c.pending, key)
	}
}

func (c *rpcConn) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// call выполняет запрос и ждёт ответ с тем же id.
func (c *rpcConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.next++
	idNum := c.next
	idRaw, err := json.Marshal(idNum)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	ch := make(chan rpcMessage, 1)
	c.pending[idKey(idRaw)] = ch
	c.mu.Unlock()

	req := rpcMessage{JSONRPC: "2.0", ID: idRaw, Method: method}
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

func (c *rpcConn) drop(id json.RawMessage) {
	c.mu.Lock()
	delete(c.pending, idKey(id))
	c.mu.Unlock()
}

// notify шлёт уведомление без id.
func (c *rpcConn) notify(method string, params any) error {
	note := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method, Params: params}
	return c.writeJSON(note)
}

// reply отвечает на запрос агента.
func (c *rpcConn) reply(id json.RawMessage, result any) error {
	res := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result}
	return c.writeJSON(res)
}

type authMethod struct {
	ID string `json:"id"`
}

type initializeResult struct {
	ProtocolVersion   int          `json:"protocolVersion"`
	AuthMethods       []authMethod `json:"authMethods"`
	AgentCapabilities struct {
		LoadSession         bool `json:"loadSession"`
		SessionCapabilities struct {
			Resume json.RawMessage `json:"resume"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
}

// pickAuthMethod выбирает вход без браузера: сохранённый токен, иначе ключ API.
func pickAuthMethod(methods []authMethod) (string, bool) {
	for _, m := range methods {
		if m.ID == "cached_token" {
			return m.ID, true
		}
	}
	for _, m := range methods {
		if strings.Contains(m.ID, "api_key") {
			return m.ID, true
		}
	}
	return "", false
}

type permOption struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
}

type permissionParams struct {
	ToolCall struct {
		Kind  string `json:"kind"`
		Title string `json:"title"`
	} `json:"toolCall"`
	Options []permOption `json:"options"`
}

// readOnlyToolKind — виды вызовов, которые диалог может выполнять.
func readOnlyToolKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "read", "search", "think", "fetch":
		return true
	default:
		return false
	}
}

// choosePermission выбирает вариант ответа на session/request_permission.
// В задаче предпочтительно «разрешить всегда», в диалоге запись и команды отклоняются.
// Второе значение false — ответа с выбором нет, запрос нужно отменить.
func choosePermission(readOnly bool, toolKind string, options []permOption) (string, bool) {
	if len(options) == 0 {
		return "", false
	}
	var prefer []string
	if readOnly && !readOnlyToolKind(toolKind) {
		prefer = []string{"reject_once", "reject_always"}
	} else if readOnly {
		prefer = []string{"allow_once", "allow_always"}
	} else {
		prefer = []string{"allow_always", "allow_once"}
	}
	for _, kind := range prefer {
		for _, opt := range options {
			if strings.EqualFold(opt.Kind, kind) && opt.OptionID != "" {
				return opt.OptionID, true
			}
		}
	}
	return "", false
}

func permissionResult(optionID string, ok bool) any {
	if !ok {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}}
}
