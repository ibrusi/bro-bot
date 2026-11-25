package acp

import (
	"path/filepath"
	"regexp"
	"strings"
)

const maxShellCommandBytes = 4096

var sedPrintScriptRE = regexp.MustCompile(`^[0-9,$;p\s]+$`)

// IsSafeReadOnlyShellCall проверяет, является ли запрос разрешения ACP вызовом
// shell-инструмента с исключительно безопасными read-only командами.
func IsSafeReadOnlyShellCall(call ToolCallPermission) bool {
	if srv := firstStringRecursive(0, []string{"ServerName", "server_name", "server", "mcpServer", "mcp_server"}, call.RawInput, call.Args, call.Meta); srv != "" {
		return false
	}
	if !isShellExecutionCall(call) {
		return false
	}
	cmd := extractShellCommand(call)
	if cmd == "" {
		return false
	}
	return IsSafeReadOnlyShellCommand(cmd)
}

func isShellExecutionCall(call ToolCallPermission) bool {
	if isShellToolName(call.Name) || isShellToolName(call.ToolName) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(call.Kind), "execute") {
		return false
	}
	noExplicitName := strings.TrimSpace(call.Name) == "" && strings.TrimSpace(call.ToolName) == ""
	return noExplicitName || isShellToolName(call.Title)
}

func isShellToolName(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "run_command", "bash", "shell", "execute_command", "terminal", "sh":
		return true
	default:
		return false
	}
}

func extractShellCommand(call ToolCallPermission) string {
	if cmd := firstStringRecursive(0, []string{"CommandLine", "command_line", "command", "cmd"}, call.RawInput, call.Args, call.Meta); cmd != "" {
		return cmd
	}
	title := strings.TrimSpace(call.Title)
	if title == "" || isShellToolName(title) {
		return ""
	}
	return title
}

// IsSafeReadOnlyShellCommand разбирает shell-строку с учётом кавычек и операторов
// и возвращает true только тогда, когда все сегменты состоят из разрешённых read-only команд
// без подстановок команд, подоболочек и перенаправлений вывода в файлы.
func IsSafeReadOnlyShellCommand(cmdLine string) bool {
	trimmed := strings.TrimSpace(cmdLine)
	if trimmed == "" || len(trimmed) > maxShellCommandBytes {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		b := trimmed[i]
		if b == '\r' || b == 0x7f || (b < 0x20 && b != '\t' && b != '\n') {
			return false
		}
	}
	segments, ok := parseShellSegments(trimmed)
	if !ok || len(segments) == 0 {
		return false
	}
	for _, seg := range segments {
		if !isSafeReadOnlySegment(seg) {
			return false
		}
	}
	return true
}

func isSepOrSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == ';' || b == '|' || b == '&'
}

