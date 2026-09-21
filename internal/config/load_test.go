package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"bro-bot/internal/ports"
)

// validEnv — минимальный набор, при котором Load обязана отработать без ошибок.
// Токена здесь нет намеренно: Load его не читает, и тесты его не задают.
func validEnv(botDir string) map[string]string {
	return map[string]string{
		envAdminID:         "12345",
		envProjectsRoot:    "/projects",
		envQuestionTimeout: "15m",
		envServiceName:     "bro-bot.service",
		envDefaultModel:    "gemini-3.1-pro-high",
		envBotDir:          botDir,
	}
}

// setEnv задаёт ровно указанный набор переменных и снимает все остальные, которые
// читает Load: иначе значение из соседнего теста или из окружения запуска
// незаметно меняло бы результат.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()

	all := []string{
		envMessenger, envAdminID, envProjectsRoot, envDefaultProject, envDefaultModel,
		envQuestionTimeout, envStepTimeout, envChatTimeout, envAutoContinueMax, envBotDir, envServiceName,
		envDBPath, envScriptsDir,
		envWhisperServerURL, envWhisperAPIKey, envWhisperModel, envWhisperLanguage,
		envWhisperPrompt, envWhisperTemperature, envWhisperLoudnorm, envWhisperTimeout,
		envDebug, envDebugLower,
	}
	for _, name := range all {
		if value, ok := env[name]; ok {
			t.Setenv(name, value)
		} else {
			t.Setenv(name, "")
		}
	}
}

func without(env map[string]string, names ...string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		out[k] = v
	}
	for _, name := range names {
		delete(out, name)
	}
	return out
}

func with(env map[string]string, name, value string) map[string]string {
	out := without(env)
	out[name] = value
	return out
}

func TestLoadRejectsBadRequiredValues(t *testing.T) {
	botDir := t.TempDir()
	base := validEnv(botDir)

	cases := []struct {
		name    string
		env     map[string]string
		wantVar string
	}{
		{"нет TELEGRAM_ADMIN_ID", without(base, envAdminID), envAdminID},
		{"TELEGRAM_ADMIN_ID не число", with(base, envAdminID, "abc"), envAdminID},
		{"TELEGRAM_ADMIN_ID нулевой", with(base, envAdminID, "0"), envAdminID},
		{"нет PROJECTS_ROOT", without(base, envProjectsRoot), envProjectsRoot},
		{"нет QUESTION_TIMEOUT", without(base, envQuestionTimeout), envQuestionTimeout},
		{"QUESTION_TIMEOUT мусор", with(base, envQuestionTimeout, "abc"), envQuestionTimeout},
		{"QUESTION_TIMEOUT нулевой", with(base, envQuestionTimeout, "0"), envQuestionTimeout},
		{"QUESTION_TIMEOUT отрицательный", with(base, envQuestionTimeout, "-5"), envQuestionTimeout},
		{"нет BOT_SERVICE_NAME", without(base, envServiceName), envServiceName},
		{"нет DEFAULT_MODEL", without(base, envDefaultModel), envDefaultModel},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, tc.env)

			_, err := Load()
			if err == nil {
				t.Fatalf("ожидалась ошибка про %s", tc.wantVar)
			}
			if !strings.Contains(err.Error(), tc.wantVar) {
				t.Errorf("ошибка не называет %s: %v", tc.wantVar, err)
			}
		})
	}
}

// TestLoadReportsEveryProblemAtOnce — ради этого Load и накапливает список: оператор
// должен починить .env за один проход, а не узнавать о бедах по одной за перезапуск.
func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	setEnv(t, without(validEnv(t.TempDir()), envAdminID, envProjectsRoot, envDefaultModel))

	_, err := Load()
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	for _, name := range []string{envAdminID, envProjectsRoot, envDefaultModel} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("в общей ошибке нет %s: %v", name, err)
		}
	}
}

func TestLoadParsesDurationFormats(t *testing.T) {
	botDir := t.TempDir()

	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"15m", 15 * time.Minute},
		{"1h", time.Hour},
		{"300s", 300 * time.Second},
		{"900", 900 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			env := validEnv(botDir)
			env[envQuestionTimeout] = tc.raw
			env[envStepTimeout] = tc.raw
			env[envChatTimeout] = tc.raw
			setEnv(t, env)

			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.QuestionTimeout != tc.want {
				t.Errorf("QuestionTimeout = %v, ожидалось %v", cfg.QuestionTimeout, tc.want)
			}
			if cfg.StepTimeout != tc.want {
				t.Errorf("StepTimeout = %v, ожидалось %v", cfg.StepTimeout, tc.want)
			}
			if cfg.ChatTimeout != tc.want {
				t.Errorf("ChatTimeout = %v, ожидалось %v", cfg.ChatTimeout, tc.want)
			}
		})
	}
}

