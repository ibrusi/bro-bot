package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/scripts"
	"bro-bot/internal/utils"
	"context"
	"errors"
	"html"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Команда /script: список скриптов, запуск с аргументами, вывод, обновляемый
// по ходу работы, и остановка кнопкой.

const (
	// scriptOutputLimit — сколько последних символов вывода видно в сообщении:
	// с запасом под заголовок и разметку в пределах лимита Telegram на правку.
	scriptOutputLimit = 3000
	// scriptBufferLimit — сколько байт хвоста вывода держится в памяти. Больше,
	// чем показывается, чтобы обрезка по символам не упиралась в многобайтный UTF-8.
	scriptBufferLimit = 64 * 1024
)

// scriptProgressInterval — как часто сообщение запуска обновляется свежим выводом.
// Тот же шаг, что у прогресса задач и чата. Переменная, а не константа, чтобы
// тесты не ждали по пять секунд.
var scriptProgressInterval = 5 * time.Second

// scriptRegistry хранит отмену запущенных скриптов: кнопка «Остановить» находит
// по идентификатору свой запуск и прерывает только его.
type scriptRegistry struct {
	mu   sync.Mutex
	next int
	runs map[int]context.CancelFunc
}

var runningScripts = &scriptRegistry{runs: make(map[int]context.CancelFunc)}

func (r *scriptRegistry) add(cancel context.CancelFunc) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	r.runs[r.next] = cancel
	return r.next
}

func (r *scriptRegistry) remove(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, id)
}

// stop прерывает запуск id и сообщает, был ли он ещё активен.
func (r *scriptRegistry) stop(id int) bool {
	r.mu.Lock()
	cancel, ok := r.runs[id]
	r.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// handleScript — обработчик команды /script: без аргументов показывает список
// скриптов, иначе запускает скрипт args[0] с аргументами args[1:]. Ожидание
// идёт в фоне, поэтому времени выполнения ничего не ограничивает, кроме кнопки
// остановки.
func handleScript(s ports.Session) error {
	lang := uiLang()

	if config.ScriptsDir == "" {
		return s.Send(i18n.T(lang, "script.dir_missing"), nil)
	}
	args := s.Args()
	if len(args) == 0 {
		return sendScriptList(s, lang)
	}
	scriptName, scriptArgs := args[0], args[1:]

	// Проверка префикса без разделителя пропускала каталог-сосед:
	// "../scripts-evil/x" при корне "/opt/scripts" давал "/opt/scripts-evil/x".
	scriptPath, err := utils.SafeJoin(config.ScriptsDir, scriptName)
	if err != nil {
		return s.Send(i18n.Tf(lang, "script.bad_name", ErrorText(err, lang)), nil)
	}
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return s.Send(i18n.Tf(lang, "script.not_found", scriptName, config.ScriptsDir), nil)
	}

	script, err := scripts.Prepare(scriptPath, scriptArgs)
	if errors.Is(err, scripts.ErrNotRunnable) {
		return s.Send(i18n.Tf(lang, "script.not_runnable", scriptName, strings.Join(scripts.Extensions(), " ")), nil)
	}
	if err != nil {
		return s.Send(i18n.Tf(lang, "script.start_error", scriptName, err), nil)
	}

	ctx, cancel := context.WithCancel(context.Background())
	run := &scriptRun{
		title:     strings.Join(args, " "),
		lang:      lang,
		m:         s.Messenger(),
		chat:      s.Chat(),
		out:       scripts.NewTailBuffer(scriptBufferLimit),
		interval:  scriptProgressInterval,
		startedAt: time.Now(),
	}
	run.id = runningScripts.add(cancel)

	ref, err := run.m.Send(context.Background(), run.chat, run.runningText(), run.runningOpts())
	if err != nil {
		runningScripts.remove(run.id)
		cancel()
		return err
	}
	run.ref = ref

	go run.execute(ctx, cancel, script)
	return nil
}

// onScriptStop — обработчик кнопки script_stop.
func onScriptStop(s ports.Session) error {
	lang := uiLang()

	id, err := strconv.Atoi(s.Callback().Payload)
	if err != nil || !runningScripts.stop(id) {
		return s.Respond(i18n.T(lang, "script.toast_not_running"))
	}
	return s.Respond(i18n.T(lang, "script.toast_stopping"))
}

