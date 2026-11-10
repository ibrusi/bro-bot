package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
)

func TestUserWantsFile(t *testing.T) {
	cases := []struct {
		prompt string
		ok     bool
		name   string
	}{
		{prompt: "запиши в файл", ok: true},
		{prompt: "пришли ответ файлом", ok: true},
		{prompt: "сохрани список в виде файла", ok: true},
		{prompt: "запиши список в notes.txt", ok: true, name: "notes.txt"},
		{prompt: "save the list to notes.txt", ok: true, name: "notes.txt"},
		{prompt: "put the report in summary.md", ok: true, name: "summary.md"},
		{prompt: "export data.csv", ok: true, name: "data.csv"},
		{prompt: "write it to a file", ok: true},
		{prompt: "write it into a file", ok: true},
		{prompt: "send this as a file", ok: true},
		{prompt: "save this to file", ok: true},
		{prompt: "write unit tests", ok: false},
		{prompt: "look at README.md", ok: false},
		{prompt: "посмотри в файле README.md", ok: false},
		{prompt: "добавь timeout в handler.go", ok: false},
		{prompt: "I need to file a bug", ok: false},
		{prompt: "restore the database and look at data.json", ok: false},
		{prompt: "", ok: false},
	}
	for _, tc := range cases {
		name, ok := userWantsFile(tc.prompt)
		if ok != tc.ok || name != tc.name {
			t.Errorf("userWantsFile(%q) = %q, %v; want %q, %v", tc.prompt, name, ok, tc.name, tc.ok)
		}
	}
}

func TestResolveProjectPathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	inside, err := resolveProjectPath(root, "uploads/a.txt")
	if err != nil || inside != filepath.Join(root, "uploads", "a.txt") {
		t.Fatalf("relative path: %q, %v", inside, err)
	}
	absInside := filepath.Join(root, "notes.txt")
	got, err := resolveProjectPath(root, absInside)
	if err != nil || got != absInside {
		t.Fatalf("absolute inside: %q, %v", got, err)
	}
	for _, raw := range []string{"../secret", "foo/../../etc/passwd", filepath.Join(filepath.Dir(root), "outside.txt")} {
		if _, err := resolveProjectPath(root, raw); err == nil {
			t.Errorf("resolveProjectPath(%q) must reject the path", raw)
		}
	}
}

