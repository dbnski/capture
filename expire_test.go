package main

import (
    "bytes"
    "context"
    "errors"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

var testCutoff = time.Date(2026, 7, 30, 12, 0, 0, 0, time.Local)

func TestParseLogTime(t *testing.T) {
    tests := []struct {
        name     string
        filename string
        want     time.Time
        wantErr  error
    }{
        {"valid file",        "status.202606201000.gz",     time.Date(2026, 6, 20, 10, 0, 0, 0, time.Local), nil},
        {"disabled task",     "rocksdb.202606201000.gz",    time.Date(2026, 6, 20, 10, 0, 0, 0, time.Local), nil},
        {"unknown task",      "mysqld.202606201000.gz",     time.Time{},                                     errNotCapturePath},
        {"uppercase task",    "STATUS.202606201000.gz",     time.Time{},                                     errNotCapturePath},
        {"empty task",        ".202606201000.gz",           time.Time{},                                     errNotCapturePath},
        {"no task",           "202606201000.gz",            time.Time{},                                     errNotCapturePath},
        {"no extension",      "status.202606201000",        time.Time{},                                     errNotCapturePath},
        {"extra extension",   "status.202606201000.gz.tmp", time.Time{},                                     errNotCapturePath},
        {"short timestamp",   "status.2026062010.gz",       time.Time{},                                     errBadTimestamp},
        {"long timestamp",    "status.2026062010001.gz",    time.Time{},                                     errBadTimestamp},
        {"invalid month",     "status.202613201000.gz",     time.Time{},                                     errBadTimestamp},
        {"non numeric stamp", "status.20260620abcd.gz",     time.Time{},                                     errBadTimestamp},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := parseLogTime(tt.filename)
            if !errors.Is(err, tt.wantErr) {
                t.Fatalf("parseLogTime(%q) error = %v, want %v", tt.filename, err, tt.wantErr)
            }
            if err == nil && !got.Equal(tt.want) {
                t.Errorf("parseLogTime(%q) = %v, want %v", tt.filename, got, tt.want)
            }
        })
    }
}

func TestParseDirTime(t *testing.T) {
    tests := []struct {
        name    string
        dirname string
        wantErr error
    }{
        {"valid date",    "20260620",  nil},
        {"short date",    "2026062",   errNotCapturePath},
        {"long date",     "202606201", errNotCapturePath},
        {"dashed date",   "2026-062",  errNotCapturePath},
        {"not a date",    "notadate",  errNotCapturePath},
        {"invalid month", "20261320",  errBadTimestamp},
        {"invalid day",   "20260632",  errBadTimestamp},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            if _, err := parseDirTime(tt.dirname); !errors.Is(err, tt.wantErr) {
                t.Errorf("parseDirTime(%q) error = %v, want %v", tt.dirname, err, tt.wantErr)
            }
        })
    }
}

func makeFile(t *testing.T, base, name string) string {
    t.Helper()

    path := filepath.Join(base, name)
    if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
        t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
    }
    if err := os.WriteFile(path, []byte("data"), 0640); err != nil {
        t.Fatalf("WriteFile(%q) error = %v", path, err)
    }
    return path
}

func makeDir(t *testing.T, base, name string) string {
    t.Helper()

    path := filepath.Join(base, name)
    if err := os.MkdirAll(path, 0750); err != nil {
        t.Fatalf("MkdirAll(%q) error = %v", path, err)
    }
    return path
}

