package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// singleInstance stops a second daemon from running beside this one.
//
// Two encoders at once cost us a session on the VW: the phone sat on a charger
// while projecting, so the USB side started its own daemon and the second
// Codec2 instance took the whole process down (core.CodecLooper, 735 MB,
// 2026-11-26 09:19). One daemon at a time is all this phone can carry: the
// wireless path owns the encoder while it projects, and the USB path is for
// when there is no projection.
//
// The lock lives in the user's cache (both daemons run as the same user) and is
// released when the process exits, so a crash cannot leave it stuck.
func singleInstance() (*os.File, error) {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "cargadgetd-daemon.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another cargadgetd-md holds %s", path)
	}
	// Say who holds it, so a stale-looking conflict is explainable.
	_ = f.Truncate(0)
	if _, err := f.Seek(0, 0); err == nil {
		fmt.Fprintf(f, "%d %s\n", os.Getpid(), filepath.Base(os.Args[0]))
	}
	return f, nil
}

// lockFile keeps the lock's file descriptor reachable for the whole process:
// a local *os.File is finalised by the garbage collector, which closes the fd
// and silently releases the flock — two daemons then run side by side, which is
// exactly what this guard exists to prevent.
var lockFile *os.File

// claimSingleInstance exits when another daemon already runs. The wireless and
// the USB launcher both call it; the second one leaves quietly.
func claimSingleInstance() {
	lock, err := singleInstance()
	if err != nil {
		log.Printf("%v — exiting (one daemon at a time: two Codec2 encoders crash this phone)", err)
		os.Exit(0)
	}
	lockFile = lock // held for the process lifetime
}
