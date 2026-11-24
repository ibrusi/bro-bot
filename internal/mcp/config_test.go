package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
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

// TestRemoveServerKeepsForeignSettings проверяет, что удаление bro_bot сохраняет
// остальные серверы, верхнеуровневые настройки и права доступа к файлу.
func TestRemoveServerKeepsForeignSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_config.json")
	original := `{
  "theme": "dark",
  "mcpServers": {
    "github": {"command": "gh-mcp", "args": ["serve"], "env": {"GITHUB_TOKEN": "secret"}, "cwd": "/srv"},
    "remote": {"url": "https://example.test/mcp", "headers": {"Authorization": "Bearer x"}},
    "bro_bot": {"command": "/old/bot", "args": ["mcp-serve"]}
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeServerFromConfig(path, "bro_bot"); err != nil {
		t.Fatalf("removeServerFromConfig: %v", err)
	}

	cfg := readJSON(t, path)
	if cfg["theme"] != "dark" {
		t.Errorf("top-level key lost: %v", cfg)
	}
	servers := cfg["mcpServers"].(map[string]interface{})
	if _, exists := servers["bro_bot"]; exists {
		t.Errorf("bro_bot entry was not removed: %v", servers)
	}
	var want map[string]interface{}
	_ = json.Unmarshal([]byte(original), &want)
	wantServers := want["mcpServers"].(map[string]interface{})
	for _, name := range []string{"github", "remote"} {
		if !reflect.DeepEqual(servers[name], wantServers[name]) {
			t.Errorf("server %s changed: %v", name, servers[name])
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file permissions changed to %o", info.Mode().Perm())
	}
}

func TestRemoveServerSkipsUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	if err := removeServerFromConfig(missing, "bro_bot"); err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("missing file must not be created")
	}

	path := filepath.Join(dir, "mcp_config.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"github":{"command":"gh"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeServerFromConfig(path, "bro_bot"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("file without bro_bot entry was rewritten")
	}
}

func TestRemoveServerLeavesBrokenFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_config.json")
	broken := `{"mcpServers": {"github": `
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeServerFromConfig(path, "bro_bot"); err == nil {
		t.Fatal("expected an error for a broken config")
	}
	if data, _ := os.ReadFile(path); string(data) != broken {
		t.Errorf("broken config was overwritten: %s", data)
	}
}

func TestRemoveAgyMCPConfigCleansBothLocationsConcurrently(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	paths := []string{
		filepath.Join(home, ".gemini", "config", "mcp_config.json"),
		filepath.Join(home, ".gemini", "antigravity-cli", "mcp_config.json"),
	}
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		data := `{"mcpServers":{"bro_bot":{"command":"/bot","args":["mcp-serve"]},"keep":{"command":"x"}}}`
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := RemoveAgyMCPConfig(); err != nil {
				t.Errorf("RemoveAgyMCPConfig: %v", err)
			}
		}()
	}
	wg.Wait()

	for _, p := range paths {
		servers := readJSON(t, p)["mcpServers"].(map[string]interface{})
		if _, ok := servers["bro_bot"]; ok {
			t.Errorf("%s still contains bro_bot: %v", p, servers)
		}
		if _, ok := servers["keep"]; !ok {
			t.Errorf("%s lost foreign server keep: %v", p, servers)
		}
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
