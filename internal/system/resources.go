package system

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/utils"
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
	"sync"
	"syscall"
	"time"
)

type HostMemoryStats struct {
	TotalBytes     int64
	AvailableBytes int64
	UsedBytes      int64
	UsedPercent    float64
}

type HostDiskStats struct {
	TotalBytes  int64
	FreeBytes   int64
	UsedBytes   int64
	FreePercent float64
	UsedPercent float64
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

type AgentCLIInfo struct {
	Installed bool
	Version   string
}

type ClaudeAgentInfo = AgentCLIInfo

type ResourcesReport struct {
	Host              HostLoadStats
	Memory            HostMemoryStats
	Disk              HostDiskStats
	CGroup            CGroupStats
	BotProc           *ProcessResourceInfo
	ActiveAgent       string
	ActiveWorkerAgent string
	ActiveWorker      *ProcessResourceInfo
	OtherWorkers      []ProcessResourceInfo
	ClaudeWorkers     []ProcessResourceInfo
	AgyVersion        string
	AgyInstalled      bool
	ClaudeVersion     string
	ClaudeInstalled   bool
	WhisperProc       *ProcessResourceInfo
	OtherWhisperProcs []ProcessResourceInfo
	WhisperURL        string
	WhisperModel      string
	WhisperInstalled  bool
	HasActiveTask     bool
	ProjectName       string
	TaskPrompt        string
	TaskElapsed       time.Duration
	GeneratedAt       time.Time
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

func readHostDisk() HostDiskStats {
	targetPath := "/"
	if config.ProjectsRoot != "" {
		if _, err := os.Stat(config.ProjectsRoot); err == nil {
			targetPath = config.ProjectsRoot
		}
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(targetPath, &stat); err != nil {
		if targetPath != "/" {
			if err := syscall.Statfs("/", &stat); err != nil {
				return HostDiskStats{}
			}
		} else {
			return HostDiskStats{}
		}
	}

	bsize := stat.Bsize
	if stat.Frsize > 0 {
		bsize = stat.Frsize
	}
	if bsize <= 0 {
		bsize = 512
	}

	totalBytes := int64(stat.Blocks) * bsize
	freeBytes := int64(stat.Bavail) * bsize
	var usedBytes int64
	if stat.Blocks >= stat.Bfree {
		usedBytes = int64(stat.Blocks-stat.Bfree) * bsize
	} else if totalBytes > freeBytes {
		usedBytes = totalBytes - freeBytes
	}

	var freePct, usedPct float64
	if totalBytes > 0 {
		freePct = (float64(freeBytes) / float64(totalBytes)) * 100
		usedPct = (float64(usedBytes) / float64(totalBytes)) * 100
	}

	return HostDiskStats{
		TotalBytes:  totalBytes,
		FreeBytes:   freeBytes,
		UsedBytes:   usedBytes,
		FreePercent: freePct,
		UsedPercent: usedPct,
	}
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

func findSystemPids(processName string) []int {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, "pgrep", "-x", processName).Output()
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

func findSystemAgyPids() []int {
	return findSystemPids("agy")
}

func findSystemClaudePids() []int {
	return findSystemPids("claude")
}

func findSystemWhisperPids() []int {
	pids := findSystemPids("whisper-server")
	if len(pids) == 0 {
		if sp := findWhisperSystemdPid(); sp > 0 {
			pids = append(pids, sp)
		}
	}
	return pids
}

func findWhisperSystemdPid() int {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "whisper-server.service", "--property=MainPID").Output()
	if err == nil {
		line := strings.TrimSpace(string(out))
		if strings.HasPrefix(line, "MainPID=") {
			if pid, err := strconv.Atoi(strings.TrimPrefix(line, "MainPID=")); err == nil && pid > 0 {
				return pid
			}
		}
	}
	return 0
}

func extractWhisperModel(commandLine string) string {
	fields := strings.Fields(commandLine)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "-m" || fields[i] == "--model" {
			modelPath := fields[i+1]
			base := filepath.Base(modelPath)
			base = strings.TrimPrefix(base, "ggml-")
			base = strings.TrimSuffix(base, ".bin")
			return base
		}
	}
	return ""
}

func isWhisperInstalled(proc *ProcessResourceInfo) bool {
	if proc != nil {
		return true
	}
	if _, err := exec.LookPath("whisper-server"); err == nil {
		return true
	}
	if _, err := os.Stat("/etc/systemd/system/whisper-server.service"); err == nil {
		return true
	}
	commonPaths := []string{
		"/home/deploy/whisper.cpp/build/bin/whisper-server",
		"/usr/local/bin/whisper-server",
		"/usr/bin/whisper-server",
	}
	for _, p := range commonPaths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

var (
	cliInfoLock   sync.RWMutex
	cachedCLIInfo = make(map[string]*AgentCLIInfo)
	cachedCLITime = make(map[string]time.Time)
)

func queryAgentCLI(binName string) AgentCLIInfo {
	cliInfoLock.RLock()
	if info, ok := cachedCLIInfo[binName]; ok && time.Since(cachedCLITime[binName]) < 5*time.Minute {
		res := *info
		cliInfoLock.RUnlock()
		return res
	}
	cliInfoLock.RUnlock()

	cliInfoLock.Lock()
	defer cliInfoLock.Unlock()

	if info, ok := cachedCLIInfo[binName]; ok && time.Since(cachedCLITime[binName]) < 5*time.Minute {
		return *info
	}

	info := AgentCLIInfo{}
	path, err := exec.LookPath(binName)
	if err != nil {
		cachedCLIInfo[binName] = &info
		cachedCLITime[binName] = time.Now()
		return info
	}
	info.Installed = true

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--version")
	out, err := cmd.Output()
	if err == nil {
		ver := strings.TrimSpace(string(out))
		if ver != "" {
			if binName == "agy" && !strings.Contains(strings.ToLower(ver), "agy") {
				info.Version = "agy " + ver
			} else {
				info.Version = ver
			}
		} else {
			info.Version = binName
		}
	} else {
		info.Version = binName
	}

	cachedCLIInfo[binName] = &info
	cachedCLITime[binName] = time.Now()
	return info
}

func queryClaudeInfo() AgentCLIInfo {
	return queryAgentCLI("claude")
}

func queryAgyInfo() AgentCLIInfo {
	return queryAgentCLI("agy")
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
	hostDisk := readHostDisk()
	cgroup := readCGroupStats()

	botPid := os.Getpid()

	config.Session.Lock()
	hasActive := config.Session.IsRunning
	projName := config.Session.CurrentProject
	prompt := config.Session.CurrentPrompt
	startedAt := config.Session.StartedAt
	config.Session.Unlock()

	activeAgent := config.ProjectState.GetCurrentAgent()
	activeWorkerAgent := activeAgent

	activeTask := domain.GlobalTaskManager.GetActiveTask()
	if activeTask != nil {
		activeView := activeTask.Snapshot()
		if activeView.Status == domain.TaskStatusRunning || activeView.Status == domain.TaskStatusWaitingInput || activeView.Status == domain.TaskStatusPlanning {
			hasActive = true
			if activeView.Project != "" {
				projName = activeView.Project
			}
			if activeView.CurrentPrompt != "" {
				prompt = activeView.CurrentPrompt
			} else if activeView.InitialPrompt != "" {
				prompt = activeView.InitialPrompt
			}
			if !activeView.StartedAt.IsZero() {
				startedAt = activeView.StartedAt
			}
			if activeView.Agent != "" {
				activeWorkerAgent = activeView.Agent
			}
		}
	}

	// PID воркера берём только у менеджера задач: он единственный, кто знает про
	// процесс шага. В api-режиме процесса в ОС нет, и PID остаётся нулевым.
	activeWorkerPid, otherPids := domain.GlobalTaskManager.GetRunningWorkerPids()

	allAgyPids := findSystemAgyPids()
	allClaudePids := findSystemClaudePids()
	allWhisperPids := findSystemWhisperPids()

	// If activeWorkerPid is in allClaudePids or allAgyPids, refine activeWorkerAgent
	for _, p := range allClaudePids {
		if p == activeWorkerPid {
			activeWorkerAgent = "claude"
			break
		}
	}
	for _, p := range allAgyPids {
		if p == activeWorkerPid {
			activeWorkerAgent = "agy"
			break
		}
	}

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
	for _, p := range allClaudePids {
		pidSet[p] = true
	}
	for _, p := range allWhisperPids {
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
		roleName := "Active Agent Worker"
		if activeWorkerAgent == "claude" {
			roleName = "Active Claude Worker"
		} else if activeWorkerAgent == "agy" {
			roleName = "Active agy Worker"
		}
		activeWorkerInfo = collectProcessInfo(activeWorkerPid, roleName, psMetrics, topCpu)
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

	var claudeWorkers []ProcessResourceInfo
	for _, p := range allClaudePids {
		if p == activeWorkerPid {
			continue
		}
		if pInfo := collectProcessInfo(p, "Background claude", psMetrics, topCpu); pInfo != nil {
			claudeWorkers = append(claudeWorkers, *pInfo)
		}
	}

	var whisperInfo *ProcessResourceInfo
	var otherWhisper []ProcessResourceInfo
	if len(allWhisperPids) > 0 {
		whisperInfo = collectProcessInfo(allWhisperPids[0], "Whisper Server", psMetrics, topCpu)
		for _, p := range allWhisperPids[1:] {
			if pInfo := collectProcessInfo(p, "Background whisper", psMetrics, topCpu); pInfo != nil {
				otherWhisper = append(otherWhisper, *pInfo)
			}
		}
	}

	whisperModel := config.WhisperModel
	if whisperInfo != nil && whisperInfo.Command != "" {
		if parsed := extractWhisperModel(whisperInfo.Command); parsed != "" {
			whisperModel = parsed
		}
	}
	whisperURL := config.WhisperServerURL
	whisperInstalled := isWhisperInstalled(whisperInfo)

	agyInfo := queryAgyInfo()
	claudeInfo := queryClaudeInfo()

	var taskElapsed time.Duration
	if hasActive && !startedAt.IsZero() {
		taskElapsed = time.Since(startedAt).Round(time.Second)
	}

	return ResourcesReport{
		Host:              hostLoad,
		Memory:            hostMem,
		Disk:              hostDisk,
		CGroup:            cgroup,
		BotProc:           botInfo,
		ActiveAgent:       activeAgent,
		ActiveWorkerAgent: activeWorkerAgent,
		ActiveWorker:      activeWorkerInfo,
		OtherWorkers:      otherWorkers,
		ClaudeWorkers:     claudeWorkers,
		AgyVersion:        agyInfo.Version,
		AgyInstalled:      agyInfo.Installed,
		ClaudeVersion:     claudeInfo.Version,
		ClaudeInstalled:   claudeInfo.Installed,
		WhisperProc:       whisperInfo,
		OtherWhisperProcs: otherWhisper,
		WhisperURL:        whisperURL,
		WhisperModel:      whisperModel,
		WhisperInstalled:  whisperInstalled,
		HasActiveTask:     hasActive,
		ProjectName:       projName,
		TaskPrompt:        prompt,
		TaskElapsed:       taskElapsed,
		GeneratedAt:       time.Now(),
	}
}

type agentSectionConfig struct {
	AgentName    string
	Icon         string
	IsActive     bool
	Installed    bool
	Version      string
	WorkerActive bool
	Worker       *ProcessResourceInfo
	OtherWorkers []ProcessResourceInfo
	ProjectName  string
	TaskPrompt   string
	TaskElapsed  time.Duration
}

func formatAgentResourceSection(sb *strings.Builder, cfg agentSectionConfig, lang string) {
	activeTag := ""
	if cfg.IsActive {
		activeTag = i18n.T(lang, "res.agent_active_tag")
	}
	sb.WriteString(i18n.Tf(lang, "res.agent_header", cfg.Icon, html.EscapeString(cfg.AgentName), activeTag))

	if cfg.WorkerActive && cfg.Worker != nil {
		sb.WriteString(i18n.Tf(lang, "res.agent_pid", cfg.Worker.PID))
		sb.WriteString(i18n.Tf(lang, "res.memory",
			formatBytes(cfg.Worker.MemoryBytes), cfg.Worker.MemoryPct))
		sb.WriteString(i18n.Tf(lang, "res.cpu", cfg.Worker.CPUPercent))
		if cfg.Worker.Elapsed != "" {
			sb.WriteString(i18n.Tf(lang, "res.agent_process_time", cfg.Worker.Elapsed))
		} else if cfg.TaskElapsed > 0 {
			sb.WriteString(i18n.Tf(lang, "res.agent_step_time", domain.FormatDuration(cfg.TaskElapsed, lang)))
		}
		if cfg.ProjectName != "" {
			sb.WriteString(i18n.Tf(lang, "res.agent_project", html.EscapeString(cfg.ProjectName)))
		}
		if cfg.TaskPrompt != "" {
			shortPrompt := utils.TruncateString(cfg.TaskPrompt, 80)
			sb.WriteString(i18n.Tf(lang, "res.agent_task", html.EscapeString(shortPrompt)))
		}
	} else if cfg.WorkerActive {
		sb.WriteString(i18n.T(lang, "res.agent_starting"))
		if cfg.ProjectName != "" {
			sb.WriteString(i18n.Tf(lang, "res.agent_project", html.EscapeString(cfg.ProjectName)))
		}
	} else {
		sb.WriteString(i18n.T(lang, "res.agent_idle"))
	}

	if cfg.Installed {
		versionStr := cfg.Version
		if versionStr == "" {
			if cfg.AgentName == "claude" {
				versionStr = "Claude Code CLI"
			} else {
				versionStr = cfg.AgentName + " CLI"
			}
		}
		sb.WriteString(i18n.Tf(lang, "res.agent_cli_ready", html.EscapeString(versionStr)))
	} else {
		sb.WriteString(i18n.T(lang, "res.agent_cli_missing"))
	}

	// Other processes
	if len(cfg.OtherWorkers) > 0 {
		sb.WriteString(i18n.Tf(lang, "res.agent_background", cfg.AgentName, len(cfg.OtherWorkers)))
		for _, other := range cfg.OtherWorkers {
			sb.WriteString(i18n.Tf(lang, "res.agent_background_item",
				other.PID, formatBytes(other.MemoryBytes), other.CPUPercent, other.Elapsed))
		}
	}
}

func FormatResourcesMessage(r ResourcesReport, lang string) string {
	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "res.header"))

	// 1. Host Server
	sb.WriteString(i18n.T(lang, "res.server_header"))
	sb.WriteString(i18n.Tf(lang, "res.cpu_load",
		r.Host.Load1, r.Host.Load5, r.Host.Load15, r.Host.Cores))
	if r.Memory.TotalBytes > 0 {
		sb.WriteString(i18n.Tf(lang, "res.ram",
			formatBytes(r.Memory.UsedBytes),
			formatBytes(r.Memory.TotalBytes),
			r.Memory.UsedPercent))
	}
	if r.Disk.TotalBytes > 0 {
		sb.WriteString(i18n.Tf(lang, "res.disk",
			formatBytes(r.Disk.FreeBytes),
			formatBytes(r.Disk.TotalBytes),
			r.Disk.FreePercent))
	}

	// 2. Systemd Service CGroup
	if r.CGroup.Available && r.CGroup.MemoryCurrentBytes > 0 {
		sb.WriteString(i18n.Tf(lang, "res.cgroup_header", html.EscapeString(r.CGroup.GroupName)))
		peakStr := ""
		if r.CGroup.MemoryPeakBytes > 0 {
			peakStr = i18n.Tf(lang, "res.cgroup_peak", formatBytes(r.CGroup.MemoryPeakBytes))
		}
		sb.WriteString(i18n.Tf(lang, "res.cgroup_ram",
			formatBytes(r.CGroup.MemoryCurrentBytes), peakStr))
		if r.CGroup.TasksCount > 0 {
			sb.WriteString(i18n.Tf(lang, "res.cgroup_tasks", r.CGroup.TasksCount))
		}
		if r.CGroup.CPUUsageUsec > 0 {
			cpuSec := float64(r.CGroup.CPUUsageUsec) / 1000000.0
			sb.WriteString(i18n.Tf(lang, "res.cgroup_cpu", cpuSec))
		}
	}

	// 3. Telegram Bot Process
	if r.BotProc != nil {
		sb.WriteString(i18n.T(lang, "res.bot_header"))
		sb.WriteString(i18n.Tf(lang, "res.bot_pid", r.BotProc.PID))
		activeAgent := r.ActiveAgent
		if activeAgent == "" {
			activeAgent = "agy"
		}
		sb.WriteString(i18n.Tf(lang, "res.bot_agent", html.EscapeString(activeAgent)))
		sb.WriteString(i18n.Tf(lang, "res.memory",
			formatBytes(r.BotProc.MemoryBytes), r.BotProc.MemoryPct))
		sb.WriteString(i18n.Tf(lang, "res.cpu", r.BotProc.CPUPercent))
		if r.BotProc.Elapsed != "" {
			sb.WriteString(i18n.Tf(lang, "res.bot_uptime",
				r.BotProc.Elapsed, r.BotProc.Threads))
		}
	}

	// 4. Agent Worker Process (agy)
	agyWorkerActive := r.HasActiveTask && (r.ActiveWorkerAgent == "" || r.ActiveWorkerAgent == "agy")
	var agyWorker *ProcessResourceInfo
	if agyWorkerActive {
		agyWorker = r.ActiveWorker
	}
	formatAgentResourceSection(&sb, agentSectionConfig{
		AgentName:    "agy",
		Icon:         "🧠",
		IsActive:     r.ActiveAgent == "agy" || r.ActiveAgent == "",
		Installed:    r.AgyInstalled,
		Version:      r.AgyVersion,
		WorkerActive: agyWorkerActive,
		Worker:       agyWorker,
		OtherWorkers: r.OtherWorkers,
		ProjectName:  r.ProjectName,
		TaskPrompt:   r.TaskPrompt,
		TaskElapsed:  r.TaskElapsed,
	}, lang)

	// 5. Agent Worker Process (claude)
	claudeWorkerActive := r.HasActiveTask && r.ActiveWorkerAgent == "claude"
	var claudeWorker *ProcessResourceInfo
	if claudeWorkerActive {
		claudeWorker = r.ActiveWorker
	}
	formatAgentResourceSection(&sb, agentSectionConfig{
		AgentName:    "claude",
		Icon:         "🟣",
		IsActive:     r.ActiveAgent == "claude",
		Installed:    r.ClaudeInstalled,
		Version:      r.ClaudeVersion,
		WorkerActive: claudeWorkerActive,
		Worker:       claudeWorker,
		OtherWorkers: r.ClaudeWorkers,
		ProjectName:  r.ProjectName,
		TaskPrompt:   r.TaskPrompt,
		TaskElapsed:  r.TaskElapsed,
	}, lang)

	// 6. Speech-to-Text Server (whisper-server)
	formatWhisperResourceSection(&sb, r, lang)

	sb.WriteString(i18n.T(lang, "res.tip"))
	sb.WriteString("<code>systemctl status tg-bot.service</code>\n")
	sb.WriteString(i18n.T(lang, "res.tip_dynamic"))

	return sb.String()
}

func formatWhisperResourceSection(sb *strings.Builder, r ResourcesReport, lang string) {
	sb.WriteString(i18n.T(lang, "res.whisper_header"))

	if r.WhisperProc != nil {
		sb.WriteString(i18n.Tf(lang, "res.bot_pid", r.WhisperProc.PID))
		sb.WriteString(i18n.Tf(lang, "res.memory",
			formatBytes(r.WhisperProc.MemoryBytes), r.WhisperProc.MemoryPct))
		sb.WriteString(i18n.Tf(lang, "res.cpu", r.WhisperProc.CPUPercent))
		if r.WhisperProc.Elapsed != "" {
			sb.WriteString(i18n.Tf(lang, "res.bot_uptime",
				r.WhisperProc.Elapsed, r.WhisperProc.Threads))
		}
	} else {
		sb.WriteString(i18n.T(lang, "res.whisper_idle"))
	}

	if r.WhisperURL != "" {
		sb.WriteString(i18n.Tf(lang, "res.whisper_endpoint", html.EscapeString(r.WhisperURL)))
	}
	if r.WhisperModel != "" {
		sb.WriteString(i18n.Tf(lang, "res.whisper_model", html.EscapeString(r.WhisperModel)))
	}

	if r.WhisperInstalled {
		sb.WriteString(i18n.T(lang, "res.whisper_ready"))
	} else {
		sb.WriteString(i18n.T(lang, "res.whisper_missing"))
	}

	if len(r.OtherWhisperProcs) > 0 {
		sb.WriteString(i18n.Tf(lang, "res.whisper_background", len(r.OtherWhisperProcs)))
		for _, other := range r.OtherWhisperProcs {
			sb.WriteString(i18n.Tf(lang, "res.agent_background_item",
				other.PID, formatBytes(other.MemoryBytes), other.CPUPercent, other.Elapsed))
		}
	}
}

func FormatCompactResourceSnippet(r ResourcesReport, lang string) string {
	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "res.compact_header"))

	var parts []string
	if r.BotProc != nil {
		parts = append(parts, fmt.Sprintf("Bot (PID %d): RAM <b>%s</b>, CPU <b>%.1f%%</b>",
			r.BotProc.PID, formatBytes(r.BotProc.MemoryBytes), r.BotProc.CPUPercent))
	}

	// 1. agy
	agyActive := r.HasActiveTask && (r.ActiveWorkerAgent == "" || r.ActiveWorkerAgent == "agy")
	if agyActive && r.ActiveWorker != nil {
		parts = append(parts, fmt.Sprintf("agy (PID %d): RAM <b>%s</b>, CPU <b>%.1f%%</b>",
			r.ActiveWorker.PID, formatBytes(r.ActiveWorker.MemoryBytes), r.ActiveWorker.CPUPercent))
	} else if agyActive {
		parts = append(parts, i18n.Tf(lang, "res.compact_starting", "agy"))
	} else {
		parts = append(parts, i18n.Tf(lang, "res.compact_idle", "agy"))
	}

	// 2. claude
	claudeActive := r.HasActiveTask && r.ActiveWorkerAgent == "claude"
	if claudeActive && r.ActiveWorker != nil {
		parts = append(parts, fmt.Sprintf("claude (PID %d): RAM <b>%s</b>, CPU <b>%.1f%%</b>",
			r.ActiveWorker.PID, formatBytes(r.ActiveWorker.MemoryBytes), r.ActiveWorker.CPUPercent))
	} else if claudeActive {
		parts = append(parts, i18n.Tf(lang, "res.compact_starting", "claude"))
	} else {
		parts = append(parts, i18n.Tf(lang, "res.compact_idle", "claude"))
	}

	// 3. whisper-server
	if r.WhisperProc != nil {
		parts = append(parts, fmt.Sprintf("whisper-server (PID %d): RAM <b>%s</b>, CPU <b>%.1f%%</b>",
			r.WhisperProc.PID, formatBytes(r.WhisperProc.MemoryBytes), r.WhisperProc.CPUPercent))
	} else {
		parts = append(parts, i18n.Tf(lang, "res.compact_idle", "whisper-server"))
	}

	for _, p := range parts {
		sb.WriteString("• " + p + "\n")
	}

	if r.CGroup.Available && r.CGroup.MemoryCurrentBytes > 0 {
		peakStr := ""
		if r.CGroup.MemoryPeakBytes > 0 {
			peakStr = i18n.Tf(lang, "res.compact_peak", formatBytes(r.CGroup.MemoryPeakBytes))
		}
		sb.WriteString(i18n.Tf(lang, "res.compact_cgroup",
			formatBytes(r.CGroup.MemoryCurrentBytes), peakStr))
	}

	return strings.TrimRight(sb.String(), "\n")
}
