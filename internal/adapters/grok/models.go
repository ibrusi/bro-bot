package grok

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"time"
)

// fallbackModelsList — запасной каталог, если `grok models` недоступен.
const fallbackModelsList = "grok-4.7 Grok 4.7\n" +
	"grok-4.7-build-fast Grok 4.7 Fast\n" +
	"grok-4.6 Grok 4.6\n" +
	"grok-4.5 Grok 4.5\n"

// parseGrokModelsOutput разбирает текст `grok models` в строки «id имя»,
// которые понимает общий реестр моделей.
func parseGrokModelsOutput(raw string) string {
	var b strings.Builder
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "*")
		line = strings.TrimPrefix(line, "-")
		line = strings.TrimPrefix(line, "•")
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(strings.ToLower(line), "grok") {
			continue
		}
		fields := strings.Fields(line)
		id := fields[0]
		if seen[id] {
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(strings.TrimSuffix(line[len(id):], "(default)"))
		name = strings.TrimSpace(name)
		if name == "" {
			name = id
		}
		b.WriteString(id)
		b.WriteByte(' ')
		b.WriteString(name)
		b.WriteByte('\n')
	}
	return b.String()
}

// listModels вызывает `grok models` и возвращает каталог в формате реестра.
func listModels(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, grokBin, "models").CombinedOutput()
	parsed := parseGrokModelsOutput(string(out))
	if parsed == "" {
		if err != nil {
			return []byte(fallbackModelsList), err
		}
		return []byte(fallbackModelsList), nil
	}
	return []byte(parsed), nil
}
