package system

import (
	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSystemFlags(t *testing.T) {
	tests := []struct {
		args       []string
		wantPull   bool
		wantForce  bool
		wantBranch string
	}{
		{args: []string{}, wantPull: false, wantForce: false, wantBranch: ""},
		{args: []string{"pull"}, wantPull: true, wantForce: false, wantBranch: ""},
		{args: []string{"-p"}, wantPull: true, wantForce: false, wantBranch: ""},
		{args: []string{"force"}, wantPull: false, wantForce: true, wantBranch: ""},
		{args: []string{"-f"}, wantPull: false, wantForce: true, wantBranch: ""},
		{args: []string{"pull", "force"}, wantPull: true, wantForce: true, wantBranch: ""},
		{args: []string{"FORCE", "PULL"}, wantPull: true, wantForce: true, wantBranch: ""},
		{args: []string{"branch", "feat/my-branch"}, wantPull: false, wantForce: false, wantBranch: "feat/my-branch"},
		{args: []string{"-b", "test"}, wantPull: false, wantForce: false, wantBranch: "test"},
		{args: []string{"--branch", "main"}, wantPull: false, wantForce: false, wantBranch: "main"},
		{args: []string{"branch=feat/test"}, wantPull: false, wantForce: false, wantBranch: "feat/test"},
		{args: []string{"--branch=feat/test"}, wantPull: false, wantForce: false, wantBranch: "feat/test"},
		{args: []string{"unknown", "arg"}, wantPull: false, wantForce: false, wantBranch: ""},
	}

	for _, tt := range tests {
		got := parseSystemFlags(tt.args)
		if got.Pull != tt.wantPull || got.Force != tt.wantForce || got.Branch != tt.wantBranch {
			t.Errorf("parseSystemFlags(%v) = {Pull: %v, Force: %v, Branch: %v}, want {Pull: %v, Force: %v, Branch: %v}",
				tt.args, got.Pull, got.Force, got.Branch, tt.wantPull, tt.wantForce, tt.wantBranch)
		}
	}
}

func TestValidateBranchName(t *testing.T) {
	valid := []string{
		"main",
		"develop",
		"feat/quota-limits",
		"claude/intelligent-volta-vfwfj5",
		"release-1.2.3",
		"v2",
	}
	for _, name := range valid {
		if err := validateBranchName(name); err != nil {
			t.Errorf("validateBranchName(%q) вернул ошибку: %v", name, err)
		}
	}

	// Ключевой случай: имя, начинающееся с дефиса, git разберёт как опцию,
	// а не как ветку.
	invalid := map[string]string{
		"подстановка опции": "--upload-pack=touch /tmp/pwn",
		"короткая опция":    "-f",
		"пустое":            "",
		"только пробелы":    "   ",
		"две точки":         "feat/..\\/etc",
		"переход вверх":     "../main",
		"двойной слэш":      "feat//x",
		"reflog":            "main@{1}",
		"завершающий слэш":  "feat/",
		"завершающая точка": "main.",
		"суффикс lock":      "main.lock",
		"пробел внутри":     "feat x",
		"точка с запятой":   "main;rm -rf /",
		"перевод строки":    "main\nls",
	}
	for what, name := range invalid {
		if err := validateBranchName(name); err == nil {
			t.Errorf("validateBranchName(%q) (%s) должен был вернуть ошибку", name, what)
		}
	}
}

func TestRestartMarkerSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()

	marker := RestartMarker{
		ChatID:      "123456789",
		MessageID:   "42",
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
	if err := os.MkdirAll(filepath.Join(tmpDir, "cmd", "bot"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "cmd", "bot", "main.go"), []byte(badGo), 0644); err != nil {
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
	if err := os.MkdirAll(filepath.Join(tmpDir, "cmd", "bot"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "cmd", "bot", "main.go"), []byte(validGo), 0644); err != nil {
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
		{"git@github.com:ibrusi/bro-bot.git", "github.com/ibrusi/bro-bot"},
		{"https://github.com/ibrusi/bro-bot.git", "github.com/ibrusi/bro-bot"},
		{"http://github.com/ibrusi/bro-bot", "github.com/ibrusi/bro-bot"},
		{"ssh://git@github.com/ibrusi/bro-bot.git", "github.com/ibrusi/bro-bot"},
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
	if !isBotProject("bro-bot", "/home/deploy/bro-bot", "") {
		t.Errorf("expected bro-bot to be recognized as bot project")
	}
	if !isBotProject("bro-bot", "/home/deploy/bro-bot", "") {
		t.Errorf("expected bro-bot to be recognized as bot project")
	}
	if !isBotProject("/home/deploy/projects/bro-bot", "/home/deploy/bro-bot", "") {
		t.Errorf("expected path to bro-bot to be recognized as bot project")
	}
	if !isBotProject("my-bot", "/opt/bots/my-bot", "") {
		t.Errorf("expected basename match to be recognized as bot project")
	}
	if isBotProject("other-web-app", "/opt/bots/my-bot", "") {
		t.Errorf("expected other-web-app to NOT be recognized as bot project")
	}

	// Проверка через проект по умолчанию из конфигурации
	setConfigDefaultProject(t, "custom-bot-project")
	if !isBotProject("custom-bot-project", "/tmp/bot", "") {
		t.Errorf("expected custom-bot-project to match config.DefaultProject")
	}
}

func TestCheckActiveTasksForSystemAction(t *testing.T) {
	// Сохраняем исходный domain.GlobalTaskManager и восстанавливаем после теста
	origTM := domain.GlobalTaskManager
	defer func() { domain.GlobalTaskManager = origTM }()

	// 1. Нет активных задач
	testTM := domain.NewTaskManager()
	domain.GlobalTaskManager = testTM

	warn, blocked := checkActiveTasksForSystemAction("/rebuild", SystemFlags{}, "/home/deploy/bro-bot", "/home/deploy/projects", "ru")
	if blocked || warn != "" {
		t.Errorf("expected no block when no tasks active, got blocked=%v, warn=%s", blocked, warn)
	}

	// 2. Активная задача на проекте бота при /rebuild pull
	task := testTM.CreateTask("bro-bot", "gemini", "Делаем рефакторинг", ports.ChatID("123"))
	task.Update(func(t *domain.TaskSession) { t.Status = domain.TaskStatusRunning })

	warn, blocked = checkActiveTasksForSystemAction("/rebuild pull", SystemFlags{}, "/home/deploy/bro-bot", "/home/deploy/projects", "ru")
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
	task2 := testTM2.CreateTask("some-other-project", "gemini", "Фича для сайта", ports.ChatID("123"))
	task2.Update(func(t *domain.TaskSession) { t.Status = domain.TaskStatusRunning })

	warn, blocked = checkActiveTasksForSystemAction("/rebuild", SystemFlags{}, "/home/deploy/bro-bot", "/home/deploy/projects", "ru")
	if !blocked {
		t.Errorf("expected block for active task on other project without force")
	}
	if !strings.Contains(warn, "Выполняются активные задачи") {
		t.Errorf("expected warning to mention active tasks, got: %s", warn)
	}

	// 4. Флаг Force отменяет задачи и разрешает выполнение
	warn, blocked = checkActiveTasksForSystemAction("/rebuild", SystemFlags{Force: true}, "/home/deploy/bro-bot", "/home/deploy/projects", "ru")
	if blocked || warn != "" {
		t.Errorf("expected Force to allow operation, got blocked=%v, warn=%s", blocked, warn)
	}
	if st := task2.Snapshot().Status; st != domain.TaskStatusCancelled {
		t.Errorf("expected task2 to be cancelled by Force, got status %s", st)
	}
}

// stubTransport — минимальный ports.Transport поверх мок-мессенджера: HandleRebuild
// нужен только для отправки и правки статусных сообщений.
type stubTransport struct {
	*mock.Messenger
}

func (stubTransport) OnCommand(string, ports.Handler)       {}
func (stubTransport) OnText(ports.Handler)                  {}
func (stubTransport) OnCallback(string, ports.Handler)      {}
func (stubTransport) Use(func(ports.Handler) ports.Handler) {}
func (stubTransport) Start(context.Context) error           { return nil }
func (stubTransport) Stop()                                 {}

// TestHandleRebuildRejectsBadBranchName сторожит не саму validateBranchName, а её вызов
// в HandleRebuild: без него имя вроде "--upload-pack=..." ушло бы в git как опция, и
// сообщение было бы уже про ошибку git, а не про недопустимое имя.
func TestHandleRebuildRejectsBadBranchName(t *testing.T) {
	origTM := domain.GlobalTaskManager
	defer func() { domain.GlobalTaskManager = origTM }()
	domain.GlobalTaskManager = domain.NewTaskManager()

	botDir := t.TempDir()
	setConfigBotDir(t, botDir)

	transport := stubTransport{Messenger: mock.New()}
	sess := &mock.Session{
		M:       transport.Messenger,
		ChatID:  ports.ChatID("12345"),
		Sender:  "12345",
		ArgsVal: []string{"branch=--upload-pack=touch /tmp/pwn"},
	}

	if err := HandleRebuild(transport, sess); err != nil {
		t.Fatalf("HandleRebuild вернул ошибку: %v", err)
	}

	texts := transport.AllTexts()
	if len(texts) == 0 {
		t.Fatal("HandleRebuild ничего не отправил")
	}
	last := texts[len(texts)-1]
	if !strings.Contains(last, "Invalid branch name") {
		t.Errorf("ожидали отказ до обращения к git, получили: %s", last)
	}
	// Сборка не должна была начаться.
	if _, err := os.Stat(filepath.Join(botDir, "bot")); err == nil {
		t.Error("сборка не должна была выполниться")
	}
}

// setConfigDefaultProject и setConfigBotDir задают снимок конфигурации на время теста:
// окружение с появлением config.Load здесь больше ни на что не влияет.
func setConfigDefaultProject(t *testing.T, value string) {
	t.Helper()

	prev := config.DefaultProject
	config.DefaultProject = value
	t.Cleanup(func() { config.DefaultProject = prev })
}

func setConfigBotDir(t *testing.T, value string) {
	t.Helper()

	prev := config.BotDir
	config.BotDir = value
	t.Cleanup(func() { config.BotDir = prev })
}
