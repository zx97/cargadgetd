// Package rgh loads GAL MD TLS material from reversegearhead.so.
package rgh

import (
	"os"
	"path/filepath"
	"strings"
)

// ConfigDefault is where aaextract keeps the pair it pulls out of the Android
// Auto APK, one subdirectory per version. It is the preferred source: an
// extracted pair is the real one Google ships, while a plugin pins whatever was
// built into it (and expires).
const ConfigDefault = "~/.local/share/cargadgetd/certs"

// DefaultSO is the plugin that exports reversegearhead_android_auto_crt/key.
const DefaultSO = "/usr/lib/cargadgetd/plugins/reversegearhead.so"

// Material is the PEMs and openssl-style summary from reversegearhead.so.
type Material struct {
	Path      string
	Cert      []byte
	Key       []byte
	CA        []byte
	Probe     string
	ExpiresIn int64
}

// ResolvePath maps daemon.certs / -certs to a path, and expands a leading "~"
// to the user's home. Without the expansion the extracted pair landed in a
// directory literally named "~" inside the working directory (measured on the
// phone: cargadgetd-deploy/bin/~/.local/share/cargadgetd/certs/…).
//
// Empty, "embedded", and "reversegearhead" mean DefaultSO (the plugin).
func ResolvePath(certs string) string {
	s := strings.TrimSpace(certs)
	switch strings.ToLower(s) {
	case "", "embedded", "reversegearhead":
		return DefaultSO
	}
	return expandUser(s)
}

// expandUser resolves a leading "~" or "~/" to the current user's home.
func expandUser(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

func missingErr(path string) error {
	return &LoadError{Path: path, Missing: true}
}

// LoadError is returned when reversegearhead.so is absent or will not load.
type LoadError struct {
	Path    string
	Missing bool
	Err     error
}

func (e *LoadError) Error() string {
	if e == nil {
		return "reversegearhead.so is missing"
	}
	if e.Missing {
		if e.Path != "" && e.Path != DefaultSO {
			return "reversegearhead.so is missing (" + e.Path + ")"
		}
		return "reversegearhead.so is missing"
	}
	if e.Err != nil {
		if e.Path != "" {
			return "reversegearhead.so failed to load (" + e.Path + "): " + e.Err.Error()
		}
		return "reversegearhead.so failed to load: " + e.Err.Error()
	}
	return "reversegearhead.so failed to load"
}

func (e *LoadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