func TestLoadAutoContinueMax(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    int
		warning bool
	}{
		{name: "не задан — значение по умолчанию", raw: "", want: defaultAutoContinueMax},
		{name: "ноль отключает автопродолжение", raw: "0", want: 0},
		{name: "положительное число", raw: " 5 ", want: 5},
		{name: "отрицательное — значение по умолчанию", raw: "-1", want: defaultAutoContinueMax, warning: true},
		{name: "мусор — значение по умолчанию", raw: "abc", want: defaultAutoContinueMax, warning: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv(t.TempDir())
			env[envAutoContinueMax] = tc.raw
			setEnv(t, env)

			var warned bool
			prevLogf := logf
			logf = func(format string, args ...any) {
				if strings.Contains(format, "%s") && len(args) > 0 && args[0] == envAutoContinueMax {
					warned = true
				}
			}
			t.Cleanup(func() { logf = prevLogf })

			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.AutoContinueMax != tc.want {
				t.Errorf("AutoContinueMax = %d, ожидалось %d", cfg.AutoContinueMax, tc.want)
			}
			if warned != tc.warning {
				t.Errorf("предупреждение = %v, ожидалось %v", warned, tc.warning)
			}
		})
	}
}

func TestLoadFillsDefaults(t *testing.T) {
	botDir := t.TempDir()
	setEnv(t, validEnv(botDir))

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Messenger != defaultMessenger {
		t.Errorf("Messenger = %q, ожидалось %q", cfg.Messenger, defaultMessenger)
	}
	if cfg.StepTimeout != defaultStepTimeout {
		t.Errorf("StepTimeout = %v, ожидалось %v", cfg.StepTimeout, defaultStepTimeout)
	}
	if cfg.ChatTimeout != defaultChatTimeout {
		t.Errorf("ChatTimeout = %v, ожидалось %v", cfg.ChatTimeout, defaultChatTimeout)
	}
	if cfg.AutoContinueMax != defaultAutoContinueMax {
		t.Errorf("AutoContinueMax = %d, ожидалось %d", cfg.AutoContinueMax, defaultAutoContinueMax)
	}
	if want := filepath.Join(botDir, "data", "bot.db"); cfg.DBPath != want {
		t.Errorf("DBPath = %q, ожидалось %q", cfg.DBPath, want)
	}
	if want := filepath.Join(botDir, "scripts"); cfg.ScriptsDir != want {
		t.Errorf("ScriptsDir = %q, ожидалось %q", cfg.ScriptsDir, want)
	}
	if cfg.DefaultProject != "" {
		t.Errorf("DefaultProject = %q, ожидалась пустая строка", cfg.DefaultProject)
	}
	if cfg.AdminID != ports.ChatID("12345") {
		t.Errorf("AdminID = %q, ожидалось 12345", cfg.AdminID)
	}
	if cfg.WhisperModel != defaultWhisperModel {
		t.Errorf("WhisperModel = %q, ожидалось %q", cfg.WhisperModel, defaultWhisperModel)
	}
	if cfg.WhisperLanguage != defaultWhisperLanguage {
		t.Errorf("WhisperLanguage = %q, ожидалось %q", cfg.WhisperLanguage, defaultWhisperLanguage)
	}
	if cfg.WhisperTimeout != defaultWhisperTimeout {
		t.Errorf("WhisperTimeout = %v, ожидалось %v", cfg.WhisperTimeout, defaultWhisperTimeout)
	}
	if cfg.Debug {
		t.Errorf("Debug = %v, ожидалось false", cfg.Debug)
	}
}

func TestLoadDebugFlag(t *testing.T) {
	botDir := t.TempDir()

	// 1. По умолчанию DEBUG не задан -> false
	setEnv(t, validEnv(botDir))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Debug {
		t.Errorf("expected Debug to be false by default, got true")
	}

	// 2. DEBUG=true -> true
	envWithDebug := validEnv(botDir)
	envWithDebug[envDebug] = "true"
	setEnv(t, envWithDebug)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if !cfg.Debug {
		t.Errorf("expected Debug to be true, got false")
	}

	// 3. debug=1 (нижний регистр) -> true
	envWithLower := validEnv(botDir)
	envWithLower[envDebugLower] = "1"
	setEnv(t, envWithLower)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if !cfg.Debug {
		t.Errorf("expected Debug to be true for debug=1, got false")
	}

	// 4. DEBUG=false -> false
	envWithFalse := validEnv(botDir)
	envWithFalse[envDebug] = "false"
	setEnv(t, envWithFalse)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Debug {
		t.Errorf("expected Debug to be false for DEBUG=false, got true")
	}
}

