package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"bro-bot/internal/ports"
)

// ScriptsDirOpKind классифицирует операцию в каталоге пользовательских скриптов SCRIPTS_DIR.
type ScriptsDirOpKind int

const (
	// ScriptsDirOpNone означает, что операция не изменяет файлы и не запускает скрипты из SCRIPTS_DIR
	// (включая чистое чтение каталога/файлов и аналитический python3, только читающий из SCRIPTS_DIR).
	ScriptsDirOpNone ScriptsDirOpKind = iota
	// ScriptsDirOpWrite означает создание, редактирование, chmod или удаление файла внутри SCRIPTS_DIR.
	ScriptsDirOpWrite
	// ScriptsDirOpExecute означает запуск скрипта, расположенного внутри SCRIPTS_DIR.
	ScriptsDirOpExecute
)

// ScriptsDirCheck описывает результат проверки взаимодействия вызова с каталогом SCRIPTS_DIR.
type ScriptsDirCheck struct {
	Kind   ScriptsDirOpKind
	Target string
}

// WriteGuardrailResult описывает срабатывание жёсткого предохранителя при проверке записи/команды.
type WriteGuardrailResult struct {
	Blocked bool
	Reason  string
}

// ResolvePathWithSymlinks нормализует target относительно workDir и разыменовывает симлинки
// (включая висячие симлинки через os.Readlink), чтобы symlink-побег не обошёл проверку каталогов.
func ResolvePathWithSymlinks(target, workDir string) (string, error) {
	return resolvePathWithHops(target, workDir, 0)
}

func resolvePathWithHops(target, workDir string, hops int) (string, error) {
	if hops > 16 {
		return "", errors.New("too many symbolic link levels")
	}
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return "", errors.New("empty path")
	}
	p := trimmed
	if !filepath.IsAbs(p) {
		if strings.TrimSpace(workDir) == "" {
			return "", ErrWorkspaceNotSet
		}
		p = filepath.Join(workDir, p)
	}
	cleaned := filepath.Clean(p)
	cur := cleaned
	var tail []string
	for {
		realCur, err := filepath.EvalSymlinks(cur)
		if err == nil {
			parts := append([]string{realCur}, tail...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if info, lerr := os.Lstat(cur); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			linkDest, rerr := os.Readlink(cur)
			if rerr == nil {
				if !filepath.IsAbs(linkDest) {
					linkDest = filepath.Join(filepath.Dir(cur), linkDest)
				}
				parts := append([]string{linkDest}, tail...)
				return resolvePathWithHops(filepath.Join(parts...), "", hops+1)
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cleaned, nil
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
}

// IsSensitiveSystemPath сообщает, относится ли путь к системным каталогам ОС, .git или чувствительным конфигам,
// куда запись и удаление в режимах /chat и /plan всегда запрещены. На операции чтения не распространяется.
func IsSensitiveSystemPath(absPath string) bool {
	cleaned := filepath.Clean(absPath)
	if cleaned == "/" {
		return true
	}
	for _, prefix := range []string{
		"/etc", "/boot", "/sys", "/proc", "/usr", "/bin", "/sbin",
		"/lib", "/lib64", "/var/lib/systemd", "/lib/systemd",
	} {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+string(filepath.Separator)) {
			return true
		}
	}
	if strings.HasPrefix(cleaned, "/dev/") && cleaned != "/dev/null" {
		return true
	}
	for _, seg := range strings.Split(filepath.ToSlash(cleaned), "/") {
		if seg == ".git" || seg == ".ssh" {
			return true
		}
	}
	switch filepath.Base(cleaned) {
	case ".bashrc", ".zshrc", ".profile", ".bash_profile":
		return true
	}
	return false
}

// IsPathWithinRoots проверяет, что target (относительно workDir) после раскрытия симлинков
// находится внутри одного из корней roots.
func IsPathWithinRoots(target, workDir string, roots []string) bool {
	if len(roots) == 0 {
		return false
	}
	resolved, err := ResolvePathWithSymlinks(target, workDir)
	if err != nil {
		return false
	}
	return isResolvedWithinRoots(resolved, roots)
}

func isResolvedWithinRoots(resolved string, roots []string) bool {
	if resolved == "" {
		return false
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		realRoot, err := ResolvePathWithSymlinks(root, "")
		if err != nil || realRoot == "/" {
			continue
		}
		rel, relErr := filepath.Rel(realRoot, resolved)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func matchMatchingRoot(resolved string, roots []string) string {
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		realRoot, err := ResolvePathWithSymlinks(root, "")
		if err != nil || realRoot == "/" {
			continue
		}
		rel, relErr := filepath.Rel(realRoot, resolved)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return realRoot
		}
	}
	return ""
}

// IsWriteOrEditToolName сообщает, относится ли имя инструмента к операциям записи, правки или удаления файлов.
func IsWriteOrEditToolName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "write_file", "write_to_file", "write", "create_file",
		"edit_file", "replace_file_content", "multi_replace_file_content", "edit",
		"patch_file", "apply_patch", "delete_file", "remove_file", "rm":
		return true
	default:
		return false
	}
}

// IsMutatingWorkspaceTool сообщает, может ли встроенный инструмент модифицировать файлы или выполнять команды.
func IsMutatingWorkspaceTool(name string) bool {
	if IsWriteOrEditToolName(name) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "run_command", "bash", "shell", "execute_command", "terminal", "sh":
		return true
	default:
		return false
	}
}

// BuildAPIPermissionRequest преобразует вызов встроенного инструмента API-адаптера в ports.PermissionRequest.
func BuildAPIPermissionRequest(toolName string, args map[string]interface{}, workDir string) ports.PermissionRequest {
	req := ports.PermissionRequest{
		ToolName: strings.TrimSpace(toolName),
		WorkDir:  workDir,
		RawInput: args,
	}
	if IsWriteOrEditToolName(toolName) {
		req.Kind = "edit"
		if p := extractString(args, "path", "file_path", "TargetFile", "target", "AbsolutePath"); p != "" {
			req.Paths = []string{p}
			req.Title = p
		}
		return req
	}
	if IsMutatingWorkspaceTool(toolName) {
		req.Kind = "execute"
		req.Command = extractString(args, "command", "CommandLine", "cmd")
		req.Title = req.Command
	}
	if strings.EqualFold(toolName, "delete_file") || strings.EqualFold(toolName, "remove_file") {
		req.Kind = "delete"
	}
	return req
}

