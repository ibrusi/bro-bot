package tools

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const (
	defaultSQLiteMaxRows      = 200
	maxSQLiteMaxRows          = 1000
	defaultSQLiteQueryTimeout = 15 * time.Second
	maxSQLiteBlobInlineBytes  = 256
)

type sqliteQueryResult struct {
	Database        string   `json:"database"`
	Columns         []string `json:"columns"`
	Rows            [][]any  `json:"rows"`
	RowCount        int      `json:"row_count"`
	Truncated       bool     `json:"truncated"`
	TruncatedReason string   `json:"truncated_reason,omitempty"`
}

type sqlTokenKind int

const (
	sqlTokWord sqlTokenKind = iota
	sqlTokIdent
	sqlTokString
	sqlTokPunct
	sqlTokOther
)

type sqlToken struct {
	kind  sqlTokenKind
	raw   string
	upper string
	start int
	end   int
}

var readOnlyArgPragmas = map[string]bool{
	"table_info":       true,
	"table_xinfo":      true,
	"table_list":       true,
	"index_list":       true,
	"index_info":       true,
	"index_xinfo":      true,
	"foreign_key_list": true,
	"integrity_check":  true,
	"quick_check":      true,
}

var readOnlyNoArgPragmas = map[string]bool{
	"database_list":   true,
	"collation_list":  true,
	"compile_options": true,
	"function_list":   true,
	"module_list":     true,
	"pragma_list":     true,
	"schema_version":  true,
	"user_version":    true,
	"application_id":  true,
	"encoding":        true,
	"page_size":       true,
	"page_count":      true,
	"freelist_count":  true,
	"journal_mode":    true,
	"stats":           true,
	"busy_timeout":    true,
	"query_only":      true,
	"synchronous":     true,
	"cache_size":      true,
	"temp_store":      true,
}

// ValidateReadOnlySQL проверяет, что строка SQL содержит ровно один безопасный read-only запрос
// (SELECT, WITH ... SELECT/VALUES, EXPLAIN [QUERY PLAN] SELECT/WITH/VALUES, VALUES или read-only PRAGMA),
// и возвращает очищенный SQL без хвостовой точки с запятой.
func ValidateReadOnlySQL(query string) (string, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "", errors.New("SQL query must not be empty")
	}

	tokens, err := scanSQLTokens(trimmed)
	if err != nil {
		return "", err
	}
	if len(tokens) == 0 {
		return "", errors.New("SQL query must not be empty")
	}

	endByte := tokens[len(tokens)-1].end
	for idx, tok := range tokens {
		if tok.kind == sqlTokPunct && tok.raw == ";" {
			if idx != len(tokens)-1 {
				return "", errors.New("only a single read-only SQL statement is allowed")
			}
			endByte = tok.start
			tokens = tokens[:idx]
			break
		}
	}
	if len(tokens) == 0 {
		return "", errors.New("SQL query must not be empty")
	}

	for _, tok := range tokens {
		if tok.kind == sqlTokWord && (tok.upper == "ATTACH" || tok.upper == "DETACH") {
			return "", errors.New("ATTACH/DETACH statements are not allowed in read-only mode")
		}
	}

	first := tokens[0]
	if first.kind != sqlTokWord {
		return "", errors.New("SQL query must start with SELECT, WITH, EXPLAIN, VALUES, or read-only PRAGMA")
	}

	switch first.upper {
	case "SELECT", "VALUES":
		// Внутри подзапросов/выражений проверяем вложенные CTE с некорректным глаголом не требуется,
		// так как в SQLite DML не может быть частью SELECT/VALUES, а PRAGMA запрещены.
	case "WITH":
		if err := validateWithReadOnlyQuery(tokens); err != nil {
			return "", err
		}
	case "EXPLAIN":
		if err := validateExplainReadOnlyQuery(tokens); err != nil {
			return "", err
		}
	case "PRAGMA":
		if err := validateReadOnlyPragma(tokens); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("SQL statement %q is not permitted in read-only mode", first.upper)
	}

	cleaned := strings.TrimSpace(trimmed[tokens[0].start:endByte])
	if cleaned == "" {
		return "", errors.New("SQL query must not be empty")
	}
	return cleaned, nil
}

