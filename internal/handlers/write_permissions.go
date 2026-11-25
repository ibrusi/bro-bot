package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/tools"
	"bro-bot/internal/utils"
	"context"
	"html"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type writePermScope string

const (
	writePermScopeChat writePermScope = "chat"
	writePermScopePlan writePermScope = "plan"
)

// parseWriteModeArg нормализует пользовательский аргумент режима прав записи
// к каноническим значениям deny / ask / allow, поддерживая все синонимы.
func parseWriteModeArg(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case domain.WriteModeDeny, "off", "ro", "readonly", "read-only", "no", "false", "0", "запретить", "выкл", "нет":
		return domain.WriteModeDeny, true
	case domain.WriteModeAsk, "prompt", "confirm", "спрашивать", "подтверждать":
		return domain.WriteModeAsk, true
	case domain.WriteModeAllow, "on", "approve", "auto", "yes", "true", "1", "разрешить", "вкл", "да":
		return domain.WriteModeAllow, true
	default:
		return "", false
	}
}

// isWriteSubcommandArgs защищает обычные промпты вида `/plan write tests` или
// `/chat write a python script` от перехвата как подкоманды настройки прав записи.
func isWriteSubcommandArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	head := strings.ToLower(strings.TrimSpace(args[0]))
	if head != "write" && head != "запись" {
		return false
	}
	if len(args) == 1 {
		return true
	}
	if len(args) > 2 {
		return false
	}
	second := strings.ToLower(strings.TrimSpace(args[1]))
	if _, ok := parseWriteModeArg(second); ok {
		return true
	}
	return second == "status" || second == "статус" || second == "help" || second == "?" || head == "запись"
}

// runningTaskWorkDirs собирает каталоги проектов с активными задачами (выполнение или планирование),
// исключая excludeTaskID (чтобы задача планирования не блокировала сама себя).
func runningTaskWorkDirs(projectsRoot string, excludeTaskID int) []string {
	if strings.TrimSpace(projectsRoot) == "" {
		return nil
	}
	tasks := domain.GlobalTaskManager.ListTasks()
	seen := make(map[string]struct{}, len(tasks))
	var out []string
	for _, task := range tasks {
		if task == nil {
			continue
		}
		view := task.Snapshot()
		if excludeTaskID > 0 && view.ID == excludeTaskID {
			continue
		}
		if view.Status != domain.TaskStatusRunning && view.Status != domain.TaskStatusPlanning {
			continue
		}
		if strings.TrimSpace(view.Project) == "" {
			continue
		}
		dir := filepath.Join(projectsRoot, view.Project)
		if realDir, err := tools.ResolvePathWithSymlinks(dir, ""); err == nil {
			dir = realDir
		}
		if _, exists := seen[dir]; exists {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	return out
}

func formatWriteModeLabel(mode, lang string) string {
	switch domain.NormalizeWriteMode(mode, domain.WriteModeDeny) {
	case domain.WriteModeAllow:
		return i18n.T(lang, "write.mode_allow_label")
	case domain.WriteModeAsk:
		return i18n.T(lang, "write.mode_ask_label")
	default:
		return i18n.T(lang, "write.mode_deny_label")
	}
}

func buildWriteModeButtonsRow(scope writePermScope, currentMode, lang string) []ports.Button {
	action := "chat_write_sel"
	if scope == writePermScopePlan {
		action = "plan_write_sel"
	}
	normalized := domain.NormalizeWriteMode(currentMode, domain.WriteModeDeny)
	modes := []struct {
		mode string
		key  string
	}{
		{domain.WriteModeDeny, "btn.write_deny"},
		{domain.WriteModeAsk, "btn.write_ask"},
		{domain.WriteModeAllow, "btn.write_allow"},
	}
	row := make([]ports.Button, 0, len(modes))
	for _, item := range modes {
		label := i18n.T(lang, item.key)
		if normalized == item.mode {
			label = "✅ " + label
		}
		row = append(row, ports.Button{
			Text:    label,
			Action:  action,
			Payload: item.mode,
		})
	}
	return row
}

func buildWriteModeKeyboard(scope writePermScope, currentMode, lang string) *ports.Keyboard {
	return &ports.Keyboard{
		Rows: [][]ports.Button{buildWriteModeButtonsRow(scope, currentMode, lang)},
	}
}

func formatWriteStatusText(scope writePermScope, currentMode, lang string) string {
	scopeLabel := i18n.T(lang, "write.scope_chat")
	if scope == writePermScopePlan {
		scopeLabel = i18n.T(lang, "write.scope_plan")
	}

	policy := config.WritePolicySnapshot()
	pathsLine := i18n.T(lang, "write.paths_empty")
	if len(policy.ChatWritePaths) > 0 {
		pathsLine = i18n.Tf(lang, "write.paths_line", html.EscapeString(strings.Join(policy.ChatWritePaths, ", ")))
	}

	var bldr strings.Builder
	bldr.WriteString(i18n.Tf(lang, "write.status",
		scopeLabel,
		formatWriteModeLabel(currentMode, lang),
		pathsLine,
		scopeLabel,
	))
	bldr.WriteString("\n\n")
	bldr.WriteString(i18n.T(lang, "write.scripts_note"))
	if strings.EqualFold(ActiveAgentName(), "claude") && strings.EqualFold(config.ProjectState.GetExecutionMode(), "mcp") {
		bldr.WriteString(i18n.T(lang, "write.claude_mcp_note"))
	}
	return bldr.String()
}

func setPersistedWriteMode(scope writePermScope, mode string) string {
	settingKey := "chat_write_mode"
	if scope == writePermScopePlan {
		config.ProjectState.SetPlanWriteMode(mode)
		mode = config.ProjectState.GetPlanWriteMode()
		settingKey = "plan_write_mode"
	} else {
		config.ProjectState.SetChatWriteMode(mode)
		mode = config.ProjectState.GetChatWriteMode()
	}
	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), settingKey, mode)
	}
	return mode
}

