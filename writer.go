package main

import (
	"compress/gzip"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

type Writer interface {
	Flush() error
	Write(p []byte) (n int, err error)
}

type RotatingLogWriter struct {
	fd           *os.File
	gz           *gzip.Writer
	basedir		 string
	prefix       string
	lastRotation time.Time
}

func NewRotatingLogWriter(basedir, prefix string) *RotatingLogWriter {
	return &RotatingLogWriter{
		basedir: basedir,
		prefix:  prefix,
	}
}

func (w *RotatingLogWriter) EnsureRotated() error {
	now := time.Now().Truncate(time.Hour)
	then := w.lastRotation

	if then != (time.Time{}) && then.Equal(now) {
		return nil
	}

	if err := w.Close(); err != nil {
		return err
	}

	logPath := filepath.Join(w.basedir, now.Format("20060102"))
	filename := filepath.Join(logPath, w.prefix + "." + now.Format("200601021504") + ".gz")

	slog.Info("Setting capture target", "task", w.prefix, "file", filename)

	if err := os.MkdirAll(logPath, 0750); err != nil {
		return err
	}
	fd, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0640)
	if err != nil {
		return err
	}

	w.fd = fd
	w.gz = gzip.NewWriter(w.fd)
	w.lastRotation = now

	return nil
}

func (w *RotatingLogWriter) Write(p []byte) (int, error) {
	if w.gz == nil {
		return 0, errors.New("write before first rotation")
	}
	return w.gz.Write(p)
}

func (w *RotatingLogWriter) Flush() error {
	if w.gz == nil {
		return errors.New("flush before first rotation")
	}
	return w.gz.Flush()
}

func (w *RotatingLogWriter) Close() error {
	var gzErr, fdErr error
	if w.gz != nil {
		gzErr = w.gz.Close()
		w.gz = nil
	}
	if w.fd != nil {
		fdErr = w.fd.Close()
		w.fd = nil
	}
	return errors.Join(gzErr, fdErr)
}
