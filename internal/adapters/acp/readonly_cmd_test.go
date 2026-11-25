package acp

import "testing"

func TestIsSafeReadOnlyShellCommand(t *testing.T) {
	allowed := []string{
		// Реальные команды диагностики из логов
		`ls -la && ls -la crates/ && docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Image}}" 2>/dev/null || true`,
		`git status -sb && git log -n 10 --oneline && ls -la`,
		`ls`,
		`tree -L 2 -a`,
		`find . -maxdepth 3 -name '*.go'`,
		`grep -rn "foo|bar" .`,
		`rg -n foo . | head -n 20`,
		`cat a.go | wc -l`,
		`sed -n '1,40p' go.mod`,
		`sed -En '10,20p;30p' go.mod`,
		`LC_ALL=C GIT_PAGER=cat git diff main..HEAD --stat`,
		`git branch`,
		`git branch -a`,
		`git branch -vv`,
		`git branch --show-current`,
		`git branch --list 'feat/*'`,
		`git tag -l 'v*'`,
		`git tag -l 'v*' --sort=-v:refname`,
		`git config --get user.name`,
		`git config --list`,
		`git stash list`,
		`git worktree list`,
		`git -C /repo status -sb`,
		`git remote -v`,
		`git submodule status`,
		`docker logs --tail 50 app 2>&1 | tail -n 20`,
		`docker stats --no-stream`,
		`docker compose -f docker-compose.yml ps`,
		`docker network ls`,
		`docker volume inspect data`,
		`docker container ls`,
		`systemctl --user status bro-bot --no-pager`,
		`journalctl -u bro-bot -n 50 --no-pager`,
		`gh pr list --limit 5`,
		`gh pr view 130 --json state`,
		`ip -br addr show`,
		`go version`,
		`go env GOPATH`,
		`cargo --version`,
		`command -v rg`,
		`[ -d crates ] && ls crates`,
		`ls >/dev/null 2>&1`,
		`ls -la 2>/dev/null`,
		`cat </dev/null`,
		`/usr/bin/ls -la`,
		`uniq -c file.txt`,
		`sort -u file.txt`,
	}

	for _, cmd := range allowed {
		if !IsSafeReadOnlyShellCommand(cmd) {
			t.Errorf("expected ALLOWED read-only command, got REJECTED: %q", cmd)
		}
	}

	rejected := []string{
		"",
		"   ",
		"rm -rf /",
		"echo hi > out.txt",
		"ls >> log",
		"ls 2>err.txt",
		"ls >/dev/null2",
		"ls 2>&1foo",
		"ls $(whoami)",
		"ls `whoami`",
		`echo "$(id)"`,
		`echo "` + "`id`" + `"`,
		"ls <(id)",
		"(ls)",
		"ls &",
		"cat <<EOF",
		"ls && rm foo",
		"ls &&",
		"| ls",
		"ls;;pwd",
		"ls;",
		"ls 'unclosed",
		`ls "unclosed`,
		"find . -delete",
		`find . -exec rm {} \;`,
		"fd -x rm",
		"rg --pre sh foo",
		"sort -o out.txt file",
		"tree -o out.txt",
		"uniq in.txt out.txt",
		"sed -i 's/a/b/' f",
		"sed -n 's/a/b/e' f",
		"sed '1,10p' f",
		"git commit -m x",
		"git push",
		"git branch new-br",
		"git branch -D old",
		"git tag v1.0",
		"git -c core.pager=sh log",
		`GIT_PAGER="sh -c evil" git log`,
		"LD_PRELOAD=/tmp/a.so ls",
		"git config user.name x",
		"git diff --output=/tmp/x",
		"git stash pop",
		"docker exec app sh",
		"docker rm app",
		"docker logs -f app",
		"docker stats",
		"docker compose up -d",
		"systemctl restart bro-bot",
		"journalctl -f",
		"journalctl --vacuum-time=1d",
		"gh pr merge 1",
		"gh api /repos",
		"go env -w FOO=bar",
		"python3 -c 'print(1)'",
		"bash -c ls",
		"./script.sh",
		"tail -f /var/log/syslog",
		"tail -n10f /var/log/syslog",
		"echo hi | tee out.txt",
		"ip addr add 1.2.3.4/24 dev eth0",
		"{ ls; }",
	}

	for _, cmd := range rejected {
		if IsSafeReadOnlyShellCommand(cmd) {
			t.Errorf("expected REJECTED command, got ALLOWED: %q", cmd)
		}
	}
}

func TestIsSafeReadOnlyShellCallPrecedenceAndForeignServers(t *testing.T) {
	cases := []struct {
		name string
		call ToolCallPermission
		want bool
	}{
		{
			name: "CommandLine takes precedence over deceptive safe Title",
			call: ToolCallPermission{
				Kind:     "execute",
				Name:     "run_command",
				Title:    "ls -la",
				RawInput: map[string]any{"CommandLine": "rm -rf /"},
			},
			want: false,
		},
		{
			name: "foreign MCP server with safe CommandLine is rejected",
			call: ToolCallPermission{
				Kind:     "execute",
				Name:     "call_mcp_tool",
				Title:    "ls -la",
				RawInput: map[string]any{"ServerName": "other_mcp", "CommandLine": "ls -la"},
			},
			want: false,
		},
		{
			name: "bare bash title without command args is rejected",
			call: ToolCallPermission{
				Kind:  "execute",
				Title: "bash",
			},
			want: false,
		},
		{
			name: "bash title with safe command in RawInput is allowed",
			call: ToolCallPermission{
				Kind:     "execute",
				Title:    "bash",
				RawInput: map[string]any{"command": "git status -sb"},
			},
			want: true,
		},
		{
			name: "run_command with empty Kind and safe CommandLine in Args is allowed",
			call: ToolCallPermission{
				Name: "run_command",
				Args: map[string]any{"CommandLine": "ls -la && git log -n 5 --oneline"},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSafeReadOnlyShellCall(tc.call); got != tc.want {
				t.Fatalf("IsSafeReadOnlyShellCall = %v, want %v", got, tc.want)
			}
		})
	}
}