// IsControlledSession сообщает, выполняется ли сессия в контролируемом режиме (/chat или /plan).
func IsControlledSession(readOnly bool, systemPrompt string) bool {
	return readOnly || strings.Contains(systemPrompt, "РЕЖИМ ДИАЛОГА") || strings.Contains(systemPrompt, "CHAT MODE")
}

// AuthorizeAPIToolCall проверяет право вызова встроенного инструмента в API-адаптерах (agy/claude)
// и возвращает флаг readOnly для этого вызова и ошибку при запрете.
func AuthorizeAPIToolCall(ctx context.Context, handler ports.PermissionHandler, toolName string, args map[string]interface{}, workDir string, controlled bool) (bool, error) {
	if !controlled {
		return false, nil
	}
	if !IsMutatingWorkspaceTool(toolName) {
		return handler == nil, nil
	}
	if handler == nil {
		return true, ErrReadOnly
	}
	req := BuildAPIPermissionRequest(toolName, args, workDir)
	decision, reason := handler.DecidePermission(ctx, req)
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	if decision == ports.PermissionDeny {
		if strings.TrimSpace(reason) == "" {
			reason = "permission denied"
		}
		return true, fmt.Errorf("permission denied: %s", reason)
	}
	return false, nil
}

// CheckScriptsDirOperation проверяет, создаёт/изменяет ли вызов файл внутри scriptsDir
// либо запускает скрипт, расположенный в scriptsDir.
// Чтение файлов и аналитический python3, только читающий файлы в scriptsDir, возвращают ScriptsDirOpNone.
func CheckScriptsDirOperation(req ports.PermissionRequest, scriptsDir string) ScriptsDirCheck {
	if strings.TrimSpace(scriptsDir) == "" {
		return ScriptsDirCheck{}
	}
	roots := []string{scriptsDir}

	if IsWriteOrEditToolName(req.ToolName) || strings.EqualFold(req.Kind, "edit") || strings.EqualFold(req.Kind, "delete") {
		for _, p := range req.Paths {
			if IsPathWithinRoots(p, req.WorkDir, roots) {
				return ScriptsDirCheck{Kind: ScriptsDirOpWrite, Target: p}
			}
		}
	}

	if strings.TrimSpace(req.Command) != "" {
		return checkShellScriptsDirOperation(req.Command, req.WorkDir, scriptsDir, 0)
	}
	return ScriptsDirCheck{}
}

func checkShellScriptsDirOperation(cmdLine, workDir, scriptsDir string, depth int) ScriptsDirCheck {
	if depth > 4 || strings.TrimSpace(cmdLine) == "" {
		return ScriptsDirCheck{}
	}
	realScripts, err := ResolvePathWithSymlinks(scriptsDir, "")
	if err != nil {
		realScripts = filepath.Clean(scriptsDir)
	}

	segments, ok := parseShellSegmentsForPolicy(cmdLine)
	if !ok {
		// Fail-closed: неразобранная команда с упоминанием каталога скриптов требует подтверждения.
		if mentionsScriptsDir(cmdLine, workDir, realScripts) && !looksLikePureReadSnippet(cmdLine) {
			return ScriptsDirCheck{Kind: ScriptsDirOpExecute, Target: cmdLine}
		}
		return ScriptsDirCheck{}
	}

	var best ScriptsDirCheck
	consider := func(c ScriptsDirCheck) {
		if c.Kind > best.Kind {
			best = c
		}
	}

	cwd := workDir
	for _, seg := range segments {
		for _, sub := range seg.subCommands {
			consider(checkShellScriptsDirOperation(sub, cwd, scriptsDir, depth+1))
		}

		cmd, args, okCmd := stripEnvAndResolveCommand(seg.tokens)
		if !okCmd {
			for _, redir := range seg.outputRedirects {
				if redir != "" && redir != "/dev/null" && IsPathWithinRoots(redir, cwd, []string{realScripts}) {
					consider(ScriptsDirCheck{Kind: ScriptsDirOpWrite, Target: redir})
				}
			}
			continue
		}

		if cmd == "cd" {
			cwd = updateCwdFromCD(cwd, workDir, args)
			continue
		}

		// Проверка перенаправлений вывода в SCRIPTS_DIR.
		for _, redir := range seg.outputRedirects {
			if redir != "" && redir != "/dev/null" && IsPathWithinRoots(redir, cwd, []string{realScripts}) {
				consider(ScriptsDirCheck{Kind: ScriptsDirOpWrite, Target: redir})
			}
		}

		// Рекурсивная проверка оболочек и обёрток (bash -c, env, timeout, nohup, nice, xargs).
		if innerCmd, nestedArgs, wrapped := unwrapCommandWrapper(cmd, args); wrapped {
			if innerCmd == "__INLINE_SHELL__" && len(nestedArgs) > 0 {
				consider(checkShellScriptsDirOperation(nestedArgs[0], cwd, scriptsDir, depth+1))
			} else {
				rebuilt := reconstructSegmentCommand(innerCmd, nestedArgs)
				if rebuilt != "" {
					consider(checkShellScriptsDirOperation(rebuilt, cwd, scriptsDir, depth+1))
				}
			}
			if best.Kind == ScriptsDirOpExecute {
				return best
			}
			continue
		}

		// 1. Сам исполняемый файл находится в SCRIPTS_DIR.
		if strings.Contains(cmd, "/") || strings.HasPrefix(cmd, ".") {
			if IsPathWithinRoots(cmd, cwd, []string{realScripts}) {
				return ScriptsDirCheck{Kind: ScriptsDirOpExecute, Target: cmd}
			}
		}

		// 2. Аналитический python3 (-c / heredoc) или запуск .py-скрипта из SCRIPTS_DIR.
		if isPythonCommandName(filepath.Base(cmd)) {
			consider(checkPythonScriptsDir(seg, args, cwd, realScripts))
			if best.Kind == ScriptsDirOpExecute {
				return best
			}
			continue
		}

		// 3. Запуск скрипта через интерпретатор или source.
		if isScriptInterpreter(filepath.Base(cmd)) {
			if target, isExec := findExecutedScriptArg(filepath.Base(cmd), args, cwd, realScripts); isExec {
				return ScriptsDirCheck{Kind: ScriptsDirOpExecute, Target: target}
			}
			continue
		}

		// 4. Явная запись/изменение/удаление/chmod в SCRIPTS_DIR.
		for _, target := range extractCommandWriteTargets(filepath.Base(cmd), args, seg.outputRedirects) {
			if target == "-" || target == "/dev/null" {
				continue
			}
			if IsPathWithinRoots(target, cwd, []string{realScripts}) {
				consider(ScriptsDirCheck{Kind: ScriptsDirOpWrite, Target: target})
			}
		}
	}

	return best
}

