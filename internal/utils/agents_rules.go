package utils

import (
	"os"
	"path/filepath"
	"strings"
)

const maxAgentsRulesBytes = 32 * 1024 // 32 KB limit

// LoadProjectAgentsRules возвращает содержимое AGENTS.md по цепочке приоритетов:
// 1. workDir/AGENTS.md
// 2. projectsRoot/AGENTS.md
func LoadProjectAgentsRules(workDir, projectsRoot string) string {
	candidates := make([]string, 0, 2)
	if workDir != "" {
		candidates = append(candidates,
			filepath.Join(workDir, "AGENTS.md"),
		)
	}
	if projectsRoot != "" {
		candidates = append(candidates,
			filepath.Join(projectsRoot, "AGENTS.md"),
		)
	}

	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			content := strings.TrimSpace(string(data))
			if content != "" {
				if len(content) > maxAgentsRulesBytes {
					content = content[:maxAgentsRulesBytes]
				}
				return content
			}
		}
	}
	return ""
}

// HasLocalAgentsRules проверяет наличие файла правил непосредственно в каталоге проекта.
func HasLocalAgentsRules(workDir string) bool {
	if workDir == "" {
		return false
	}
	for _, name := range []string{"AGENTS.md", "GEMINI.md"} {
		if _, err := os.Stat(filepath.Join(workDir, name)); err == nil {
			return true
		}
	}
	return false
}
