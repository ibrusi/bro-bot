package system

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"tg-agent-bot/internal/domain"
	"time"

	tele "gopkg.in/telebot.v3"
)

func TestParseSystemFlags(t *testing.T) {
	tests := []struct {
		args      []string
		wantPull  bool
		wantForce bool
	}{
		{args: []string{}, wantPull: false, wantForce: false},
		{args: []string{"pull"}, wantPull: true, wantForce: false},
		{args: []string{"-p"}, wantPull: true, wantForce: false},
		{args: []string{"force"}, wantPull: false, wantForce: true},
		{args: []string{"-f"}, wantPull: false, wantForce: true},
		{args: []string{"pull", "force"}, wantPull: true, wantForce: true},
		{args: []string{"FORCE", "PULL"}, wantPull: true, wantForce: true},
		{args: []string{"unknown", "arg"}, wantPull: false, wantForce: false},
	}

	for _, tt := range tests {
		got := parseSystemFlags(tt.args)
		if got.Pull != tt.wantPull || got.Force != tt.wantForce {
			t.Errorf("parseSystemFlags(%v) = {Pull: %v, Force: %v}, want {Pull: %v, Force: %v}",
				tt.args, got.Pull, got.Force, tt.wantPull, tt.wantForce)
		}
	}
}

func TestRestartMarkerSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()

	marker := RestartMarker{
		ChatID:      123456789,
		MessageID:   42,
		Action:      "rebuild",
		TriggeredAt: time.Now().Truncate(time.Second),
		GitCommit:   "abc1234",
		GitBranch:   "main",
	}

	if err := saveRestartMarker(tmpDir, marker); err != nil {
		t.Fatalf("saveRestartMarker failed: %v", err)
	}

	loaded, err := loadAndClearRestartMarker(tmpDir)
	if err != nil {
		t.Fatalf("loadAndClearRestartMarker failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil marker")
	}

	if loaded.ChatID != marker.ChatID || loaded.MessageID != marker.MessageID || loaded.Action != marker.Action {
		t.Errorf("loaded marker mismatch: got %+v, want %+v", loaded, marker)
	}

	// Should be deleted after load
	loadedSecond, err := loadAndClearRestartMarker(tmpDir)
	if err != nil {
		t.Fatalf("second load error: %v", err)
	}
	if loadedSecond != nil {
		t.Errorf("expected nil marker on second load, got %+v", loadedSecond)
	}
}

func TestPerformBuildInvalidCode(t *testing.T) {
	tmpDir := t.TempDir()

	// Write invalid go file
	badGo := `package system
func main() { syntax error here
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(badGo), 0644); err != nil {
		t.Fatal(err)
	}
	// Write go.mod
	goMod := `module testbot
go 1.22
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := performBuild(ctx, tmpDir)
	if err == nil {
		t.Fatal("expected build error for invalid go code, got nil")
	}

	// Check that tmp binary does not exist
	if _, err := os.Stat(filepath.Join(tmpDir, "bot.tmp")); !os.IsNotExist(err) {
		t.Errorf("bot.tmp should have been cleaned up on error")
	}
}

func TestPerformBuildValidCode(t *testing.T) {
	tmpDir := t.TempDir()

	// Write valid go file
	validGo := `package system
import "fmt"
func main() { fmt.Println("hello") }
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(validGo), 0644); err != nil {
		t.Fatal(err)
	}
	goMod := `module testbot
go 1.22
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := performBuild(ctx, tmpDir)
	if err != nil {
		t.Fatalf("expected build to succeed, got error: %v", err)
	}

	targetBin := filepath.Join(tmpDir, "bot")
	fi, err := os.Stat(targetBin)
	if err != nil {
		t.Fatalf("expected target binary %s to exist: %v", targetBin, err)
	}
	if fi.Mode()&0111 == 0 {
		t.Errorf("expected target binary to be executable, got mode: %v", fi.Mode())
	}
}