func parseShellSegments(s string) ([][]string, bool) {
	var segments [][]string
	var curSeg []string
	var curTok strings.Builder
	hasTok := false
	needSegment := false
	inSingle := false
	inDouble := false

	flushTok := func() {
		if hasTok {
			curSeg = append(curSeg, curTok.String())
			curTok.Reset()
			hasTok = false
		}
	}

	flushSeg := func(requireNonEmpty bool) bool {
		flushTok()
		if len(curSeg) == 0 {
			return !requireNonEmpty
		}
		segments = append(segments, curSeg)
		curSeg = nil
		return true
	}

	n := len(s)
	for i := 0; i < n; i++ {
		ch := s[i]

		if inSingle {
			if ch == '\'' {
				inSingle = false
			} else {
				curTok.WriteByte(ch)
			}
			continue
		}

		if inDouble {
			switch ch {
			case '"':
				inDouble = false
			case '`':
				return nil, false
			case '$':
				if i+1 < n && s[i+1] == '(' {
					return nil, false
				}
				curTok.WriteByte(ch)
			case '\\':
				if i+1 >= n {
					return nil, false
				}
				next := s[i+1]
				if next == '"' || next == '\\' || next == '$' || next == '`' {
					curTok.WriteByte(next)
					i++
				} else if next == '\n' {
					i++
				} else {
					curTok.WriteByte(ch)
					curTok.WriteByte(next)
					i++
				}
			default:
				curTok.WriteByte(ch)
			}
			continue
		}

		// Вне кавычек.
		switch ch {
		case ' ', '\t':
			flushTok()
			continue
		case '\n':
			if !flushSeg(false) {
				return nil, false
			}
			continue
		case '\'':
			inSingle = true
			hasTok = true
			needSegment = false
			continue
		case '"':
			inDouble = true
			hasTok = true
			needSegment = false
			continue
		case '\\':
			if i+1 >= n {
				return nil, false
			}
			next := s[i+1]
			if next == '\n' {
				i++
				continue
			}
			curTok.WriteByte(next)
			hasTok = true
			needSegment = false
			i++
			continue
		case '`', '(', ')':
			return nil, false
		case '$':
			if i+1 < n && s[i+1] == '(' {
				return nil, false
			}
			curTok.WriteByte(ch)
			hasTok = true
			needSegment = false
			continue
		case ';':
			if needSegment || !flushSeg(true) {
				return nil, false
			}
			needSegment = true
			continue
		case '|':
			if needSegment || !flushSeg(true) {
				return nil, false
			}
			if i+1 < n && (s[i+1] == '|' || s[i+1] == '&') {
				i++
			}
			needSegment = true
			continue
		case '&':
			if i+1 < n && s[i+1] == '&' {
				if needSegment || !flushSeg(true) {
					return nil, false
				}
				i++
				needSegment = true
				continue
			}
			if i+1 < n && s[i+1] == '>' {
				if i+2 < n && (s[i+2] == '>' || s[i+2] == '&') {
					return nil, false
				}
				flushTok()
				target, nextIdx, ok := consumeRedirectTarget(s, i+2)
				if !ok || target != "/dev/null" {
					return nil, false
				}
				i = nextIdx - 1
				continue
			}
			return nil, false
		case '2', '1', '0':
			if !hasTok {
				if (ch == '2' && strings.HasPrefix(s[i:], "2>&1")) || (ch == '1' && strings.HasPrefix(s[i:], "1>&2")) {
					after := i + 4
					if after < n && !isSepOrSpace(s[after]) {
						return nil, false
					}
					needSegment = false
					i += 3
					continue
				}
				if (ch == '2' || ch == '1') && i+1 < n && s[i+1] == '>' {
					if i+2 < n && (s[i+2] == '>' || s[i+2] == '|' || s[i+2] == '&') {
						return nil, false
					}
					target, nextIdx, ok := consumeRedirectTarget(s, i+2)
					if !ok || target != "/dev/null" {
						return nil, false
					}
					needSegment = false
					i = nextIdx - 1
					continue
				}
				if ch == '0' && i+1 < n && s[i+1] == '<' {
					if i+2 < n && (s[i+2] == '<' || s[i+2] == '>' || s[i+2] == '&' || s[i+2] == '(') {
						return nil, false
					}
					target, nextIdx, ok := consumeRedirectTarget(s, i+2)
					if !ok || target != "/dev/null" {
						return nil, false
					}
					needSegment = false
					i = nextIdx - 1
					continue
				}
			}
			curTok.WriteByte(ch)
			hasTok = true
			needSegment = false
			continue
		case '>', '<':
			if i+1 < n {
				next := s[i+1]
				if next == '>' || next == '<' || next == '|' || next == '&' || next == '(' {
					return nil, false
				}
			}
			flushTok()
			target, nextIdx, ok := consumeRedirectTarget(s, i+1)
			if !ok || target != "/dev/null" {
				return nil, false
			}
			i = nextIdx - 1
			continue
		case '#':
			if !hasTok {
				for i < n && s[i] != '\n' {
					i++
				}
				if i < n {
					// Переводим указатель назад, чтобы внешний цикл обработал '\n'.
					i--
				}
				continue
			}
			curTok.WriteByte(ch)
			hasTok = true
			needSegment = false
			continue
		default:
			curTok.WriteByte(ch)
			hasTok = true
			needSegment = false
		}
	}

	if inSingle || inDouble || needSegment {
		return nil, false
	}
	if !flushSeg(false) {
		return nil, false
	}
	if len(segments) == 0 {
		return nil, false
	}
	return segments, true
}