func validateWithReadOnlyQuery(tokens []sqlToken) error {
	if len(tokens) < 2 || tokens[0].kind != sqlTokWord || tokens[0].upper != "WITH" {
		return errors.New("invalid WITH query")
	}
	idx := 1
	if idx < len(tokens) && tokens[idx].kind == sqlTokWord && tokens[idx].upper == "RECURSIVE" {
		idx++
	}
	for {
		if idx >= len(tokens) || (tokens[idx].kind != sqlTokWord && tokens[idx].kind != sqlTokIdent) {
			return errors.New("invalid CTE name in WITH query")
		}
		idx++
		if idx < len(tokens) && tokens[idx].kind == sqlTokPunct && tokens[idx].raw == "(" {
			next, ok := skipSQLBalancedParens(tokens, idx)
			if !ok {
				return errors.New("unbalanced parentheses in WITH query")
			}
			idx = next
		}
		if idx >= len(tokens) || tokens[idx].kind != sqlTokWord || tokens[idx].upper != "AS" {
			return errors.New("expected AS in WITH query")
		}
		idx++
		if idx < len(tokens) && tokens[idx].kind == sqlTokWord && tokens[idx].upper == "NOT" {
			idx++
		}
		if idx < len(tokens) && tokens[idx].kind == sqlTokWord && tokens[idx].upper == "MATERIALIZED" {
			idx++
		}
		if idx >= len(tokens) || tokens[idx].kind != sqlTokPunct || tokens[idx].raw != "(" {
			return errors.New("expected CTE subquery in WITH query")
		}
		bodyStart := idx + 1
		next, ok := skipSQLBalancedParens(tokens, idx)
		if !ok {
			return errors.New("unbalanced parentheses in WITH query")
		}
		bodyTokens := tokens[bodyStart : next-1]
		if len(bodyTokens) == 0 {
			return errors.New("empty CTE subquery in WITH query")
		}
		if err := validateCTESubquery(bodyTokens); err != nil {
			return err
		}
		idx = next
		if idx < len(tokens) && tokens[idx].kind == sqlTokPunct && tokens[idx].raw == "," {
			idx++
			continue
		}
		break
	}
	if idx >= len(tokens) || tokens[idx].kind != sqlTokWord || (tokens[idx].upper != "SELECT" && tokens[idx].upper != "VALUES") {
		return errors.New("WITH query must end with SELECT or VALUES")
	}
	return nil
}

func validateCTESubquery(tokens []sqlToken) error {
	i := 0
	for i < len(tokens) && tokens[i].kind == sqlTokPunct && tokens[i].raw == "(" {
		i++
	}
	if i >= len(tokens) || tokens[i].kind != sqlTokWord {
		return errors.New("CTE subquery must be SELECT, VALUES, or WITH")
	}
	switch tokens[i].upper {
	case "SELECT", "VALUES":
		return nil
	case "WITH":
		return validateWithReadOnlyQuery(tokens[i:])
	default:
		return fmt.Errorf("CTE subquery statement %q is not permitted in read-only mode", tokens[i].upper)
	}
}

func validateExplainReadOnlyQuery(tokens []sqlToken) error {
	if len(tokens) < 2 {
		return errors.New("EXPLAIN requires a read-only statement")
	}
	idx := 1
	if idx+1 < len(tokens) &&
		tokens[idx].kind == sqlTokWord && tokens[idx].upper == "QUERY" &&
		tokens[idx+1].kind == sqlTokWord && tokens[idx+1].upper == "PLAN" {
		idx += 2
	}
	if idx >= len(tokens) || tokens[idx].kind != sqlTokWord {
		return errors.New("EXPLAIN requires SELECT, WITH, or VALUES")
	}
	switch tokens[idx].upper {
	case "SELECT", "VALUES":
		return nil
	case "WITH":
		return validateWithReadOnlyQuery(tokens[idx:])
	default:
		return fmt.Errorf("EXPLAIN %s is not permitted in read-only mode", tokens[idx].upper)
	}
}