func isScriptInterpreter(baseCmd string) bool {
	switch baseCmd {
	case "sh", "bash", "zsh", "dash", "ksh", "ash", "fish",
		"node", "deno", "bun", "ruby", "perl", "php", "lua",
		"source", ".", "awk", "gawk", "mawk":
		return true
	default:
		return false
	}
}

func findExecutedScriptArg(baseCmd string, args []string, cwd, realScripts string) (string, bool) {
	if baseCmd == "awk" || baseCmd == "gawk" || baseCmd == "mawk" {
		for i := 0; i < len(args); i++ {
			if (args[i] == "-f" || args[i] == "--file") && i+1 < len(args) {
				if IsPathWithinRoots(args[i+1], cwd, []string{realScripts}) {
					return args[i+1], true
				}
				i++
			} else if strings.HasPrefix(args[i], "-f") && len(args[i]) > 2 {
				p := args[i][2:]
				if IsPathWithinRoots(p, cwd, []string{realScripts}) {
					return p, true
				}
			}
		}
		return "", false
	}

	expectFile := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) && IsPathWithinRoots(args[i+1], cwd, []string{realScripts}) {
				return args[i+1], true
			}
			return "", false
		}
		if expectFile {
			if IsPathWithinRoots(arg, cwd, []string{realScripts}) {
				return arg, true
			}
			expectFile = false
			continue
		}
		if arg == "-c" || arg == "-e" || arg == "-r" {
			return "", false
		}
		if strings.HasPrefix(arg, "-") {
			if arg == "-o" || arg == "--rcfile" || arg == "--init-file" {
				i++
			}
			continue
		}
		if IsPathWithinRoots(arg, cwd, []string{realScripts}) {
			return arg, true
		}
		return "", false
	}
	return "", false
}

var (
	pyOpenWriteModeRE = regexp.MustCompile(`open\s*\([^,)]+,(?:\s*['"][rwaxbtU+]*[wax+][rwaxbtU+]*['"]|[^)]*\bmode\s*=\s*['"][rwaxbtU+]*[wax+][rwaxbtU+]*['"])`)
	pyWriteSignals    = []string{
		".write_text(", ".write_bytes(", ".unlink(", ".rename(", ".replace(", ".chmod(", ".mkdir(", ".rmdir(",
		"os.remove(", "os.unlink(", "os.chmod(", "os.chown(", "os.rename(", "os.replace(", "os.mkdir(", "os.makedirs(",
		"shutil.copy", "shutil.move", "shutil.rmtree", "pathlib.path(",
	}
	pyExecSignals = []string{
		"subprocess.", "os.system(", "os.popen(", "os.exec", "os.spawn", "pty.spawn(",
		"runpy.", "importlib.", "exec(open(", "exec(path(",
	}
)

func checkPythonScriptsDir(seg shellSegment, args []string, cwd, realScripts string) ScriptsDirCheck {
	var inlineCode []string
	var scriptPath string
	afterDoubleDash := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !afterDoubleDash && arg == "--" {
			afterDoubleDash = true
			continue
		}
		if !afterDoubleDash && arg == "-c" && i+1 < len(args) {
			inlineCode = append(inlineCode, args[i+1])
			break
		}
		if !afterDoubleDash && strings.HasPrefix(arg, "-c") && len(arg) > 2 {
			inlineCode = append(inlineCode, arg[2:])
			break
		}
		if !afterDoubleDash && (arg == "-m" || arg == "-W" || arg == "-X") {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if !afterDoubleDash && strings.HasPrefix(arg, "-") && arg != "-" {
			continue
		}
		if arg != "-" {
			scriptPath = arg
		}
		break
	}

	if scriptPath != "" && IsPathWithinRoots(scriptPath, cwd, []string{realScripts}) {
		return ScriptsDirCheck{Kind: ScriptsDirOpExecute, Target: scriptPath}
	}

	if len(inlineCode) == 0 && seg.hasHeredoc {
		inlineCode = append(inlineCode, seg.heredocs...)
	}
	if len(inlineCode) == 0 {
		return ScriptsDirCheck{}
	}

	joined := strings.Join(inlineCode, "\n")
	if !mentionsScriptsDir(joined, cwd, realScripts) {
		return ScriptsDirCheck{}
	}

	lower := strings.ToLower(joined)
	for _, sig := range pyExecSignals {
		if strings.Contains(lower, sig) {
			return ScriptsDirCheck{Kind: ScriptsDirOpExecute, Target: realScripts}
		}
	}
	if pyOpenWriteModeRE.MatchString(joined) {
		return ScriptsDirCheck{Kind: ScriptsDirOpWrite, Target: realScripts}
	}
	for _, sig := range pyWriteSignals {
		if strings.Contains(lower, sig) {
			if sig == "pathlib.path(" {
				continue
			}
			return ScriptsDirCheck{Kind: ScriptsDirOpWrite, Target: realScripts}
		}
	}

	// Чистое аналитическое чтение файлов внутри SCRIPTS_DIR разрешено.
	return ScriptsDirCheck{}
}

func mentionsScriptsDir(text, cwd, realScripts string) bool {
	if realScripts == "" {
		return false
	}
	cleanReal := filepath.Clean(realScripts)
	if strings.Contains(text, cleanReal) {
		return true
	}
	base := filepath.Base(cleanReal)
	if base == "" || cwd == "" {
		return false
	}
	// Проверяем, соответствует ли относительное имя каталога (например "scripts/") реальному SCRIPTS_DIR из cwd.
	candidate, err := ResolvePathWithSymlinks(base, cwd)
	if err == nil && candidate == cleanReal {
		if strings.Contains(text, base+"/") || strings.Contains(text, "'"+base+"'") || strings.Contains(text, `"`+base+`"`) {
			return true
		}
	}
	if IsPathWithinRoots(".", cwd, []string{cleanReal}) {
		return true
	}
	return false
}