func TestSendProjectFileConfinesAndCapsSize(t *testing.T) {
	root := t.TempDir()
	notes := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(notes, []byte("saved-list"), 0644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(root, "huge.bin")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxTelegramFileSize + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	mt := mock.New()
	ref, err := sendProjectFile(mt, "1", root, "en", "notes.txt", "список")
	if err != nil || ref.ID == "" {
		t.Fatalf("send notes: ref=%+v err=%v", ref, err)
	}
	if len(mt.Documents) != 1 || mt.Documents[0].Doc.FileName != "notes.txt" || string(mt.Documents[0].Doc.Content) != "saved-list" {
		t.Fatalf("document = %+v", mt.Documents)
	}
	if mt.Documents[0].Doc.Caption != "список" || mt.Documents[0].Doc.MIME != "text/plain" {
		t.Fatalf("caption/mime = %q %q", mt.Documents[0].Doc.Caption, mt.Documents[0].Doc.MIME)
	}

	if _, err := sendProjectFile(mt, "1", root, "en", "../secret.txt", ""); err == nil {
		t.Fatal("a path outside the project must be rejected")
	}
	if _, err := sendProjectFile(mt, "1", root, "en", link, ""); err == nil {
		t.Fatal("a symlink out of the project must be rejected")
	}
	if _, err := sendProjectFile(mt, "1", root, "en", huge, ""); err == nil {
		t.Fatal("a file over 20 MB must be rejected")
	}
	if len(mt.Documents) != 1 {
		t.Fatalf("rejected paths must not be sent, documents = %d", len(mt.Documents))
	}
}

func TestDeliverRequestedFileSkipsSourceAndKeepsReadme(t *testing.T) {
	root := t.TempDir()
	readme := filepath.Join(root, "README.md")
	if err := os.WriteFile(readme, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	mainGo := filepath.Join(root, "main.go")
	if err := os.WriteFile(mainGo, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var files writtenFileCollector
	files.Observe("write", &domain.StreamToolInfo{Name: "write", Parameters: map[string]any{"path": "main.go"}})
	files.Observe("read", &domain.StreamToolInfo{Name: "read", Parameters: map[string]any{"path": "README.md"}})

	mt := mock.New()
	deliverRequestedFiles(mt, "1", root, "en", []string{"запиши в файл"}, files.Paths(), "полный ответ", nil)
	if len(mt.Documents) != 1 {
		t.Fatalf("documents = %+v", mt.Documents)
	}
	doc := mt.Documents[0].Doc
	if doc.FileName != "answer.md" || string(doc.Content) != "полный ответ" {
		t.Fatalf("fallback document = %q %q", doc.FileName, doc.Content)
	}
	saved := filepath.Join(root, uploadsDirName, "answer.md")
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("answer must be saved under uploads: %v", err)
	}
	body, err := os.ReadFile(readme)
	if err != nil || string(body) != "original" {
		t.Fatalf("README.md must stay untouched, got %q err=%v", body, err)
	}

	notes := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(notes, []byte("from-agent"), 0644); err != nil {
		t.Fatal(err)
	}
	var named writtenFileCollector
	named.Observe("write_file", &domain.StreamToolInfo{Parameters: map[string]any{"file_path": "main.go"}})
	named.Observe("Write", &domain.StreamToolInfo{Parameters: map[string]any{"path": notes}})
	named.Observe("edit", &domain.StreamToolInfo{Parameters: map[string]any{"TargetFile": "other.go"}})
	mt.Documents = nil
	deliverRequestedFiles(mt, "1", root, "en", []string{"save the list to notes.txt"}, named.Paths(), "не этот текст", nil)
	if len(mt.Documents) != 1 || string(mt.Documents[0].Doc.Content) != "from-agent" || mt.Documents[0].Doc.FileName != "notes.txt" {
		t.Fatalf("named file = %+v", mt.Documents)
	}
}

func TestStepSendsTheFileTheUserNamed(t *testing.T) {
	mt := setupTestApp(t)
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "notes.txt"), []byte("saved-list"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	const agentName = "file-agent"
	framework := &mock.AgentFramework{
		Name:     agentName,
		Response: "Готово.",
		ExtraEvents: []string{
			`{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"write","tool_info":{"name":"write","parameters":{"path":"main.go"}}}}`,
			`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"write","tool_info":{"name":"write","parameters":{"path":"notes.txt"}}}}`,
		},
	}
	reg := testAgentRegistry()
	reg.Register(agents.Spec{
		Name:   agentName,
		NewMCP: func() ports.AgentFramework { return framework },
		NewAPI: func() ports.AgentFramework { return framework },
	})
	setAgentRegistry(reg)
	if _, err := SwitchActiveAgent(agentName); err != nil {
		t.Fatal(err)
	}

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", agentName, "save the list to notes.txt", testChatID, false)
	task.Update(func(ts *domain.TaskSession) { ts.Status = domain.TaskStatusRunning })

	res := executeStepForTask(mt.Messenger, testChatID, task, workDir, "save the list to notes.txt", "mock-model", "en")
	if res.Outcome != StepOutcomeSuccess {
		t.Fatalf("outcome = %v, err = %v", res.Outcome, res.Error)
	}
	if len(mt.Documents) != 1 || mt.Documents[0].Doc.FileName != "notes.txt" || string(mt.Documents[0].Doc.Content) != "saved-list" {
		t.Fatalf("documents = %+v", mt.Documents)
	}
}

func TestChatSavesTheAnswerWhenNoFileWasWritten(t *testing.T) {
	mt, agent := setupChatTestApp(t, "полный текст ответа")
	agent.ExtraEvents = []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"write","input":{"path":"main.go"}}]}}`,
	}
	workDir := filepath.Join(config.ProjectsRoot, "testproj")
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sendText(t, mt, "запиши ответ в файл")
	waitFor(t, "файл с ответом", func() bool {
		session := domain.GlobalChatManager.Get("testproj")
		return session != nil && !session.IsRunning() && len(mt.Documents) > 0
	})

	doc := mt.Documents[0].Doc
	if doc.FileName != "answer.md" || !strings.Contains(string(doc.Content), "полный текст ответа") {
		t.Fatalf("document = %q %q", doc.FileName, doc.Content)
	}
	saved := filepath.Join(workDir, uploadsDirName, "answer.md")
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("chat answer must land in uploads: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "answer.md")); err == nil {
		t.Fatal("chat must not create answer.md in the project root")
	}
}
