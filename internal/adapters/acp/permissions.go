package acp

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bro-bot/internal/ports"
	"bro-bot/internal/tools"
)

// PermOption описывает вариант ответа на запрос разрешения session/request_permission.
type PermOption struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
}

// ToolCallPermission содержит метаданные вызова инструмента из session/request_permission.
type ToolCallPermission struct {
	ToolCallID string         `json:"toolCallId"`
	Kind       string         `json:"kind"`
	Name       string         `json:"name"`
	ToolName   string         `json:"toolName"`
	Title      string         `json:"title"`
	RawInput   map[string]any `json:"rawInput"`
	Args       map[string]any `json:"args"`
	Meta       map[string]any `json:"_meta"`
	Locations  []struct {
		Path string `json:"path"`
	} `json:"locations"`
}

// MergeFrom дополняет пустые поля и отсутствующие ключи параметров из предыдущего снимка вызова.
func (c *ToolCallPermission) MergeFrom(prev ToolCallPermission) {
	if c.ToolCallID == "" {
		c.ToolCallID = prev.ToolCallID
	}
	if c.Kind == "" {
		c.Kind = prev.Kind
	}
	if c.Name == "" {
		c.Name = prev.Name
	}
	if c.ToolName == "" {
		c.ToolName = prev.ToolName
	}
	if c.Title == "" {
		c.Title = prev.Title
	}
	if len(c.Locations) == 0 && len(prev.Locations) > 0 {
		c.Locations = append([]struct {
			Path string `json:"path"`
		}(nil), prev.Locations...)
	}
	c.RawInput = mergeAnyMaps(prev.RawInput, c.RawInput)
	c.Args = mergeAnyMaps(prev.Args, c.Args)
	c.Meta = mergeAnyMaps(prev.Meta, c.Meta)
}