func TestExpireStep(t *testing.T) {
    tests := []struct {
        name     string
        setup    func(t *testing.T, base string)
        wantKept []string
        wantGone []string
    }{
        {
            name: "expired file and its directory are removed",
            setup: func(t *testing.T, base string) {
                makeFile(t, base, "20260620/status.202606201000.gz")
            },
            wantGone: []string{"20260620/status.202606201000.gz", "20260620"},
        },
        {
            name: "file inside the retention period is kept",
            setup: func(t *testing.T, base string) {
                makeFile(t, base, "20260730/status.202607301100.gz")
            },
            wantKept: []string{"20260730/status.202607301100.gz", "20260730"},
        },
        {
            name: "file that expires exactly at the cutoff is kept",
            setup: func(t *testing.T, base string) {
                // Named 11:00, so it stays open until 12:00, which is the cutoff.
                makeFile(t, base, "20260730/innodb.202607301100.gz")
            },
            wantKept: []string{"20260730/innodb.202607301100.gz"},
        },
        {
            name: "unknown file is kept and keeps its directory",
            setup: func(t *testing.T, base string) {
                makeFile(t, base, "20260620/status.202606201000.gz")
                makeFile(t, base, "20260620/mysqld.202606201000.gz")
            },
            wantKept: []string{"20260620/mysqld.202606201000.gz", "20260620"},
            wantGone: []string{"20260620/status.202606201000.gz"},
        },
        {
            name: "directories that are not dates are untouched",
            setup: func(t *testing.T, base string) {
                makeFile(t, base, "notadate/status.202606201000.gz")
                makeFile(t, base, "2026-06-20/status.202606201000.gz")
            },
            wantKept: []string{
                "notadate/status.202606201000.gz",
                "2026-06-20/status.202606201000.gz",
            },
        },
        {
            name: "file in the base directory is kept",
            setup: func(t *testing.T, base string) {
                makeFile(t, base, "status.202606201000.gz")
            },
            wantKept: []string{"status.202606201000.gz"},
        },
        {
            name: "expired empty directory is removed",
            setup: func(t *testing.T, base string) {
                makeDir(t, base, "20260620")
            },
            wantGone: []string{"20260620"},
        },
        {
            name: "recent empty directory is kept",
            setup: func(t *testing.T, base string) {
                makeDir(t, base, "20260730")
            },
            wantKept: []string{"20260730"},
        },
        {
            name: "directory named like a log file is kept and not searched",
            setup: func(t *testing.T, base string) {
                makeFile(t, base, "20260620/status.202606201000.gz/status.202606201000.gz")
            },
            wantKept: []string{
                "20260620/status.202606201000.gz/status.202606201000.gz",
                "20260620/status.202606201000.gz",
                "20260620",
            },
        },
        {
            name: "symlink named like a log file is kept",
            setup: func(t *testing.T, base string) {
                target := makeFile(t, base, "keep/status.202606201000.gz")
                makeDir(t, base, "20260620")

                link := filepath.Join(base, "20260620/innodb.202606201000.gz")
                if err := os.Symlink(target, link); err != nil {
                    t.Fatalf("Symlink(%q) error = %v", link, err)
                }
            },
            wantKept: []string{
                "20260620/innodb.202606201000.gz",
                "keep/status.202606201000.gz",
                "20260620",
            },
        },
        {
            name: "directory with an invalid date is kept with its files",
            setup: func(t *testing.T, base string) {
                makeFile(t, base, "20261320/status.202606201000.gz")
            },
            wantKept: []string{"20261320/status.202606201000.gz", "20261320"},
        },
        {
            name: "file with an invalid timestamp is kept",
            setup: func(t *testing.T, base string) {
                makeFile(t, base, "20260620/status.202613201000.gz")
            },
            wantKept: []string{"20260620/status.202613201000.gz", "20260620"},
        },
        {
            name: "symlinked date directory is not searched",
            setup: func(t *testing.T, base string) {
                target := makeDir(t, base, "real")
                makeFile(t, base, "real/status.202606201000.gz")

                link := filepath.Join(base, "20260620")
                if err := os.Symlink(target, link); err != nil {
                    t.Fatalf("Symlink(%q) error = %v", link, err)
                }
            },
            wantKept: []string{"20260620", "real/status.202606201000.gz"},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            base := t.TempDir()
            tt.setup(t, base)

            if err := newExpirer(base).step(testCutoff); err != nil {
                t.Fatalf("step() error = %v", err)
            }

            for _, name := range tt.wantKept {
                if _, err := os.Lstat(filepath.Join(base, name)); err != nil {
                    t.Errorf("%q was removed, want kept: %v", name, err)
                }
            }
            for _, name := range tt.wantGone {
                if _, err := os.Lstat(filepath.Join(base, name)); err == nil {
                    t.Errorf("%q still exists, want removed", name)
                }
            }
        })
    }
}

func captureLogs(t *testing.T) *bytes.Buffer {
    t.Helper()

    buf     := &bytes.Buffer{}
    previous := slog.Default()

    slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
    t.Cleanup(func() { slog.SetDefault(previous) })

    return buf
}

