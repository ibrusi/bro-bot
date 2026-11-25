package tools

import (
	"os"
	"path/filepath"
	"testing"

	"bro-bot/internal/ports"
)

func TestResolvePathWithSymlinksAndSensitivePaths(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "datasets")
	outside := filepath.Join(base, "secret")
	if err := os.MkdirAll(allowed, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(link, "newfile.csv")
	if IsPathWithinRoots(target, base, []string{allowed}) {
		t.Fatalf("symlink escape %q must NOT be considered within %q", target, allowed)
	}

	validTarget := filepath.Join(allowed, "sub", "candles.csv")
	if !IsPathWithinRoots(validTarget, base, []string{allowed}) {
		t.Fatalf("expected %q to be within %q", validTarget, allowed)
	}

	sensitive := []string{
		"/etc/passwd",
		"/boot/grub/grub.cfg",
		"/sys/class/net",
		"/proc/1/status",
		"/usr/local/bin/tool",
		"/dev/sda",
		"/home/u/repo/.git/config",
		"/home/u/.ssh/authorized_keys",
		"/home/u/.bashrc",
	}
	for _, p := range sensitive {
		if !IsSensitiveSystemPath(p) {
			t.Errorf("IsSensitiveSystemPath(%q) = false, want true", p)
		}
	}
	nonSensitive := []string{
		"/dev/null",
		"/home/u/datasets/candles.csv",
		"/tmp/out.json",
	}
	for _, p := range nonSensitive {
		if IsSensitiveSystemPath(p) {
			t.Errorf("IsSensitiveSystemPath(%q) = true, want false", p)
		}
	}
}

func TestIsSafeReadOnlyShellCommandExtensions(t *testing.T) {
	allowed := []string{
		"systemctl status bro-bot",
		"systemctl is-active bro-bot",
		"systemctl list-units --type=service",
		"journalctl -u bro-bot -n 50 --no-pager",
		"cat /etc/os-release",
		"head -n 20 /proc/meminfo",
		"python3 -c 'print(1 + 1)'",
		"curl -s https://example.com -o /tmp/out.json",
	}
	for _, cmd := range allowed {
		if !IsSafeReadOnlyShellCommand(cmd) {
			t.Errorf("IsSafeReadOnlyShellCommand(%q) = false, want true", cmd)
		}
	}

	denied := []string{
		"systemctl restart bro-bot",
		"systemctl stop bro-bot",
		"systemctl enable bro-bot",
		"curl -s https://example.com -o /home/deploy/out.json",
		"cat /etc/os-release > /home/deploy/leak.txt",
		"sudo systemctl status bro-bot",
	}
	for _, cmd := range denied {
		if IsSafeReadOnlyShellCommand(cmd) {
			t.Errorf("IsSafeReadOnlyShellCommand(%q) = true, want false", cmd)
		}
	}
}