func mergeAnyMaps(base, override map[string]any) map[string]any {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// ToolCallRegistry накапливает метаданные tool_call/tool_call_update по toolCallId,
// чтобы обогащать последующие tool_call_update и запросы session/request_permission.
type ToolCallRegistry struct {
	mu    sync.Mutex
	calls map[string]ToolCallPermission
}

// Observe сохраняет метаданные вызова из session/update (tool_call / tool_call_update).
func (r *ToolCallRegistry) Observe(raw json.RawMessage) {
	if r == nil || len(raw) == 0 {
		return
	}
	var wrapper struct {
		Update struct {
			SessionUpdate string         `json:"sessionUpdate"`
			ToolCallID    string         `json:"toolCallId"`
			Kind          string         `json:"kind"`
			Name          string         `json:"name"`
			ToolName      string         `json:"toolName"`
			Title         string         `json:"title"`
			RawInput      map[string]any `json:"rawInput"`
			Args          map[string]any `json:"args"`
			Meta          map[string]any `json:"_meta"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &wrapper) != nil {
		return
	}
	u := wrapper.Update
	if (u.SessionUpdate != "tool_call" && u.SessionUpdate != "tool_call_update") || strings.TrimSpace(u.ToolCallID) == "" {
		return
	}
	cur := ToolCallPermission{
		ToolCallID: strings.TrimSpace(u.ToolCallID),
		Kind:       strings.TrimSpace(u.Kind),
		Name:       strings.TrimSpace(u.Name),
		ToolName:   strings.TrimSpace(u.ToolName),
		Title:      strings.TrimSpace(u.Title),
		RawInput:   u.RawInput,
		Args:       u.Args,
		Meta:       u.Meta,
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = make(map[string]ToolCallPermission)
	}
	if prev, ok := r.calls[cur.ToolCallID]; ok {
		cur.MergeFrom(prev)
	}
	r.calls[cur.ToolCallID] = cur
}

// Enrich дополняет ToolCallPermission кэшированными метаданными по toolCallId.
func (r *ToolCallRegistry) Enrich(call ToolCallPermission) ToolCallPermission {
	if r == nil || strings.TrimSpace(call.ToolCallID) == "" {
		return call
	}
	r.mu.Lock()
	prev, ok := r.calls[strings.TrimSpace(call.ToolCallID)]
	r.mu.Unlock()
	if !ok {
		return call
	}
	call.MergeFrom(prev)
	return call
}

// EnrichStepUpdate восполняет имя и параметры инструмента в StreamStepUpdate из накопленного реестра.
func (r *ToolCallRegistry) EnrichStepUpdate(su *StreamStepUpdate) {
	if r == nil || su == nil || su.StepType != "tool" || strings.TrimSpace(su.ToolCallID) == "" {
		return
	}
	r.mu.Lock()
	cached, ok := r.calls[strings.TrimSpace(su.ToolCallID)]
	r.mu.Unlock()
	if !ok {
		return
	}
	params := mergeAnyMaps(cached.Args, cached.RawInput)
	if su.ToolInfo != nil {
		params = mergeAnyMaps(params, su.ToolInfo.Parameters)
	}
	u := sessionUpdate{
		ToolCallID: su.ToolCallID,
		Name:       cached.Name,
		ToolName:   cached.ToolName,
		Title:      cached.Title,
		Kind:       cached.Kind,
	}
	label := toolLabel(u, params)
	if label != "" && (su.ToolName == "" || su.ToolName == "tool" || su.ToolName == su.ToolCallID) {
		su.ToolName = label
	}
	if su.ToolInfo == nil {
		su.ToolInfo = &StreamToolInfo{}
	}
	if su.ToolInfo.Name == "" || su.ToolInfo.Name == "tool" || su.ToolInfo.Name == su.ToolCallID {
		su.ToolInfo.Name = su.ToolName
	}
	if len(params) > 0 {
		su.ToolInfo.Parameters = params
	}
}

// PermissionParams — параметры запроса session/request_permission.
type PermissionParams struct {
	ToolCall ToolCallPermission `json:"toolCall"`
	Options  []PermOption       `json:"options"`
}

// ReadOnlyToolKind сообщает, относится ли kind к безопасным операциям чтения.
func ReadOnlyToolKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "read", "search", "think", "fetch":
		return true
	default:
		return false
	}
}

func isInteractiveQuestionToolName(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ask_question", "askquestion", "run ask_question",
		"ask_user_question", "askuserquestion", "ask_user":
		return true
	default:
		return false
	}
}

// IsInteractiveTurnTool сообщает, является ли инструмент вопросом или сообщением пользователю,
// после которого пустой текстовый ответ модели считается штатным завершением хода.
func IsInteractiveTurnTool(name string) bool {
	if isInteractiveQuestionToolName(name) {
		return true
	}
	canon := tools.NormalizeBotToolName(name)
	switch canon {
	case "ask_user", "telegram_send_message", "send_file":
		return true
	default:
		return false
	}
}

func isSafeReadOnlyBuiltinName(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "read_file", "view_file", "list_dir", "find_by_name", "grep_search",
		"glob", "read", "ls", "search", "web_search", "search_web",
		"fetch_web_page", "read_url_content", "read_resource", "list_resources",
		"command_status", "view_file_outline", "view_code_item", "enter_plan_mode",
		"search_tool", "update_steps":
		return true
	default:
		return false
	}
}

// IsBotOrInteractiveTool разрешает безопасные инструменты bro_bot, поиск MCP-инструментов,
// безопасные встроенные инструменты чтения и инструмент вопросов в режимах /chat и /plan.
func IsBotOrInteractiveTool(call ToolCallPermission) bool {
	// Явный ServerName имеет наивысший приоритет: чужой MCP-сервер запрещён безусловно,
	// даже если имя инструмента совпадает с интерактивным (например ask_user).
	if srv := firstStringRecursive(0, []string{"ServerName", "server_name", "server", "mcpServer", "mcp_server"}, call.RawInput, call.Args, call.Meta); srv != "" {
		return isBroBotServerName(srv)
	}

	name := strings.ToLower(strings.TrimSpace(call.Name))
	toolName := strings.ToLower(strings.TrimSpace(call.ToolName))
	title := strings.ToLower(strings.TrimSpace(call.Title))

	if isInteractiveQuestionToolName(name) || isInteractiveQuestionToolName(toolName) || isInteractiveQuestionToolName(title) {
		return true
	}

	// Grok: поиск метаданных подключённых MCP-инструментов безопасен в ReadOnly.
	if name == "search_tool" || toolName == "search_tool" || title == "search_tool" || strings.HasPrefix(title, "search tools:") {
		return true
	}

	// Вызов субагента в ReadOnly разрешаем строго для read-only типа research.
	if name == "invoke_subagent" || toolName == "invoke_subagent" {
		subType := strings.ToLower(firstStringRecursive(0, []string{"TypeName", "type_name", "subagent_type", "type", "agent"}, call.RawInput, call.Args, call.Meta))
		return subType == "research"
	}

	if tn := firstStringRecursive(0, []string{"ToolName", "tool_name", "tool", "mcpToolName", "mcp_tool_name"}, call.RawInput, call.Args, call.Meta); tn != "" {
		return IsBotMCPToolTitle(tn)
	}

	if IsBotMCPToolTitle(call.Name) || IsBotMCPToolTitle(call.ToolName) || IsBotMCPToolTitle(call.Title) {
		return true
	}

	return isSafeReadOnlyBuiltinName(name) || isSafeReadOnlyBuiltinName(toolName) || isSafeReadOnlyBuiltinName(title)
}

func isBroBotServerName(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "bro_bot" || s == "bro-bot"
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func firstStringRecursive(depth int, keys []string, sources ...any) string {
	if depth > 4 {
		return ""
	}
	for _, src := range sources {
		m, ok := src.(map[string]any)
		if !ok || m == nil {
			continue
		}
		if val := firstString(m, keys...); val != "" {
			return val
		}
		for _, v := range m {
			if nested, ok := v.(map[string]any); ok {
				if val := firstStringRecursive(depth+1, keys, nested); val != "" {
					return val
				}
			}
		}
	}
	return ""
}

// IsBotMCPToolTitle распознаёт вызовы MCP-инструментов самого bro-bot (bro_bot / bro-bot)
// только по строгому белому списку инструментов моста.
func IsBotMCPToolTitle(toolTitle string) bool {
	lower := strings.ToLower(strings.TrimSpace(toolTitle))
	if lower == "" {
		return false
	}
	if lower == "bro_bot" || lower == "bro-bot" {
		return true
	}
	if tools.IsBotToolName(lower) {
		return true
	}
	if normalized := tools.NormalizeBotToolName(lower); normalized != lower && tools.IsBotToolName(normalized) {
		return true
	}
	if !strings.Contains(lower, "bro_bot") && !strings.Contains(lower, "bro-bot") {
		return false
	}
	// Разбираем формат вида "bro_bot: ask_user" или суффикс после '__', '/', ':', ' ' при наличии bro_bot.
	base := lower
	for _, sep := range []string{"__", "/", ":", " "} {
		if idx := strings.LastIndex(base, sep); idx != -1 {
			base = strings.TrimSpace(base[idx+len(sep):])
		}
	}
	return tools.IsBotToolName(base)
}

// IsAllowedInReadOnly сообщает, разрешён ли вызов инструмента в режиме чтения (/chat и /plan).
func IsAllowedInReadOnly(call ToolCallPermission) bool {
	if ReadOnlyToolKind(call.Kind) || IsBotOrInteractiveTool(call) {
		return true
	}
	return IsSafeReadOnlyShellCall(call)
}

// DeniedPermission описывает отклонённый в контролируемом режиме запрос разрешения инструмента.
type DeniedPermission struct {
	Tool   string
	Title  string
	Reason string
}

// NewDeniedPermission собирает описание отклонённого вызова инструмента из метаданных ACP,
// предпочитая фактическую shell-команду в параметрах заголовку-заглушке вроде "bash".
func NewDeniedPermission(call ToolCallPermission) DeniedPermission {
	return NewDeniedPermissionWithReason(call, "")
}

// NewDeniedPermissionWithReason собирает описание отклонённого вызова инструмента с указанием причины отказа.
func NewDeniedPermissionWithReason(call ToolCallPermission, reason string) DeniedPermission {
	tool := firstNonEmptyString(call.Name, call.ToolName)
	if tool == "" {
		if isShellToolName(call.Title) {
			tool = strings.TrimSpace(call.Title)
		} else {
			tool = firstNonEmptyString(call.Kind, "tool")
		}
	}
	title := strings.TrimSpace(call.Title)
	if cmd := extractShellCommand(call); cmd != "" {
		title = cmd
	}
	return DeniedPermission{
		Tool:   tool,
		Title:  title,
		Reason: strings.TrimSpace(reason),
	}
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// FormatDeniedPermissions собирает компактное описание отклонённых вызовов для логов,
// recovery-промпта и сообщения об ошибке.
func FormatDeniedPermissions(denied []DeniedPermission) string {
	if len(denied) == 0 {
		return ""
	}
	parts := make([]string, 0, len(denied))
	seen := make(map[string]bool, len(denied))
	for _, d := range denied {
		tool := strings.TrimSpace(d.Tool)
		if tool == "" {
			tool = "tool"
		}
		title := truncateRunes(strings.TrimSpace(d.Title), 160)
		label := tool
		if title != "" && !strings.EqualFold(title, tool) {
			label = tool + ": " + title
		}
		if reason := truncateRunes(strings.TrimSpace(d.Reason), 120); reason != "" {
			label += " [" + reason + "]"
		}
		if !seen[label] {
			seen[label] = true
			parts = append(parts, label)
		}
	}
	return strings.Join(parts, "; ")
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// ReadOnlyRecoveryPrompt формирует инструкцию восстановления после того,
// как отказ в доступе в read-only режиме оборвал ход агента без текстового ответа.
func ReadOnlyRecoveryPrompt(deniedSummary string) string {
	return "Your previous tool call (" + deniedSummary + ") was denied because this session is strictly read-only. " +
		"Do NOT retry that command or call any write/edit/mutating tools or .sh scripts. " +
		"Answer the user's request directly in the user's language using already gathered information, read_file, sqlite_query, " +
		"or allowed read-only analysis commands (python3, read-only sqlite3, HTTP GET via curl/wget to stdout or /tmp), " +
		"or concisely explain what cannot be done in read-only mode."
}

// ControlledDeniedRecoveryPrompt формирует инструкцию восстановления после того,
// как отказ в доступе оборвал ход агента без текстового ответа.
func ControlledDeniedRecoveryPrompt(deniedSummary string, interactive bool) string {
	if !interactive {
		return ReadOnlyRecoveryPrompt(deniedSummary)
	}
	return "Your previous tool call (" + deniedSummary + ") was denied by policy or user decision. " +
		"Do NOT retry that denied command or path modification. " +
		"Continue answering the user's request directly in the user's language or concisely explain what cannot be modified."
}

// BuildPermissionRequest преобразует метаданные ACP-запроса разрешения в единый ports.PermissionRequest.
func BuildPermissionRequest(call ToolCallPermission, workDir string) ports.PermissionRequest {
	tool := firstNonEmptyString(call.Name, call.ToolName)
	if tool == "" && isShellToolName(call.Title) {
		tool = strings.TrimSpace(call.Title)
	}
	kind := strings.ToLower(strings.TrimSpace(call.Kind))
	var cmd string
	if isShellExecutionCall(call) {
		cmd = extractShellCommand(call)
		if kind == "" {
			kind = "execute"
		}
	}

	paths := extractPermissionPaths(call)
	if len(paths) == 0 && (tools.IsWriteOrEditToolName(tool) || kind == "edit" || kind == "delete") {
		titleTrimmed := strings.TrimSpace(call.Title)
		if titleTrimmed != "" && !strings.ContainsAny(titleTrimmed, " \t\n") && (strings.Contains(titleTrimmed, "/") || strings.Contains(titleTrimmed, ".")) {
			paths = append(paths, titleTrimmed)
		}
	}

	raw := mergePermissionInputs(call.RawInput, call.Args, call.Meta)
	title := strings.TrimSpace(call.Title)
	if cmd != "" {
		title = cmd
	} else if title == "" && len(paths) > 0 {
		title = paths[0]
	} else if title == "" {
		title = tool
	}

	return ports.PermissionRequest{
		ToolName: tool,
		Kind:     kind,
		Title:    title,
		Command:  cmd,
		Paths:    paths,
		WorkDir:  workDir,
		RawInput: raw,
	}
}

func mergePermissionInputs(maps ...map[string]any) map[string]any {
	var out map[string]any
	for _, m := range maps {
		if len(m) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(m))
		}
		for k, v := range m {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
	}
	return out
}

func extractPermissionPaths(call ToolCallPermission) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}

	pathKeys := []string{"path", "file_path", "filePath", "TargetFile", "target_file", "target", "AbsolutePath", "absolute_path"}
	if p := firstStringRecursive(0, pathKeys, call.RawInput, call.Args, call.Meta); p != "" {
		add(p)
	}
	for _, src := range []map[string]any{call.RawInput, call.Args, call.Meta} {
		if src == nil {
			continue
		}
		for _, k := range pathKeys {
			if s, ok := src[k].(string); ok {
				add(s)
			}
		}
		for _, k := range []string{"paths", "files"} {
			switch val := src[k].(type) {
			case []string:
				for _, item := range val {
					add(item)
				}
			case []any:
				for _, item := range val {
					if s, ok := item.(string); ok {
						add(s)
					} else if m, ok := item.(map[string]any); ok {
						if s := firstString(m, pathKeys...); s != "" {
							add(s)
						}
					}
				}
			}
		}
	}
	for _, loc := range call.Locations {
		add(loc.Path)
	}
	return out
}

var (
	rejectPermissionPrefer = []string{"reject_once", "reject_always", "reject", "deny_once", "deny_always", "deny"}
	allowOnceOnlyPrefer    = []string{"allow_once", "allow"}
)

func selectPermissionOption(options []PermOption, prefer []string, allowFallbackReject bool) (string, bool) {
	if len(options) == 0 {
		return "", false
	}
	for _, kind := range prefer {
		for _, opt := range options {
			if strings.EqualFold(opt.Kind, kind) && opt.OptionID != "" {
				return opt.OptionID, true
			}
		}
	}
	if allowFallbackReject {
		for _, opt := range options {
			if opt.OptionID == "" {
				continue
			}
			kl := strings.ToLower(opt.Kind + " " + opt.OptionID)
			if strings.Contains(kl, "reject") || strings.Contains(kl, "deny") {
				return opt.OptionID, true
			}
		}
	}
	return "", false
}

// ResolvePermission вычисляет ответ на session/request_permission с учётом
// флага readOnly, политики SCRIPTS_DIR, жёстких предохранителей и PermissionHandler.
// В контролируемом режиме (/chat, /plan) одобрения всегда возвращают allow_once (никогда allow_always).
func ResolvePermission(ctx context.Context, readOnly bool, scriptsDir, workDir string, handler ports.PermissionHandler, call ToolCallPermission, options []PermOption) (optionID string, ok bool, allowed bool, denyReason string) {
	if !readOnly {
		id, chosen := ChoosePermission(false, call, options)
		return id, chosen, true, ""
	}

	req := BuildPermissionRequest(call, workDir)

	if handler == nil {
		if guard := tools.CheckWriteGuardrail(req, nil); guard.Blocked {
			id, chosen := selectPermissionOption(options, rejectPermissionPrefer, true)
			return id, chosen, false, guard.Reason
		}
		if sc := tools.CheckScriptsDirOperation(req, scriptsDir); sc.Kind != tools.ScriptsDirOpNone {
			id, chosen := selectPermissionOption(options, rejectPermissionPrefer, true)
			return id, chosen, false, "SCRIPTS_DIR operation blocked in deny mode"
		}
		if !IsAllowedInReadOnly(call) {
			id, chosen := selectPermissionOption(options, rejectPermissionPrefer, true)
			return id, chosen, false, ""
		}
		id, chosen := ChoosePermission(true, call, options)
		return id, chosen, true, ""
	}

	// Безопасные read-only инструменты (кроме shell и записей/запуска SCRIPTS_DIR) пропускаются сразу.
	if (ReadOnlyToolKind(call.Kind) || IsBotOrInteractiveTool(call)) &&
		!tools.CheckWriteGuardrail(req, nil).Blocked &&
		tools.CheckScriptsDirOperation(req, scriptsDir).Kind == tools.ScriptsDirOpNone {
		id, chosen := selectPermissionOption(options, allowOnceOnlyPrefer, false)
		return id, chosen, true, ""
	}

	decision, reason := handler.DecidePermission(ctx, req)
	if ctx.Err() != nil {
		id, chosen := selectPermissionOption(options, rejectPermissionPrefer, true)
		return id, chosen, false, "cancelled"
	}
	if decision == ports.PermissionDeny {
		id, chosen := selectPermissionOption(options, rejectPermissionPrefer, true)
		return id, chosen, false, reason
	}

	// Никогда не передаём allow_always в ACP CLI при контролируемом режиме:
	// агент обязан продолжать отправлять session/request_permission для предохранителей и SCRIPTS_DIR.
	id, chosen := selectPermissionOption(options, allowOnceOnlyPrefer, false)
	return id, chosen, true, ""
}

// ChoosePermission выбирает вариант ответа на session/request_permission
// с учётом режима readOnly (/chat, /plan) и метаданных вызываемого инструмента.
func ChoosePermission(readOnly bool, call ToolCallPermission, options []PermOption) (string, bool) {
	if len(options) == 0 {
		return "", false
	}
	allowedInReadOnly := IsAllowedInReadOnly(call)
	var prefer []string
	switch {
	case readOnly && !allowedInReadOnly:
		prefer = rejectPermissionPrefer
	case readOnly:
		prefer = []string{"allow_once", "allow_always", "allow"}
	default:
		prefer = []string{"allow_always", "allow_once", "allow"}
	}
	return selectPermissionOption(options, prefer, readOnly && !allowedInReadOnly)
}

// WaitProcess дожидается завершения runDone и readDone, а затем завершения дочернего процесса
// с льготным таймаутом перед принудительной остановкой.
func WaitProcess(cmd *exec.Cmd, runDone, readDone <-chan struct{}, finishedOK *atomic.Bool, kill func() error) error {
	if cmd == nil {
		return nil
	}
	if runDone != nil {
		<-runDone
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()

	if readDone != nil {
		select {
		case <-readDone:
		case <-timer.C:
			if kill != nil {
				_ = kill()
			}
			<-readDone
			err := cmd.Wait()
			if finishedOK != nil && finishedOK.Load() {
				return nil
			}
			return err
		}
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	select {
	case err := <-waitCh:
		return err
	case <-timer.C:
		if kill != nil {
			_ = kill()
		}
		err := <-waitCh
		if finishedOK != nil && finishedOK.Load() {
			return nil
		}
		return err
	}
}

// PermissionResult формирует JSON-ответ на session/request_permission.
func PermissionResult(optionID string, ok bool) any {
	if !ok {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}}
}
