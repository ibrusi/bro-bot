package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func readJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("invalid JSON written: %v\n%s", err, data)
	}
	return out
}

// TestEnsureServerKeepsForeignSettings — раньше файл перечитывался в структуру
// только с command и args, и у чужих серверов молча пропадали env, url и прочее.
func TestEnsureServerKeepsForeignSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_config.json")
	original := `{
  "theme": "dark",
  "mcpServers": {
    "github": {"command": "gh-mcp", "args": ["serve"], "env": {"GITHUB_TOKEN": "secret"}, "cwd": "/srv"},
    "remote": {"url": "https://example.test/mcp", "headers": {"Authorization": "Bearer x"}},
    "bro_bot": {"command": "/old/bot", "args": ["mcp-serve"], "env": {"DEBUG": "1"}}
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensureServerInConfig(path, "bro_bot", "/new/bot", []string{"mcp-serve", "--socket", "/s"}); err != nil {
		t.Fatalf("ensureServerInConfig: %v", err)
	}

	cfg := readJSON(t, path)
	if cfg["theme"] != "dark" {
		t.Errorf("top-level key lost: %v", cfg)
	}
	servers := cfg["mcpServers"].(map[string]interface{})
	var want map[string]interface{}
	_ = json.Unmarshal([]byte(original), &want)
	wantServers := want["mcpServers"].(map[string]interface{})
	for _, name := range []string{"github", "remote"} {
		if !reflect.DeepEqual(servers[name], wantServers[name]) {
			t.Errorf("server %s changed: %v", name, servers[name])
		}
	}
	bot := servers["bro_bot"].(map[string]interface{})
	if bot["command"] != "/new/bot" || !reflect.DeepEqual(bot["args"], []interface{}{"mcp-serve", "--socket", "/s"}) {
		t.Errorf("bro_bot entry = %v", bot)
	}
	if !reflect.DeepEqual(bot["env"], map[string]interface{}{"DEBUG": "1"}) {
		t.Errorf("extra fields of bro_bot entry lost: %v", bot)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file permissions changed to %o", info.Mode().Perm())
	}
}

func TestEnsureServerSkipsUnchangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_config.json")
	if err := ensureServerInConfig(path, "bro_bot", "/bot", []string{"mcp-serve"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureServerInConfig(path, "bro_bot", "/bot", []string{"mcp-serve"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Перезапись идёт через rename, поэтому новый файл — это новый inode.
	if !os.SameFile(before, after) {
		t.Error("unchanged config was rewritten")
	}
}

func TestEnsureServerLeavesBrokenFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_config.json")
	broken := `{"mcpServers": {"github": `
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureServerInConfig(path, "bro_bot", "/bot", []string{"mcp-serve"}); err == nil {
		t.Fatal("expected an error for a broken config")
	}
	if data, _ := os.ReadFile(path); string(data) != broken {
		t.Errorf("broken config was overwritten: %s", data)
	}
}

func TestWriteTempMCPConfigFilePassesSocket(t *testing.T) {
	path, err := WriteTempMCPConfigFile("/run/bro/bridge.sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	servers := readJSON(t, path)["mcpServers"].(map[string]interface{})
	args := servers["bro_bot"].(map[string]interface{})["args"]
	if !reflect.DeepEqual(args, []interface{}{"mcp-serve", "--socket", "/run/bro/bridge.sock"}) {
		t.Errorf("args = %v", args)
	}
}
