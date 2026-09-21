package system

import (
	"bro-bot/internal/i18n"
	"strings"
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{15 * 1024 * 1024, "15.0 MB"},
		{1024 * 1024 * 1024, "1.00 GB"},
		{int64(2.5 * 1024 * 1024 * 1024), "2.50 GB"},
	}

	for _, tt := range tests {
		got := formatBytes(tt.input)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReadHostLoadAndMemory(t *testing.T) {
	load := readHostLoad()
	if load.Cores <= 0 {
		t.Errorf("expected positive cores count, got %d", load.Cores)
	}

	mem := readHostMemory()
	if mem.TotalBytes <= 0 {
		t.Errorf("expected positive total memory, got %d", mem.TotalBytes)
	}
}

func TestReadHostDisk(t *testing.T) {
	disk := readHostDisk()
	if disk.TotalBytes <= 0 {
		t.Errorf("expected positive total disk, got %d", disk.TotalBytes)
	}
	if disk.FreeBytes < 0 {
		t.Errorf("expected non-negative free disk, got %d", disk.FreeBytes)
	}
	if disk.FreeBytes > disk.TotalBytes {
		t.Errorf("expected free disk <= total disk, got free=%d total=%d", disk.FreeBytes, disk.TotalBytes)
	}
	if disk.FreePercent < 0 || disk.FreePercent > 100 {
		t.Errorf("expected free percent between 0 and 100, got %f", disk.FreePercent)
	}
}

func TestFormatResourcesMessageIdle(t *testing.T) {
	report := ResourcesReport{
		Host: HostLoadStats{
			Load1:  0.25,
			Load5:  0.30,
			Load15: 0.20,
			Cores:  4,
		},
		Memory: HostMemoryStats{
			TotalBytes:  8 * 1024 * 1024 * 1024,
			UsedBytes:   2 * 1024 * 1024 * 1024,
			UsedPercent: 25.0,
		},
		CGroup: CGroupStats{
			Available:          true,
			GroupName:          "tg-bot.service",
			MemoryCurrentBytes: 120 * 1024 * 1024,
			MemoryPeakBytes:    150 * 1024 * 1024,
			TasksCount:         8,
			CPUUsageUsec:       2500000,
		},
		BotProc: &ProcessResourceInfo{
			PID:         1234,
			Name:        "bot",
			CPUPercent:  0.5,
			MemoryBytes: 15 * 1024 * 1024,
			MemoryPct:   0.2,
			Elapsed:     "01:30:00",
			Threads:     6,
		},
		HasActiveTask: false,
		GeneratedAt:   time.Now(),
	}

	msg := FormatResourcesMessage(report, "ru")
	if !strings.Contains(msg, "Мониторинг ресурсов") {
		t.Fatalf("expected title in message: %s", msg)
	}
	if !strings.Contains(msg, "Простаивает") {
		t.Fatalf("expected idle worker status in message: %s", msg)
	}
	if !strings.Contains(msg, "1234") {
		t.Fatalf("expected bot PID in message: %s", msg)
	}
	if !strings.Contains(msg, "120.0 MB") {
		t.Fatalf("expected cgroup memory in message: %s", msg)
	}
}

func TestFormatResourcesMessageActiveTask(t *testing.T) {
	report := ResourcesReport{
		Host: HostLoadStats{
			Load1: 1.5,
			Cores: 2,
		},
		Memory: HostMemoryStats{
			TotalBytes: 4 * 1024 * 1024 * 1024,
			UsedBytes:  1 * 1024 * 1024 * 1024,
		},
		BotProc: &ProcessResourceInfo{
			PID:         100,
			CPUPercent:  0.1,
			MemoryBytes: 12 * 1024 * 1024,
		},
		ActiveWorker: &ProcessResourceInfo{
			PID:         200,
			CPUPercent:  15.5,
			MemoryBytes: 250 * 1024 * 1024,
			MemoryPct:   6.1,
			Elapsed:     "00:45",
		},
		AgyInstalled:  true,
		AgyVersion:    "agy 1.2.4",
		HasActiveTask: true,
		ProjectName:   "my-cool-project",
		TaskPrompt:    "do some work with files",
		TaskElapsed:   45 * time.Second,
	}

	msg := FormatResourcesMessage(report, "ru")
	if !strings.Contains(msg, "выполняет задачу") {
		t.Fatalf("expected active task state: %s", msg)
	}
	if !strings.Contains(msg, "my-cool-project") {
		t.Fatalf("expected project name: %s", msg)
	}
	if !strings.Contains(msg, "200") {
		t.Fatalf("expected worker PID: %s", msg)
	}
	if !strings.Contains(msg, "15.5%") {
		t.Fatalf("expected worker CPU: %s", msg)
	}
	if !strings.Contains(msg, "• CLI: <code>agy 1.2.4</code> (🟢 <i>готов к работе</i>)") {
		t.Fatalf("expected agy CLI line in message: %s", msg)
	}

	compact := FormatCompactResourceSnippet(report, "ru")
	if !strings.Contains(compact, "Bot (PID 100)") {
		t.Fatalf("expected bot in compact snippet: %s", compact)
	}
	if !strings.Contains(compact, "agy (PID 200)") {
		t.Fatalf("expected agy in compact snippet: %s", compact)
	}
}

func TestFormatResourcesMessageClaudeActiveTask(t *testing.T) {
	report := ResourcesReport{
		Host: HostLoadStats{
			Load1: 0.8,
			Cores: 4,
		},
		Memory: HostMemoryStats{
			TotalBytes: 8 * 1024 * 1024 * 1024,
			UsedBytes:  2 * 1024 * 1024 * 1024,
		},
		BotProc: &ProcessResourceInfo{
			PID:         100,
			CPUPercent:  0.2,
			MemoryBytes: 20 * 1024 * 1024,
		},
		ActiveAgent:       "claude",
		ActiveWorkerAgent: "claude",
		ActiveWorker: &ProcessResourceInfo{
			PID:         333,
			CPUPercent:  22.4,
			MemoryBytes: 180 * 1024 * 1024,
			MemoryPct:   2.2,
			Elapsed:     "01:15",
		},
		ClaudeInstalled: true,
		ClaudeVersion:   "2.1.273 (Claude Code)",
		HasActiveTask:   true,
		ProjectName:     "claude-proj",
		TaskPrompt:      "refactor database schema",
		TaskElapsed:     75 * time.Second,
	}

	msg := FormatResourcesMessage(report, "ru")
	if !strings.Contains(msg, "Агент задач (claude) — 🟢 <i>активен</i>") {
		t.Fatalf("expected claude active header: %s", msg)
	}
	if !strings.Contains(msg, "PID: <code>333</code>") {
		t.Fatalf("expected claude worker PID: %s", msg)
	}
	if !strings.Contains(msg, "claude-proj") {
		t.Fatalf("expected project name: %s", msg)
	}
	if !strings.Contains(msg, "refactor database schema") {
		t.Fatalf("expected prompt: %s", msg)
	}
	if !strings.Contains(msg, "2.1.273 (Claude Code)") {
		t.Fatalf("expected claude version: %s", msg)
	}
	if !strings.Contains(msg, "bot|agy|claude") {
		t.Fatalf("expected claude in console tip: %s", msg)
	}

	compact := FormatCompactResourceSnippet(report, "ru")
	if !strings.Contains(compact, "claude (PID 333)") {
		t.Fatalf("expected claude in compact snippet: %s", compact)
	}
}

func TestFormatResourcesMessageClaudeIdleAndWorkers(t *testing.T) {
	report := ResourcesReport{
		Host: HostLoadStats{
			Load1: 0.5,
			Cores: 4,
		},
		Memory: HostMemoryStats{
			TotalBytes: 8 * 1024 * 1024 * 1024,
			UsedBytes:  2 * 1024 * 1024 * 1024,
		},
		BotProc: &ProcessResourceInfo{
			PID:         100,
			CPUPercent:  0.1,
			MemoryBytes: 15 * 1024 * 1024,
		},
		ActiveAgent:     "claude",
		ClaudeInstalled: true,
		ClaudeVersion:   "2.1.273 (Claude Code)",
		HasActiveTask:   false,
		OtherWorkers: []ProcessResourceInfo{
			{
				PID:         888,
				CPUPercent:  0.5,
				MemoryBytes: 40 * 1024 * 1024,
				Elapsed:     "05:00",
			},
		},
		ClaudeWorkers: []ProcessResourceInfo{
			{
				PID:         777,
				CPUPercent:  1.2,
				MemoryBytes: 50 * 1024 * 1024,
				Elapsed:     "10:00",
			},
		},
	}

	msg := FormatResourcesMessage(report, "ru")
	if !strings.Contains(msg, "Активный CLI агент: <code>claude</code>") {
		t.Fatalf("expected active agent in bot section: %s", msg)
	}
	if !strings.Contains(msg, "Фоновые процессы claude: <code>1</code> шт.") {
		t.Fatalf("expected claude background workers count: %s", msg)
	}
	if !strings.Contains(msg, "777") {
		t.Fatalf("expected claude worker PID: %s", msg)
	}
	if !strings.Contains(msg, "Фоновые процессы agy: <code>1</code> шт.") {
		t.Fatalf("expected agy background workers count: %s", msg)
	}
	if !strings.Contains(msg, "888") {
		t.Fatalf("expected agy worker PID: %s", msg)
	}

	compact := FormatCompactResourceSnippet(report, "ru")
	if !strings.Contains(compact, "claude: 💤 <i>idle</i>") {
		t.Fatalf("expected claude idle in compact snippet: %s", compact)
	}
}

func TestFormatResourcesMessageUnifiedAgentSections(t *testing.T) {
	// Case 1: Both installed, agy active and running task, claude idle
	rep1 := ResourcesReport{
		ActiveAgent:       "agy",
		ActiveWorkerAgent: "agy",
		HasActiveTask:     true,
		AgyInstalled:      true,
		AgyVersion:        "agy 1.2.4",
		ClaudeInstalled:   true,
		ClaudeVersion:     "2.1.273 (Claude Code)",
		ActiveWorker: &ProcessResourceInfo{
			PID:         101,
			CPUPercent:  12.0,
			MemoryBytes: 150 * 1024 * 1024,
			Elapsed:     "02:15",
		},
		ProjectName: "demo-proj",
		TaskPrompt:  "fix bug in code",
	}

	msg1 := FormatResourcesMessage(rep1, "ru")

	// Verify agy section
	if !strings.Contains(msg1, "🧠 <b>Агент задач (agy) — 🟢 <i>активен</i>:</b>") {
		t.Fatalf("expected agy active header in msg1: %s", msg1)
	}
	if !strings.Contains(msg1, "• PID: <code>101</code> | Состояние: ⚡️ <b>выполняет задачу</b>") {
		t.Fatalf("expected agy task state in msg1: %s", msg1)
	}
	if !strings.Contains(msg1, "• CLI: <code>agy 1.2.4</code> (🟢 <i>готов к работе</i>)") {
		t.Fatalf("expected agy cli line in msg1: %s", msg1)
	}

	// Verify claude section
	if !strings.Contains(msg1, "🟣 <b>Агент задач (claude):</b>") {
		t.Fatalf("expected claude idle header in msg1: %s", msg1)
	}
	if !strings.Contains(msg1, "• Состояние: 💤 <i>Простаивает (нет активных задач)</i>") {
		t.Fatalf("expected claude idle state in msg1: %s", msg1)
	}
	if !strings.Contains(msg1, "• CLI: <code>2.1.273 (Claude Code)</code> (🟢 <i>готов к работе</i>)") {
		t.Fatalf("expected claude cli line in msg1: %s", msg1)
	}

	// Case 2: Neither CLI found in PATH
	rep2 := ResourcesReport{
		ActiveAgent:     "agy",
		AgyInstalled:    false,
		ClaudeInstalled: false,
	}

	msg2 := FormatResourcesMessage(rep2, "ru")
	// Check that both have "• CLI: 🔴 <i>не найден в PATH</i>"
	countNotFound := strings.Count(msg2, "• CLI: 🔴 <i>не найден в PATH</i>")
	if countNotFound != 2 {
		t.Fatalf("expected 2 instances of CLI not found, got %d in: %s", countNotFound, msg2)
	}
}

func TestFormatResourcesMessageWithDisk(t *testing.T) {
	report := ResourcesReport{
		Host: HostLoadStats{
			Load1:  0.15,
			Load5:  0.20,
			Load15: 0.10,
			Cores:  2,
		},
		Memory: HostMemoryStats{
			TotalBytes:  4 * 1024 * 1024 * 1024,
			UsedBytes:   1 * 1024 * 1024 * 1024,
			UsedPercent: 25.0,
		},
		Disk: HostDiskStats{
			TotalBytes:  60 * 1024 * 1024 * 1024,
			FreeBytes:   45 * 1024 * 1024 * 1024,
			UsedBytes:   15 * 1024 * 1024 * 1024,
			FreePercent: 75.0,
			UsedPercent: 25.0,
		},
	}

	msg := FormatResourcesMessage(report, "ru")
	if !strings.Contains(msg, "💻 <b>Сервер:</b>") {
		t.Fatalf("expected Server header in message: %s", msg)
	}
	if !strings.Contains(msg, "• Диск: свободно <b>45.00 GB</b> из <b>60.00 GB</b> (<code>75.0%</code> свободно)") {
		t.Fatalf("expected disk line in message: %s", msg)
	}
}

// TestFormatResourcesMessageDefaultLanguageIsEnglish фиксирует язык по умолчанию:
// остальные проверки отчёта идут на русском, и без этого теста английский рендер
// остался бы непокрытым.
func TestFormatResourcesMessageDefaultLanguageIsEnglish(t *testing.T) {
	report := ResourcesReport{
		Host:        HostLoadStats{Load1: 1.0, Cores: 2},
		Memory:      HostMemoryStats{TotalBytes: 4 << 30, UsedBytes: 1 << 30},
		BotProc:     &ProcessResourceInfo{PID: 1234, CPUPercent: 0.5, MemoryBytes: 30 << 20},
		GeneratedAt: time.Now(),
	}

	msg := FormatResourcesMessage(report, i18n.Default)
	for _, want := range []string{"Resource monitoring", "Server:", "Idle (no active tasks)", "1234"} {
		if !strings.Contains(msg, want) {
			t.Errorf("в английском отчёте нет %q: %s", want, msg)
		}
	}
}

func TestFormatCompactResourceSnippetAllComponents(t *testing.T) {
	// Case 1: All idle
	repIdle := ResourcesReport{
		BotProc: &ProcessResourceInfo{
			PID:         100,
			CPUPercent:  0.2,
			MemoryBytes: 25 * 1024 * 1024,
		},
		HasActiveTask: false,
	}
	compactIdle := FormatCompactResourceSnippet(repIdle, "ru")
	for _, want := range []string{
		"Bot (PID 100): RAM <b>25.0 MB</b>, CPU <b>0.2%</b>",
		"• agy: 💤 <i>idle</i>",
		"• claude: 💤 <i>idle</i>",
		"• whisper-server: 💤 <i>idle</i>",
	} {
		if !strings.Contains(compactIdle, want) {
			t.Errorf("expected %q in compact snippet, got:\n%s", want, compactIdle)
		}
	}

	// Case 2: Agy active task, Claude idle, Whisper running
	repAgy := ResourcesReport{
		BotProc: &ProcessResourceInfo{
			PID:         100,
			CPUPercent:  0.2,
			MemoryBytes: 25 * 1024 * 1024,
		},
		ActiveAgent:       "agy",
		ActiveWorkerAgent: "agy",
		HasActiveTask:     true,
		ActiveWorker: &ProcessResourceInfo{
			PID:         200,
			CPUPercent:  15.0,
			MemoryBytes: 150 * 1024 * 1024,
		},
		WhisperProc: &ProcessResourceInfo{
			PID:         300,
			CPUPercent:  1.0,
			MemoryBytes: 200 * 1024 * 1024,
		},
	}
	compactAgy := FormatCompactResourceSnippet(repAgy, "ru")
	for _, want := range []string{
		"Bot (PID 100): RAM <b>25.0 MB</b>, CPU <b>0.2%</b>",
		"• agy (PID 200): RAM <b>150.0 MB</b>, CPU <b>15.0%</b>",
		"• claude: 💤 <i>idle</i>",
		"• whisper-server (PID 300): RAM <b>200.0 MB</b>, CPU <b>1.0%</b>",
	} {
		if !strings.Contains(compactAgy, want) {
			t.Errorf("expected %q in compact snippet, got:\n%s", want, compactAgy)
		}
	}

	// Case 3: Claude active task, Agy idle, Whisper running
	repClaude := ResourcesReport{
		BotProc: &ProcessResourceInfo{
			PID:         100,
			CPUPercent:  0.2,
			MemoryBytes: 25 * 1024 * 1024,
		},
		ActiveAgent:       "claude",
		ActiveWorkerAgent: "claude",
		HasActiveTask:     true,
		ActiveWorker: &ProcessResourceInfo{
			PID:         400,
			CPUPercent:  20.5,
			MemoryBytes: 180 * 1024 * 1024,
		},
		WhisperProc: &ProcessResourceInfo{
			PID:         300,
			CPUPercent:  0.5,
			MemoryBytes: 190 * 1024 * 1024,
		},
	}
	compactClaude := FormatCompactResourceSnippet(repClaude, "ru")
	for _, want := range []string{
		"Bot (PID 100): RAM <b>25.0 MB</b>, CPU <b>0.2%</b>",
		"• agy: 💤 <i>idle</i>",
		"• claude (PID 400): RAM <b>180.0 MB</b>, CPU <b>20.5%</b>",
		"• whisper-server (PID 300): RAM <b>190.0 MB</b>, CPU <b>0.5%</b>",
	} {
		if !strings.Contains(compactClaude, want) {
			t.Errorf("expected %q in compact snippet, got:\n%s", want, compactClaude)
		}
	}
}

func TestFormatResourcesMessageWithWhisperServer(t *testing.T) {
	// Case 1: Whisper server running
	reportRunning := ResourcesReport{
		Host: HostLoadStats{Load1: 0.5, Cores: 4},
		Memory: HostMemoryStats{
			TotalBytes:  8 * 1024 * 1024 * 1024,
			UsedBytes:   2 * 1024 * 1024 * 1024,
			UsedPercent: 25.0,
		},
		BotProc: &ProcessResourceInfo{
			PID:         100,
			CPUPercent:  0.1,
			MemoryBytes: 20 * 1024 * 1024,
		},
		WhisperProc: &ProcessResourceInfo{
			PID:         500,
			CPUPercent:  2.5,
			MemoryBytes: 172 * 1024 * 1024,
			MemoryPct:   2.1,
			Elapsed:     "01:23:45",
			Threads:     11,
		},
		WhisperURL:       "http://127.0.0.1:8080/inference",
		WhisperModel:     "base-q5_1",
		WhisperInstalled: true,
		OtherWhisperProcs: []ProcessResourceInfo{
			{
				PID:         501,
				CPUPercent:  0.0,
				MemoryBytes: 100 * 1024 * 1024,
				Elapsed:     "00:10",
			},
		},
		GeneratedAt: time.Now(),
	}

	msgRunning := FormatResourcesMessage(reportRunning, "ru")
	for _, want := range []string{
		"🎙 <b>Сервер распознавания речи (whisper-server):</b>",
		"• PID: <code>500</code> | Состояние: 🟢 <i>active</i>",
		"• Память (RSS): <b>172.0 MB</b> (<code>2.1%</code> RAM)",
		"• Нагрузка CPU: <b>2.5%</b>",
		"• Аптайм: <code>01:23:45</code> | Потоков: <code>11</code>",
		"• Эндпоинт: <code>http://127.0.0.1:8080/inference</code>",
		"• Модель: <code>base-q5_1</code>",
		"• Служба: 🟢 <i>установлена (whisper-server.service)</i>",
		"• Фоновые процессы whisper-server: <code>1</code> шт.",
		"501",
		"top -p $(pgrep -d, -f 'bot|agy|claude|whisper')",
	} {
		if !strings.Contains(msgRunning, want) {
			t.Errorf("expected %q in /top output, got:\n%s", want, msgRunning)
		}
	}

	// Case 2: Whisper server idle / stopped
	reportIdle := ResourcesReport{
		Host:        HostLoadStats{Load1: 0.2, Cores: 4},
		Memory:      HostMemoryStats{TotalBytes: 8 << 30, UsedBytes: 1 << 30},
		BotProc:     &ProcessResourceInfo{PID: 100, CPUPercent: 0.1, MemoryBytes: 20 << 20},
		WhisperURL:  "http://127.0.0.1:8080/inference",
		GeneratedAt: time.Now(),
	}

	msgIdle := FormatResourcesMessage(reportIdle, "ru")
	for _, want := range []string{
		"🎙 <b>Сервер распознавания речи (whisper-server):</b>",
		"• Состояние: 💤 <i>Простаивает (процесс не запущен)</i>",
		"• Эндпоинт: <code>http://127.0.0.1:8080/inference</code>",
		"• Служба: 🔴 <i>не найдена (make install-whisper)</i>",
	} {
		if !strings.Contains(msgIdle, want) {
			t.Errorf("expected %q in /top idle output, got:\n%s", want, msgIdle)
		}
	}
}

func TestFormatResourcesMessageWhisperEnglish(t *testing.T) {
	report := ResourcesReport{
		Host:    HostLoadStats{Load1: 0.5, Cores: 2},
		Memory:  HostMemoryStats{TotalBytes: 4 << 30, UsedBytes: 1 << 30},
		BotProc: &ProcessResourceInfo{PID: 100, CPUPercent: 0.1, MemoryBytes: 20 << 20},
		WhisperProc: &ProcessResourceInfo{
			PID:         600,
			CPUPercent:  0.0,
			MemoryBytes: 150 << 20,
			MemoryPct:   3.5,
			Elapsed:     "00:30",
			Threads:     4,
		},
		WhisperURL:       "http://127.0.0.1:8080/inference",
		WhisperModel:     "small",
		WhisperInstalled: true,
		GeneratedAt:      time.Now(),
	}

	msg := FormatResourcesMessage(report, "en")
	for _, want := range []string{
		"Speech recognition server (whisper-server):",
		"PID: <code>600</code> | State: 🟢 <i>active</i>",
		"Endpoint: <code>http://127.0.0.1:8080/inference</code>",
		"Model: <code>small</code>",
		"Service: 🟢 <i>installed (whisper-server.service)</i>",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected %q in English /top output, got:\n%s", want, msg)
		}
	}
}

func TestExtractWhisperModel(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{
			cmd:  "/home/deploy/whisper.cpp/build/bin/whisper-server -m /models/ggml-base-q5_1.bin --port 8080",
			want: "base-q5_1",
		},
		{
			cmd:  "whisper-server --model /opt/whisper/models/ggml-small.bin -t 4",
			want: "small",
		},
		{
			cmd:  "whisper-server -m /models/custom-model.bin",
			want: "custom-model",
		},
		{
			cmd:  "whisper-server --port 8080",
			want: "",
		},
	}

	for _, tc := range cases {
		got := extractWhisperModel(tc.cmd)
		if got != tc.want {
			t.Errorf("extractWhisperModel(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}