func TestCheckScriptsDirOperation(t *testing.T) {
	base := t.TempDir()
	botDir := filepath.Join(base, "bro-bot")
	scriptsDir := filepath.Join(botDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		t.Fatal(err)
	}
	scriptPy := filepath.Join(scriptsDir, "load.py")
	scriptSh := filepath.Join(scriptsDir, "run.sh")

	tests := []struct {
		name string
		req  ports.PermissionRequest
		want ScriptsDirOpKind
	}{
		{
			name: "write_file inside scriptsDir",
			req:  ports.PermissionRequest{ToolName: "write_file", Kind: "edit", WorkDir: botDir, Paths: []string{scriptPy}},
			want: ScriptsDirOpWrite,
		},
		{
			name: "edit_file relative path inside scriptsDir",
			req:  ports.PermissionRequest{ToolName: "edit_file", Kind: "edit", WorkDir: botDir, Paths: []string{"scripts/load.py"}},
			want: ScriptsDirOpWrite,
		},
		{
			name: "write_file outside scriptsDir",
			req:  ports.PermissionRequest{ToolName: "write_file", Kind: "edit", WorkDir: botDir, Paths: []string{filepath.Join(base, "data.csv")}},
			want: ScriptsDirOpNone,
		},
		{
			name: "shell cat reading scriptsDir",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: botDir, Command: "cat " + scriptPy},
			want: ScriptsDirOpNone,
		},
		{
			name: "shell ls reading scriptsDir",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: botDir, Command: "ls -la " + scriptsDir},
			want: ScriptsDirOpNone,
		},
		{
			name: "analytical python3 reading script in scriptsDir",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: botDir, Command: `python3 -c "print(open('` + scriptPy + `').read())"`},
			want: ScriptsDirOpNone,
		},
		{
			name: "python3 executing script in scriptsDir",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: botDir, Command: "python3 " + scriptPy},
			want: ScriptsDirOpExecute,
		},
		{
			name: "python3 -c with subprocess targeting scriptsDir",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: botDir, Command: `python3 -c "import subprocess; subprocess.run(['bash', '` + scriptSh + `'])"`},
			want: ScriptsDirOpExecute,
		},
		{
			name: "python3 -c writing into scriptsDir",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: botDir, Command: `python3 -c "open('` + scriptPy + `', 'w').write('x')"`},
			want: ScriptsDirOpWrite,
		},
		{
			name: "direct script execution",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: botDir, Command: "./scripts/run.sh --dry-run"},
			want: ScriptsDirOpExecute,
		},
		{
			name: "bash wrapper execution",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: botDir, Command: "env FOO=1 timeout 30 bash " + scriptSh},
			want: ScriptsDirOpExecute,
		},
		{
			name: "source script",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: botDir, Command: "source " + scriptSh},
			want: ScriptsDirOpExecute,
		},
		{
			name: "shell redirect writing into scriptsDir",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: botDir, Command: "echo hi > " + filepath.Join(scriptsDir, "note.txt")},
			want: ScriptsDirOpWrite,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckScriptsDirOperation(tt.req, scriptsDir)
			if got.Kind != tt.want {
				t.Errorf("CheckScriptsDirOperation() = %v (target=%q), want %v", got.Kind, got.Target, tt.want)
			}
		})
	}
}

func TestCheckWriteGuardrail(t *testing.T) {
	base := t.TempDir()
	busyProject := filepath.Join(base, "active-repo")
	freeData := filepath.Join(base, "datasets")
	if err := os.MkdirAll(busyProject, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(freeData, 0755); err != nil {
		t.Fatal(err)
	}
	busy := []string{busyProject}

	blockedCases := []struct {
		name string
		req  ports.PermissionRequest
	}{
		{
			name: "foreign MCP server",
			req:  ports.PermissionRequest{ToolName: "mcp_custom_exec", RawInput: map[string]any{"ServerName": "github"}},
		},
		{
			name: "non-research subagent",
			req:  ports.PermissionRequest{ToolName: "invoke_subagent", RawInput: map[string]any{"TypeName": "self"}},
		},
		{
			name: "write_file to /etc",
			req:  ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{"/etc/cron.d/job"}},
		},
		{
			name: "write_file to .git",
			req:  ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(freeData, ".git", "hooks", "pre-commit")}},
		},
		{
			name: "write_file into busy project",
			req:  ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(busyProject, "main.go")}},
		},
		{
			name: "sudo command",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "sudo apt update"},
		},
		{
			name: "systemctl restart",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "systemctl restart bro-bot"},
		},
		{
			name: "git commit",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "git commit -m wip"},
		},
		{
			name: "gh pr create",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "gh pr create --fill"},
		},
		{
			name: "rm -rf",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "rm -rf " + freeData},
		},
		{
			name: "find -delete",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "find " + freeData + " -type f -delete"},
		},
		{
			name: "relative command inside busy workDir",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: busyProject, Command: "touch local.txt"},
		},
		{
			name: "python writing /etc",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: `python3 -c "open('/etc/test', 'w').write('x')"`},
		},
	}

	for _, tt := range blockedCases {
		t.Run("blocked/"+tt.name, func(t *testing.T) {
			res := CheckWriteGuardrail(tt.req, busy)
			if !res.Blocked {
				t.Errorf("CheckWriteGuardrail(%s) allowed, want blocked", tt.name)
			}
		})
	}

	allowedCases := []struct {
		name string
		req  ports.PermissionRequest
	}{
		{
			name: "bro_bot MCP server",
			req:  ports.PermissionRequest{ToolName: "mcp_bro_bot_sqlite_query", RawInput: map[string]any{"ServerName": "bro_bot"}},
		},
		{
			name: "research subagent",
			req:  ports.PermissionRequest{ToolName: "invoke_subagent", RawInput: map[string]any{"TypeName": "research"}},
		},
		{
			name: "write_file into free datasets",
			req:  ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(freeData, "candles.csv")}},
		},
		{
			name: "systemctl status",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "systemctl status bro-bot"},
		},
		{
			name: "git status",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "git status --short"},
		},
		{
			name: "cat /etc/os-release",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "cat /etc/os-release"},
		},
		{
			name: "curl downloading outside busy project even when cwd is busyProject",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", WorkDir: busyProject, Command: "curl -s https://example.com -o " + filepath.Join(freeData, "out.json")},
		},
	}

	for _, tt := range allowedCases {
		t.Run("allowed/"+tt.name, func(t *testing.T) {
			res := CheckWriteGuardrail(tt.req, busy)
			if res.Blocked {
				t.Errorf("CheckWriteGuardrail(%s) blocked (%s), want allowed", tt.name, res.Reason)
			}
		})
	}
}

