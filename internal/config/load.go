package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bro-bot/internal/ports"
)

// Имена переменных окружения собраны здесь, чтобы страж envguard_test и тексты
// ошибок пользовались одним списком, а не россыпью строковых литералов.
const (
	envMessenger       = "MESSENGER"
	envBotToken        = "TELEGRAM_BOT_TOKEN"
	envAdminID         = "TELEGRAM_ADMIN_ID"
	envProjectsRoot    = "PROJECTS_ROOT"
	envDefaultProject  = "DEFAULT_PROJECT"
	envDefaultModel    = "DEFAULT_MODEL"
	envQuestionTimeout = "QUESTION_TIMEOUT"
	envStepTimeout     = "STEP_TIMEOUT"
	envChatTimeout     = "CHAT_TIMEOUT"
	envBotDir          = "BOT_DIR"
	envServiceName     = "BOT_SERVICE_NAME"
	envDBPath          = "SQLITE_DB_PATH"
	envScriptsDir         = "SCRIPTS_DIR"
	envWhisperServerURL   = "WHISPER_SERVER_URL"
	envWhisperAPIKey      = "WHISPER_API_KEY"
	envWhisperModel       = "WHISPER_MODEL"
	envWhisperLanguage    = "WHISPER_LANGUAGE"
	envWhisperPrompt      = "WHISPER_PROMPT"
	envWhisperTemperature = "WHISPER_TEMPERATURE"
	envWhisperLoudnorm    = "WHISPER_LOUDNORM"
	envWhisperTimeout     = "WHISPER_TIMEOUT"
	envDebug              = "DEBUG"
	envDebugLower         = "debug"
	envSandboxEnabled     = "SANDBOX_ENABLED"
)

// Значения по умолчанию для необязательных параметров.
const (
	defaultMessenger          = "telegram"
	defaultStepTimeout        = 30 * time.Minute
	defaultChatTimeout        = 5 * time.Minute
	defaultWhisperTimeout     = 60 * time.Second
	defaultWhisperModel       = "base-q5_1"
	defaultWhisperLanguage    = "en"
	defaultWhisperTemperature = 0.0
	defaultWhisperLoudnorm    = true
	defaultSandboxEnabled     = true
)

// Config — снимок настроек окружения, снятый один раз при старте процесса.
//
// Секретов здесь нет намеренно: структуру рано или поздно печатают целиком через %+v —
// в диагностике, в упавшем тесте, в отчёте. Токен бота поэтому в снимок не попадает,
// его отдаёт BotToken по требованию.
type Config struct {
	Messenger       string
	AdminID         ports.ChatID
	ProjectsRoot    string
	DefaultProject  string // пусто — берём первую папку в ProjectsRoot
	DefaultModel    string // сырое значение: псевдоним разрешает реестр моделей
	QuestionTimeout time.Duration
	StepTimeout     time.Duration
	ChatTimeout     time.Duration
	BotDir          string
	ServiceName     string
	DBPath          string
	ScriptsDir      string

	// Whisper (распознавание речи)
	WhisperServerURL   string
	WhisperAPIKey      string
	WhisperModel       string
	WhisperLanguage    string
	WhisperPrompt      string
	WhisperTemperature float64
	WhisperLoudnorm    bool
	WhisperTimeout     time.Duration

	// Debug режим
	Debug bool

	// SandboxEnabled включает запуск команд агентов в изолированной песочнице (bubblewrap).
	SandboxEnabled bool
}

// executablePath подменяется в тестах: это единственная часть Load, которая смотрит
// не в окружение, а на сам процесс.
var executablePath = os.Executable

// logf подменяется в тестах, чтобы проверить, что про испорченное значение
// необязательного параметра бот предупреждает, а не молчит.
var logf = log.Printf

