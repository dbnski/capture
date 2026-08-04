package main

import (
    "context"
    "errors"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "syscall"
    "time"

    "github.com/samber/lo"
)

const (
    expireCheckInterval = time.Hour
    logTimeFormat       = "200601021504"
    dateDirFormat       = "20060102"
)

const (
    logLifetime = time.Hour
    dirLifetime = 25 * time.Hour
)

var (
    errNotCapturePath = errors.New("not a capture path")
    errBadTimestamp   = errors.New("invalid timestamp")
)

func isDigits(s string) bool {
    for _, c := range s {
        if c < '0' || c > '9' {
            return false
        }
    }
    return len(s) > 0
}

func parseLogTime(name string) (time.Time, error) {
    base, found := strings.CutSuffix(name, ".gz")
    if !found {
        return time.Time{}, errNotCapturePath
    }

    prefix, stamp, found := strings.Cut(base, ".")
    if !found || !lo.Contains(taskNames(), prefix) {
        return time.Time{}, errNotCapturePath
    }

    started, err := time.ParseInLocation(logTimeFormat, stamp, time.Local)
    if err != nil {
        return time.Time{}, fmt.Errorf("%w: %w", errBadTimestamp, err)
    }

    return started, nil
}

func parseDirTime(name string) (time.Time, error) {
    if len(name) != len(dateDirFormat) || !isDigits(name) {
        return time.Time{}, errNotCapturePath
    }

    date, err := time.ParseInLocation(dateDirFormat, name, time.Local)
    if err != nil {
        return time.Time{}, fmt.Errorf("%w: %w", errBadTimestamp, err)
    }

    return date, nil
}

type expirer struct {
    basedir  string
    reported map[string]struct{}
}

func newExpirer(basedir string) *expirer {
    return &expirer{
        basedir:  basedir,
        reported: make(map[string]struct{}),
    }
}

func (e *expirer) warnOnce(path, msg string, args ...any) {
    if _, seen := e.reported[path]; seen {
        return
    }
    e.reported[path] = struct{}{}

    slog.Warn(msg, append([]any{"path", path}, args...)...)
}

func (e *expirer) step(cutoff time.Time) error {
    entries, err := os.ReadDir(e.basedir)
    if err != nil {
        return err
    }

    for _, entry := range entries {
        dirPath := filepath.Join(e.basedir, entry.Name())

        if !entry.IsDir() {
            e.warnOnce(dirPath, "Log path is not a directory, will not expire")
            continue
        }

        date, err := parseDirTime(entry.Name())
        if err != nil {
            switch {
            case errors.Is(err, errNotCapturePath):
                e.warnOnce(dirPath, "Log directory does not follow the date scheme, will not expire")
            case errors.Is(err, errBadTimestamp):
                e.warnOnce(dirPath, "Log directory has an invalid date, will not expire", "error", err)
            default:
                e.warnOnce(dirPath, "Failed to parse log directory name", "error", err)
            }
            continue
        }

        if err := e.expireFiles(dirPath, cutoff); err != nil {
            slog.Warn("Failed to read log directory", "path", dirPath, "error", err)
            continue
        }

        if date.Add(dirLifetime).Before(cutoff) {
            e.expireDir(dirPath)
        }
    }

    e.pruneReported()

    return nil
}

func (e *expirer) pruneReported() {
    for path := range e.reported {
        if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
            delete(e.reported, path)
        }
    }
}

func (e *expirer) expireFiles(dirPath string, cutoff time.Time) error {
    entries, err := os.ReadDir(dirPath)
    if err != nil {
        return err
    }

    for _, entry := range entries {
        filename := filepath.Join(dirPath, entry.Name())

        if !entry.Type().IsRegular() {
            e.warnOnce(filename, "Log path is not a regular file, will not expire")
            continue
        }

        started, err := parseLogTime(entry.Name())
        if err != nil {
            switch {
            case errors.Is(err, errNotCapturePath):
                e.warnOnce(filename, "Log file does not follow the capture naming scheme, will not expire")
            case errors.Is(err, errBadTimestamp):
                e.warnOnce(filename, "Log file has an invalid timestamp, will not expire", "error", err)
            default:
                e.warnOnce(filename, "Failed to parse log file name", "error", err)
            }
            continue
        }

        if !started.Add(logLifetime).Before(cutoff) {
            continue
        }

        if err := os.Remove(filename); err != nil {
            slog.Warn("Failed to delete expired log file", "file", filename, "error", err)
            continue
        }
        slog.Info("Deleted expired log file", "file", filename)
    }

    return nil
}

func (e *expirer) expireDir(dirPath string) {
    err := os.Remove(dirPath)
    if err == nil {
        slog.Info("Removed empty log directory", "path", dirPath)
        return
    }

    if errors.Is(err, syscall.ENOTEMPTY) {
        e.warnOnce(dirPath, "Log directory not empty, will not remove")
        return
    }

    slog.Warn("Failed to remove log directory", "path", dirPath, "error", err)
}

func expire(ctx context.Context, basedir string, retention time.Duration) error {
    if retention <= 0 {
        return nil
    }

    task := newExpirer(basedir)

    timer := time.NewTimer(0)

    for {
        select {
        case <- ctx.Done():
            slog.Info("Expiration task stopped")
            return nil
        case now := <- timer.C:
            timer.Reset(expireCheckInterval)

            if err := task.step(now.Add(-retention)); err != nil {
                slog.Warn("Failed to expire log files, will retry", "path", basedir, "error", err)
            }
        }
    }
}