func writeScopeLabel(scope writePermScope, lang string) string {
	if scope == writePermScopePlan {
		return i18n.T(lang, "write.scope_plan")
	}
	return i18n.T(lang, "write.scope_chat")
}

func currentWriteMode(scope writePermScope) string {
	if scope == writePermScopePlan {
		return config.ProjectState.GetPlanWriteMode()
	}
	return config.ProjectState.GetChatWriteMode()
}

func applyWriteModeCommand(s ports.Session, scope writePermScope) error {
	lang := uiLang()
	args := s.Args()
	cur := currentWriteMode(scope)
	scopeLabel := writeScopeLabel(scope, lang)

	if len(args) <= 1 {
		return s.Send(formatWriteStatusText(scope, cur, lang), ports.RichWith(buildWriteModeKeyboard(scope, cur, lang)))
	}
	sub := strings.ToLower(strings.TrimSpace(args[1]))
	if sub == "status" || sub == "статус" || sub == "help" || sub == "?" {
		return s.Send(formatWriteStatusText(scope, cur, lang), ports.RichWith(buildWriteModeKeyboard(scope, cur, lang)))
	}

	mode, ok := parseWriteModeArg(args[1])
	if !ok {
		return s.Send(i18n.Tf(lang, "write.usage", scopeLabel), ports.Rich())
	}

	actual := setPersistedWriteMode(scope, mode)
	extra := ""
	if actual != domain.WriteModeDeny && strings.EqualFold(ActiveAgentName(), "claude") && strings.EqualFold(config.ProjectState.GetExecutionMode(), "mcp") {
		extra = i18n.T(lang, "write.claude_mcp_note")
	}
	return s.Send(i18n.Tf(lang, "write.switched", scopeLabel, formatWriteModeLabel(actual, lang), extra),
		ports.RichWith(buildWriteModeKeyboard(scope, actual, lang)))
}

func applyWriteModeCallback(s ports.Session, scope writePermScope) error {
	lang := uiLang()
	cb := s.Callback()
	if cb == nil {
		return nil
	}
	mode, ok := parseWriteModeArg(cb.Payload)
	if !ok {
		return s.Respond(i18n.T(lang, "task.toast_bad_params"))
	}
	actual := setPersistedWriteMode(scope, mode)
	scopeLabel := writeScopeLabel(scope, lang)
	_ = s.Respond(i18n.Tf(lang, "write.toast_switched", scopeLabel, actual))

	extra := ""
	if actual != domain.WriteModeDeny && strings.EqualFold(ActiveAgentName(), "claude") && strings.EqualFold(config.ProjectState.GetExecutionMode(), "mcp") {
		extra = i18n.T(lang, "write.claude_mcp_note")
	}
	return s.Send(i18n.Tf(lang, "write.switched", scopeLabel, formatWriteModeLabel(actual, lang), extra),
		ports.RichWith(buildWriteModeKeyboard(scope, actual, lang)))
}

func onChatWriteSel(s ports.Session) error {
	return applyWriteModeCallback(s, writePermScopeChat)
}

