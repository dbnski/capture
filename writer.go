package main

import (
	"compress/gzip"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Writer interface {
	Flush() error
	Write(p []byte) (n int, err error)
}

type RotatingLogWriter struct {
	fd           *os.File
	gz           *gzip.Writer
	prefix       string
	lastRotation time.Time
	mu           *sync.Mutex
}

func NewRotatingLogWriter(mu *sync.Mutex, prefix string) *RotatingLogWriter {
	return &RotatingLogWriter{
		mu:     mu,
		prefix: prefix,
	}
}

func (w *RotatingLogWriter) ensurePath(logPath string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := os.Stat(logPath); err != nil {
		if os.IsNotExist(err) {
			if err := os.Mkdir(logPath, 0750); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return nil
}

func (w *RotatingLogWriter) EnsureRotated() error {
	now  := time.Now()
	then := w.lastRotation

	if then != (time.Time{}) && then.Truncate(time.Hour).Equal(now.Truncate(time.Hour)) {
		return nil
	}

	if w.fd != nil {
		if err := w.gz.Close(); err != nil {
			return err
		}
		if err := w.fd.Close(); err != nil {
			return err
		}
		w.fd = nil
		w.gz = nil
	}

	logPath := filepath.Join(options.Path, now.Format("20060102"))
	if err := w.ensurePath(logPath); err != nil {
		return err
	}

	filename := filepath.Join(logPath, w.prefix + "." + now.Format("20060102T1500") + ".gz")
	fd, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0640)
	if err != nil {
		return err
	}

	w.fd = fd
	w.gz = gzip.NewWriter(w.fd)
	w.lastRotation = now

	slog.Info("Capturing to file", "type", w.prefix, "file", filename)

	return nil
}

func (w *RotatingLogWriter) Write(p []byte) (int, error) {
	return w.gz.Write(p)
}

func (w *RotatingLogWriter) Flush() error {
	return w.gz.Flush()
}

func (w *RotatingLogWriter) Close() error {
	if w.gz != nil {
		if err := w.gz.Close(); err != nil {
			return err
		}
	}
	if w.fd != nil {
		return w.fd.Close()
	}
	return nil
}