func looksLikePureReadSnippet(cmd string) bool {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	for _, prefix := range []string{"ls ", "cat ", "head ", "tail ", "grep ", "rg ", "stat ", "file "} {
		if strings.HasPrefix(lower, prefix) && !strings.Contains(lower, ">") {
			return true
		}
	}
	return false
}

// CheckWriteGuardrail проверяет строгие предохранители, которые действуют в /chat и /plan всегда
// (независимо от режима deny/ask/allow, CHAT_WRITE_PATHS и «На весь ход»).
func CheckWriteGuardrail(req ports.PermissionRequest, busyProjectDirs []string) WriteGuardrailResult {
	if srv := extractStringFromAnyMap(req.RawInput, "ServerName", "server_name", "server", "mcpServer", "mcp_server"); srv != "" {
		sl := strings.ToLower(strings.TrimSpace(srv))
		if sl != "bro_bot" && sl != "bro-bot" {
			return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("foreign MCP server %q is forbidden", srv)}
		}
	}

	if strings.EqualFold(req.ToolName, "invoke_subagent") {
		subType := strings.ToLower(extractStringFromAnyMap(req.RawInput, "TypeName", "type_name", "subagent_type", "type", "agent"))
		if subType != "research" {
			return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("subagent %q is forbidden in chat/plan mode", subType)}
		}
	}

	if IsWriteOrEditToolName(req.ToolName) || strings.EqualFold(req.Kind, "edit") || strings.EqualFold(req.Kind, "delete") {
		for _, p := range req.Paths {
			resolved, err := ResolvePathWithSymlinks(p, req.WorkDir)
			if err != nil {
				continue
			}
			if IsSensitiveSystemPath(resolved) {
				return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("writing to protected system/git path %q is forbidden", p)}
			}
			if busy := matchMatchingRoot(resolved, busyProjectDirs); busy != "" {
				return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("cannot modify %q while another task is running in %q", p, filepath.Base(busy))}
			}
		}
	}

	if strings.TrimSpace(req.Command) != "" {
		return CheckMutatingShellGuardrail(req.Command, req.WorkDir, busyProjectDirs)
	}
	return WriteGuardrailResult{}
}

// CheckMutatingShellGuardrail проверяет shell-команду на запрещённые в /chat и /plan мутации:
// sudo/su/doas, мутирующий systemctl/service, git/gh мутации, rm -rf, find -delete,
// запись в системные пути / .git / ~/.ssh и запись в проекты с активными задачами.
func CheckMutatingShellGuardrail(cmdLine, workDir string, busyProjectDirs []string) WriteGuardrailResult {
	return checkMutatingShellGuardrail(cmdLine, workDir, busyProjectDirs, 0)
}

func checkMutatingShellGuardrail(cmdLine, workDir string, busyProjectDirs []string, depth int) WriteGuardrailResult {
	if depth > 4 || strings.TrimSpace(cmdLine) == "" {
		return WriteGuardrailResult{}
	}

	segments, ok := parseShellSegmentsForPolicy(cmdLine)
	if !ok {
		return guardrailRawFallback(cmdLine)
	}

	cwd := workDir
	for _, seg := range segments {
		for _, sub := range seg.subCommands {
			if res := checkMutatingShellGuardrail(sub, cwd, busyProjectDirs, depth+1); res.Blocked {
				return res
			}
		}

		// Всегда проверяем цели перенаправления вывода, даже у read-only команд.
		for _, redir := range seg.outputRedirects {
			if redir == "" || redir == "/dev/null" {
				continue
			}
			if res := checkWriteTargetGuardrail(redir, cwd, busyProjectDirs); res.Blocked {
				return res
			}
		}

		// Безопасная read-only команда (включая systemctl status, git status, cat /etc/...,
		// аналитический python3) не блокируется, если её редиректы ведут только в /dev/null или /tmp.
		if segRedirectsSafeForReadOnly(seg.outputRedirects) && isSafeReadOnlySegment(seg) {
			cmd, args, okCmd := stripEnvAndResolveCommand(seg.tokens)
			if okCmd && isPythonCommandName(filepath.Base(cmd)) {
				if res := checkInlinePythonGuardrail(seg, args, cwd, busyProjectDirs); res.Blocked {
					return res
				}
			}
			continue
		}

		cmd, args, okCmd := stripEnvAndResolveCommand(seg.tokens)
		if !okCmd {
			continue
		}

		if cmd == "cd" {
			cwd = updateCwdFromCD(cwd, workDir, args)
			continue
		}

		baseCmd := filepath.Base(cmd)

		// Обёртки и вложенный shell проверяются рекурсивно.
		if innerCmd, nestedArgs, wrapped := unwrapCommandWrapper(cmd, args); wrapped {
			if innerCmd == "__INLINE_SHELL__" && len(nestedArgs) > 0 {
				if res := checkMutatingShellGuardrail(nestedArgs[0], cwd, busyProjectDirs, depth+1); res.Blocked {
					return res
				}
			} else if rebuilt := reconstructSegmentCommand(innerCmd, nestedArgs); rebuilt != "" {
				if res := checkMutatingShellGuardrail(rebuilt, cwd, busyProjectDirs, depth+1); res.Blocked {
					return res
				}
			}
			continue
		}

		// Проверка python3 (например open('/etc/...') на запись или subprocess с sudo).
		if isPythonCommandName(baseCmd) {
			if res := checkInlinePythonGuardrail(seg, args, cwd, busyProjectDirs); res.Blocked {
				return res
			}
		}

		// 1. Эскалация привилегий.
		switch baseCmd {
		case "sudo", "su", "doas", "pkexec", "visudo":
			return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("privilege escalation via %q is forbidden in chat/plan mode", baseCmd)}
		}

		// 2. Мутирующий systemctl и управление ОС/дисками.
		switch {
		case baseCmd == "systemctl":
			if !isSafeReadOnlySystemctl(args) {
				return WriteGuardrailResult{Blocked: true, Reason: "mutating systemctl command is forbidden in chat/plan mode"}
			}
			continue
		case baseCmd == "service", baseCmd == "shutdown", baseCmd == "reboot", baseCmd == "poweroff",
			baseCmd == "halt", baseCmd == "init", baseCmd == "telinit", baseCmd == "mount", baseCmd == "umount",
			baseCmd == "fdisk", baseCmd == "parted", baseCmd == "shred", baseCmd == "wipefs",
			baseCmd == "mkfs" || strings.HasPrefix(baseCmd, "mkfs."):
			return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("system command %q is forbidden in chat/plan mode", baseCmd)}
		case baseCmd == "dd":
			for _, a := range args {
				if strings.HasPrefix(a, "of=") {
					target := strings.TrimPrefix(a, "of=")
					if res := checkWriteTargetGuardrail(target, cwd, busyProjectDirs); res.Blocked {
						return res
					}
				}
			}
		}

		// 3. Git и GH мутации.
		if baseCmd == "git" {
			if !isSafeReadOnlyGit(args) {
				return WriteGuardrailResult{Blocked: true, Reason: "git mutation is forbidden in chat/plan mode"}
			}
			continue
		}
		if baseCmd == "gh" {
			if !isSafeReadOnlyGH(args) {
				return WriteGuardrailResult{Blocked: true, Reason: "mutating gh command is forbidden in chat/plan mode"}
			}
			continue
		}

		// 4. Опасный rm и find -delete/-exec rm.
		if baseCmd == "rm" {
			if res := checkDestructiveRM(args, cwd, busyProjectDirs); res.Blocked {
				return res
			}
		}
		if baseCmd == "find" && isDestructiveFind(args) {
			return WriteGuardrailResult{Blocked: true, Reason: "destructive find (-delete / -exec rm) is forbidden in chat/plan mode"}
		}

		// 5. Проверка целевых путей записи в системные каталоги и проекты с активными задачами.
		targets := extractCommandWriteTargets(baseCmd, args, seg.outputRedirects)
		for _, target := range targets {
			if target == "" || target == "-" || target == "/dev/null" {
				continue
			}
			if res := checkWriteTargetGuardrail(target, cwd, busyProjectDirs); res.Blocked {
				return res
			}
		}

		// 6. Если рабочий каталог команды лежит внутри проекта с другой активной задачей,
		// блокируем любую мутирующую команду, если не доказано, что все цели записи вне этого проекта.
		if cwd != "" {
			if realCwd, err := ResolvePathWithSymlinks(cwd, ""); err == nil {
				if busyRoot := matchMatchingRoot(realCwd, busyProjectDirs); busyRoot != "" {
					if !allTargetsExplicitlyOutsideRoot(targets, cwd, busyRoot) {
						return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("cannot run mutating command in %q while another task is running there", filepath.Base(busyRoot))}
					}
				}
			}
		}
	}

	return WriteGuardrailResult{}
}