func onPlanWriteSel(s ports.Session) error {
	return applyWriteModeCallback(s, writePermScopePlan)
}

type pendingWriteApproval struct {
	ch           chan ports.PermissionDecision
	allowTurnBtn bool
	done         atomic.Bool
	once         sync.Once
	editOnce     sync.Once

	mu       sync.Mutex
	msgRef   ports.MessageRef
	cardHTML string
}

func (p *pendingWriteApproval) resolve(decision ports.PermissionDecision) bool {
	won := false
	p.once.Do(func() {
		p.done.Store(true)
		p.ch <- decision
		won = true
	})
	return won
}

func (p *pendingWriteApproval) setMessageRef(ref ports.MessageRef) {
	p.mu.Lock()
	p.msgRef = ref
	p.mu.Unlock()
}

func (p *pendingWriteApproval) updateCard(m ports.Messenger, fallbackRef *ports.MessageRef, suffix string) {
	if m == nil {
		return
	}
	p.editOnce.Do(func() {
		p.mu.Lock()
		ref := p.msgRef
		cardHTML := p.cardHTML
		p.mu.Unlock()
		if ref.ID == "" && fallbackRef != nil {
			ref = *fallbackRef
		}
		if ref.ID == "" {
			return
		}
		_ = m.Edit(context.Background(), ref, cardHTML+suffix, ports.Rich())
	})
}

type writeApprovalRegistry struct {
	mu    sync.Mutex
	seq   atomic.Uint64
	items map[string]*pendingWriteApproval
}

var writeApprovals = &writeApprovalRegistry{
	items: make(map[string]*pendingWriteApproval),
}

func (r *writeApprovalRegistry) register(entry *pendingWriteApproval) string {
	id := strconv.FormatUint(r.seq.Add(1), 36)
	r.mu.Lock()
	r.items[id] = entry
	r.mu.Unlock()
	return id
}

func (r *writeApprovalRegistry) get(id string) (*pendingWriteApproval, bool) {
	r.mu.Lock()
	entry, ok := r.items[id]
	r.mu.Unlock()
	return entry, ok
}

func (r *writeApprovalRegistry) delete(id string) {
	r.mu.Lock()
	delete(r.items, id)
	r.mu.Unlock()
}

func (r *writeApprovalRegistry) resolve(id string, decision ports.PermissionDecision) (*pendingWriteApproval, bool) {
	r.mu.Lock()
	entry, ok := r.items[id]
	if ok {
		delete(r.items, id)
	}
	r.mu.Unlock()
	if !ok || entry == nil {
		return nil, false
	}
	if !entry.resolve(decision) {
		return entry, false
	}
	return entry, true
}

// writePermissionController реализует ports.PermissionHandler для контролируемых сессий
// /chat и /plan с жёсткими предохранителями, правилами SCRIPTS_DIR, CHAT_WRITE_PATHS и кнопками.
type writePermissionController struct {
	m               ports.Messenger
	chat            ports.ChatID
	scope           writePermScope
	taskID          int
	project         string
	projectsRoot    string
	lang            string
	mode            string
	scriptsDir      string
	writePaths      []string
	approvalTimeout time.Duration

	allowTurn atomic.Bool
	promptSem chan struct{}
}

func newWritePermissionController(m ports.Messenger, chat ports.ChatID, scope writePermScope, taskID int, project, projectsRoot, lang string) *writePermissionController {
	policy := config.WritePolicySnapshot()
	mode := config.ProjectState.GetChatWriteMode()
	if scope == writePermScopePlan {
		mode = config.ProjectState.GetPlanWriteMode()
	}
	timeout := policy.ChatApprovalTimeout
	if timeout <= 0 {
		timeout = config.DefaultChatApprovalTimeout
	}
	if projectsRoot == "" {
		projectsRoot = config.ProjectsRoot
	}
	return &writePermissionController{
		m:               m,
		chat:            chat,
		scope:           scope,
		taskID:          taskID,
		project:         project,
		projectsRoot:    projectsRoot,
		lang:            lang,
		mode:            mode,
		scriptsDir:      policy.ScriptsDir,
		writePaths:      append([]string(nil), policy.ChatWritePaths...),
		approvalTimeout: timeout,
		promptSem:       make(chan struct{}, 1),
	}
}