func TestIsAutoAllowedWrite(t *testing.T) {
	base := t.TempDir()
	datasets := filepath.Join(base, "pair-edge-datasets")
	scriptsDir := filepath.Join(datasets, "scripts")
	busyProject := filepath.Join(base, "busy-repo")
	outside := "/srv/outside-not-allowed"
	for _, dir := range []string{datasets, scriptsDir, busyProject} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	symOutside := filepath.Join(datasets, "sym_out")
	if err := os.Symlink(outside, symOutside); err != nil {
		t.Fatal(err)
	}
	symSys := filepath.Join(datasets, "sym_etc")
	if err := os.Symlink("/etc", symSys); err != nil {
		t.Fatal(err)
	}

	allowedRoots := []string{datasets}
	busy := []string{busyProject}

	yes := []struct {
		name string
		req  ports.PermissionRequest
	}{
		{
			name: "write_file in CHAT_WRITE_PATHS",
			req:  ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(datasets, "moex.csv")}},
		},
		{
			name: "write_file in /tmp when CHAT_WRITE_PATHS non-empty",
			req:  ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{"/tmp/bro_cache.json"}},
		},
		{
			name: "curl -o inside CHAT_WRITE_PATHS",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "curl -s https://iss.moex.com -o " + filepath.Join(datasets, "moex.json")},
		},
		{
			name: "mkdir and redirect inside CHAT_WRITE_PATHS",
			req:  ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "mkdir -p " + filepath.Join(datasets, "1m") + " && echo a > " + filepath.Join(datasets, "1m", "sber.csv")},
		},
	}
	for _, tt := range yes {
		t.Run("yes/"+tt.name, func(t *testing.T) {
			if !IsAutoAllowedWrite(tt.req, allowedRoots, busy, scriptsDir) {
				t.Errorf("IsAutoAllowedWrite(%s) = false, want true", tt.name)
			}
		})
	}

	no := []struct {
		name  string
		req   ports.PermissionRequest
		roots []string
	}{
		{
			name:  "empty CHAT_WRITE_PATHS disables /tmp too",
			req:   ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{"/tmp/a.csv"}},
			roots: nil,
		},
		{
			name:  "write outside CHAT_WRITE_PATHS",
			req:   ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(outside, "a.csv")}},
			roots: allowedRoots,
		},
		{
			name:  "write into SCRIPTS_DIR nested under allowedRoots still asks",
			req:   ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(scriptsDir, "helper.py")}},
			roots: allowedRoots,
		},
		{
			name:  "symlink escape out of CHAT_WRITE_PATHS",
			req:   ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(symOutside, "evil.txt")}},
			roots: allowedRoots,
		},
		{
			name:  "curl to outside path",
			req:   ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "curl -s https://example.com -o " + filepath.Join(outside, "x.json")},
			roots: allowedRoots,
		},
		{
			name:  "shell command with variable/subshell fail-closed",
			req:   ports.PermissionRequest{ToolName: "run_command", Kind: "execute", Command: "curl -s https://example.com -o " + filepath.Join(datasets, "$(date).json")},
			roots: allowedRoots,
		},
		{
			name:  "write to busy project",
			req:   ports.PermissionRequest{ToolName: "write_file", Kind: "edit", Paths: []string{filepath.Join(busyProject, "file.go")}},
			roots: []string{busyProject},
		},
	}
	for _, tt := range no {
		t.Run("no/"+tt.name, func(t *testing.T) {
			if IsAutoAllowedWrite(tt.req, tt.roots, busy, scriptsDir) {
				t.Errorf("IsAutoAllowedWrite(%s) = true, want false", tt.name)
			}
		})
	}
}