// scriptRun — один запуск скрипта и его сообщение в чате.
type scriptRun struct {
	id        int
	title     string
	lang      string
	m         ports.Messenger
	chat      ports.ChatID
	ref       ports.MessageRef
	out       *scripts.TailBuffer
	interval  time.Duration
	startedAt time.Time
}

// execute ждёт завершения скрипта, обновляя сообщение по ходу работы, и в конце
// заменяет его итогом без кнопки остановки.
func (r *scriptRun) execute(ctx context.Context, cancel context.CancelFunc, script *scripts.Script) {
	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go r.reportProgress(stopProgress, progressDone)

	runErr := script.Run(ctx, r.out)

	close(stopProgress)
	<-progressDone
	runningScripts.remove(r.id)
	cancel()

	editOrSend(r.m, r.chat, r.ref, r.finalText(runErr))
}

// reportProgress раз в interval перерисовывает сообщение запуска, пока не закрыт stop.
func (r *scriptRun) reportProgress(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	if r.ref.ID == "" || !r.m.Capabilities().EditMessages {
		return
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_ = r.m.Edit(context.Background(), r.ref, r.runningText(), r.runningOpts())
		}
	}
}

func (r *scriptRun) runningText() string {
	header := i18n.Tf(r.lang, "script.running", html.EscapeString(r.title), r.elapsed())
	return header + "\n\n" + r.outputBlock()
}

func (r *scriptRun) runningOpts() *ports.SendOptions {
	if !r.m.Capabilities().InlineButtons {
		return ports.Rich()
	}
	return ports.RichWith(&ports.Keyboard{Rows: [][]ports.Button{{
		{Text: i18n.T(r.lang, "btn.script_stop"), Action: "script_stop", Payload: strconv.Itoa(r.id)},
	}}})
}

func (r *scriptRun) finalText(runErr error) string {
	title := html.EscapeString(r.title)
	var header string
	switch {
	case errors.Is(runErr, context.Canceled):
		header = i18n.Tf(r.lang, "script.stopped", title, r.elapsed())
	case runErr != nil:
		header = i18n.Tf(r.lang, "script.failed", title, r.elapsed(), html.EscapeString(runErr.Error()))
	default:
		header = i18n.Tf(r.lang, "script.finished", title, r.elapsed())
	}
	return header + "\n\n" + r.outputBlock()
}

func (r *scriptRun) elapsed() string {
	return domain.FormatDuration(time.Since(r.startedAt), r.lang)
}

// outputBlock показывает хвост вывода: при долгой работе важнее последние строки.
// Управляющие последовательности терминала вычищаются, иначе в чате остался бы мусор.
func (r *scriptRun) outputBlock() string {
	raw, truncated := r.out.Snapshot()
	clean := utils.AnsiRegex.ReplaceAllString(raw, "")
	clean = strings.TrimRight(strings.ToValidUTF8(clean, "�"), " \t\n")
	if clean == "" {
		return i18n.T(r.lang, "script.no_output")
	}

	if runes := []rune(clean); len(runes) > scriptOutputLimit {
		clean = string(runes[len(runes)-scriptOutputLimit:])
		truncated = true
	}
	if truncated {
		clean = "…\n" + clean
	}
	return "<pre>" + html.EscapeString(clean) + "</pre>"
}

// sendScriptList присылает список скриптов, которые можно передать /script.
func sendScriptList(s ports.Session, lang string) error {
	names, err := scripts.List(config.ScriptsDir)
	if err != nil && !os.IsNotExist(err) {
		return s.Send(i18n.Tf(lang, "script.list_error", config.ScriptsDir, err), nil)
	}
	if len(names) == 0 {
		return s.Send(i18n.Tf(lang, "script.list_empty", config.ScriptsDir, strings.Join(scripts.Extensions(), " ")), nil)
	}

	var bldr strings.Builder
	bldr.WriteString(i18n.Tf(lang, "script.list_header", config.ScriptsDir))
	for _, name := range names {
		bldr.WriteString("• " + name + "\n")
	}
	bldr.WriteString(i18n.T(lang, "script.list_footer"))
	return s.Send(utils.TruncateString(bldr.String(), scriptOutputLimit), nil)
}
