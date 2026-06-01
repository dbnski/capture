package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var (
	mockMutex   sync.Mutex
	mockOnce    sync.Once
	mockCounter atomic.Int64
	mockDSNs    = map[string]mockQuerySet{}
)

type mockQuerySet map[string]mockResult

type mockResult struct {
	columns []string
	rows    [][]driver.Value
}

type mockDriver struct{}

func (d *mockDriver) Open(dsn string) (driver.Conn, error) {
	mockMutex.Lock()
	qs, ok := mockDSNs[dsn]
	mockMutex.Unlock()
	if !ok {
		return nil, fmt.Errorf("mock: unknown DSN %q", dsn)
	}
	return &mockConn{queries: qs}, nil
}

type mockConn struct {
	queries mockQuerySet
}

func (c *mockConn) Prepare(query string) (driver.Stmt, error) {
	r, ok := c.queries[query]
	if !ok {
		return nil, fmt.Errorf("mock: unexpected query %q", query)
	}
	return &mockStmt{result: r}, nil
}

func (c *mockConn) Close() error {
	return nil
}

func (c *mockConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("mock: no transactions")
}

type mockStmt struct {
	result mockResult
}

func (s *mockStmt) Close() error {
	return nil
}

func (s *mockStmt) NumInput() int {
	return 0
}

func (s *mockStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, fmt.Errorf("mock: no exec")
}

func (s *mockStmt) Query([]driver.Value) (driver.Rows, error) {
	return &mockRows{result: s.result}, nil
}

type mockRows struct {
	result mockResult
	pos    int
}

func (r *mockRows) Columns() []string {
    return r.result.columns
}

func (r *mockRows) Close() error {
    return nil
}

func (r *mockRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.result.rows) {
		return io.EOF
	}
	copy(dest, r.result.rows[r.pos])
	r.pos++
	return nil
}

func openMockDB(t *testing.T, queries mockQuerySet) *sql.DB {
	t.Helper()

	mockOnce.Do(func() {
		sql.Register("mock", &mockDriver{})
	})

	dsn := fmt.Sprintf("mock-%d", mockCounter.Add(1))

	mockMutex.Lock()
	mockDSNs[dsn] = queries
	mockMutex.Unlock()
	t.Cleanup(func() {
		mockMutex.Lock()
		delete(mockDSNs, dsn)
		mockMutex.Unlock()
	})

	db, err := sql.Open("mock", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
	})

	return db
}

type dummyWriter struct {
	bytes.Buffer
}

func (w *dummyWriter) Flush() error {
	return nil
}

func bodyLines(t *testing.T, out string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	values := make([]string, len(lines))
	r := regexp.MustCompile(`^\d{2}:\d{2}:\d{2} \| `)
	for i, l := range lines {
		if m := r.FindString(l); m == "" {
			t.Errorf("line %d missing HH:MM:SS prefix: %q", i, l)
		} else {
            values[i] = l[len(m):]
        }
	}
	return values
}

