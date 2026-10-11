package mcp

import (
	"os"
	"strconv"
	"testing"
)

func TestParseStatPPID(t *testing.T) {
	// Имя процесса со скобками и пробелами не должно сбивать разбор полей.
	got, err := parseStatPPID("4242 (my (odd) name) S 1717 4242 4242 0 -1")
	if err != nil || got != 1717 {
		t.Errorf("parseStatPPID = %d, %v; want 1717", got, err)
	}
	for _, bad := range []string{"", "4242 no-parens S 1", "4242 (name)", "4242 (name) S x"} {
		if _, err := parseStatPPID(bad); err == nil {
			t.Errorf("parseStatPPID(%q) must fail", bad)
		}
	}
}

func TestParentPIDOfSelf(t *testing.T) {
	if _, err := os.Stat("/proc/" + strconv.Itoa(os.Getpid())); err != nil {
		t.Skip("/proc не соответствует PID namespace процесса (например, в песочнице)")
	}
	got, err := ParentPID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if got != os.Getppid() {
		t.Errorf("ParentPID = %d, want %d", got, os.Getppid())
	}
}