func consumeRedirectTarget(s string, start int) (string, int, bool) {
	n := len(s)
	i := start
	for i < n && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= n {
		return "", i, false
	}

	var b strings.Builder
	hasWord := false
	inSingle := false
	inDouble := false

	for i < n {
		ch := s[i]
		if inSingle {
			if ch == '\'' {
				inSingle = false
			} else {
				b.WriteByte(ch)
			}
			i++
			continue
		}
		if inDouble {
			switch ch {
			case '"':
				inDouble = false
			case '`':
				return "", i, false
			case '$':
				if i+1 < n && s[i+1] == '(' {
					return "", i, false
				}
				b.WriteByte(ch)
			case '\\':
				if i+1 >= n {
					return "", i, false
				}
				next := s[i+1]
				if next == '"' || next == '\\' || next == '$' || next == '`' {
					b.WriteByte(next)
					i++
				} else {
					b.WriteByte(ch)
					b.WriteByte(next)
					i++
				}
			default:
				b.WriteByte(ch)
			}
			i++
			continue
		}

		if isSepOrSpace(ch) {
			break
		}
		switch ch {
		case '\'':
			inSingle = true
			hasWord = true
		case '"':
			inDouble = true
			hasWord = true
		case '\\':
			if i+1 >= n || s[i+1] == '\n' {
				return "", i, false
			}
			b.WriteByte(s[i+1])
			hasWord = true
			i++
		case '`', '(', ')', '>', '<':
			return "", i, false
		case '$':
			if i+1 < n && s[i+1] == '(' {
				return "", i, false
			}
			b.WriteByte(ch)
			hasWord = true
		default:
			b.WriteByte(ch)
			hasWord = true
		}
		i++
	}

	if inSingle || inDouble || !hasWord {
		return "", i, false
	}
	return b.String(), i, true
}

func isSafeReadOnlySegment(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	for _, tok := range tokens {
		if tok == "{" || tok == "}" {
			return false
		}
	}

	idx := 0
	for idx < len(tokens) {
		key, val, isAssign := splitEnvAssignment(tokens[idx])
		if !isAssign {
			break
		}
		if !isSafeEnvAssignment(key, val) {
			return false
		}
		idx++
	}
	if idx >= len(tokens) {
		return false
	}

	cmd := tokens[idx]
	args := tokens[idx+1:]
	if cmd == "" {
		return false
	}
	if cmd != "[" && strings.ContainsAny(cmd, "$*?[~") {
		return false
	}
	if strings.Contains(cmd, "/") {
		if strings.Contains(cmd, "..") {
			return false
		}
		cleaned := filepath.Clean(cmd)
		dir := filepath.Dir(cleaned)
		if dir != "/bin" && dir != "/usr/bin" && dir != "/usr/local/bin" {
			return false
		}
		cmd = filepath.Base(cleaned)
	}

	if alwaysReadOnlyCommands[cmd] {
		return true
	}

	switch cmd {
	case "tail":
		return isSafeReadOnlyTail(args)
	case "rg":
		return isSafeReadOnlyRG(args)
	case "sort":
		return isSafeReadOnlySort(args)
	case "tree":
		return isSafeReadOnlyTree(args)
	case "uniq":
		return isSafeReadOnlyUniq(args)
	case "find":
		return isSafeReadOnlyFind(args)
	case "fd":
		return isSafeReadOnlyFD(args)
	case "sed":
		return isSafeReadOnlySed(args)
	case "command":
		return isSafeReadOnlyCommandBuiltin(args)
	case "git":
		return isSafeReadOnlyGit(args)
	case "docker":
		return isSafeReadOnlyDocker(args)
	case "docker-compose":
		return isSafeReadOnlyDockerCompose(args)
	case "systemctl":
		return isSafeReadOnlySystemctl(args)
	case "journalctl":
		return isSafeReadOnlyJournalctl(args)
	case "gh":
		return isSafeReadOnlyGH(args)
	case "ip":
		return isSafeReadOnlyIP(args)
	case "go":
		return isSafeReadOnlyGo(args)
	case "cargo", "rustc", "rustup", "node", "npm", "pnpm", "yarn", "python", "python3", "pip", "pip3":
		return len(args) == 1 && (args[0] == "--version" || args[0] == "-V" || args[0] == "-v" || args[0] == "version")
	default:
		return false
	}
}