func TestCaptureProcesslistColumns8(t *testing.T) {
	row := []driver.Value{int64(42), "alice", "host1:3306", "appdb", "Query", int64(3), "executing", "SELECT 1"}
	db  := openMockDB(t, mockQuerySet{
		"SHOW FULL PROCESSLIST": {
			columns: []string{"Id", "User", "Host", "Db", "Command", "Time", "State", "Info"},
			rows:    [][]driver.Value{row},
		},
	})

	var w dummyWriter
	if err := captureProcesslist()(context.Background(), db, &w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lines := bodyLines(t, w.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 output line, got %d", len(lines))
	}
	want := fmt.Sprintf("%d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t\t%-10s\t%s",
		row[0], row[1], row[2], row[3], row[4], row[5], row[6], row[7])
	if lines[0] != want {
		t.Errorf("body =\n%q\nwant\n%q", lines[0], want)
	}
}

func TestCaptureProcesslistColumns10(t *testing.T) {
	row := []driver.Value{int64(7), "bob", "host2:1234", "testdb", "Sleep", int64(0), "", nil, int64(100), int64(200)}
	db := openMockDB(t, mockQuerySet{
		"SHOW FULL PROCESSLIST": {
			columns: []string{"Id", "User", "Host", "Db", "Command", "Time", "State", "Info", "Rows_sent", "Rows_examined"},
			rows:    [][]driver.Value{row},
		},
	})

	var w dummyWriter
	if err := captureProcesslist()(context.Background(), db, &w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lines := bodyLines(t, w.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 output line, got %d", len(lines))
	}
	want := fmt.Sprintf("%d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t\t%-10s\t%d\t%d\t",
		row[0], row[1], row[2], row[3], row[4], row[5], row[6], row[8], row[9])
	if lines[0] != want {
		t.Errorf("body =\n%q\nwant\n%q", lines[0], want)
	}
}

func TestCaptureProcesslistColumns11(t *testing.T) {
	row := []driver.Value{int64(99), "carol", "host3:5678", "analytics", "Query", int64(12), "Sending data", "SELECT COUNT(*)", int64(12345), int64(50), int64(9999)}
	db := openMockDB(t, mockQuerySet{
		"SHOW FULL PROCESSLIST": {
			columns: []string{"Id", "User", "Host", "Db", "Command", "Time", "State", "Info", "Time_ms", "Rows_sent", "Rows_examined"},
			rows:    [][]driver.Value{row},
		},
	})

	var w dummyWriter
	if err := captureProcesslist()(context.Background(), db, &w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lines := bodyLines(t, w.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 output line, got %d", len(lines))
	}
	want := fmt.Sprintf("%d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t%d\t\t%-10s\t%d\t%d\t%s",
		row[0], row[1], row[2], row[3], row[4], row[5], row[8], row[6], row[9], row[10], row[7])
	if lines[0] != want {
		t.Errorf("body =\n%q\nwant\n%q", lines[0], want)
	}
}

func TestCaptureProcesslistNulls(t *testing.T) {
	row := []driver.Value{int64(1), "system user", "", nil, "Daemon", nil, "Waiting for next activation", nil}
	db := openMockDB(t, mockQuerySet{
		"SHOW FULL PROCESSLIST": {
			columns: []string{"Id", "User", "Host", "Db", "Command", "Time", "State", "Info"},
			rows:    [][]driver.Value{row},
		},
	})

	var w dummyWriter
	if err := captureProcesslist()(context.Background(), db, &w); err != nil {
		t.Fatalf("NULL Time must not cause an error: %v", err)
	}

	lines := bodyLines(t, w.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 output line, got %d", len(lines))
	}
	want := fmt.Sprintf("%d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t\t%-10s\t%s",
		row[0], row[1], row[2], "", row[4], 0, row[6], "")
	if lines[0] != want {
		t.Errorf("body =\n%q\nwant\n%q", lines[0], want)
	}
}

func TestCaptureProcesslistMultilineQuery(t *testing.T) {
	row := []driver.Value{int64(5), "root", "localhost", "mydb", "Query", int64(1), "executing", "SELECT *\nFROM t\nWHERE id=1"}
	db := openMockDB(t, mockQuerySet{
		"SHOW FULL PROCESSLIST": {
			columns: []string{"Id", "User", "Host", "Db", "Command", "Time", "State", "Info"},
			rows:    [][]driver.Value{row},
		},
	})

	var w dummyWriter
	if err := captureProcesslist()(context.Background(), db, &w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lines := bodyLines(t, w.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 output line, got %d", len(lines))
	}
	want := fmt.Sprintf("%d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t\t%-10s\t%s",
		row[0], row[1], row[2], row[3], row[4], row[5], row[6], "SELECT * FROM t WHERE id=1")
	if lines[0] != want {
		t.Errorf("body =\n%q\nwant\n%q", lines[0], want)
	}
}

func TestCaptureProcesslistUnexpectedColumnCount(t *testing.T) {
	db := openMockDB(t, mockQuerySet{
		"SHOW FULL PROCESSLIST": {
			columns: []string{"Id", "User", "Host", "Db", "Command", "Time", "State", "Info", "Extra"},
		},
	})

	var w dummyWriter
	if err := captureProcesslist()(context.Background(), db, &w); err == nil {
		t.Fatal("expected error for unexpected column count, got nil")
	}
}

func TestCaptureInnodb(t *testing.T) {
	statusLines := []string{
		"=====================================",
		"2024-01-01 00:00:00 INNODB MONITOR OUTPUT",
		"=====================================",
	}
	db := openMockDB(t, mockQuerySet{
		"SHOW ENGINE INNODB STATUS": {
			columns: []string{"Type", "Name", "Status"},
			rows: [][]driver.Value{
				{"InnoDB", "", strings.Join(statusLines, "\n")},
			},
		},
	})

	var w dummyWriter
	if err := captureInnodb()(context.Background(), db, &w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lines := bodyLines(t, w.String())
	if len(lines) != len(statusLines) {
		t.Fatalf("expected %d output lines, got %d", len(statusLines), len(lines))
	}
	for i, want := range statusLines {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
}

func TestCaptureStatus(t *testing.T) {
	rows := [][]driver.Value{
		{"Connections", "12345"},
		{"Uptime", "86400"},
		{"Queries", "999999"},
	}
	db := openMockDB(t, mockQuerySet{
		"SHOW GLOBAL STATUS": {
			columns: []string{"Variable_name", "Value"},
			rows:    rows,
		},
	})

	var w dummyWriter
	if err := captureStatus()(context.Background(), db, &w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantLines := make([]string, len(rows))
	for i, r := range rows {
		wantLines[i] = fmt.Sprintf("%s = %s", r[0], r[1])
	}
	lines := bodyLines(t, w.String())
	if len(lines) != len(wantLines) {
		t.Fatalf("expected %d output lines, got %d", len(wantLines), len(lines))
	}
	for i, want := range wantLines {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
}

func TestCaptureStatusNulls(t *testing.T) {
	db := openMockDB(t, mockQuerySet{
		"SHOW GLOBAL STATUS": {
			columns: []string{"Variable_name", "Value"},
			rows: [][]driver.Value{
				{"ssl_cipher", nil},
			},
		},
	})

	var w dummyWriter
	if err := captureStatus()(context.Background(), db, &w); err != nil {
		t.Fatalf("NULL Value must not cause an error: %v", err)
	}

	lines := bodyLines(t, w.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 output line, got %d", len(lines))
	}
	if lines[0] != "ssl_cipher = " {
		t.Errorf("line = %q, want %q", lines[0], "ssl_cipher = ")
	}
}

func TestWriteInSingleLineUnsafe(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no newlines",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "single newline in middle",
			input:    "hello\nworld",
			expected: "hello world",
		},
		{
			name:     "multiple newlines",
			input:    "hello\nworld\ntest",
			expected: "hello world test",
		},
		{
			name:     "consecutive newlines",
			input:    "hello\n\nworld",
			expected: "hello  world",
		},
		{
			name:     "newline at start",
			input:    "\nhello",
			expected: " hello",
		},
		{
			name:     "newline at end",
			input:    "hello\n",
			expected: "hello ",
		},
		{
			name:     "newline at both ends",
			input:    "\nhello\n",
			expected: " hello ",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "only newlines",
			input:    "\n\n\n",
			expected: "   ",
		},
		{
			name:     "complex multiline text",
			input:    "SELECT *\nFROM users\nWHERE id = 1",
			expected: "SELECT * FROM users WHERE id = 1",
		},
		{
			name:     "text with trailing newlines",
			input:    "query text\n\n",
			expected: "query text  ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeInSingleLineUnsafe(&buf, tt.input)

			got := buf.String()
			if got != tt.expected {
				t.Errorf("writeInSingleLineUnsafe() = %q, want %q", got, tt.expected)
			}
		})
	}
}
