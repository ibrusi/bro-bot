package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSystemFlags(t *testing.T) {
	tests := []struct {
		args      []string
		wantPull  bool
		wantForce bool
	}{
		{args: []string{}, wantPull: false, wantForce: false},
		{args: []string{"pull"}, wantPull: true, wantForce: false},
		{args: []string{"-p"}, wantPull: true, wantForce: false},
		{args: []string{"force"}, wantPull: false, wantForce: true},
		{args: []string{"-f"}, wantPull: false, wantForce: true},
		{args: []string{"pull", "force"}, wantPull: true, wantForce: true},
		{args: []string{"FORCE", "PULL"}, wantPull: true, wantForce: true},
		{args: []string{"unknown", "arg"}, wantPull: false, wantForce: false},
	}

	for _, tt := range tests {
		got := parseSystemFlags(tt.args)
		if got.Pull != tt.wantPull || got.Force != tt.wantForce {
			t.Errorf("parseSystemFlags(%v) = {Pull: %v, Force: %v}, want {Pull: %v, Force: %v}",
				tt.args, got.Pull, got.Force, tt.wantPull, tt.wantForce)
		}
	}
}

func TestRestartMarkerSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()

	marker := RestartMarker{
		ChatID:      123456789,
		MessageID:   42,
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
	badGo := `package main
func main() { syntax error here
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(badGo), 0644); err != nil {
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
	validGo := `package main
import "fmt"
func main() { fmt.Println("hello") }
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(validGo), 0644); err != nil {
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