// Every warning needs manual action and the run repeats every hour, so each path must be
// reported only once no matter how many passes see it.
func TestExpireStepWarnsOnce(t *testing.T) {
    tests := []struct {
        name    string
        setup   func(t *testing.T, base string)
        wantLog string
    }{
        {
            name:    "invalid date directory",
            setup:   func(t *testing.T, base string) { makeDir(t, base, "20261320") },
            wantLog: "Log directory has an invalid date",
        },
        {
            name:    "invalid file timestamp",
            setup:   func(t *testing.T, base string) { makeFile(t, base, "20260620/status.202613201000.gz") },
            wantLog: "Log file has an invalid timestamp",
        },
        {
            name:    "non date directory",
            setup:   func(t *testing.T, base string) { makeDir(t, base, "notadate") },
            wantLog: "Log directory does not follow the date scheme",
        },
        {
            name:    "file in the base directory",
            setup:   func(t *testing.T, base string) { makeFile(t, base, "status.202606201000.gz") },
            wantLog: "Log path is not a directory",
        },
        {
            name:    "unknown file in a date directory",
            setup:   func(t *testing.T, base string) { makeFile(t, base, "20260620/backup.tar.gz") },
            wantLog: "Log file does not follow the capture naming scheme",
        },
        {
            name:    "directory named like a log file",
            setup:   func(t *testing.T, base string) { makeDir(t, base, "20260620/status.202606201000.gz") },
            wantLog: "Log path is not a regular file",
        },
        {
            name:    "expired directory that is not empty",
            setup:   func(t *testing.T, base string) { makeFile(t, base, "20260620/mysqld.202606201000.gz") },
            wantLog: "Log directory not empty",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            base := t.TempDir()
            tt.setup(t, base)

            logs := captureLogs(t)
            task := newExpirer(base)

            for i := range 3 {
                if err := task.step(testCutoff); err != nil {
                    t.Fatalf("step() %d error = %v", i, err)
                }
            }

            if got := strings.Count(logs.String(), tt.wantLog); got != 1 {
                t.Errorf("reported %q %d times, want 1", tt.wantLog, got)
            }
        })
    }
}

// A path that is cleared must drop out of the reported set, so the map does not grow
// forever and the same path is reported again if it comes back.
func TestExpireStepForgetsClearedPaths(t *testing.T) {
    base := t.TempDir()
    path := makeFile(t, base, "20260620/backup.tar.gz")

    logs := captureLogs(t)
    task := newExpirer(base)

    if err := task.step(testCutoff); err != nil {
        t.Fatalf("step() error = %v", err)
    }

    if err := os.Remove(path); err != nil {
        t.Fatalf("Remove(%q) error = %v", path, err)
    }
    if err := task.step(testCutoff); err != nil {
        t.Fatalf("step() after remove error = %v", err)
    }

    if len(task.reported) != 0 {
        t.Errorf("reported = %v, want empty", task.reported)
    }

    makeFile(t, base, "20260620/backup.tar.gz")
    if err := task.step(testCutoff); err != nil {
        t.Fatalf("step() after restore error = %v", err)
    }

    const wantLog = "Log file does not follow the capture naming scheme"
    if got := strings.Count(logs.String(), wantLog); got != 2 {
        t.Errorf("reported %q %d times, want 2", wantLog, got)
    }
}

// A tree that holds nothing but our own live logs must stay silent, or the log fills up
// with warnings on every hourly pass.
func TestExpireStepQuietOnOwnPaths(t *testing.T) {
    base := t.TempDir()
    makeFile(t, base, "20260730/status.202607301100.gz")
    makeFile(t, base, "20260730/innodb.202607301100.gz")

    logs := captureLogs(t)

    if err := newExpirer(base).step(testCutoff); err != nil {
        t.Fatalf("step() error = %v", err)
    }

    if strings.Contains(logs.String(), "level=WARN") {
        t.Errorf("unexpected warning: %s", logs.String())
    }
}

func TestExpireStepUnreadableBase(t *testing.T) {
    base := filepath.Join(t.TempDir(), "missing")

    if err := newExpirer(base).step(testCutoff); err == nil {
        t.Error("step() error = nil, want an error")
    }
}

func TestExpireDisabled(t *testing.T) {
    base := t.TempDir()
    makeFile(t, base, "20260620/status.202606201000.gz")

    if err := expire(context.Background(), base, 0); err != nil {
        t.Fatalf("expire() error = %v", err)
    }

    if _, err := os.Lstat(filepath.Join(base, "20260620/status.202606201000.gz")); err != nil {
        t.Errorf("file was removed while expiration is disabled: %v", err)
    }
}