func TestLoadWhisperOptions(t *testing.T) {
	botDir := t.TempDir()

	// 1. По умолчанию (WHISPER_PROMPT не задан в env — словарь пуст)
	setEnv(t, validEnv(botDir))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.WhisperPrompt != "" {
		t.Errorf("expected empty WhisperPrompt by default, got %q", cfg.WhisperPrompt)
	}
	if cfg.WhisperTemperature != defaultWhisperTemperature {
		t.Errorf("expected default WhisperTemperature %v, got %v", defaultWhisperTemperature, cfg.WhisperTemperature)
	}
	if cfg.WhisperLoudnorm != defaultWhisperLoudnorm {
		t.Errorf("expected default WhisperLoudnorm %v, got %v", defaultWhisperLoudnorm, cfg.WhisperLoudnorm)
	}

	// 2. Явные пользовательские значения
	envCustom := validEnv(botDir)
	envCustom[envWhisperPrompt] = "дебаг, код, тестирование"
	envCustom[envWhisperTemperature] = "0.2"
	envCustom[envWhisperLoudnorm] = "false"
	setEnv(t, envCustom)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.WhisperPrompt != "дебаг, код, тестирование" {
		t.Errorf("expected WhisperPrompt 'дебаг, код, тестирование', got %q", cfg.WhisperPrompt)
	}
	if cfg.WhisperTemperature != 0.2 {
		t.Errorf("expected WhisperTemperature 0.2, got %v", cfg.WhisperTemperature)
	}
	if cfg.WhisperLoudnorm {
		t.Errorf("expected WhisperLoudnorm false, got true")
	}

	// 3. Отключение промпта (none / off / false)
	for _, val := range []string{"none", "off", "false", "NONE", "Off"} {
		envDisabled := validEnv(botDir)
		envDisabled[envWhisperPrompt] = val
		setEnv(t, envDisabled)
		cfgDisabled, err := Load()
		if err != nil {
			t.Fatalf("Load() failed for %q: %v", val, err)
		}
		if cfgDisabled.WhisperPrompt != "" {
			t.Errorf("expected empty WhisperPrompt for %q, got %q", val, cfgDisabled.WhisperPrompt)
		}
	}

	// 4. Некорректная температура (откат к значению по умолчанию с предупреждением)
	var warnings []string
	prev := logf
	logf = func(format string, args ...any) {
		warnings = append(warnings, format)
	}
	defer func() { logf = prev }()

	envBadTemp := validEnv(botDir)
	envBadTemp[envWhisperTemperature] = "invalid_temp"
	setEnv(t, envBadTemp)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.WhisperTemperature != defaultWhisperTemperature {
		t.Errorf("expected default WhisperTemperature on invalid input, got %v", cfg.WhisperTemperature)
	}
	if len(warnings) == 0 {
		t.Errorf("expected warning for bad temperature, got none")
	}
}

// TestLoadWarnsAboutBrokenOptionalTimeout — опечатка в необязательном таймауте не должна
// ронять бота, но и молчать о ней нельзя: иначе бот тихо работает не с тем значением.
func TestLoadWarnsAboutBrokenOptionalTimeout(t *testing.T) {
	var warnings []string
	prev := logf
	logf = func(format string, args ...any) {
		warnings = append(warnings, format)
	}
	t.Cleanup(func() { logf = prev })

	env := validEnv(t.TempDir())
	env[envStepTimeout] = "30min"
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("испорченный необязательный таймаут не должен быть ошибкой: %v", err)
	}
	if cfg.StepTimeout != defaultStepTimeout {
		t.Errorf("StepTimeout = %v, ожидалось значение по умолчанию %v", cfg.StepTimeout, defaultStepTimeout)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "%s") {
		t.Errorf("ожидалось одно предупреждение про STEP_TIMEOUT, получено %v", warnings)
	}
}

func TestLoadTrimsValues(t *testing.T) {
	botDir := t.TempDir()
	env := validEnv(botDir)
	env[envAdminID] = "  12345  "
	env[envProjectsRoot] = " /projects "
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("значение в пробелах должно разбираться: %v", err)
	}
	if cfg.AdminID != ports.ChatID("12345") {
		t.Errorf("AdminID = %q, ожидалось 12345", cfg.AdminID)
	}
	if cfg.ProjectsRoot != "/projects" {
		t.Errorf("ProjectsRoot = %q, ожидалось /projects", cfg.ProjectsRoot)
	}
}