// Load читает окружение, проверяет его и возвращает снимок.
//
// Функция чистая: она ничего не пишет в переменные пакета и ничего не кеширует.
// Кеш здесь был бы ошибкой — Start вызывают повторно в тестах, и каждый вызов обязан
// видеть текущее окружение.
//
// Про все проблемы сообщается разом: иначе оператор правит .env по одной переменной
// за перезапуск, потому что узнаёт только о первой.
func Load() (Config, error) {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	cfg := Config{
		Messenger:      strings.ToLower(envTrim(envMessenger)),
		ProjectsRoot:   envTrim(envProjectsRoot),
		DefaultProject: envTrim(envDefaultProject),
		DefaultModel:   envTrim(envDefaultModel),
		ServiceName:    envTrim(envServiceName),
	}
	if cfg.Messenger == "" {
		cfg.Messenger = defaultMessenger
	}

	adminID, err := strconv.ParseInt(envTrim(envAdminID), 10, 64)
	if err != nil || adminID == 0 {
		fail("ОБЯЗАТЕЛЬНЫЙ параметр %s не задан или некорректен", envAdminID)
	} else {
		cfg.AdminID = ports.ChatID(strconv.FormatInt(adminID, 10))
	}

	if cfg.ProjectsRoot == "" {
		fail("ОБЯЗАТЕЛЬНЫЙ параметр %s не задан", envProjectsRoot)
	}
	if cfg.ServiceName == "" {
		fail("ОБЯЗАТЕЛЬНЫЙ параметр %s не задан", envServiceName)
	}
	if cfg.DefaultModel == "" {
		fail("ОБЯЗАТЕЛЬНЫЙ параметр %s не задан", envDefaultModel)
	}

	switch raw := envTrim(envQuestionTimeout); {
	case raw == "":
		fail("ОБЯЗАТЕЛЬНЫЙ параметр %s не задан", envQuestionTimeout)
	default:
		d, err := parseDuration(raw)
		if err != nil {
			fail("Некорректный формат %s", envQuestionTimeout)
		} else {
			cfg.QuestionTimeout = d
		}
	}

	cfg.StepTimeout = durationOrDefault(envStepTimeout, defaultStepTimeout)
	cfg.ChatTimeout = durationOrDefault(envChatTimeout, defaultChatTimeout)

	botDir, err := resolveBotDir()
	if err != nil {
		fail("%s", err)
	}
	cfg.BotDir = botDir

	// Пути считаются от каталога бота, только когда переменная не задана вовсе:
	// заданное относительное значение исторически считается от рабочего каталога
	// процесса, и менять эту точку отсчёта значило бы переехать чужую базу данных.
	cfg.DBPath = envTrim(envDBPath)
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(botDir, "data", "bot.db")
	}
	cfg.ScriptsDir = envTrim(envScriptsDir)
	if cfg.ScriptsDir == "" {
		cfg.ScriptsDir = filepath.Join(botDir, "scripts")
	}

	cfg.WhisperServerURL = envTrim(envWhisperServerURL)
	cfg.WhisperAPIKey = envTrim(envWhisperAPIKey)
	cfg.WhisperModel = envTrim(envWhisperModel)
	if cfg.WhisperModel == "" {
		cfg.WhisperModel = defaultWhisperModel
	}
	cfg.WhisperLanguage = envTrim(envWhisperLanguage)
	if cfg.WhisperLanguage == "" {
		cfg.WhisperLanguage = defaultWhisperLanguage
	}
	cfg.WhisperTimeout = durationOrDefault(envWhisperTimeout, defaultWhisperTimeout)

	cfg.WhisperPrompt = envTrim(envWhisperPrompt)
	if strings.ToLower(cfg.WhisperPrompt) == "none" || strings.ToLower(cfg.WhisperPrompt) == "off" || strings.ToLower(cfg.WhisperPrompt) == "false" {
		cfg.WhisperPrompt = ""
	}

	if rawTemp := envTrim(envWhisperTemperature); rawTemp != "" {
		temp, err := strconv.ParseFloat(rawTemp, 64)
		if err != nil || temp < 0 {
			logf("Предупреждение: некорректный формат %s (%q), используется значение по умолчанию %v", envWhisperTemperature, rawTemp, defaultWhisperTemperature)
			cfg.WhisperTemperature = defaultWhisperTemperature
		} else {
			cfg.WhisperTemperature = temp
		}
	} else {
		cfg.WhisperTemperature = defaultWhisperTemperature
	}

	if rawLoudnorm := envTrim(envWhisperLoudnorm); rawLoudnorm != "" {
		cfg.WhisperLoudnorm = parseBool(rawLoudnorm)
	} else {
		cfg.WhisperLoudnorm = defaultWhisperLoudnorm
	}

	rawDebug := envTrim(envDebug)
	if rawDebug == "" {
		rawDebug = envTrim(envDebugLower)
	}
	cfg.Debug = parseBool(rawDebug)

	if rawSandbox := envTrim(envSandboxEnabled); rawSandbox != "" {
		cfg.SandboxEnabled = parseBool(rawSandbox)
	} else {
		cfg.SandboxEnabled = defaultSandboxEnabled
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("конфигурация: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
}

// Apply публикует снимок в переменных пакета. Единственный их писатель в бою:
// остальной код читает config.ProjectsRoot и соседей, ничего не зная об окружении.
func Apply(c Config) {
	AdminID = c.AdminID
	ProjectsRoot = c.ProjectsRoot
	DefaultProject = c.DefaultProject
	DefaultModel = c.DefaultModel
	QuestionTimeout = c.QuestionTimeout
	StepTimeout = c.StepTimeout
	ChatTimeout = c.ChatTimeout
	BotDir = c.BotDir
	ServiceName = c.ServiceName
	DBPath = c.DBPath
	ScriptsDir = c.ScriptsDir
	WhisperServerURL = c.WhisperServerURL
	WhisperAPIKey = c.WhisperAPIKey
	WhisperModel = c.WhisperModel
	WhisperLanguage = c.WhisperLanguage
	WhisperPrompt = c.WhisperPrompt
	WhisperTemperature = c.WhisperTemperature
	WhisperLoudnorm = c.WhisperLoudnorm
	WhisperTimeout = c.WhisperTimeout
	Debug = c.Debug
	SandboxEnabled = c.SandboxEnabled
}

// BotToken возвращает токен бота, нигде его не сохраняя.
//
// В Config токену места нет: снимок печатают целиком, а токен — нет. Поэтому значение
// живёт ровно столько, сколько нужно вызывающему, чтобы отдать его адаптеру мессенджера.
// В тексте ошибки — только имя переменной.
func BotToken() (string, error) {
	token := envTrim(envBotToken)
	if token == "" {
		return "", fmt.Errorf("ОБЯЗАТЕЛЬНЫЙ параметр %s не задан", envBotToken)
	}
	return token, nil
}

// resolveBotDir определяет каталог с исходниками бота: он нужен для /rebuild и для
// маркера перезапуска. Раньше это решалось в двух местах по-разному — в обработчиках
// подставлялся жёстко зашитый путь, а в system каталог определялся по исполняемому
// файлу, — и команды работали не с тем каталогом, который лежал в конфигурации.
func resolveBotDir() (string, error) {
	if dir := envTrim(envBotDir); dir != "" {
		return dir, nil
	}
	exe, err := executablePath()
	if err == nil {
		exeDir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(exeDir, "go.mod")); err == nil {
			return exeDir, nil
		}
	}
	return "", fmt.Errorf("ОБЯЗАТЕЛЬНЫЙ параметр %s не задан и не удалось определить его автоматически", envBotDir)
}

// parseDuration понимает оба формата, которые исторически принимал бот: строку
// time.ParseDuration (30m, 1h, 1800s) и голое число секунд.
func parseDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d, nil
	}
	if sec, err := strconv.Atoi(raw); err == nil && sec > 0 {
		return time.Duration(sec) * time.Second, nil
	}
	return 0, fmt.Errorf("некорректный формат длительности: %q", raw)
}

// durationOrDefault — для необязательных таймаутов: и пустое значение, и мусор дают
// значение по умолчанию, но про мусор надо предупредить, иначе опечатка в .env
// молча меняет поведение бота.
func durationOrDefault(name string, def time.Duration) time.Duration {
	raw := envTrim(name)
	if raw == "" {
		return def
	}
	d, err := parseDuration(raw)
	if err != nil {
		logf("Предупреждение: некорректный формат %s, используется значение по умолчанию %v", name, def)
		return def
	}
	return d
}

func envTrim(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

// parseBool преобразует строку в булево значение. Пустая строка и некорректные
// значения возвращают false.
func parseBool(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return raw == "true" || raw == "1" || raw == "yes" || raw == "on"
}

