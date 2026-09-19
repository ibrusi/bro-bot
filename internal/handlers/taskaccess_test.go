package handlers

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Страж доступа к состоянию задачи.
//
// Мьютекс TaskSession приватный, поэтому ручную блокировку снаружи домена не пропустит
// компилятор. Но поля задачи экспортированы, и прочитать их без синхронизации технически
// можно — именно это и ловит тест: вне домена состояние задачи доступно только через
// Snapshot и Update.
//
// Переменные задачи находятся без информации о типах — по трём источникам, которые
// покрывают весь фактический код: параметры с типом *domain.TaskSession, присваивания
// из методов менеджера задач и композитные литералы. Разбор идёт по функциям, поэтому
// имя из одной функции не влияет на другую (иначе параметр закрытия `t` спутался бы
// с `t *testing.T`).

// taskFactories — методы менеджера задач, возвращающие задачу первым значением.
var taskFactories = map[string]bool{
	"GetTask": true, "GetActiveTask": true, "CreateTask": true,
	"CreateTaskWithPlan": true, "CreateTaskWithPlanAndAgent": true,
	"ResumeTask": true, "CancelTask": true, "AddFollowup": true,
	"SetActiveTask": true, "GetTaskByMessageID": true,
	"GetNextQueuedTaskForProject": true,
}

// taskSessionFields читает имена экспортированных полей TaskSession прямо из домена,
// чтобы новое поле попадало под охрану само, без правки этого теста.
func taskSessionFields(t *testing.T) map[string]bool {
	t.Helper()

	path := filepath.Join("..", "domain", "tasks.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("разбор %s: %v", path, err)
	}

	fields := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "TaskSession" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, f := range st.Fields.List {
			for _, name := range f.Names {
				if name.IsExported() {
					fields[name.Name] = true
				}
			}
		}
		return false
	})

	if len(fields) == 0 {
		t.Fatal("не удалось прочитать поля TaskSession — тест перестал бы что-либо сторожить")
	}
	return fields
}

// isTaskSessionPtr сообщает, что выражение типа — это *domain.TaskSession или *TaskSession.
func isTaskSessionPtr(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	return isTaskSessionType(star.X)
}

func isTaskSessionType(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		return ok && pkg.Name == "domain" && x.Sel.Name == "TaskSession"
	case *ast.Ident:
		return x.Name == "TaskSession"
	}
	return false
}

// updateScope — тело закрытия, переданного в Update, и имя его параметра-задачи.
type updateScope struct {
	start, end token.Pos
	param      string
}

func (u updateScope) contains(pos token.Pos) bool {
	return pos >= u.start && pos <= u.end
}

// isTaskProducer — выражение, дающее задачу: вызов фабрики менеджера или &TaskSession{…}.
// Получатель обязан быть менеджером задач: у хранилища есть свой GetTask, возвращающий
// запись из базы, и путать их нельзя.
func isTaskProducer(expr ast.Expr, managers map[string]bool) bool {
	switch x := expr.(type) {
	case *ast.UnaryExpr:
		if x.Op != token.AND {
			return false
		}
		lit, ok := x.X.(*ast.CompositeLit)
		return ok && isTaskSessionType(lit.Type)
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		return ok && taskFactories[sel.Sel.Name] && isTaskManager(sel.X, managers)
	}
	return false
}

// isTaskManager распознаёт domain.GlobalTaskManager и переменные менеджера задач.
func isTaskManager(expr ast.Expr, managers map[string]bool) bool {
	switch x := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		return ok && pkg.Name == "domain" && x.Sel.Name == "GlobalTaskManager"
	case *ast.Ident:
		return managers[x.Name]
	}
	return false
}

// taskManagerVars собирает переменные, полученные из конструкторов менеджера задач.
func taskManagerVars(fn *ast.FuncDecl) map[string]bool {
	managers := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range assign.Rhs {
			if i >= len(assign.Lhs) {
				break
			}
			call, ok := rhs.(*ast.CallExpr)
			if !ok {
				continue
			}
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				name = fun.Sel.Name
			case *ast.Ident:
				name = fun.Name
			}
			if name != "NewTaskManager" && name != "NewTaskManagerWithStorage" {
				continue
			}
			if ident, ok := assign.Lhs[i].(*ast.Ident); ok && ident.Name != "_" {
				managers[ident.Name] = true
			}
		}
		return true
	})
	return managers
}

