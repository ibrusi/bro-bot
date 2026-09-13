package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type HostMemoryStats struct {
	TotalBytes     int64
	AvailableBytes int64
	UsedBytes      int64
	UsedPercent    float64
}

type HostLoadStats struct {
	Load1  float64
	Load5  float64
	Load15 float64
	Cores  int
}

type CGroupStats struct {
	Available          bool
	GroupName          string
	MemoryCurrentBytes int64
	MemoryPeakBytes    int64
	TasksCount         int
	CPUUsageUsec       int64
}

type ProcessResourceInfo struct {
	PID         int
	PPID        int
	Name        string
	Role        string
	State       string
	CPUPercent  float64
	MemoryBytes int64
	MemoryPct   float64
	Threads     int
	Elapsed     string
	Command     string
}

type ResourcesReport struct {
	Host          HostLoadStats
	Memory        HostMemoryStats
	CGroup        CGroupStats
	BotProc       *ProcessResourceInfo
	ActiveWorker  *ProcessResourceInfo
	OtherWorkers  []ProcessResourceInfo
	HasActiveTask bool
	ProjectName   string
	TaskPrompt    string
	TaskElapsed   time.Duration
	GeneratedAt   time.Time
}

func formatBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	case b > 0:
		return fmt.Sprintf("%d B", b)
	default:
		return "0 B"
	}
}

func readHostMemory() HostMemoryStats {
	stats := HostMemoryStats{}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return stats
	}

	var totalKB, availKB, freeKB, buffersKB, cachedKB int64
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		val, _ := strconv.ParseInt(parts[1], 10, 64)
		switch key {
		case "MemTotal":
			totalKB = val
		case "MemAvailable":
			availKB = val
		case "MemFree":
			freeKB = val
		case "Buffers":
			buffersKB = val
		case "Cached":
			cachedKB = val
		}
	}

	if availKB == 0 && (freeKB > 0 || buffersKB > 0 || cachedKB > 0) {
		availKB = freeKB + buffersKB + cachedKB
	}

	stats.TotalBytes = totalKB * 1024
	stats.AvailableBytes = availKB * 1024
	if stats.TotalBytes > stats.AvailableBytes {
		stats.UsedBytes = stats.TotalBytes - stats.AvailableBytes
	}
	if stats.TotalBytes > 0 {
		stats.UsedPercent = (float64(stats.UsedBytes) / float64(stats.TotalBytes)) * 100
	}
	return stats
}

func readHostLoad() HostLoadStats {
	stats := HostLoadStats{
		Cores: runtime.NumCPU(),
	}
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return stats
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		stats.Load1, _ = strconv.ParseFloat(fields[0], 64)
		stats.Load5, _ = strconv.ParseFloat(fields[1], 64)
		stats.Load15, _ = strconv.ParseFloat(fields[2], 64)
	}
	return stats
}