func TestPerformGitCheckout(t *testing.T) {
	tmpDir := t.TempDir()

	// Инициализируем git репозиторий
	runGit := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", tmpDir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v, output: %s", args, err, string(out))
		}
	}

	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")

	// Создаем тестовый файл и коммит
	testFile := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial commit")

	// Создаем ветку feat-test
	runGit("branch", "feat-test")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Переключаемся на feat-test без force
	out, err := performGitCheckout(ctx, tmpDir, "feat-test", false)
	if err != nil {
		t.Fatalf("performGitCheckout to feat-test failed: %v, out: %s", err, out)
	}

	branch := getGitBranch(tmpDir)
	if branch != "feat-test" {
		t.Errorf("expected branch feat-test, got %s", branch)
	}

	// Создаем файл на feat-test и коммитим
	file2 := filepath.Join(tmpDir, "feature.txt")
	if err := os.WriteFile(file2, []byte("feature content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "feature.txt")
	runGit("commit", "-m", "feature commit")

	// Теперь модифицируем README.md без коммита
	if err := os.WriteFile(testFile, []byte("# Modified Repo\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Попытка переключения на master/main с флагом force=true
	defaultBranch := "master"
	outMaster, errMaster := exec.Command("git", "-C", tmpDir, "rev-parse", "--verify", "main").CombinedOutput()
	if errMaster == nil && len(outMaster) > 0 {
		defaultBranch = "main"
	}

	outForce, errForce := performGitCheckout(ctx, tmpDir, defaultBranch, true)
	if errForce != nil {
		t.Fatalf("expected forced checkout to succeed, got err: %v, out: %s", errForce, outForce)
	}

	// Проверяем попытку переключения на несуществующую ветку
	_, err = performGitCheckout(ctx, tmpDir, "non-existent-branch", false)
	if err == nil {
		t.Errorf("expected error when checking out non-existent branch, got nil")
	}
}

func TestNormalizeGitURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"git@github.com:ibrusi/tg-bot-agent.git", "github.com/ibrusi/tg-bot-agent"},
		{"https://github.com/ibrusi/tg-bot-agent.git", "github.com/ibrusi/tg-bot-agent"},
		{"http://github.com/ibrusi/tg-bot-agent", "github.com/ibrusi/tg-bot-agent"},
		{"ssh://git@github.com/ibrusi/tg-bot-agent.git", "github.com/ibrusi/tg-bot-agent"},
		{"https://gitlab.com/group/sub/repo.git/", "gitlab.com/group/sub/repo"},
	}

	for _, tc := range tests {
		got := normalizeGitURL(tc.input)
		if got != tc.expected {
			t.Errorf("normalizeGitURL(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestIsBotProject(t *testing.T) {
	if !isBotProject("tg-bot-agent", "/home/deploy/tg-agent-bot", "") {
		t.Errorf("expected tg-bot-agent to be recognized as bot project")
	}
	if !isBotProject("tg-agent-bot", "/home/deploy/tg-agent-bot", "") {
		t.Errorf("expected tg-agent-bot to be recognized as bot project")
	}
	if !isBotProject("/home/deploy/projects/tg-bot-agent", "/home/deploy/tg-agent-bot", "") {
		t.Errorf("expected path to tg-bot-agent to be recognized as bot project")
	}
	if !isBotProject("my-bot", "/opt/bots/my-bot", "") {
		t.Errorf("expected basename match to be recognized as bot project")
	}
	if isBotProject("other-web-app", "/opt/bots/my-bot", "") {
		t.Errorf("expected other-web-app to NOT be recognized as bot project")
	}

	// Проверка через DEFAULT_PROJECT
	os.Setenv("DEFAULT_PROJECT", "custom-bot-project")
	defer os.Unsetenv("DEFAULT_PROJECT")
	if !isBotProject("custom-bot-project", "/tmp/bot", "") {
		t.Errorf("expected custom-bot-project to match DEFAULT_PROJECT")
	}
}

func TestCheckActiveTasksForSystemAction(t *testing.T) {
	// Сохраняем исходный domain.GlobalTaskManager и восстанавливаем после теста
	origTM := domain.GlobalTaskManager
	defer func() { domain.GlobalTaskManager = origTM }()

	// 1. Нет активных задач
	testTM := domain.NewTaskManager()
	domain.GlobalTaskManager = testTM

	warn, blocked := checkActiveTasksForSystemAction("/rebuild", SystemFlags{}, "/home/deploy/tg-agent-bot", "/home/deploy/projects")
	if blocked || warn != "" {
		t.Errorf("expected no block when no tasks active, got blocked=%v, warn=%s", blocked, warn)
	}

	// 2. Активная задача на проекте бота при /rebuild pull
	task := testTM.CreateTask("tg-bot-agent", "gemini", "Делаем рефакторинг", tele.ChatID(123))
	task.Status = domain.TaskStatusRunning

	warn, blocked = checkActiveTasksForSystemAction("/rebuild pull", SystemFlags{}, "/home/deploy/tg-agent-bot", "/home/deploy/projects")
	if !blocked {
		t.Errorf("expected block for active task on bot project")
	}
	if !strings.Contains(warn, "На проекте бота выполняется активная задача") {
		t.Errorf("expected warning to mention bot project task, got: %s", warn)
	}
	if !strings.Contains(warn, "Смена ветки на main, git pull и сборка") {
		t.Errorf("expected warning to mention branch switch and pull, got: %s", warn)
	}

	// 3. Активная задача на другом проекте
	testTM2 := domain.NewTaskManager()
	domain.GlobalTaskManager = testTM2
	task2 := testTM2.CreateTask("some-other-project", "gemini", "Фича для сайта", tele.ChatID(123))
	task2.Status = domain.TaskStatusRunning

	warn, blocked = checkActiveTasksForSystemAction("/rebuild", SystemFlags{}, "/home/deploy/tg-agent-bot", "/home/deploy/projects")
	if !blocked {
		t.Errorf("expected block for active task on other project without force")
	}
	if !strings.Contains(warn, "Выполняются активные задачи") {
		t.Errorf("expected warning to mention active tasks, got: %s", warn)
	}

	// 4. Флаг Force отменяет задачи и разрешает выполнение
	warn, blocked = checkActiveTasksForSystemAction("/rebuild", SystemFlags{Force: true}, "/home/deploy/tg-agent-bot", "/home/deploy/projects")
	if blocked || warn != "" {
		t.Errorf("expected Force to allow operation, got blocked=%v, warn=%s", blocked, warn)
	}
	if task2.Status != domain.TaskStatusCancelled {
		t.Errorf("expected task2 to be cancelled by Force, got status %s", task2.Status)
	}
}