func validateReadOnlyPragma(tokens []sqlToken) error {
	for _, tok := range tokens {
		if tok.kind == sqlTokPunct && tok.raw == "=" {
			return errors.New("PRAGMA assignment is not permitted in read-only mode")
		}
	}
	if len(tokens) < 2 {
		return errors.New("PRAGMA requires a pragma name")
	}
	idx := 1
	if len(tokens) >= 4 && tokens[2].kind == sqlTokPunct && tokens[2].raw == "." {
		if tokens[1].kind != sqlTokWord && tokens[1].kind != sqlTokIdent {
			return errors.New("invalid schema name in PRAGMA")
		}
		idx = 3
	}
	if idx >= len(tokens) || (tokens[idx].kind != sqlTokWord && tokens[idx].kind != sqlTokIdent) {
		return errors.New("invalid PRAGMA name")
	}
	name := strings.ToLower(unquoteSQLIdent(tokens[idx]))
	rest := tokens[idx+1:]

	if readOnlyNoArgPragmas[name] {
		if len(rest) > 0 {
			return fmt.Errorf("PRAGMA %s with arguments is not permitted in read-only mode", name)
		}
		return nil
	}
	if readOnlyArgPragmas[name] {
		if len(rest) == 0 {
			return nil
		}
		if rest[0].kind != sqlTokPunct || rest[0].raw != "(" {
			return fmt.Errorf("invalid arguments for PRAGMA %s", name)
		}
		next, ok := skipSQLBalancedParens(rest, 0)
		if !ok || next != len(rest) {
			return fmt.Errorf("invalid arguments for PRAGMA %s", name)
		}
		return nil
	}
	return fmt.Errorf("PRAGMA %q is not permitted in read-only mode", name)
}

func unquoteSQLIdent(tok sqlToken) string {
	s := tok.raw
	if tok.kind != sqlTokIdent || len(s) < 2 {
		return s
	}
	switch s[0] {
	case '"':
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	case '`':
		return strings.ReplaceAll(s[1:len(s)-1], "``", "`")
	case '[':
		return s[1 : len(s)-1]
	default:
		return s
	}
}

