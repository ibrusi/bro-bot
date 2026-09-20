package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectAgentsRules(t *testing.T) {
	tempDir := t.TempDir()
	projRoot := filepath.Join(tempDir, "projects")
	workDir := filepath.Join(projRoot, "my-app")

	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}

	// 1. Initially no rules
	if rules := LoadProjectAgentsRules(workDir, projRoot); rules != "" {
		t.Fatalf("expected empty rules, got: %q", rules)
	}
	if HasLocalAgentsRules(workDir) {
		t.Fatalf("expected HasLocalAgentsRules to be false")
	}

	// 2. Global rule in projRoot
	globalRule := "# Global Project Rules\nFollow DDD and clean code."
	if err := os.WriteFile(filepath.Join(projRoot, "AGENTS.md"), []byte(globalRule), 0644); err != nil {
		t.Fatalf("write global rule: %v", err)
	}

	if got := LoadProjectAgentsRules(workDir, projRoot); got != globalRule {
		t.Fatalf("expected global rule, got: %q", got)
	}
	if HasLocalAgentsRules(workDir) {
		t.Fatalf("expected HasLocalAgentsRules to still be false for local dir")
	}

	// 3. Local rule in workDir overrides global rule
	localRule := "# Local Project Rules\nLocal senior developer instructions."
	if err := os.WriteFile(filepath.Join(workDir, "AGENTS.md"), []byte(localRule), 0644); err != nil {
		t.Fatalf("write local rule: %v", err)
	}

	if got := LoadProjectAgentsRules(workDir, projRoot); got != localRule {
		t.Fatalf("expected local rule override, got: %q", got)
	}
	if !HasLocalAgentsRules(workDir) {
		t.Fatalf("expected HasLocalAgentsRules to be true")
	}

	// 4. Large file truncation at 32 KB
	largeRule := strings.Repeat("A", 40*1024)
	if err := os.WriteFile(filepath.Join(workDir, "AGENTS.md"), []byte(largeRule), 0644); err != nil {
		t.Fatalf("write large rule: %v", err)
	}

	gotLarge := LoadProjectAgentsRules(workDir, projRoot)
	if len(gotLarge) != maxAgentsRulesBytes {
		t.Fatalf("expected truncated rule length %d, got %d", maxAgentsRulesBytes, len(gotLarge))
	}

	// 5. Legacy AGENT.md support
	_ = os.Remove(filepath.Join(workDir, "AGENTS.md"))
	legacyRule := "# Legacy AGENT.md Rules"
	if err := os.WriteFile(filepath.Join(workDir, "AGENT.md"), []byte(legacyRule), 0644); err != nil {
		t.Fatalf("write legacy rule: %v", err)
	}
	if got := LoadProjectAgentsRules(workDir, projRoot); got != legacyRule {
		t.Fatalf("expected legacy rule, got: %q", got)
	}
	if !HasLocalAgentsRules(workDir) {
		t.Fatalf("expected HasLocalAgentsRules to recognize AGENT.md")
	}
}