func readCGroupStats() CGroupStats {
	stats := CGroupStats{
		GroupName: "tg-bot.service",
	}

	cgroupDir := ""
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "0::") {
				subPath := strings.TrimPrefix(line, "0::")
				subPath = strings.TrimSpace(subPath)
				if subPath != "" && subPath != "/" {
					candidate := filepath.Join("/sys/fs/cgroup", subPath)
					if fi, statErr := os.Stat(candidate); statErr == nil && fi.IsDir() {
						cgroupDir = candidate
						stats.GroupName = filepath.Base(subPath)
						break
					}
				}
			}
		}
	}

	if cgroupDir == "" {
		candidate := "/sys/fs/cgroup/system.slice/tg-bot.service"
		if fi, statErr := os.Stat(candidate); statErr == nil && fi.IsDir() {
			cgroupDir = candidate
		}
	}

	if cgroupDir != "" {
		if curData, err := os.ReadFile(filepath.Join(cgroupDir, "memory.current")); err == nil {
			v, _ := strconv.ParseInt(strings.TrimSpace(string(curData)), 10, 64)
			stats.MemoryCurrentBytes = v
			stats.Available = true
		}
		if peakData, err := os.ReadFile(filepath.Join(cgroupDir, "memory.peak")); err == nil {
			v, _ := strconv.ParseInt(strings.TrimSpace(string(peakData)), 10, 64)
			stats.MemoryPeakBytes = v
		}
		if pidsData, err := os.ReadFile(filepath.Join(cgroupDir, "pids.current")); err == nil {
			v, _ := strconv.Atoi(strings.TrimSpace(string(pidsData)))
			stats.TasksCount = v
		}
		if cpuData, err := os.ReadFile(filepath.Join(cgroupDir, "cpu.stat")); err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(cpuData))
			for scanner.Scan() {
				parts := strings.Fields(scanner.Text())
				if len(parts) >= 2 && parts[0] == "usage_usec" {
					stats.CPUUsageUsec, _ = strconv.ParseInt(parts[1], 10, 64)
					break
				}
			}
		}
	}

	// Fallback to systemctl show if available and memory not populated
	if stats.MemoryCurrentBytes == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
		defer cancel()
		out, err := exec.CommandContext(ctx, "systemctl", "show", "tg-bot.service",
			"--property=MemoryCurrent,MemoryPeak,CPUUsageNSec,TasksCurrent").Output()
		if err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(out))
			for scanner.Scan() {
				kv := strings.SplitN(scanner.Text(), "=", 2)
				if len(kv) != 2 {
					continue
				}
				key, val := kv[0], kv[1]
				switch key {
				case "MemoryCurrent":
					if v, err := strconv.ParseInt(val, 10, 64); err == nil && v > 0 {
						stats.MemoryCurrentBytes = v
						stats.Available = true
					}
				case "MemoryPeak":
					if v, err := strconv.ParseInt(val, 10, 64); err == nil && v > 0 {
						stats.MemoryPeakBytes = v
					}
				case "TasksCurrent":
					if v, err := strconv.Atoi(val); err == nil {
						stats.TasksCount = v
					}
				case "CPUUsageNSec":
					if v, err := strconv.ParseInt(val, 10, 64); err == nil {
						stats.CPUUsageUsec = v / 1000
					}
				}
			}
		}
	}

	return stats
}

type procStatusMeta struct {
	Name        string
	State       string
	VmRSSBytes  int64
	Threads     int
	PPID        int
	CommandLine string
}

func readProcStatus(pid int) procStatusMeta {
	meta := procStatusMeta{}
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return meta
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch key {
		case "Name":
			meta.Name = val
		case "State":
			meta.State = val
		case "PPid":
			meta.PPID, _ = strconv.Atoi(val)
		case "Threads":
			meta.Threads, _ = strconv.Atoi(val)
		case "VmRSS":
			fields := strings.Fields(val)
			if len(fields) > 0 {
				kb, _ := strconv.ParseInt(fields[0], 10, 64)
				meta.VmRSSBytes = kb * 1024
			}
		}
	}

	if cmdData, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		cmdParts := strings.Split(string(cmdData), "\x00")
		var nonEmp []string
		for _, cp := range cmdParts {
			if strings.TrimSpace(cp) != "" {
				nonEmp = append(nonEmp, cp)
			}
		}
		meta.CommandLine = strings.Join(nonEmp, " ")
	}

	return meta
}

type psMetric struct {
	CPUPercent float64
	MemPercent float64
	Elapsed    string
	Comm       string
}

func queryPsMetrics(pids []int) map[int]psMetric {
	result := make(map[int]psMetric)
	if len(pids) == 0 {
		return result
	}

	var pidStrs []string
	for _, p := range pids {
		if p > 0 {
			pidStrs = append(pidStrs, strconv.Itoa(p))
		}
	}
	if len(pidStrs) == 0 {
		return result
	}

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ps", "-p", strings.Join(pidStrs, ","), "-o", "pid,%cpu,%mem,etime,comm", "--no-headers")
	out, err := cmd.Output()
	if err != nil {
		return result
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 5 {
			pid, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			cpu, _ := strconv.ParseFloat(fields[1], 64)
			mem, _ := strconv.ParseFloat(fields[2], 64)
			etime := fields[3]
			comm := fields[4]
			result[pid] = psMetric{
				CPUPercent: cpu,
				MemPercent: mem,
				Elapsed:    etime,
				Comm:       comm,
			}
		}
	}
	return result
}

