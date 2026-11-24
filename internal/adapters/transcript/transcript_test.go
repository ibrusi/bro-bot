package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSessionFile(t *testing.T, home, workDir, conversationID, content string) {
	t.Helper()
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	dir := filepath.Join(home, ".claude", "projects", projectSlug(absWorkDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, conversationID+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestReadSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := "/home/deploy/projects/demo-project"
	convID := "11111111-1111-1111-1111-111111111111"

	lines := []string{
		`{"type":"queue-operation","operation":"enqueue"}`,
		`{"type":"user","isSidechain":false,"timestamp":"t1","message":{"role":"user","content":"Привет, агент"}}`,
		`{"type":"assistant","isSidechain":false,"timestamp":"t2","message":{"role":"assistant","content":[{"type":"thinking","thinking":"..."},{"type":"text","text":"Привет! Чем помочь?"}]}}`,
		`{"type":"user","isSidechain":false,"timestamp":"t3","message":{"role":"user","content":[{"type":"tool_result","content":"some tool output"}]}}`,
		`{"type":"assistant","isSidechain":true,"timestamp":"t4","message":{"role":"assistant","content":[{"type":"text","text":"Скрытый ответ субагента"}]}}`,
		`{"type":"assistant","isSidechain":false,"timestamp":"t5","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{}}]}}`,
		`{"type":"assistant","isSidechain":false,"timestamp":"t6","message":{"role":"assistant","content":[{"type":"text","text":"Готово"}]}}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	writeSessionFile(t, home, workDir, convID, content)

	turns, err := ReadSession(workDir, convID)
	if err != nil {
		t.Fatalf("ReadSession() error = %v", err)
	}

	want := []Turn{
		{Role: "user", Text: "Привет, агент", Timestamp: "t1"},
		{Role: "assistant", Text: "Привет! Чем помочь?", Timestamp: "t2"},
		{Role: "assistant", Text: "Готово", Timestamp: "t6"},
	}
	if len(turns) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(turns), len(want), turns)
	}
	for i, w := range want {
		if turns[i].Role != w.Role || turns[i].Text != w.Text || turns[i].Timestamp != w.Timestamp {
			t.Errorf("turn %d = %+v, want %+v", i, turns[i], w)
		}
	}
}

func TestReadSessionNotFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := ReadSession("/home/deploy/projects/demo-project", "missing-session-id")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("ReadSession() error = %v, want ErrSessionNotFound", err)
	}
}

func TestReadSessionFallsBackToAgyTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	convID := "agy-conv-uuid-123"
	agyPath, err := AgySessionFilePath(convID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(agyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		`{"type":"GENERIC","source":"SYSTEM","created_at":"t0","content":"skip me"}`,
		`{"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"t1","content":"<USER_REQUEST>\nСобери релиз\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\ntime\n</ADDITIONAL_METADATA>"}`,
		`{"type":"PLANNER_RESPONSE","source":"MODEL","created_at":"t2","thinking":"hmm","content":"План готов."}`,
	}, "\n") + "\n"
	if err := os.WriteFile(agyPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	turns, err := ReadSession("/home/deploy/projects/demo-project", convID)
	if err != nil {
		t.Fatalf("ReadSession agy fallback failed: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].Text != "Собери релиз" {
		t.Errorf("unexpected first turn: %+v", turns[0])
	}
	if turns[1].Role != "assistant" || turns[1].Text != "План готов." {
		t.Errorf("unexpected second turn: %+v", turns[1])
	}
}