func splitEnvAssignment(tok string) (key, val string, ok bool) {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return "", "", false
	}
	key = tok[:eq]
	for i := 0; i < len(key); i++ {
		c := key[i]
		isAlpha := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
		isDigit := c >= '0' && c <= '9'
		if (i == 0 && !isAlpha) || (!isAlpha && !isDigit) {
			return "", "", false
		}
	}
	return key, tok[eq+1:], true
}

func isSafeEnvAssignment(key, val string) bool {
	switch key {
	case "LC_ALL", "LC_CTYPE", "LC_COLLATE", "LANG", "TZ", "NO_COLOR", "CLICOLOR", "TERM", "COLUMNS", "LINES", "GREP_COLOR", "GREP_COLORS":
		return true
	case "PAGER", "GIT_PAGER", "SYSTEMD_PAGER":
		return val == "" || val == "cat"
	default:
		return false
	}
}

var alwaysReadOnlyCommands = map[string]bool{
	"ls": true, "ll": true, "dir": true, "pwd": true, "stat": true, "file": true,
	"wc": true, "du": true, "df": true, "realpath": true, "readlink": true,
	"basename": true, "dirname": true, "which": true, "type": true,
	"cat": true, "tac": true, "nl": true, "head": true, "cut": true, "tr": true,
	"diff": true, "cmp": true, "comm": true, "column": true, "fold": true, "fmt": true,
	"rev": true, "paste": true, "join": true,
	"md5sum": true, "sha1sum": true, "sha256sum": true, "sha512sum": true, "cksum": true,
	"strings": true, "od": true,
	"date": true, "uname": true, "uptime": true, "hostname": true, "whoami": true, "id": true,
	"nproc": true, "lscpu": true, "free": true, "vmstat": true, "lsblk": true, "lsof": true,
	"ps": true, "pgrep": true, "ss": true, "netstat": true,
	"true": true, "false": true, "echo": true, "printf": true, "test": true, "[": true,
	"grep": true, "egrep": true, "fgrep": true, "zgrep": true, "ag": true, "ack": true, "jq": true,
}

func isShortFlagWithChars(arg string, chars string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 2 {
		return false
	}
	return strings.ContainsAny(arg[1:], chars)
}

func isSafeReadOnlyTail(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--follow" || strings.HasPrefix(arg, "--follow=") || isShortFlagWithChars(arg, "fF") {
			return false
		}
	}
	return true
}

func isSafeReadOnlyRG(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--pre" || strings.HasPrefix(arg, "--pre=") ||
			arg == "--pre-glob" || strings.HasPrefix(arg, "--pre-glob=") {
			return false
		}
	}
	return true
}

func isSafeReadOnlySort(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--output" || strings.HasPrefix(arg, "--output=") ||
			arg == "--compress-program" || strings.HasPrefix(arg, "--compress-program=") {
			return false
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && !strings.HasPrefix(arg, "-t") && strings.ContainsRune(arg[1:], 'o') {
			return false
		}
	}
	return true
}

func isSafeReadOnlyTree(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--output" || strings.HasPrefix(arg, "--output=") || isShortFlagWithChars(arg, "o") {
			return false
		}
	}
	return true
}

func isSafeReadOnlyUniq(args []string) bool {
	posCount := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			posCount += len(args) - i - 1
			break
		}
		if arg == "-f" || arg == "-s" || arg == "-w" ||
			arg == "--skip-fields" || arg == "--skip-chars" || arg == "--check-chars" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			continue
		}
		posCount++
		if posCount > 1 {
			return false
		}
	}
	return true
}

func isSafeReadOnlyFind(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fls", "-fprint", "-fprint0", "-fprintf":
			return false
		}
	}
	return true
}

func isSafeReadOnlyFD(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--exec" || strings.HasPrefix(arg, "--exec=") ||
			arg == "--exec-batch" || strings.HasPrefix(arg, "--exec-batch=") ||
			isShortFlagWithChars(arg, "xX") {
			return false
		}
	}
	return true
}