func checkWriteTargetGuardrail(target, cwd string, busyProjectDirs []string) WriteGuardrailResult {
	resolved, err := ResolvePathWithSymlinks(target, cwd)
	if err != nil {
		return WriteGuardrailResult{}
	}
	if IsSensitiveSystemPath(resolved) {
		return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("writing to protected system/git path %q is forbidden", target)}
	}
	if busy := matchMatchingRoot(resolved, busyProjectDirs); busy != "" {
		return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("cannot modify %q while another task is running in %q", target, filepath.Base(busy))}
	}
	return WriteGuardrailResult{}
}

func checkDestructiveRM(args []string, cwd string, busyProjectDirs []string) WriteGuardrailResult {
	stopFlags := false
	for _, arg := range args {
		if !stopFlags && arg == "--" {
			stopFlags = true
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "-") {
			if arg == "--recursive" || isShortFlagWithChars(arg, "rR") {
				return WriteGuardrailResult{Blocked: true, Reason: "recursive rm (-r/-rf) is forbidden in chat/plan mode"}
			}
			continue
		}
		clean := strings.TrimSpace(arg)
		if clean == "/" || clean == "/*" || clean == "~" || clean == "$HOME" || clean == "." || clean == ".." || clean == "*" {
			return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("destructive rm target %q is forbidden", arg)}
		}
		if res := checkWriteTargetGuardrail(clean, cwd, busyProjectDirs); res.Blocked {
			return res
		}
	}
	return WriteGuardrailResult{}
}

func isDestructiveFind(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "-delete" {
			return true
		}
		if (args[i] == "-exec" || args[i] == "-execdir" || args[i] == "-ok" || args[i] == "-okdir") && i+1 < len(args) {
			target := filepath.Base(args[i+1])
			if target == "rm" || target == "rmdir" || target == "shred" || target == "unlink" {
				return true
			}
		}
	}
	return false
}

func checkInlinePythonGuardrail(seg shellSegment, args []string, cwd string, busyProjectDirs []string) WriteGuardrailResult {
	var code string
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" && i+1 < len(args) {
			code = args[i+1]
			break
		}
		if strings.HasPrefix(args[i], "-c") && len(args[i]) > 2 {
			code = args[i][2:]
			break
		}
	}
	if code == "" && seg.hasHeredoc {
		code = strings.Join(seg.heredocs, "\n")
	}
	if code == "" {
		return WriteGuardrailResult{}
	}
	lower := strings.ToLower(code)
	for _, sig := range []string{"sudo ", "systemctl restart", "systemctl stop", "systemctl start", "rm -rf", "rm -fr"} {
		if strings.Contains(lower, sig) {
			return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("forbidden command %q inside inline python", strings.TrimSpace(sig))}
		}
	}
	if pyOpenWriteModeRE.MatchString(code) || strings.Contains(lower, ".write_text(") || strings.Contains(lower, ".write_bytes(") {
		for _, p := range extractQuotedPaths(code) {
			if res := checkWriteTargetGuardrail(p, cwd, busyProjectDirs); res.Blocked {
				return res
			}
		}
	}
	return WriteGuardrailResult{}
}

var quotedPathRE = regexp.MustCompile(`['"](/[^'"\s]+)['"]`)

func extractQuotedPaths(code string) []string {
	matches := quotedPathRE.FindAllStringSubmatch(code, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			out = append(out, m[1])
		}
	}
	return out
}