func queryInstantCpuTop(pids []int) map[int]float64 {
	result := make(map[int]float64)
	if len(pids) == 0 {
		return result
	}
	var pidStrs []string
	for _, p := range pids {
		if p > 0 {
			pidStrs = append(pidStrs, strconv.Itoa(p))
		}
	}
	if len(pidStrs) == 0 {
		return result
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	// 2 iterations with 0.25s delay to get true instantaneous process CPU
	cmd := exec.CommandContext(ctx, "top", "-b", "-n", "2", "-d", "0.25", "-p", strings.Join(pidStrs, ","))
	out, err := cmd.Output()
	if err != nil {
		return result
	}

	// Split by iterations ("top - ")
	chunks := strings.Split(string(out), "top - ")
	if len(chunks) < 2 {
		return result
	}
	lastChunk := chunks[len(chunks)-1]

	scanner := bufio.NewScanner(strings.NewReader(lastChunk))
	inProcList := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "PID ") || strings.Contains(line, "PID USER") {
			inProcList = true
			continue
		}
		if !inProcList || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 10 {
			pid, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			// field 8 is %CPU in standard top
			cpuVal, err := strconv.ParseFloat(fields[8], 64)
			if err == nil {
				result[pid] = cpuVal
			}
		}
	}

	return result
}

func findSystemAgyPids() []int {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, "pgrep", "-x", "agy").Output()
	if err != nil {
		return nil
	}

	var pids []int
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if p, err := strconv.Atoi(text); err == nil && p > 0 {
			pids = append(pids, p)
		}
	}
	return pids
}

func collectProcessInfo(pid int, role string, psMap map[int]psMetric, topCpuMap map[int]float64) *ProcessResourceInfo {
	if pid <= 0 {
		return nil
	}

	meta := readProcStatus(pid)
	if meta.Name == "" && meta.VmRSSBytes == 0 {
		// Process might not exist
		if ps, ok := psMap[pid]; !ok || ps.Comm == "" {
			return nil
		}
	}

	info := &ProcessResourceInfo{
		PID:         pid,
		PPID:        meta.PPID,
		Name:        meta.Name,
		Role:        role,
		State:       meta.State,
		MemoryBytes: meta.VmRSSBytes,
		Threads:     meta.Threads,
		Command:     meta.CommandLine,
	}

	if ps, ok := psMap[pid]; ok {
		info.CPUPercent = ps.CPUPercent
		info.MemoryPct = ps.MemPercent
		info.Elapsed = ps.Elapsed
		if info.Name == "" {
			info.Name = ps.Comm
		}
	}

	// If instantaneous top sampled this PID, prefer it
	if instantCpu, ok := topCpuMap[pid]; ok {
		info.CPUPercent = instantCpu
	}

	return info
}

func CollectResourceReport(detailedInstantCpu bool) ResourcesReport {
	hostLoad := readHostLoad()
	hostMem := readHostMemory()
	cgroup := readCGroupStats()

	botPid := os.Getpid()

	session.Lock()
	hasActive := session.isRunning
	projName := session.currentProject
	prompt := session.currentPrompt
	startedAt := session.startedAt
	var activeWorkerPid int
	if session.cmd != nil && session.cmd.Process != nil {
		activeWorkerPid = session.cmd.Process.Pid
	}
	session.Unlock()

	activeTask := taskManager.GetActiveTask()
	if activeTask != nil {
		activeTask.Lock()
		if activeTask.Status == TaskStatusRunning || activeTask.Status == TaskStatusWaitingInput {
			hasActive = true
			if activeTask.Project != "" {
				projName = activeTask.Project
			}
			if activeTask.CurrentPrompt != "" {
				prompt = activeTask.CurrentPrompt
			} else if activeTask.InitialPrompt != "" {
				prompt = activeTask.InitialPrompt
			}
			if !activeTask.StartedAt.IsZero() {
				startedAt = activeTask.StartedAt
			}
			if activeTask.Cmd != nil && activeTask.Cmd.Process != nil && activeTask.Cmd.Process.Pid > 0 {
				activeWorkerPid = activeTask.Cmd.Process.Pid
			}
		}
		activeTask.Unlock()
	}

	activePid, otherPids := taskManager.GetRunningWorkerPids()
	if activePid > 0 {
		activeWorkerPid = activePid
	}

	allAgyPids := findSystemAgyPids()

	// Compile unique PIDs to query
	pidSet := make(map[int]bool)
	pidSet[botPid] = true
	if activeWorkerPid > 0 {
		pidSet[activeWorkerPid] = true
	}
	for _, p := range otherPids {
		pidSet[p] = true
	}
	for _, p := range allAgyPids {
		pidSet[p] = true
	}

	var pids []int
	for p := range pidSet {
		pids = append(pids, p)
	}

	psMetrics := queryPsMetrics(pids)

	var topCpu map[int]float64
	if detailedInstantCpu {
		topCpu = queryInstantCpuTop(pids)
	}

	botInfo := collectProcessInfo(botPid, "Telegram Bot", psMetrics, topCpu)

	var activeWorkerInfo *ProcessResourceInfo
	if activeWorkerPid > 0 {
		activeWorkerInfo = collectProcessInfo(activeWorkerPid, "Active Agent Worker", psMetrics, topCpu)
	}

	var otherWorkers []ProcessResourceInfo
	for _, p := range allAgyPids {
		if p == activeWorkerPid {
			continue
		}
		if pInfo := collectProcessInfo(p, "Background agy", psMetrics, topCpu); pInfo != nil {
			otherWorkers = append(otherWorkers, *pInfo)
		}
	}

	var taskElapsed time.Duration
	if hasActive && !startedAt.IsZero() {
		taskElapsed = time.Since(startedAt).Round(time.Second)
	}

	return ResourcesReport{
		Host:          hostLoad,
		Memory:        hostMem,
		CGroup:        cgroup,
		BotProc:       botInfo,
		ActiveWorker:  activeWorkerInfo,
		OtherWorkers:  otherWorkers,
		HasActiveTask: hasActive,
		ProjectName:   projName,
		TaskPrompt:    prompt,
		TaskElapsed:   taskElapsed,
		GeneratedAt:   time.Now(),
	}
}

