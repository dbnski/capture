package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureRotatedCreatesFile(t *testing.T) {
	dir := t.TempDir()
	w := NewRotatingLogWriter(dir, "myprefix")
	defer w.Close()

	now := time.Now()
	if err := w.EnsureRotated(); err != nil {
		t.Fatalf("EnsureRotated() error = %v", err)
	}

	dateDir := filepath.Join(dir, now.Format("20060102"))
	entries, err := os.ReadDir(dateDir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", dateDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}

	filename := entries[0].Name()
	if !strings.HasPrefix(filename, "myprefix.") {
		t.Errorf("filename %q does not start with 'myprefix.'", filename)
	}
	if !strings.HasSuffix(filename, ".gz") {
		t.Errorf("filename %q does not end with '.gz'", filename)
	}
	expectedTimestamp := now.Truncate(time.Hour).Format("200601021504")
	if !strings.Contains(filename, expectedTimestamp) {
		t.Errorf("filename %q does not contain expected timestamp %q", filename, expectedTimestamp)
	}
}

func TestEnsureRotatedIdempotentSameHour(t *testing.T) {
	dir := t.TempDir()
	w := NewRotatingLogWriter(dir, "test")
	defer w.Close()

	if err := w.EnsureRotated(); err != nil {
		t.Fatalf("first EnsureRotated() error = %v", err)
	}
	fd1 := w.fd

	if err := w.EnsureRotated(); err != nil {
		t.Fatalf("second EnsureRotated() error = %v", err)
	}

	if w.fd != fd1 {
		t.Error("second EnsureRotated() within same hour should not open a new file")
	}
}

func TestEnsureRotatedRotatesOnNewHour(t *testing.T) {
	dir := t.TempDir()
	w := NewRotatingLogWriter(dir, "test")
	defer w.Close()

	if err := w.EnsureRotated(); err != nil {
		t.Fatalf("first EnsureRotated() error = %v", err)
	}
	fd1 := w.fd

	// Backdate lastRotation to a previous hour to force rotation on next call.
	w.lastRotation = time.Now().Add(-2 * time.Hour)

	if err := w.EnsureRotated(); err != nil {
		t.Fatalf("second EnsureRotated() error = %v", err)
	}

	if w.fd == fd1 {
		t.Error("second EnsureRotated() after hour boundary should have opened a new file")
	}
}

func TestEnsurePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "newdir")

	if err := ensurePath(path); err != nil {
		t.Fatalf("ensurePath() on new dir error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() after ensurePath() error = %v", err)
	}
	if !info.IsDir() {
		t.Error("ensurePath() should have created a directory")
	}

	if err := ensurePath(path); err != nil {
		t.Errorf("ensurePath() on existing dir error = %v, want nil", err)
	}
}