func isSafeReadOnlySed(args []string) bool {
	hasQuiet := false
	var scripts []string
	expectScript := false
	stopFlags := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if expectScript {
			scripts = append(scripts, arg)
			expectScript = false
			continue
		}
		if !stopFlags && arg == "--" {
			stopFlags = true
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "-") && arg != "-" {
			if arg == "--quiet" || arg == "--silent" {
				hasQuiet = true
				continue
			}
			if arg == "--in-place" || strings.HasPrefix(arg, "--in-place=") ||
				arg == "--file" || strings.HasPrefix(arg, "--file=") {
				return false
			}
			if arg == "-e" || arg == "--expression" {
				expectScript = true
				continue
			}
			if strings.HasPrefix(arg, "--expression=") {
				scripts = append(scripts, strings.TrimPrefix(arg, "--expression="))
				continue
			}
			if !strings.HasPrefix(arg, "--") {
				if isShortFlagWithChars(arg, "if") {
					return false
				}
				if strings.ContainsRune(arg[1:], 'n') {
					hasQuiet = true
				}
				if strings.HasPrefix(arg, "-e") && len(arg) > 2 {
					scripts = append(scripts, arg[2:])
				}
			}
			continue
		}
		if len(scripts) == 0 {
			scripts = append(scripts, arg)
		}
	}

	if expectScript || !hasQuiet || len(scripts) == 0 {
		return false
	}
	for _, sc := range scripts {
		if !strings.ContainsRune(sc, 'p') || !sedPrintScriptRE.MatchString(sc) {
			return false
		}
	}
	return true
}

func isSafeReadOnlyCommandBuiltin(args []string) bool {
	if len(args) < 2 {
		return false
	}
	if args[0] != "-v" && args[0] != "-V" {
		return false
	}
	for _, arg := range args[1:] {
		if strings.TrimSpace(arg) == "" || strings.HasPrefix(arg, "-") {
			return false
		}
	}
	return true
}

var gitAlwaysReadOnlySubcommands = map[string]bool{
	"status": true, "log": true, "show": true, "rev-parse": true, "rev-list": true,
	"describe": true, "ls-files": true, "ls-tree": true, "ls-remote": true,
	"cat-file": true, "blame": true, "shortlog": true, "grep": true,
	"merge-base": true, "name-rev": true, "check-ignore": true, "check-attr": true,
	"for-each-ref": true, "count-objects": true, "cherry": true, "whatchanged": true,
	"version": true, "help": true,
}

func isSafeReadOnlyGit(args []string) bool {
	i := 0
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			break
		}
		switch {
		case (arg == "--version" || arg == "-v") && i == len(args)-1:
			return true
		case arg == "-C" || arg == "--git-dir" || arg == "--work-tree":
			if i+1 >= len(args) {
				return false
			}
			i += 2
		case arg == "--no-pager" || arg == "-P" || arg == "--bare" ||
			arg == "--literal-pathspecs" || arg == "--glob-pathspecs" ||
			arg == "--noglob-pathspecs" || arg == "--icase-pathspecs" ||
			arg == "--no-optional-locks" ||
			strings.HasPrefix(arg, "--git-dir=") || strings.HasPrefix(arg, "--work-tree="):
			i++
		default:
			return false
		}
	}
	if i >= len(args) {
		return false
	}
	sub := args[i]
	rest := args[i+1:]

	if gitAlwaysReadOnlySubcommands[sub] {
		return true
	}
	switch sub {
	case "diff":
		for _, a := range rest {
			if a == "--" {
				break
			}
			if a == "--output" || strings.HasPrefix(a, "--output=") || a == "--ext-diff" {
				return false
			}
		}
		return true
	case "branch":
		return isSafeReadOnlyGitBranch(rest)
	case "tag":
		return isSafeReadOnlyGitTag(rest)
	case "remote":
		return len(rest) == 0 || (len(rest) == 1 && (rest[0] == "-v" || rest[0] == "--verbose")) ||
			rest[0] == "show" || rest[0] == "get-url"
	case "config":
		return isSafeReadOnlyGitConfig(rest)
	case "stash":
		return len(rest) > 0 && (rest[0] == "list" || rest[0] == "show")
	case "reflog":
		return len(rest) == 0 || strings.HasPrefix(rest[0], "-") || rest[0] == "show"
	case "worktree":
		return len(rest) > 0 && rest[0] == "list"
	case "submodule":
		return len(rest) > 0 && (rest[0] == "status" || rest[0] == "summary")
	default:
		return false
	}
}

