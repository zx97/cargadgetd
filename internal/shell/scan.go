package shell

import (
	"log"
	"os"
	"path/filepath"
	"sort"
)

// pluginOpener is OpenPlugin; tests swap it to assert Load does not block.
// settingsOpener maps a settings-only library and leaves it loaded.
var (
	pluginOpener      = OpenPlugin
	settingsOpener    = openSettings
	settingsOnlyCheck = settingsOnly
)

// PluginDir is the only place the host dlopens C ABI .so apps from.
const PluginDir = "/usr/lib/cargadgetd/plugins"

// ListPlugins returns libcargadgetd_app_<id>.so paths. Other .so files in the
// directory (reversegearhead.so, a bare music.so) are not apps. The first
// directory wins per basename.
func ListPlugins(dirs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if _, err := appIDFromBasename(name); err != nil {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out
}

// Load lists libcargadgetd_app_<id>.so files and dlopens in the background so
// USB/session startup is not blocked. Each app is attached after dlopen, then
// handed to the shell once its init returns. Duplicate ids are skipped.
// A settings-only library (settings_count, no draw) is mapped with RTLD_GLOBAL
// so the settings app can dlsym it, and is not attached as an app.
// WaitLoad waits until every dlopen has finished (ok or skip).
func Load(s *Shell, dirs []string) []string {
	type job struct{ path, id string }
	var jobs []job
	var settings []string
	var ids []string
	for _, path := range ListPlugins(dirs) {
		if settingsOnlyCheck(path) {
			settings = append(settings, path)
			continue
		}
		id, err := AppIDFromFile(path)
		if err != nil {
			log.Printf("shell: skip %s: %v", path, err)
			continue
		}
		if s.Has(id) {
			continue
		}
		ids = append(ids, id)
		jobs = append(jobs, job{path: path, id: id})
	}
	// MapLibre (the maps app) needs static TLS; dlopen it before the other
	// plugins so the static TLS surplus is still large enough. Loaded last it
	// fails with "cannot allocate memory in static TLS block" and the map area
	// stays blank.
	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].id == "maps" {
			return i != j && true
		}
		if jobs[j].id == "maps" {
			return false
		}
		return false
	})
	if len(jobs) == 0 && len(settings) == 0 {
		return ids
	}
	s.loadWG.Add(1)
	go func() {
		defer s.loadWG.Done()
		for _, path := range settings {
			if err := settingsOpener(path); err != nil {
				log.Printf("shell: settings %s: %v", path, err)
				continue
			}
			log.Printf("shell: settings %s", path)
		}
		for _, j := range jobs {
			a, err := pluginOpener(j.path)
			if err != nil {
				log.Printf("shell: skip %s: %v", j.path, err)
				continue
			}
			log.Printf("shell: loaded %s (%s)", a.ID, a.Name)
			s.attach(a)
		}
	}()
	return ids
}