func (c *writePermissionController) DecidePermission(ctx context.Context, req ports.PermissionRequest) (ports.PermissionDecision, string) {
	busyDirs := runningTaskWorkDirs(c.projectsRoot, c.taskID)

	// 1. Жёсткие предохранители (всегда, независимо от режима и CHAT_WRITE_PATHS).
	if guard := tools.CheckWriteGuardrail(req, busyDirs); guard.Blocked {
		return ports.PermissionDeny, guard.Reason
	}

	// 2. Спец-правила SCRIPTS_DIR.
	scriptsCheck := tools.CheckScriptsDirOperation(req, c.scriptsDir)
	switch scriptsCheck.Kind {
	case tools.ScriptsDirOpExecute:
		if c.mode == domain.WriteModeDeny {
			return ports.PermissionDeny, "executing scripts from SCRIPTS_DIR is forbidden in deny mode"
		}
		// И в ask, и в allow запуск скриптов из SCRIPTS_DIR всегда требует подтверждения кнопкой.
		return c.askUserApproval(ctx, req, false, scriptsCheck)

	case tools.ScriptsDirOpWrite:
		if c.mode == domain.WriteModeDeny {
			return ports.PermissionDeny, "modifying files in SCRIPTS_DIR is forbidden in deny mode"
		}
		if c.mode == domain.WriteModeAllow {
			return ports.PermissionAllowOnce, ""
		}
		// В ask запись в SCRIPTS_DIR всегда запрашивает подтверждение без «На весь ход» и без CHAT_WRITE_PATHS.
		return c.askUserApproval(ctx, req, false, scriptsCheck)
	}

	// 3. Read-only и диагностические команды разрешены без подтверждения во всех режимах.
	if req.Command != "" && tools.IsSafeReadOnlyShellCommand(req.Command) {
		return ports.PermissionAllowOnce, ""
	}
	if !tools.IsMutatingWorkspaceTool(req.ToolName) &&
		!strings.EqualFold(req.Kind, "edit") &&
		!strings.EqualFold(req.Kind, "delete") &&
		!strings.EqualFold(req.Kind, "execute") &&
		strings.TrimSpace(req.Command) == "" &&
		len(req.Paths) == 0 {
		return ports.PermissionAllowOnce, ""
	}

	// 4. Режим deny блокирует любые мутации.
	if c.mode == domain.WriteModeDeny {
		return ports.PermissionDeny, "writing is disabled in deny mode"
	}

	// 5. Режим allow разрешает обычную запись вне SCRIPTS_DIR и жёстких предохранителей сразу.
	if c.mode == domain.WriteModeAllow {
		return ports.PermissionAllowOnce, ""
	}

	// 6. Режим ask: быстрые разрешения вне SCRIPTS_DIR (одобрение на весь ход или CHAT_WRITE_PATHS).
	if c.allowTurn.Load() {
		return ports.PermissionAllowAlways, ""
	}
	if tools.IsAutoAllowedWrite(req, c.writePaths, busyDirs, c.scriptsDir) {
		return ports.PermissionAllowOnce, ""
	}

	// 7. Режим ask: интерактивное подтверждение с кнопкой «На весь ход».
	return c.askUserApproval(ctx, req, true, scriptsCheck)
}

func (c *writePermissionController) askUserApproval(ctx context.Context, req ports.PermissionRequest, allowTurnBtn bool, scriptsCheck tools.ScriptsDirCheck) (ports.PermissionDecision, string) {
	select {
	case c.promptSem <- struct{}{}:
		defer func() { <-c.promptSem }()
	case <-ctx.Done():
		return ports.PermissionDeny, "cancelled"
	}

	if allowTurnBtn && c.allowTurn.Load() {
		return ports.PermissionAllowAlways, ""
	}

	if c.m == nil || !c.m.Capabilities().InlineButtons {
		return ports.PermissionDeny, "interactive confirmation buttons are unavailable"
	}

	cardBody := c.formatApprovalCardHTML(req, scriptsCheck)
	waitFooter := i18n.Tf(c.lang, "perm.timeout_hint", domain.FormatDuration(c.approvalTimeout, c.lang))

	entry := &pendingWriteApproval{
		ch:           make(chan ports.PermissionDecision, 1),
		allowTurnBtn: allowTurnBtn,
		cardHTML:     cardBody,
	}
	id := writeApprovals.register(entry)

	row := []ports.Button{
		{Text: i18n.T(c.lang, "btn.perm_once"), Action: "perm_once", Payload: id},
	}
	if allowTurnBtn {
		row = append(row, ports.Button{Text: i18n.T(c.lang, "btn.perm_turn"), Action: "perm_turn", Payload: id})
	}
	row = append(row, ports.Button{Text: i18n.T(c.lang, "btn.perm_deny"), Action: "perm_deny", Payload: id})

	ref, err := c.m.Send(context.Background(), c.chat, cardBody+waitFooter, ports.RichWith(&ports.Keyboard{Rows: [][]ports.Button{row}}))
	if err != nil {
		writeApprovals.delete(id)
		return ports.PermissionDeny, "failed to deliver confirmation card"
	}
	entry.setMessageRef(ref)

	timer := time.NewTimer(c.approvalTimeout)
	defer timer.Stop()

	select {
	case decision := <-entry.ch:
		if decision == ports.PermissionAllowAlways && allowTurnBtn {
			c.allowTurn.Store(true)
		}
		if decision == ports.PermissionDeny {
			return ports.PermissionDeny, "denied by user"
		}
		return decision, ""
	case <-timer.C:
		if winner, ok := writeApprovals.resolve(id, ports.PermissionDeny); ok && winner != nil {
			winner.updateCard(c.m, &ref, i18n.Tf(c.lang, "perm.result_timeout", domain.FormatDuration(c.approvalTimeout, c.lang)))
		}
		return ports.PermissionDeny, "confirmation timed out"
	case <-ctx.Done():
		if winner, ok := writeApprovals.resolve(id, ports.PermissionDeny); ok && winner != nil {
			winner.updateCard(c.m, &ref, i18n.T(c.lang, "perm.result_cancelled"))
		}
		return ports.PermissionDeny, "cancelled"
	}
}