func FormatResourcesMessage(r ResourcesReport) string {
	var sb strings.Builder
	sb.WriteString("📊 <b>Мониторинг ресурсов (CPU / RAM)</b>\n\n")

	// 1. Host Server
	sb.WriteString("💻 <b>Сервер:</b>\n")
	sb.WriteString(fmt.Sprintf("• CPU Load: <code>%.2f, %.2f, %.2f</code> (%d vCPU)\n",
		r.Host.Load1, r.Host.Load5, r.Host.Load15, r.Host.Cores))
	if r.Memory.TotalBytes > 0 {
		sb.WriteString(fmt.Sprintf("• RAM: <b>%s</b> / <b>%s</b> (<code>%.1f%%</code> занято)\n",
			formatBytes(r.Memory.UsedBytes),
			formatBytes(r.Memory.TotalBytes),
			r.Memory.UsedPercent))
	}

	// 2. Systemd Service CGroup
	if r.CGroup.Available && r.CGroup.MemoryCurrentBytes > 0 {
		sb.WriteString(fmt.Sprintf("\n🛡 <b>Сервис бота (cgroup %s):</b>\n", html.EscapeString(r.CGroup.GroupName)))
		peakStr := ""
		if r.CGroup.MemoryPeakBytes > 0 {
			peakStr = fmt.Sprintf(" (пик: <b>%s</b>)", formatBytes(r.CGroup.MemoryPeakBytes))
		}
		sb.WriteString(fmt.Sprintf("• Потребление RAM: <b>%s</b>%s\n",
			formatBytes(r.CGroup.MemoryCurrentBytes), peakStr))
		if r.CGroup.TasksCount > 0 {
			sb.WriteString(fmt.Sprintf("• Потоков/задач: <code>%d</code>\n", r.CGroup.TasksCount))
		}
		if r.CGroup.CPUUsageUsec > 0 {
			cpuSec := float64(r.CGroup.CPUUsageUsec) / 1000000.0
			sb.WriteString(fmt.Sprintf("• Суммарное время CPU: <code>%.1fs</code>\n", cpuSec))
		}
	}

	// 3. Telegram Bot Process
	if r.BotProc != nil {
		sb.WriteString("\n🤖 <b>Telegram-бот (bot):</b>\n")
		sb.WriteString(fmt.Sprintf("• PID: <code>%d</code> | Состояние: 🟢 <i>active</i>\n", r.BotProc.PID))
		sb.WriteString(fmt.Sprintf("• Память (RSS): <b>%s</b> (<code>%.1f%%</code> RAM)\n",
			formatBytes(r.BotProc.MemoryBytes), r.BotProc.MemoryPct))
		sb.WriteString(fmt.Sprintf("• Нагрузка CPU: <b>%.1f%%</b>\n", r.BotProc.CPUPercent))
		if r.BotProc.Elapsed != "" {
			sb.WriteString(fmt.Sprintf("• Аптайм: <code>%s</code> | Потоков: <code>%d</code>\n",
				r.BotProc.Elapsed, r.BotProc.Threads))
		}
	}

	// 4. Agent Worker Process (agy)
	sb.WriteString("\n🧠 <b>Агент задач (agy):</b>\n")
	if r.HasActiveTask && r.ActiveWorker != nil {
		sb.WriteString(fmt.Sprintf("• PID: <code>%d</code> | Состояние: ⚡️ <b>выполняет задачу</b>\n", r.ActiveWorker.PID))
		sb.WriteString(fmt.Sprintf("• Память (RSS): <b>%s</b> (<code>%.1f%%</code> RAM)\n",
			formatBytes(r.ActiveWorker.MemoryBytes), r.ActiveWorker.MemoryPct))
		sb.WriteString(fmt.Sprintf("• Нагрузка CPU: <b>%.1f%%</b>\n", r.ActiveWorker.CPUPercent))
		if r.ActiveWorker.Elapsed != "" {
			sb.WriteString(fmt.Sprintf("• Время процесса: <code>%s</code>\n", r.ActiveWorker.Elapsed))
		} else if r.TaskElapsed > 0 {
			sb.WriteString(fmt.Sprintf("• Время шага: <code>%s</code>\n", r.TaskElapsed))
		}
		if r.ProjectName != "" {
			sb.WriteString(fmt.Sprintf("• Проект: <code>%s</code>\n", html.EscapeString(r.ProjectName)))
		}
		if r.TaskPrompt != "" {
			shortPrompt := truncateString(r.TaskPrompt, 80)
			sb.WriteString(fmt.Sprintf("• Задача: <i>%s</i>\n", html.EscapeString(shortPrompt)))
		}
	} else if r.HasActiveTask {
		sb.WriteString("• Состояние: ⚙️ <i>Инициализация или запуск процесса...</i>\n")
		if r.ProjectName != "" {
			sb.WriteString(fmt.Sprintf("• Проект: <code>%s</code>\n", html.EscapeString(r.ProjectName)))
		}
	} else {
		sb.WriteString("• Состояние: 💤 <i>Простаивает (нет активных задач)</i>\n")
	}

	// 5. Other agy processes
	if len(r.OtherWorkers) > 0 {
		sb.WriteString(fmt.Sprintf("\n⚠️ <b>Другие процессы agy в системе (%d):</b>\n", len(r.OtherWorkers)))
		for _, other := range r.OtherWorkers {
			sb.WriteString(fmt.Sprintf("• PID <code>%d</code>: RAM <b>%s</b>, CPU <b>%.1f%%</b>, аптайм <code>%s</code>\n",
				other.PID, formatBytes(other.MemoryBytes), other.CPUPercent, other.Elapsed))
		}
	}

	sb.WriteString("\n💡 <i>Совет: в консоли Linux для мгновенного мониторинга сервиса используйте:</i>\n")
	sb.WriteString("<code>systemctl status tg-bot.service</code>\n")
	sb.WriteString("<i>или для динамики:</i> <code>top -p $(pgrep -d, -f 'bot|agy')</code>")

	return sb.String()
}