func isSafeReadOnlyGitBranch(args []string) bool {
	allowsPositional := false
	hasPositional := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				hasPositional = true
			}
			break
		}
		switch arg {
		case "--delete", "--move", "--copy", "--force", "--set-upstream-to", "--unset-upstream", "--edit-description", "--create-reflog":
			return false
		case "-l", "--list", "--show-current":
			allowsPositional = true
			continue
		case "--contains", "--no-contains", "--merged", "--no-merged", "--points-at":
			allowsPositional = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		case "--format", "--sort", "--abbrev", "--color", "--column":
			i++
			continue
		}
		if strings.HasPrefix(arg, "--set-upstream-to=") {
			return false
		}
		if strings.HasPrefix(arg, "--contains=") || strings.HasPrefix(arg, "--no-contains=") ||
			strings.HasPrefix(arg, "--merged=") || strings.HasPrefix(arg, "--no-merged=") ||
			strings.HasPrefix(arg, "--points-at=") {
			allowsPositional = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if !strings.HasPrefix(arg, "--") && isShortFlagWithChars(arg, "dDmMcCfu") {
				return false
			}
			continue
		}
		hasPositional = true
	}
	return !hasPositional || allowsPositional
}

func isSafeReadOnlyGitTag(args []string) bool {
	if len(args) == 0 {
		return true
	}
	allowsPositional := false
	hasPositional := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				hasPositional = true
			}
			break
		}
		switch {
		case arg == "-l" || arg == "--list":
			allowsPositional = true
		case arg == "--contains" || arg == "--no-contains" || arg == "--merged" || arg == "--no-merged" || arg == "--points-at":
			allowsPositional = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case strings.HasPrefix(arg, "--contains=") || strings.HasPrefix(arg, "--no-contains=") ||
			strings.HasPrefix(arg, "--merged=") || strings.HasPrefix(arg, "--no-merged=") ||
			strings.HasPrefix(arg, "--points-at="):
			allowsPositional = true
		case arg == "-i" || arg == "--ignore-case" || arg == "-n" || strings.HasPrefix(arg, "-n") ||
			strings.HasPrefix(arg, "--sort=") || strings.HasPrefix(arg, "--format=") ||
			arg == "--column" || strings.HasPrefix(arg, "--column=") ||
			arg == "--color" || strings.HasPrefix(arg, "--color="):
			// Безопасные флаги листинга.
		case arg == "--sort" || arg == "--format":
			i++
		case strings.HasPrefix(arg, "-"):
			return false
		default:
			hasPositional = true
		}
	}
	return !hasPositional || allowsPositional
}

func isSafeReadOnlyGitConfig(args []string) bool {
	hasReadFlag := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--unset", "--unset-all", "--add", "--replace-all", "--remove-section", "--rename-section",
			"-e", "--edit", "-f", "--file":
			return false
		case "-l", "--list", "--get", "--get-all", "--get-regexp", "--get-urlmatch":
			hasReadFlag = true
		}
		if strings.HasPrefix(arg, "--unset=") || strings.HasPrefix(arg, "--file=") {
			return false
		}
	}
	return hasReadFlag
}

func isSafeReadOnlyDocker(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "--version" || args[0] == "-v" {
		return len(args) == 1
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "ps", "images", "inspect", "top", "port", "diff", "history", "version", "info":
		return true
	case "logs":
		return !hasFollowFlag(rest)
	case "stats":
		return hasNoStreamFlag(rest)
	case "network", "volume":
		return len(rest) > 0 && (rest[0] == "ls" || rest[0] == "inspect")
	case "container":
		if len(rest) == 0 {
			return false
		}
		switch rest[0] {
		case "ls", "ps", "inspect", "top", "port", "diff":
			return true
		case "logs":
			return !hasFollowFlag(rest[1:])
		case "stats":
			return hasNoStreamFlag(rest[1:])
		default:
			return false
		}
	case "image":
		return len(rest) > 0 && (rest[0] == "ls" || rest[0] == "inspect" || rest[0] == "history")
	case "compose":
		return isSafeReadOnlyDockerCompose(rest)
	default:
		return false
	}
}

