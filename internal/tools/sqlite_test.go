package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateReadOnlySQL(t *testing.T) {
	allowed := []struct {
		in   string
		want string
	}{
		{"SELECT 1", "SELECT 1"},
		{"  SELECT id, created_at, updated_at, deleted_at FROM trades; ", "SELECT id, created_at, updated_at, deleted_at FROM trades"},
		{"/* header */ -- line\nSELECT replace(symbol, '/', '-') FROM pairs;", "SELECT replace(symbol, '/', '-') FROM pairs"},
		{"SELECT 'DELETE FROM trades; DROP TABLE x' AS literal", "SELECT 'DELETE FROM trades; DROP TABLE x' AS literal"},
		{"WITH recent AS (SELECT * FROM trades LIMIT 5) SELECT count(*) FROM recent", "WITH recent AS (SELECT * FROM trades LIMIT 5) SELECT count(*) FROM recent"},
		{"WITH RECURSIVE cnt(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM cnt WHERE x<5) SELECT x FROM cnt;", "WITH RECURSIVE cnt(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM cnt WHERE x<5) SELECT x FROM cnt"},
		{"EXPLAIN QUERY PLAN SELECT * FROM trades", "EXPLAIN QUERY PLAN SELECT * FROM trades"},
		{"EXPLAIN WITH c AS (SELECT 1) SELECT * FROM c", "EXPLAIN WITH c AS (SELECT 1) SELECT * FROM c"},
		{"VALUES (1, 2), (3, 4)", "VALUES (1, 2), (3, 4)"},
		{"PRAGMA table_info('trades')", "PRAGMA table_info('trades')"},
		{"PRAGMA main.table_list", "PRAGMA main.table_list"},
		{"PRAGMA journal_mode", "PRAGMA journal_mode"},
		{"PRAGMA integrity_check('trades')", "PRAGMA integrity_check('trades')"},
		{"SELECT \"delete\" FROM \"insert\"", "SELECT \"delete\" FROM \"insert\""},
	}

	for _, tc := range allowed {
		got, err := ValidateReadOnlySQL(tc.in)
		if err != nil {
			t.Errorf("ValidateReadOnlySQL(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ValidateReadOnlySQL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	rejected := []string{
		"",
		"   ",
		"-- only comment",
		"/* only block comment */ ;",
		"INSERT INTO trades VALUES (1)",
		"UPDATE trades SET pnl = 0",
		"DELETE FROM trades",
		"REPLACE INTO trades VALUES (1)",
		"DROP TABLE trades",
		"CREATE TABLE t (x INT)",
		"ALTER TABLE trades ADD COLUMN y TEXT",
		"VACUUM",
		"REINDEX",
		"ATTACH DATABASE '/tmp/x.db' AS ext",
		"DETACH DATABASE ext",
		"BEGIN TRANSACTION",
		"COMMIT",
		"SELECT 1; DROP TABLE trades",
		"SELECT 1; SELECT 2",
		"WITH c AS (SELECT 1) INSERT INTO t SELECT * FROM c",
		"WITH c AS (SELECT 1) DELETE FROM t",
		"WITH c AS (SELECT 1) UPDATE t SET x = 1",
		"WITH c AS (SELECT 1) REPLACE INTO t VALUES (1)",
		"EXPLAIN DELETE FROM trades",
		"EXPLAIN QUERY PLAN INSERT INTO t VALUES (1)",
		"PRAGMA query_only = OFF",
		"PRAGMA journal_mode = DELETE",
		"PRAGMA journal_mode(DELETE)",
		"PRAGMA query_only(0)",
		"PRAGMA user_version = 1",
		"PRAGMA wal_checkpoint(TRUNCATE)",
		"PRAGMA unknown_pragma",
		"SELECT 'unterminated",
		"SELECT /* unterminated",
		"SELECT \"unterminated",
		"SELECT [unterminated",
	}

	for _, q := range rejected {
		if got, err := ValidateReadOnlySQL(q); err == nil {
			t.Errorf("ValidateReadOnlySQL(%q) unexpectedly succeeded with %q", q, got)
		}
	}
}

func TestQuerySQLiteLiveWALReadOnlyAndTruncation(t *testing.T) {
	workDir := t.TempDir()
	dbDir := filepath.Join(workDir, "data", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "live.db")

	writer, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	if _, err := writer.Exec(`CREATE TABLE candles (id INTEGER PRIMARY KEY, symbol TEXT, price REAL, raw BLOB, note TEXT);`); err != nil {
		t.Fatal(err)
	}
	// Вставляем строки без принудительного checkpoint, чтобы проверить чтение свежих кадров из -wal.
	for i := 1; i <= 5; i++ {
		if _, err := writer.Exec(`INSERT INTO candles (id, symbol, price, raw, note) VALUES (?, ?, ?, ?, ?)`,
			i, "SBER", float64(i)*100.5, []byte{0x00, 0x01, 0xff}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.Exec(`INSERT INTO candles (id, symbol, price, raw, note) VALUES (6, 'GAZP', 150.25, ?, ?)`,
		[]byte("short-utf8"), strings.Repeat("A", 500)); err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(workDir)
	ctx := context.Background()

	// 1. Чтение живой WAL-БД и усечение по max_rows.
	out, err := exec.QuerySQLite(ctx, "data/db/live.db", "SELECT id, symbol, price, raw, note FROM candles ORDER BY id", 3)
	if err != nil {
		t.Fatalf("QuerySQLite failed: %v", err)
	}
	var res sqliteQueryResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal result: %v\n%s", err, out)
	}
	if res.Database != "data/db/live.db" || res.RowCount != 3 || !res.Truncated || res.TruncatedReason != "max_rows" {
		t.Fatalf("unexpected query result metadata: %+v", res)
	}
	if len(res.Columns) != 5 || res.Rows[0][1] != "SBER" || res.Rows[0][3] != "<blob 3 bytes>" || res.Rows[0][4] != nil {
		t.Fatalf("unexpected row values: %+v", res.Rows)
	}

	// 2. Все 6 строк видны, включая короткий UTF-8 blob и длинный текст.
	outAll, err := exec.QuerySQLite(ctx, "data/db/live.db", "SELECT id, raw, length(note) FROM candles WHERE id = 6", 10)
	if err != nil {
		t.Fatalf("QuerySQLite row 6 failed: %v", err)
	}
	if !strings.Contains(outAll, "short-utf8") || !strings.Contains(outAll, "500") {
		t.Errorf("expected row 6 WAL data, got:\n%s", outAll)
	}

	// 3. Ограничение вывода maxOutputBytes на больших текстовых ячейках.
	for i := 10; i < 40; i++ {
		if _, err := writer.Exec(`INSERT INTO candles (id, symbol, price, raw, note) VALUES (?, 'BIG', 1.0, X'01', ?)`,
			i, strings.Repeat("Z", 4000)); err != nil {
			t.Fatal(err)
		}
	}
	bigOut, err := exec.QuerySQLite(ctx, "data/db/live.db", "SELECT id, note FROM candles WHERE id >= 10 ORDER BY id", 100)
	if err != nil {
		t.Fatalf("QuerySQLite big rows failed: %v", err)
	}
	var bigRes sqliteQueryResult
	if err := json.Unmarshal([]byte(bigOut), &bigRes); err != nil {
		t.Fatalf("unmarshal big result: %v", err)
	}
	if !bigRes.Truncated || bigRes.TruncatedReason != "max_output_bytes" || len(bigOut) > maxOutputBytes {
		t.Errorf("expected max_output_bytes truncation under %d bytes, got truncated=%v reason=%q len=%d",
			maxOutputBytes, bigRes.Truncated, bigRes.TruncatedReason, len(bigOut))
	}

	// 4. Read-only PRAGMA и пустой результат.
	emptyOut, err := exec.QuerySQLite(ctx, "data/db/live.db", "SELECT id FROM candles WHERE id = 99999", 10)
	if err != nil {
		t.Fatalf("QuerySQLite empty failed: %v", err)
	}
	if !strings.Contains(emptyOut, `"row_count": 0`) || !strings.Contains(emptyOut, `"rows": []`) {
		t.Errorf("expected empty array JSON, got:\n%s", emptyOut)
	}
	if pragmaOut, err := exec.QuerySQLite(ctx, "data/db/live.db", "PRAGMA table_info('candles')", 50); err != nil || !strings.Contains(pragmaOut, "symbol") {
		t.Errorf("PRAGMA table_info failed: err=%v out=%s", err, pragmaOut)
	}

	// 5. Мутации запрещены.
	if _, err := exec.QuerySQLite(ctx, "data/db/live.db", "INSERT INTO candles (id, symbol) VALUES (999, 'NO')", 10); err == nil {
		t.Error("expected INSERT to be rejected")
	}

	// 6. Проверка path traversal, symlink escape, отсутствующего файла и директории.
	outsideDir := t.TempDir()
	outsideDB := filepath.Join(outsideDir, "secret.db")
	if err := os.WriteFile(outsideDB, []byte("not-allowed"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outsideDB, filepath.Join(dbDir, "escape.db"))

	for _, bad := range []string{"../outside.db", "data/db/escape.db", "data/db/missing.db", "data/db", ""} {
		if _, err := exec.QuerySQLite(ctx, bad, "SELECT 1", 10); err == nil {
			t.Errorf("expected error for invalid db path %q", bad)
		}
	}

	// 7. Проверка файла базы с правами 0444 (только чтение).
	roDBPath := filepath.Join(dbDir, "readonly.db")
	roWriter, err := sql.Open("sqlite", "file:"+roDBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roWriter.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('ok');`); err != nil {
		t.Fatal(err)
	}
	_ = roWriter.Close()
	if err := os.Chmod(roDBPath, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDBPath, 0o644) })

	roOut, err := exec.QuerySQLite(ctx, "data/db/readonly.db", "SELECT v FROM t", 10)
	if err != nil || !strings.Contains(roOut, "ok") {
		t.Errorf("expected QuerySQLite to read 0444 DB, got err=%v out=%s", err, roOut)
	}

	// 8. Проверка WAL-БД с правами 0444 (когда открытый writer ещё держит -wal/-shm).
	walROPath := filepath.Join(dbDir, "wal_ro.db")
	walWriter, err := sql.Open("sqlite", "file:"+walROPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer walWriter.Close()
	if _, err := walWriter.Exec(`CREATE TABLE items (n INTEGER); INSERT INTO items VALUES (42);`); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(walROPath, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(walROPath, 0o644) })
	_ = os.Chmod(walROPath+"-wal", 0o444)
	t.Cleanup(func() { _ = os.Chmod(walROPath+"-wal", 0o644) })
	_ = os.Chmod(walROPath+"-shm", 0o444)
	t.Cleanup(func() { _ = os.Chmod(walROPath+"-shm", 0o644) })

	walROOut, err := exec.QuerySQLite(ctx, "data/db/wal_ro.db", "SELECT n FROM items", 10)
	if err != nil || !strings.Contains(walROOut, "42") {
		t.Errorf("expected QuerySQLite to read 0444 WAL DB, got err=%v out=%s", err, walROOut)
	}
}