func skipSQLBalancedParens(tokens []sqlToken, openIdx int) (int, bool) {
	if openIdx < 0 || openIdx >= len(tokens) || tokens[openIdx].kind != sqlTokPunct || tokens[openIdx].raw != "(" {
		return 0, false
	}
	depth := 0
	for i := openIdx; i < len(tokens); i++ {
		if tokens[i].kind == sqlTokPunct {
			if tokens[i].raw == "(" {
				depth++
			} else if tokens[i].raw == ")" {
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
		}
	}
	return 0, false
}

func scanSQLTokens(s string) ([]sqlToken, error) {
	var tokens []sqlToken
	n := len(s)
	i := 0
	for i < n {
		ch := s[i]
		if unicode.IsSpace(rune(ch)) {
			i++
			continue
		}
		if ch == '-' && i+1 < n && s[i+1] == '-' {
			i += 2
			for i < n && s[i] != '\n' {
				i++
			}
			continue
		}
		if ch == '/' && i+1 < n && s[i+1] == '*' {
			end := strings.Index(s[i+2:], "*/")
			if end == -1 {
				return nil, errors.New("unterminated block comment in SQL query")
			}
			i = i + 2 + end + 2
			continue
		}
		if (ch == 'x' || ch == 'X') && i+1 < n && s[i+1] == '\'' {
			next, ok := scanSQLQuoted(s, i+1, '\'')
			if !ok {
				return nil, errors.New("unterminated blob literal in SQL query")
			}
			tokens = append(tokens, sqlToken{kind: sqlTokString, raw: s[i:next], start: i, end: next})
			i = next
			continue
		}
		if ch == '\'' {
			next, ok := scanSQLQuoted(s, i, '\'')
			if !ok {
				return nil, errors.New("unterminated string literal in SQL query")
			}
			tokens = append(tokens, sqlToken{kind: sqlTokString, raw: s[i:next], start: i, end: next})
			i = next
			continue
		}
		if ch == '"' || ch == '`' {
			next, ok := scanSQLQuoted(s, i, ch)
			if !ok {
				return nil, errors.New("unterminated quoted identifier in SQL query")
			}
			tokens = append(tokens, sqlToken{kind: sqlTokIdent, raw: s[i:next], start: i, end: next})
			i = next
			continue
		}
		if ch == '[' {
			end := strings.IndexByte(s[i+1:], ']')
			if end == -1 {
				return nil, errors.New("unterminated bracket identifier in SQL query")
			}
			next := i + 1 + end + 1
			tokens = append(tokens, sqlToken{kind: sqlTokIdent, raw: s[i:next], start: i, end: next})
			i = next
			continue
		}
		if ch == '?' || ch == ':' || ch == '@' || ch == '$' {
			start := i
			i++
			for i < n && isSQLWordCont(s[i]) {
				i++
			}
			tokens = append(tokens, sqlToken{kind: sqlTokOther, raw: s[start:i], start: start, end: i})
			continue
		}
		if isSQLDigit(ch) || (ch == '.' && i+1 < n && isSQLDigit(s[i+1])) {
			start := i
			i++
			for i < n && (isSQLDigit(s[i]) || isSQLWordCont(s[i]) || s[i] == '.') {
				if (s[i] == 'e' || s[i] == 'E') && i+1 < n && (s[i+1] == '+' || s[i+1] == '-') {
					i += 2
					continue
				}
				i++
			}
			tokens = append(tokens, sqlToken{kind: sqlTokOther, raw: s[start:i], start: start, end: i})
			continue
		}
		if isSQLWordStart(ch) {
			start := i
			i++
			for i < n && isSQLWordCont(s[i]) {
				i++
			}
			raw := s[start:i]
			tokens = append(tokens, sqlToken{
				kind:  sqlTokWord,
				raw:   raw,
				upper: strings.ToUpper(raw),
				start: start,
				end:   i,
			})
			continue
		}
		tokens = append(tokens, sqlToken{kind: sqlTokPunct, raw: s[i : i+1], start: i, end: i + 1})
		i++
	}
	return tokens, nil
}

func scanSQLQuoted(s string, start int, quote byte) (int, bool) {
	n := len(s)
	i := start + 1
	for i < n {
		if s[i] == quote {
			if i+1 < n && s[i+1] == quote {
				i += 2
				continue
			}
			return i + 1, true
		}
		i++
	}
	return n, false
}

func isSQLDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isSQLWordStart(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '_' || b >= 0x80
}

func isSQLWordCont(b byte) bool {
	return isSQLWordStart(b) || isSQLDigit(b) || b == '$'
}

// QuerySQLite выполняет один read-only SQL-запрос к базе SQLite внутри WorkDir.
// База открывается с mode=ro, busy_timeout(3000) и PRAGMA query_only=ON (без immutable=1,
// чтобы корректно видеть свежие данные в живых WAL-базах).
func (e *Executor) QuerySQLite(ctx context.Context, dbPath, query string, maxRows int) (string, error) {
	if strings.TrimSpace(dbPath) == "" {
		return "", errors.New("missing required argument 'path'")
	}
	cleanedSQL, err := ValidateReadOnlySQL(query)
	if err != nil {
		return "", err
	}

	resolved, err := e.SafeResolvePath(dbPath)
	if err != nil {
		return "", err
	}
	absWorkDir, err := filepath.Abs(e.WorkDir)
	if err != nil {
		return "", fmt.Errorf("invalid workspace directory %q: %w", e.WorkDir, err)
	}
	realWorkDir, err := filepath.EvalSymlinks(absWorkDir)
	if err != nil {
		realWorkDir = absWorkDir
	}
	realDB, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrFileNotFound, dbPath)
		}
		return "", err
	}
	relReal, err := filepath.Rel(realWorkDir, realDB)
	if err != nil || relReal == ".." || strings.HasPrefix(relReal, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q escapes workspace via symlink", ErrPathEscapesWorkDir, dbPath)
	}

	info, err := os.Stat(realDB)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrFileNotFound, dbPath)
		}
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory: %s", dbPath)
	}

	if maxRows <= 0 {
		maxRows = defaultSQLiteMaxRows
	} else if maxRows > maxSQLiteMaxRows {
		maxRows = maxSQLiteMaxRows
	}

	dsn := "file:" + escapeSQLiteURIPath(realDB) + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", fmt.Errorf("open sqlite database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	qCtx, cancel := context.WithTimeout(ctx, defaultSQLiteQueryTimeout)
	defer cancel()

	conn, err := db.Conn(qCtx)
	if err != nil {
		return "", fmt.Errorf("connect sqlite database: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(qCtx, "PRAGMA query_only = ON"); err != nil {
		return "", fmt.Errorf("enforce query_only pragma: %w", err)
	}

	rows, err := conn.QueryContext(qCtx, cleanedSQL)
	if err != nil {
		return "", fmt.Errorf("execute SQLite query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", fmt.Errorf("read result columns: %w", err)
	}
	if cols == nil {
		cols = []string{}
	}

	resultRows := make([][]any, 0)
	var truncated bool
	var truncatedReason string
	approxBytes := 256 + len(dbPath)*2
	for _, c := range cols {
		approxBytes += len(c) + 8
	}
	const maxResultJSONBytes = maxOutputBytes - 1024

	scanDest := make([]any, len(cols))
	scanPtrs := make([]any, len(cols))
	for i := range scanDest {
		scanPtrs[i] = &scanDest[i]
	}

	for rows.Next() {
		if len(resultRows) >= maxRows {
			truncated = true
			truncatedReason = "max_rows"
			break
		}
		if err := rows.Scan(scanPtrs...); err != nil {
			return "", fmt.Errorf("scan row: %w", err)
		}
		rowVals := make([]any, len(cols))
		for i, raw := range scanDest {
			rowVals[i] = normalizeSQLiteCellValue(raw)
		}
		rowBytes, err := json.Marshal(rowVals)
		if err != nil {
			return "", fmt.Errorf("encode row: %w", err)
		}
		if len(resultRows) > 0 && approxBytes+len(rowBytes)+16 > maxResultJSONBytes {
			truncated = true
			truncatedReason = "max_output_bytes"
			break
		}
		if len(resultRows) == 0 && approxBytes+len(rowBytes)+16 > maxResultJSONBytes {
			rowVals = truncateOversizedRow(rowVals, maxResultJSONBytes-approxBytes-16)
			resultRows = append(resultRows, rowVals)
			truncated = true
			truncatedReason = "max_output_bytes"
			break
		}
		approxBytes += len(rowBytes) + 16
		resultRows = append(resultRows, rowVals)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate rows: %w", err)
	}

	res := sqliteQueryResult{
		Database:        filepath.ToSlash(relReal),
		Columns:         cols,
		Rows:            resultRows,
		RowCount:        len(resultRows),
		Truncated:       truncated,
		TruncatedReason: truncatedReason,
	}
	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

func escapeSQLiteURIPath(p string) string {
	slashed := filepath.ToSlash(p)
	slashed = strings.ReplaceAll(slashed, "%", "%25")
	slashed = strings.ReplaceAll(slashed, "?", "%3F")
	slashed = strings.ReplaceAll(slashed, "#", "%23")
	return slashed
}

func normalizeSQLiteCellValue(v any) any {
	switch val := v.(type) {
	case nil:
		return nil
	case []byte:
		if len(val) <= maxSQLiteBlobInlineBytes && utf8.Valid(val) && bytes.IndexByte(val, 0) == -1 {
			return string(val)
		}
		return fmt.Sprintf("<blob %d bytes>", len(val))
	case time.Time:
		return val.Format(time.RFC3339Nano)
	default:
		return val
	}
}

func truncateOversizedRow(row []any, budget int) []any {
	if len(row) == 0 {
		return row
	}
	perColBudget := budget / len(row)
	if perColBudget < 128 {
		perColBudget = 128
	}
	out := make([]any, len(row))
	for i, cell := range row {
		s, ok := cell.(string)
		if !ok || len(s) <= perColBudget {
			out[i] = cell
			continue
		}
		cut := perColBudget
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		out[i] = s[:cut] + "... [truncated]"
	}
	return out
}