func isSafeReadOnlyDockerCompose(args []string) bool {
	i := 0
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			break
		}
		switch {
		case arg == "-f" || arg == "--file" || arg == "-p" || arg == "--project-name" ||
			arg == "--project-directory" || arg == "--env-file" || arg == "--profile":
			if i+1 >= len(args) {
				return false
			}
			i += 2
		case strings.HasPrefix(arg, "--file=") || strings.HasPrefix(arg, "--project-name=") ||
			strings.HasPrefix(arg, "--project-directory=") || strings.HasPrefix(arg, "--env-file=") ||
			strings.HasPrefix(arg, "--profile="):
			i++
		default:
			return false
		}
	}
	if i >= len(args) {
		return false
	}
	switch args[i] {
	case "ps", "config", "images", "ls", "top", "port", "version":
		return true
	case "logs":
		return !hasFollowFlag(args[i+1:])
	default:
		return false
	}
}

func hasFollowFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--follow" || strings.HasPrefix(arg, "--follow=") || isShortFlagWithChars(arg, "f") {
			return true
		}
	}
	return false
}

func hasNoStreamFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--no-stream" {
			return true
		}
	}
	return false
}

func isSafeReadOnlySystemctl(args []string) bool {
	i := 0
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			break
		}
		switch {
		case arg == "--version" && i == len(args)-1:
			return true
		case arg == "-n" || arg == "--lines" || arg == "-t" || arg == "--type" ||
			arg == "--state" || arg == "-p" || arg == "--property" || arg == "-o" || arg == "--output":
			if i+1 >= len(args) {
				return false
			}
			i += 2
		case arg == "--user" || arg == "--system" || arg == "--no-pager" || arg == "--plain" ||
			arg == "--full" || arg == "--all" || arg == "-a" || arg == "-l" ||
			arg == "--no-legend" || arg == "-q" || arg == "--quiet" || arg == "--value" ||
			arg == "--failed" || arg == "--reverse" ||
			strings.HasPrefix(arg, "--lines=") || strings.HasPrefix(arg, "--type=") ||
			strings.HasPrefix(arg, "--state=") || strings.HasPrefix(arg, "--property=") ||
			strings.HasPrefix(arg, "--output="):
			i++
		default:
			return false
		}
	}
	if i >= len(args) {
		return false
	}
	switch args[i] {
	case "status", "is-active", "is-enabled", "is-failed", "is-system-running",
		"list-units", "list-unit-files", "list-timers", "list-sockets",
		"list-dependencies", "show", "cat":
		return true
	default:
		return false
	}
}

func isSafeReadOnlyJournalctl(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--follow" || strings.HasPrefix(arg, "--follow=") ||
			arg == "--flush" || arg == "--rotate" || arg == "--sync" ||
			arg == "--relinquish-var" || arg == "--setup-keys" || arg == "--update-catalog" ||
			strings.HasPrefix(arg, "--vacuum-") || isShortFlagWithChars(arg, "f") {
			return false
		}
	}
	return true
}

func isSafeReadOnlyGH(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "--version", "version", "status":
		return len(args) == 1
	case "pr":
		return len(args) > 1 && (args[1] == "list" || args[1] == "view" || args[1] == "status" || args[1] == "diff" || args[1] == "checks")
	case "issue":
		return len(args) > 1 && (args[1] == "list" || args[1] == "view" || args[1] == "status")
	case "run", "release":
		return len(args) > 1 && (args[1] == "list" || args[1] == "view")
	case "repo":
		return len(args) > 1 && (args[1] == "view" || args[1] == "list")
	default:
		return false
	}
}

func isSafeReadOnlyIP(args []string) bool {
	i := 0
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			break
		}
		switch arg {
		case "-4", "-6", "-o", "-br", "--brief", "-c", "-color", "-j", "-json",
			"-s", "-stats", "-d", "-details", "-h", "-human":
			i++
		default:
			return false
		}
	}
	if i >= len(args) {
		return false
	}
	switch args[i] {
	case "addr", "address", "a", "route", "r", "link", "l", "neigh", "n":
	default:
		return false
	}
	if i+1 >= len(args) {
		return true
	}
	return args[i+1] == "show" || args[i+1] == "list"
}

func isSafeReadOnlyGo(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "version", "doc":
		return true
	case "env":
		for _, arg := range args[1:] {
			if arg == "-w" || arg == "-u" || strings.HasPrefix(arg, "-w=") || strings.HasPrefix(arg, "-u=") || isShortFlagWithChars(arg, "wu") {
				return false
			}
		}
		return true
	default:
		return false
	}
}