func isTaskListProducer(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == "ListTasks" || sel.Sel.Name == "GetActiveOrQueuedTasks"
}

// scanFunc собирает в пределах одной функции имена переменных задачи и области
// закрытий Update.
func scanFunc(fn *ast.FuncDecl) (taskVars map[string]bool, scopes []updateScope) {
	taskVars = map[string]bool{}
	managers := taskManagerVars(fn)

	if fn.Type.Params != nil {
		for _, f := range fn.Type.Params.List {
			if !isTaskSessionPtr(f.Type) {
				continue
			}
			for _, name := range f.Names {
				taskVars[name.Name] = true
			}
		}
	}

	ast.Inspect(fn, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Update" || len(x.Args) != 1 {
				return true
			}
			lit, ok := x.Args[0].(*ast.FuncLit)
			if !ok || lit.Type.Params == nil {
				return true
			}
			for _, f := range lit.Type.Params.List {
				if !isTaskSessionPtr(f.Type) {
					continue
				}
				for _, name := range f.Names {
					scopes = append(scopes, updateScope{
						start: lit.Body.Pos(), end: lit.Body.End(), param: name.Name,
					})
				}
			}

		case *ast.AssignStmt:
			for i, rhs := range x.Rhs {
				if i >= len(x.Lhs) {
					break
				}
				ident, ok := x.Lhs[i].(*ast.Ident)
				if !ok || ident.Name == "_" {
					continue
				}
				if isTaskProducer(rhs, managers) {
					taskVars[ident.Name] = true
				}
			}

		case *ast.RangeStmt:
			if !isTaskListProducer(x.X) {
				return true
			}
			if ident, ok := x.Value.(*ast.Ident); ok && ident.Name != "_" {
				taskVars[ident.Name] = true
			}
		}
		return true
	})
	return taskVars, scopes
}

type violation struct {
	pos   string
	ident string
	field string
	why   string
}

func TestNoDirectTaskFieldAccessOutsideDomain(t *testing.T) {
	fields := taskSessionFields(t)

	var found []violation
	for _, dir := range []string{".", filepath.Join("..", "system")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("чтение %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			// Сам страж оперирует именами полей как данными.
			if e.Name() == "taskaccess_test.go" {
				continue
			}
			found = append(found, checkFile(t, filepath.Join(dir, e.Name()), fields)...)
		}
	}

	if len(found) == 0 {
		return
	}
	sort.Slice(found, func(i, j int) bool { return found[i].pos < found[j].pos })
	var b strings.Builder
	b.WriteString("состояние задачи вне домена доступно только через Snapshot и Update:\n")
	for _, v := range found {
		b.WriteString(fmt.Sprintf("  %s: %s.%s — %s\n", v.pos, v.ident, v.field, v.why))
	}
	t.Error(b.String())
}

func checkFile(t *testing.T, path string, fields map[string]bool) []violation {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("разбор %s: %v", path, err)
	}

	var found []violation
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		taskVars, scopes := scanFunc(fn)

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}

			// Внутри закрытия Update поля правятся напрямую — в этом его смысл.
			// Зато вызывать методы задачи нельзя: мьютекс уже захвачен.
			for _, sc := range scopes {
				if sc.param != ident.Name || !sc.contains(sel.Pos()) {
					continue
				}
				if !fields[sel.Sel.Name] && !strings.HasSuffix(sel.Sel.Name, "Locked") {
					found = append(found, violation{
						pos:   fset.Position(sel.Pos()).String(),
						ident: ident.Name, field: sel.Sel.Name,
						why: "вызов метода задачи внутри Update — мьютекс уже захвачен",
					})
				}
				return true
			}

			if taskVars[ident.Name] && fields[sel.Sel.Name] {
				found = append(found, violation{
					pos:   fset.Position(sel.Pos()).String(),
					ident: ident.Name, field: sel.Sel.Name,
					why: "прямое обращение к полю задачи без синхронизации",
				})
			}
			return true
		})
	}
	return found
}