func FormatCompactResourceSnippet(r ResourcesReport) string {
	var sb strings.Builder
	sb.WriteString("💻 <b>Ресурсы:</b>\n")

	var parts []string
	if r.BotProc != nil {
		parts = append(parts, fmt.Sprintf("Bot (PID %d): RAM <b>%s</b>, CPU <b>%.1f%%</b>",
			r.BotProc.PID, formatBytes(r.BotProc.MemoryBytes), r.BotProc.CPUPercent))
	}
	if r.HasActiveTask && r.ActiveWorker != nil {
		parts = append(parts, fmt.Sprintf("agy (PID %d): RAM <b>%s</b>, CPU <b>%.1f%%</b>",
			r.ActiveWorker.PID, formatBytes(r.ActiveWorker.MemoryBytes), r.ActiveWorker.CPUPercent))
	} else if r.HasActiveTask {
		parts = append(parts, "agy: <i>запуск...</i>")
	} else {
		parts = append(parts, "agy: 💤 <i>idle</i>")
	}

	for _, p := range parts {
		sb.WriteString("• " + p + "\n")
	}

	if r.CGroup.Available && r.CGroup.MemoryCurrentBytes > 0 {
		peakStr := ""
		if r.CGroup.MemoryPeakBytes > 0 {
			peakStr = fmt.Sprintf(" (пик %s)", formatBytes(r.CGroup.MemoryPeakBytes))
		}
		sb.WriteString(fmt.Sprintf("• CGroup всего: RAM <b>%s</b>%s\n",
			formatBytes(r.CGroup.MemoryCurrentBytes), peakStr))
	}

	return strings.TrimRight(sb.String(), "\n")
}
