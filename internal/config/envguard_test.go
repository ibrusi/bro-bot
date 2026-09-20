package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Страж чтения окружения.
//
// Конфигурацию бота читает config.Load — одна функция, один раз, на старте. Пока это
// правило держится, значение по умолчанию у переменной ровно одно и расходиться ему
// негде. Ровно так его когда-то и нарушили: BOT_DIR читался в двух местах с разными
// запасными значениями, и /rebuild работал не с тем каталогом, что лежал в конфигурации.
//
// Компилятор такое не ловит: os.Getenv доступен откуда угодно. Ловит этот тест.

// allowedEnvReaders — места, где чтение окружения осознанно оставлено, и причина.
var allowedEnvReaders = map[string]string{
	"internal/config/load.go": "единственное место, где читается конфигурация бота",

	// Ключи и переопределения моделей читаются лениво намеренно: /mode api должен
	// подхватывать ключ, дописанный в .env, без перезапуска бота.
	"internal/agents/registry.go":                "ключ агента проверяется в момент сборки адаптера",
	"internal/adapters/agy/api.go":               "ключ Gemini читается на каждый запрос",
	"internal/adapters/agy/gemini_models.go":     "GEMINI_API_MODEL переопределяет модель на лету",
	"internal/adapters/claude/api.go":            "ключ Claude читается на каждый запрос",
	"internal/adapters/claude/models.go":         "CLAUDE_API_MODEL переопределяет модель на лету",
	"internal/adapters/claude/adapter.go":        "ключ нужен, чтобы решить, спрашивать ли живой список моделей",
	"internal/adapters/claude/spec.go":           "список переменных с ключом для реестра",
	"internal/adapters/agy/spec.go":              "список переменных с ключом для реестра",
	"internal/handlers/clone.go":                 "HOME для поиска публичного ключа деплоя",
	"internal/adapters/transcript/transcript.go": "HOME через os.UserHomeDir",
	"internal/storage/sqlite.go":                 "HOME через os.UserHomeDir",
	"internal/mcp/config.go":                     "HOME через os.UserHomeDir для mcp_config.json",
}

type envRead struct {
	pos  string
	call string
}

func TestEnvIsReadOnlyInAllowedPlaces(t *testing.T) {
	root := filepath.Join("..", "..")

	var found []envRead
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if _, ok := allowedEnvReaders[rel]; ok {
			return nil
		}

		found = append(found, scanEnvReads(t, path, rel)...)
		return nil
	})
	if err != nil {
		t.Fatalf("обход репозитория: %v", err)
	}

	if len(found) == 0 {
		return
	}
	sort.Slice(found, func(i, j int) bool { return found[i].pos < found[j].pos })
	var b strings.Builder
	b.WriteString("окружение читается мимо config.Load:\n")
	for _, r := range found {
		b.WriteString(fmt.Sprintf("  %s: %s — добавьте параметр в config.Config либо впишите файл в allowedEnvReaders с причиной\n", r.pos, r.call))
	}
	t.Error(b.String())
}

func scanEnvReads(t *testing.T, path, rel string) []envRead {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("разбор %s: %v", path, err)
	}

	var found []envRead
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "os" {
			return true
		}
		switch sel.Sel.Name {
		case "Getenv", "LookupEnv", "UserHomeDir":
		default:
			return true
		}

		position := fset.Position(call.Pos())
		found = append(found, envRead{
			pos:  fmt.Sprintf("%s:%d", rel, position.Line),
			call: "os." + sel.Sel.Name,
		})
		return true
	})
	return found
}