func guardrailRawFallback(cmdLine string) WriteGuardrailResult {
	lower := strings.ToLower(cmdLine)
	for _, needle := range []string{"sudo ", "systemctl restart", "systemctl stop", "systemctl start", "git push", "git commit", "rm -rf", "rm -fr"} {
		if strings.Contains(lower, needle) {
			return WriteGuardrailResult{Blocked: true, Reason: fmt.Sprintf("forbidden operation %q in unparseable shell command", strings.TrimSpace(needle))}
		}
	}
	return WriteGuardrailResult{}
}

func segRedirectsSafeForReadOnly(redirs []string) bool {
	for _, r := range redirs {
		if !isSafeReadOnlyOutputRedirectTarget(r) {
			return false
		}
	}
	return true
}

// IsAutoAllowedWrite реализует fail-closed проверку: разрешает запись без интерактивной кнопки
// в режиме ask только тогда, когда все цели записи однозначно известны, лежат внутри
// allowedRoots (или /tmp), не пересекаются с SCRIPTS_DIR, системными путями и активными задачами.
func IsAutoAllowedWrite(req ports.PermissionRequest, allowedRoots, busyProjectDirs []string, scriptsDir string) bool {
	if len(allowedRoots) == 0 {
		return false
	}
	if CheckScriptsDirOperation(req, scriptsDir).Kind != ScriptsDirOpNone {
		return false
	}
	var scriptsSlice []string
	if strings.TrimSpace(scriptsDir) != "" {
		scriptsSlice = []string{scriptsDir}
	}

	isAllowedTarget := func(target, cwd string) bool {
		if target == "/dev/null" {
			return true
		}
		if strings.ContainsAny(target, "$*?[~") {
			return false
		}
		resolved, err := ResolvePathWithSymlinks(target, cwd)
		if err != nil {
			return false
		}
		if IsSensitiveSystemPath(resolved) {
			return false
		}
		if len(scriptsSlice) > 0 && isResolvedWithinRoots(resolved, scriptsSlice) {
			return false
		}
		if matchMatchingRoot(resolved, busyProjectDirs) != "" {
			return false
		}
		return isResolvedWithinRoots(resolved, allowedRoots) || isResolvedWithinRoots(resolved, []string{"/tmp"})
	}

	if IsWriteOrEditToolName(req.ToolName) || strings.EqualFold(req.Kind, "edit") || strings.EqualFold(req.Kind, "delete") {
		if len(req.Paths) == 0 {
			return false
		}
		for _, p := range req.Paths {
			if !isAllowedTarget(p, req.WorkDir) {
				return false
			}
		}
		return true
	}

	if strings.TrimSpace(req.Command) == "" {
		return false
	}

	segments, ok := parseShellSegmentsForPolicy(req.Command)
	if !ok {
		return false
	}

	cwd := req.WorkDir
	sawWriteTarget := false
	for _, seg := range segments {
		if seg.hasUnsafeSubst || len(seg.subCommands) > 0 {
			return false
		}

		cmd, args, okCmd := stripEnvAndResolveCommand(seg.tokens)
		if !okCmd {
			return false
		}
		if cmd == "cd" {
			if len(args) != 1 || strings.ContainsAny(args[0], "$*?[~") {
				return false
			}
			cwd = updateCwdFromCD(cwd, req.WorkDir, args)
			continue
		}

		baseCmd := filepath.Base(cmd)

		// Проверяем все перенаправления вывода сегмента.
		for _, redir := range seg.outputRedirects {
			if redir == "" {
				return false
			}
			if !isAllowedTarget(redir, cwd) {
				return false
			}
			if redir != "/dev/null" {
				sawWriteTarget = true
			}
		}

		// Для curl/wget с записью на диск (-o/-O) извлекаем цели до проверки read-only сегмента,
		// чтобы путь записи в /tmp или allowedRoots учитывался в sawWriteTarget.
		if targets, known := extractAutoAllowableCommandTargets(baseCmd, args, cwd, seg.outputRedirects); known && len(targets) > 0 {
			for _, t := range targets {
				if !isAllowedTarget(t, cwd) {
					return false
				}
				if t != "/dev/null" {
					sawWriteTarget = true
				}
			}
			continue
		}

		// Если команда сама по себе read-only без явных целей записи, достаточно проверки редиректов выше.
		if isSafeReadOnlySegment(seg) {
			continue
		}

		return false
	}

	return sawWriteTarget
}

func extractAutoAllowableCommandTargets(baseCmd string, args []string, cwd string, redirs []string) ([]string, bool) {
	switch baseCmd {
	case "mkdir", "touch", "chmod":
		var out []string
		stopFlags := false
		modeNext := false
		for i, a := range args {
			if !stopFlags && a == "--" {
				stopFlags = true
				continue
			}
			if modeNext {
				modeNext = false
				continue
			}
			if !stopFlags && strings.HasPrefix(a, "-") {
				if baseCmd == "mkdir" && (a == "-m" || a == "--mode") {
					modeNext = true
				}
				continue
			}
			// Для chmod первый позиционный аргумент без '/' — это права (755, +x), а не путь.
			if baseCmd == "chmod" && len(out) == 0 && i == firstNonFlagIdx(args) && !strings.Contains(a, "/") {
				continue
			}
			out = append(out, a)
		}
		return out, len(out) > 0

	case "tee":
		var out []string
		stopFlags := false
		for _, a := range args {
			if !stopFlags && a == "--" {
				stopFlags = true
				continue
			}
			if !stopFlags && strings.HasPrefix(a, "-") {
				continue
			}
			out = append(out, a)
		}
		out = append(out, redirs...)
		return out, len(out) > 0

	case "cp", "mv", "install":
		targets := extractCommandWriteTargets(baseCmd, args, redirs)
		return targets, len(targets) > 0

	case "cat", "echo", "printf":
		if len(redirs) > 0 {
			return redirs, true
		}
		return nil, false

	case "curl":
		return extractCurlDownloadTargets(args, cwd)

	case "wget":
		return extractWgetDownloadTargets(args)

	default:
		return nil, false
	}
}

func firstNonFlagIdx(args []string) int {
	for i, a := range args {
		if a == "--" {
			return i + 1
		}
		if !strings.HasPrefix(a, "-") {
			return i
		}
	}
	return len(args)
}