func (c *writePermissionController) formatApprovalCardHTML(req ports.PermissionRequest, scriptsCheck tools.ScriptsDirCheck) string {
	var bldr strings.Builder
	if c.scope == writePermScopePlan {
		bldr.WriteString(i18n.Tf(c.lang, "perm.header_plan", c.taskID, html.EscapeString(c.project)))
	} else {
		bldr.WriteString(i18n.Tf(c.lang, "perm.header_chat", html.EscapeString(c.project)))
	}

	switch scriptsCheck.Kind {
	case tools.ScriptsDirOpExecute:
		bldr.WriteString(i18n.T(c.lang, "perm.kind_scripts_exec"))
	case tools.ScriptsDirOpWrite:
		bldr.WriteString(i18n.T(c.lang, "perm.kind_scripts_write"))
	default:
		bldr.WriteString(i18n.T(c.lang, "perm.kind_write"))
	}

	if strings.TrimSpace(req.ToolName) != "" {
		bldr.WriteString(i18n.Tf(c.lang, "perm.detail_tool", html.EscapeString(req.ToolName)))
	}
	target := scriptsCheck.Target
	if len(req.Paths) > 0 {
		target = strings.Join(req.Paths, ", ")
	}
	if strings.TrimSpace(target) != "" {
		bldr.WriteString(i18n.Tf(c.lang, "perm.detail_path", html.EscapeString(utils.TruncateString(target, 300))))
	}
	if strings.TrimSpace(req.Command) != "" {
		bldr.WriteString(i18n.Tf(c.lang, "perm.detail_cmd", html.EscapeString(utils.TruncateString(req.Command, 600))))
	}
	return bldr.String()
}

func onPermOnce(s ports.Session) error {
	return handlePermCallback(s, ports.PermissionAllowOnce, false, "perm.result_allowed_once", "perm.toast_allowed_once")
}

func onPermTurn(s ports.Session) error {
	return handlePermCallback(s, ports.PermissionAllowAlways, true, "perm.result_allowed_turn", "perm.toast_allowed_turn")
}

func onPermDeny(s ports.Session) error {
	return handlePermCallback(s, ports.PermissionDeny, false, "perm.result_denied", "perm.toast_denied")
}

func handlePermCallback(s ports.Session, decision ports.PermissionDecision, isTurn bool, suffixKey, toastKey string) error {
	lang := uiLang()
	cb := s.Callback()
	if cb == nil {
		return nil
	}
	id := strings.TrimSpace(cb.Payload)
	entry, exists := writeApprovals.get(id)
	if !exists || entry == nil || entry.done.Load() {
		return s.Respond(i18n.T(lang, "perm.toast_expired"))
	}
	if isTurn && !entry.allowTurnBtn {
		return s.Respond(i18n.T(lang, "perm.toast_turn_forbidden"))
	}

	winner, won := writeApprovals.resolve(id, decision)
	if !won || winner == nil {
		return s.Respond(i18n.T(lang, "perm.toast_expired"))
	}
	winner.updateCard(s.Messenger(), cb.Message, i18n.T(lang, suffixKey))
	return s.Respond(i18n.T(lang, toastKey))
}
