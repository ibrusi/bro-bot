package system

import (
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

	msg := FormatResourcesMessage(report)
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
		HasActiveTask: true,
		ProjectName:   "my-cool-project",
		TaskPrompt:    "do some work with files",
		TaskElapsed:   45 * time.Second,
	}

	msg := FormatResourcesMessage(report)
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

	compact := FormatCompactResourceSnippet(report)
	if !strings.Contains(compact, "Bot (PID 100)") {
		t.Fatalf("expected bot in compact snippet: %s", compact)
	}
	if !strings.Contains(compact, "agy (PID 200)") {
		t.Fatalf("expected agy in compact snippet: %s", compact)
	}
}