func extractCurlDownloadTargets(args []string, cwd string) ([]string, bool) {
	var targets []string
	var urls []string
	hasRemoteName := false
	stopFlags := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !stopFlags && arg == "--" {
			stopFlags = true
			continue
		}
		if stopFlags || !strings.HasPrefix(arg, "-") || arg == "-" {
			if !isSafeHTTPURL(arg) {
				return nil, false
			}
			urls = append(urls, arg)
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, val, hasEq := strings.Cut(arg, "=")
			consumeVal := func() (string, bool) {
				if hasEq {
					return val, true
				}
				if i+1 >= len(args) {
					return "", false
				}
				i++
				return args[i], true
			}
			switch name {
			case "--silent", "--show-error", "--fail", "--fail-with-body", "--location",
				"--include", "--head", "--verbose", "--insecure", "--compressed",
				"--globoff", "--no-buffer", "--http1.1", "--http2", "--ipv4", "--ipv6",
				"--no-progress-meter", "--get", "--create-dirs":
			case "--remote-name":
				hasRemoteName = true
			case "--request":
				v, ok := consumeVal()
				if !ok || !isSafeReadOnlyHTTPMethod(v) {
					return nil, false
				}
			case "--output", "--dump-header":
				v, ok := consumeVal()
				if !ok {
					return nil, false
				}
				if v != "-" {
					targets = append(targets, v)
				}
			case "--url":
				v, ok := consumeVal()
				if !ok || !isSafeHTTPURL(v) {
					return nil, false
				}
				urls = append(urls, v)
			case "--header", "--user-agent", "--referer", "--max-time", "--connect-timeout",
				"--retry", "--retry-delay", "--retry-max-time", "--write-out", "--range",
				"--cacert", "--capath", "--data-urlencode":
				if _, ok := consumeVal(); !ok {
					return nil, false
				}
			default:
				// Не разрешаем POST/upload флаги (-d/--data/-F) без подтверждения кнопкой.
				return nil, false
			}
			continue
		}

		for j := 1; j < len(arg); j++ {
			c := arg[j]
			switch c {
			case 's', 'S', 'f', 'L', 'i', 'I', 'v', 'k', '4', '6', 'g', 'N', 'q', '0', 'G':
			case 'O':
				hasRemoteName = true
			case 'H', 'A', 'e', 'm', 'w', 'r', 'X', 'o', 'D':
				var v string
				if j+1 < len(arg) {
					v = arg[j+1:]
					j = len(arg)
				} else if i+1 < len(args) {
					i++
					v = args[i]
				} else {
					return nil, false
				}
				switch c {
				case 'X':
					if !isSafeReadOnlyHTTPMethod(v) {
						return nil, false
					}
				case 'o', 'D':
					if v != "-" {
						targets = append(targets, v)
					}
				}
				j = len(arg)
			default:
				return nil, false
			}
		}
	}

	if len(urls) == 0 {
		return nil, false
	}
	if hasRemoteName {
		if strings.TrimSpace(cwd) == "" {
			return nil, false
		}
		targets = append(targets, filepath.Join(cwd, "remote-download"))
	}
	return targets, len(targets) > 0
}

func extractWgetDownloadTargets(args []string) ([]string, bool) {
	var targets []string
	var urls []string
	stopFlags := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !stopFlags && arg == "--" {
			stopFlags = true
			continue
		}
		if stopFlags || !strings.HasPrefix(arg, "-") || arg == "-" {
			if !isSafeHTTPURL(arg) {
				return nil, false
			}
			urls = append(urls, arg)
			continue
		}
		if arg == "-nv" {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, val, hasEq := strings.Cut(arg, "=")
			consumeVal := func() (string, bool) {
				if hasEq {
					return val, true
				}
				if i+1 >= len(args) {
					return "", false
				}
				i++
				return args[i], true
			}
			switch name {
			case "--quiet", "--server-response", "--no-verbose", "--no-check-certificate",
				"--content-on-error", "--inet4-only", "--inet6-only", "--no-hsts":
			case "--output-document", "--directory-prefix", "--output-file", "--append-output":
				v, ok := consumeVal()
				if !ok {
					return nil, false
				}
				if v != "-" {
					targets = append(targets, v)
				}
			case "--method":
				v, ok := consumeVal()
				if !ok || !isSafeReadOnlyHTTPMethod(v) {
					return nil, false
				}
			case "--timeout", "--connect-timeout", "--read-timeout", "--tries",
				"--user-agent", "--header", "--ca-certificate", "--ca-directory":
				if _, ok := consumeVal(); !ok {
					return nil, false
				}
			default:
				return nil, false
			}
			continue
		}

		for j := 1; j < len(arg); j++ {
			c := arg[j]
			switch c {
			case 'q', 'S', '4', '6':
			case 'O', 'P', 'T', 't', 'U', 'o', 'a':
				var v string
				if j+1 < len(arg) {
					v = arg[j+1:]
					j = len(arg)
				} else if i+1 < len(args) {
					i++
					v = args[i]
				} else {
					return nil, false
				}
				if (c == 'O' || c == 'P' || c == 'o' || c == 'a') && v != "-" {
					targets = append(targets, v)
				}
				j = len(arg)
			default:
				return nil, false
			}
		}
	}

	if len(urls) == 0 {
		return nil, false
	}
	return targets, len(targets) > 0
}