func TestResolveBotDir(t *testing.T) {
	t.Run("заданное значение побеждает автоопределение", func(t *testing.T) {
		dir := t.TempDir()
		setEnv(t, validEnv(dir))

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.BotDir != dir {
			t.Errorf("BotDir = %q, ожидалось %q", cfg.BotDir, dir)
		}
	})

	t.Run("рядом с бинарником go.mod — берём его каталог", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
			t.Fatal(err)
		}
		stubExecutable(t, filepath.Join(dir, "bot"))
		setEnv(t, without(validEnv(""), envBotDir))

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.BotDir != dir {
			t.Errorf("BotDir = %q, ожидался каталог исполняемого файла %q", cfg.BotDir, dir)
		}
	})

	t.Run("определить нечем — ошибка на старте", func(t *testing.T) {
		stubExecutable(t, filepath.Join(t.TempDir(), "bot"))
		setEnv(t, without(validEnv(""), envBotDir))

		_, err := Load()
		if err == nil {
			t.Fatal("ожидалась ошибка: каталог бота определить нечем")
		}
		if !strings.Contains(err.Error(), envBotDir) {
			t.Errorf("ошибка не называет %s: %v", envBotDir, err)
		}
	})
}

func stubExecutable(t *testing.T, path string) {
	t.Helper()

	prev := executablePath
	executablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { executablePath = prev })
}

// TestLoadIsNotMemoized — Start вызывают повторно в тестах, поэтому кеш в Load
// молча ломал бы весь пакет обработчиков. Тест стоит здесь как предупреждение.
func TestLoadIsNotMemoized(t *testing.T) {
	botDir := t.TempDir()

	setEnv(t, with(validEnv(botDir), envProjectsRoot, "/first"))
	first, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	setEnv(t, with(validEnv(botDir), envProjectsRoot, "/second"))
	second, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if first.ProjectsRoot != "/first" || second.ProjectsRoot != "/second" {
		t.Errorf("Load закешировала результат: %q и %q", first.ProjectsRoot, second.ProjectsRoot)
	}
}

// TestApplyPublishesEveryField обходит поля Config через reflect, чтобы новое поле,
// которое забыли перенести в Apply, роняло тест само.
func TestApplyPublishesEveryField(t *testing.T) {
	cfg := Config{
		Messenger:       "telegram",
		AdminID:         ports.ChatID("777"),
		ProjectsRoot:    "/projects",
		DefaultProject:  "bro-bot",
		DefaultModel:    "gemini-3.1-pro-high",
		QuestionTimeout: 11 * time.Minute,
		StepTimeout:     22 * time.Minute,
		ChatTimeout:     33 * time.Minute,
		AutoContinueMax: 5,
		BotDir:          "/bot",
		ServiceName:     "bro-bot.service",
		DBPath:           "/bot/data/bot.db",
		ScriptsDir:       "/bot/scripts",
		WhisperServerURL: "http://127.0.0.1:8080",
		WhisperAPIKey:    "secret",
		WhisperModel:       "small",
		WhisperLanguage:    "ru",
		WhisperPrompt:      "дебаг, код",
		WhisperTemperature: 0.2,
		WhisperLoudnorm:    true,
		WhisperTimeout:     44 * time.Second,
		Debug:              true,
	}

	Apply(cfg)
	t.Cleanup(func() { Apply(Config{}) })

	// Messenger живёт только в снимке: адаптер мессенджера выбирают один раз в
	// корне композиции, глобальная переменная для этого не нужна.
	published := map[string]any{
		"AdminID":            AdminID,
		"ProjectsRoot":       ProjectsRoot,
		"DefaultProject":     DefaultProject,
		"DefaultModel":       DefaultModel,
		"QuestionTimeout":    QuestionTimeout,
		"StepTimeout":        StepTimeout,
		"ChatTimeout":        ChatTimeout,
		"AutoContinueMax":    AutoContinueMax,
		"BotDir":             BotDir,
		"ServiceName":        ServiceName,
		"DBPath":             DBPath,
		"ScriptsDir":         ScriptsDir,
		"WhisperServerURL":   WhisperServerURL,
		"WhisperAPIKey":      WhisperAPIKey,
		"WhisperModel":       WhisperModel,
		"WhisperLanguage":    WhisperLanguage,
		"WhisperPrompt":      WhisperPrompt,
		"WhisperTemperature": WhisperTemperature,
		"WhisperLoudnorm":    WhisperLoudnorm,
		"WhisperTimeout":     WhisperTimeout,
		"Debug":              Debug,
	}

	v := reflect.ValueOf(cfg)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if name == "Messenger" {
			continue
		}
		got, ok := published[name]
		if !ok {
			t.Errorf("поле %s не публикуется через Apply — добавьте его туда и в этот тест", name)
			continue
		}
		if !reflect.DeepEqual(got, v.Field(i).Interface()) {
			t.Errorf("поле %s: опубликовано %v, в снимке %v", name, got, v.Field(i).Interface())
		}
	}
}

func TestBotTokenNamesVariableWithoutValue(t *testing.T) {
	t.Setenv(envBotToken, "")

	_, err := BotToken()
	if err == nil {
		t.Fatal("ожидалась ошибка про незаданный токен")
	}
	if !strings.Contains(err.Error(), envBotToken) {
		t.Errorf("ошибка не называет переменную: %v", err)
	}
}
