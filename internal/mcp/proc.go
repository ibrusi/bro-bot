package mcp

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ParentPID возвращает PID родителя процесса pid из /proc/<pid>/stat.
func ParentPID(pid int) (int, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	return parseStatPPID(string(data))
}

// parseStatPPID достаёт PPID из строки /proc/<pid>/stat. Имя процесса в скобках
// может содержать пробелы и скобки, поэтому поля считаются от последней ")":
// за ней идут состояние и PPID.
func parseStatPPID(stat string) (int, error) {
	idx := strings.LastIndexByte(stat, ')')
	if idx < 0 {
		return 0, fmt.Errorf("mcp: malformed /proc stat: %q", stat)
	}
	fields := strings.Fields(stat[idx+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("mcp: malformed /proc stat: %q", stat)
	}
	return strconv.Atoi(fields[1])
}