func extractCommandWriteTargets(baseCmd string, args, redirs []string) []string {
	var out []string
	out = append(out, redirs...)

	switch baseCmd {
	case "mkdir", "touch", "tee":
		for _, a := range args {
			if a == "--" || strings.HasPrefix(a, "-") {
				continue
			}
			out = append(out, a)
		}
	case "chmod", "chown":
		posIdx := 0
		for _, a := range args {
			if a == "--" || strings.HasPrefix(a, "-") {
				continue
			}
			if posIdx > 0 {
				out = append(out, a)
			}
			posIdx++
		}
	case "cp", "install", "ln":
		var pos []string
		for i := 0; i < len(args); i++ {
			a := args[i]
			if a == "-t" || a == "--target-directory" {
				if i+1 < len(args) {
					out = append(out, args[i+1])
					i++
				}
				continue
			}
			if strings.HasPrefix(a, "--target-directory=") {
				out = append(out, strings.TrimPrefix(a, "--target-directory="))
				continue
			}
			if a == "--" || strings.HasPrefix(a, "-") {
				continue
			}
			pos = append(pos, a)
		}
		if len(pos) > 0 {
			out = append(out, pos[len(pos)-1])
		}
	case "mv":
		for i := 0; i < len(args); i++ {
			a := args[i]
			if a == "-t" || a == "--target-directory" {
				if i+1 < len(args) {
					out = append(out, args[i+1])
					i++
				}
				continue
			}
			if a == "--" || strings.HasPrefix(a, "-") {
				continue
			}
			out = append(out, a)
		}
	case "rm", "rmdir", "unlink", "shred":
		for _, a := range args {
			if a == "--" || strings.HasPrefix(a, "-") {
				continue
			}
			out = append(out, a)
		}
	case "curl":
		for i := 0; i < len(args); i++ {
			a := args[i]
			if (a == "-o" || a == "--output") && i+1 < len(args) {
				out = append(out, args[i+1])
				i++
			} else if strings.HasPrefix(a, "--output=") {
				out = append(out, strings.TrimPrefix(a, "--output="))
			} else if strings.HasPrefix(a, "-o") && len(a) > 2 {
				out = append(out, a[2:])
			}
		}
	case "wget":
		for i := 0; i < len(args); i++ {
			a := args[i]
			if (a == "-O" || a == "--output-document" || a == "-P" || a == "--directory-prefix") && i+1 < len(args) {
				out = append(out, args[i+1])
				i++
			} else if strings.HasPrefix(a, "--output-document=") {
				out = append(out, strings.TrimPrefix(a, "--output-document="))
			} else if strings.HasPrefix(a, "--directory-prefix=") {
				out = append(out, strings.TrimPrefix(a, "--directory-prefix="))
			} else if (strings.HasPrefix(a, "-O") || strings.HasPrefix(a, "-P")) && len(a) > 2 {
				out = append(out, a[2:])
			}
		}
	}
	return out
}

func allTargetsExplicitlyOutsideRoot(targets []string, cwd, busyRoot string) bool {
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if t == "/dev/null" {
			continue
		}
		if strings.TrimSpace(t) == "" || strings.ContainsAny(t, "$*?[~") {
			return false
		}
		resolved, err := ResolvePathWithSymlinks(t, cwd)
		if err != nil {
			return false
		}
		if isResolvedWithinRoots(resolved, []string{busyRoot}) {
			return false
		}
	}
	return true
}

func stripEnvAndResolveCommand(tokens []string) (cmd string, args []string, ok bool) {
	idx := 0
	for idx < len(tokens) {
		if _, _, isAssign := splitEnvAssignment(tokens[idx]); !isAssign {
			break
		}
		idx++
	}
	if idx >= len(tokens) || strings.TrimSpace(tokens[idx]) == "" {
		return "", nil, false
	}
	return tokens[idx], tokens[idx+1:], true
}

func updateCwdFromCD(curCwd, baseWorkDir string, args []string) string {
	if len(args) == 0 {
		return baseWorkDir
	}
	target := strings.TrimSpace(args[0])
	if target == "" || target == "-" || strings.ContainsAny(target, "$*?[~") {
		return curCwd
	}
	resolved, err := ResolvePathWithSymlinks(target, curCwd)
	if err != nil {
		return curCwd
	}
	return resolved
}

func unwrapCommandWrapper(cmd string, args []string) (innerCmd string, nestedArgs []string, wrapped bool) {
	base := filepath.Base(cmd)
	switch base {
	case "bash", "sh", "zsh", "dash", "ksh":
		for i := 0; i < len(args); i++ {
			a := args[i]
			if a == "--" {
				break
			}
			if a == "-c" || a == "-lc" || a == "-ec" {
				if i+1 < len(args) {
					return "__INLINE_SHELL__", []string{args[i+1]}, true
				}
				return "", nil, false
			}
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a[1:], 'c') {
				if i+1 < len(args) {
					return "__INLINE_SHELL__", []string{args[i+1]}, true
				}
				return "", nil, false
			}
		}
		return "", nil, false

	case "env":
		i := 0
		for i < len(args) {
			a := args[i]
			if a == "--" {
				i++
				break
			}
			if a == "-u" || a == "--unset" || a == "-C" || a == "--chdir" || a == "-S" || a == "--split-string" {
				i += 2
				continue
			}
			if strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
				i++
				continue
			}
			break
		}
		if i < len(args) {
			return args[i], args[i+1:], true
		}
		return "", nil, false

	case "command":
		i := 0
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			if args[i] == "-v" || args[i] == "-V" {
				return "", nil, false
			}
			i++
		}
		if i < len(args) {
			return args[i], args[i+1:], true
		}
		return "", nil, false

	case "nohup":
		if len(args) > 0 {
			return args[0], args[1:], true
		}
		return "", nil, false

	case "nice":
		i := 0
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			if args[i] == "-n" && i+1 < len(args) {
				i += 2
				continue
			}
			i++
		}
		if i < len(args) {
			return args[i], args[i+1:], true
		}
		return "", nil, false

	case "timeout":
		i := 0
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			if (args[i] == "-k" || args[i] == "-s" || args[i] == "--kill-after" || args[i] == "--signal") && i+1 < len(args) {
				i += 2
				continue
			}
			i++
		}
		if i+1 < len(args) {
			return args[i+1], args[i+2:], true
		}
		return "", nil, false

	case "xargs":
		i := 0
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			switch args[i] {
			case "-I", "-L", "-n", "-P", "-d", "-s", "-E":
				i += 2
			default:
				i++
			}
		}
		if i < len(args) {
			return args[i], args[i+1:], true
		}
		return "", nil, false

	default:
		return "", nil, false
	}
}

func reconstructSegmentCommand(cmd string, args []string) string {
	if cmd == "" {
		return ""
	}
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, shellQuoteArg(cmd))
	for _, a := range args {
		parts = append(parts, shellQuoteArg(a))
	}
	return strings.Join(parts, " ")
}

func shellQuoteArg(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`()<>|&;") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func extractStringFromAnyMap(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}
	for _, v := range m {
		if nested, ok := v.(map[string]any); ok {
			if found := extractStringFromAnyMap(nested, keys...); found != "" {
				return found
			}
		}
	}
	return ""
}
