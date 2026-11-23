package main

import (
	"io"
	"os"
	"sync"
	"time"
)

// reopenWriter is an io.Writer on a log file that survives the file being
// deleted or rotated. A daemon started with a shell redirect keeps writing into
// the unlinked inode when someone removes the log: the file looks gone and the
// session leaves no trace. This writer notices and reopens.
//
// The check runs at most once a second so a busy session (hundreds of lines per
// second) does not stat the file on every line.
type reopenWriter struct {
	path string
	// minCheck is how often the path may be stat'ed; 0 means one second.
	minCheck time.Duration

	mu      sync.Mutex
	f       *os.File
	info    os.FileInfo
	checked time.Time
}

// LogPath exposes the file this writer is bound to.
func (w *reopenWriter) LogPath() string { return w.path }

// Write appends p, reopening the file first if it was removed or replaced.
func (w *reopenWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureLocked(); err != nil {
		return 0, err
	}
	return w.f.Write(p)
}

// ensureLocked opens or reopens the file when the path points somewhere else
// (deleted, moved, or replaced by a rotation).
func (w *reopenWriter) ensureLocked() error {
	every := w.minCheck
	if every <= 0 {
		every = time.Second
	}
	now := time.Now()
	if w.f != nil && now.Sub(w.checked) < every {
		return nil
	}
	w.checked = now
	if w.f != nil {
		if st, err := os.Stat(w.path); err == nil && w.info != nil && os.SameFile(st, w.info) {
			return nil
		}
		_ = w.f.Close()
		w.f = nil
	}
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, statErr := f.Stat()
	w.f = f
	w.info = nil
	if statErr == nil {
		w.info = info
	}
	return nil
}

// Close releases the file.
func (w *reopenWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

var _ io.WriteCloser = (*reopenWriter)(nil)
