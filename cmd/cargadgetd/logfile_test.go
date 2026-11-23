package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A log removed while the daemon runs must come back: writing into the unlinked
// inode is how a whole session went missing on the phone.
func TestReopenWriterRecreatesDeletedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wireless.log")
	w := &reopenWriter{path: path, minCheck: 1}
	defer w.Close()

	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log was not recreated: %v", err)
	}
	if got := string(b); got != "second\n" {
		t.Fatalf("recreated log = %q, want only the new line", got)
	}
}

// A rotation (move + new file) must also be followed.
func TestReopenWriterFollowsRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wireless.log")
	w := &reopenWriter{path: path, minCheck: 1}
	defer w.Close()

	if _, err := w.Write([]byte("old\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(path + ".1")
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(old), "old") {
		t.Fatalf("rotated file = %q", old)
	}
	if got := string(cur); got != "new\n" {
		t.Fatalf("new file = %q", got)
	}
}
