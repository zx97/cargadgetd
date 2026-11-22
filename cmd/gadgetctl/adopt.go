package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/ffs"
	"github.com/cargadgetd/cargadgetd/internal/usbgadget"
)

// This kernel's gadgets_make registers one sysfs device,
// /sys/devices/virtual/android_usb/android1, and does not destroy it when
// the configfs gadget is removed. A second mkdir then fails with EEXIST,
// which userspace sees as ENOMEM. The class directory exists even before
// that device node. When it is present, reuse usb-moded's gadget instead
// of creating another one.

var (
	androidUSBClassPath = "/sys/class/android_usb"
	gadgetConfigRoot    = "/sys/kernel/config/usb_gadget"
	gadgetSnapFile      = "/run/cargadgetd/gadget.snap"
	gadgetUnbindSettle  = 200 * time.Millisecond
)

type gadgetSnap struct {
	Gadget  string       `json:"gadget"`
	Files   []savedFile  `json:"files"`
	Configs []configSnap `json:"configs"`
}

type savedFile struct {
	Rel     string `json:"rel"`
	Data    string `json:"data"`
	Existed bool   `json:"existed"`
}

type configSnap struct {
	Name  string     `json:"name"`
	Links []linkSnap `json:"links"`
}

type linkSnap struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

// accessoryProductName, when set by main, overrides the gadget product string
// for the accessory session: the head unit lists the phone by that string.
var accessoryProductName string

// accessoryVendor is the manufacturer string for the accessory descriptors.
var accessoryVendor = "Jolla"

// identityFiles are snapshotted and restored with the ask gadget.
// An empty val is left as the phone set it. A Pixel comes back from AOA
// START as 18d1:2d00 still named Google / Pixel / its real serial. Replacing
// those with cargadgetd / Android Auto MD is what this car enumerated, and
// it configured the interface and never sent the version request.
var identityFiles = []struct{ rel, val string }{
	{"idVendor", "0x18d1"},
	{"idProduct", "0x2d00"},
	{"bcdDevice", ""},
	{"bcdUSB", ""},
	{"strings/0x409/serialnumber", ""},
	{"strings/0x409/manufacturer", ""},
	{"strings/0x409/product", ""},
}

// selectAdoptedGadget returns usb-moded's gadget when this kernel cannot
// host a second configfs gadget. configured is the private aa path.
func selectAdoptedGadget(configured string) (string, bool) {
	st, err := os.Stat(androidUSBClassPath)
	if err != nil || !st.IsDir() {
		return "", false
	}
	ents, err := os.ReadDir(gadgetConfigRoot)
	if err != nil {
		return "", false
	}
	base := filepath.Base(configured)
	if base != "aa" {
		if st, err := os.Stat(configured); err == nil && st.IsDir() {
			return configured, true
		}
	}
	var others []string
	for _, e := range ents {
		if !e.IsDir() || e.Name() == base || e.Name() == "aa" {
			continue
		}
		others = append(others, filepath.Join(gadgetConfigRoot, e.Name()))
	}
	for _, p := range others {
		if filepath.Base(p) == "g1" {
			return p, true
		}
	}
	if len(others) == 1 {
		return others[0], true
	}
	return "", false
}

func captureGadget(gadget string) (*gadgetSnap, error) {
	snap := &gadgetSnap{Gadget: gadget}
	for _, id := range identityFiles {
		p := filepath.Join(gadget, id.rel)
		b, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				snap.Files = append(snap.Files, savedFile{Rel: id.rel})
				continue
			}
			return nil, fmt.Errorf("read %s: %w", id.rel, err)
		}
		snap.Files = append(snap.Files, savedFile{Rel: id.rel, Data: string(b), Existed: true})
	}
	configs, err := configNames(gadget)
	if err != nil {
		return nil, err
	}
	if len(configs) == 0 {
		return nil, fmt.Errorf("gadget %s has no config", gadget)
	}
	for _, name := range configs {
		links, err := configLinks(filepath.Join(gadget, "configs", name))
		if err != nil {
			return nil, err
		}
		snap.Configs = append(snap.Configs, configSnap{Name: name, Links: links})
	}
	return snap, nil
}

func configNames(gadget string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(gadget, "configs"))
	if err != nil {
		return nil, fmt.Errorf("read configs: %w", err)
	}
	var names []string
	for _, e := range ents {
		if e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.SliceStable(names, func(i, j int) bool {
		rank := func(n string) int {
			switch n {
			case "b.1":
				return 0
			case "c.1":
				return 1
			default:
				return 2
			}
		}
		ri, rj := rank(names[i]), rank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	return names, nil
}

func configLinks(dir string) ([]linkSnap, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var links []linkSnap
	for _, e := range ents {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("readlink %s: %w", e.Name(), err)
		}
		links = append(links, linkSnap{Name: e.Name(), Target: target})
	}
	return links, nil
}

func unlinkConfigLinks(gadget string, configs []configSnap) error {
	for _, c := range configs {
		dir := filepath.Join(gadget, "configs", c.Name)
		for _, l := range c.Links {
			p := filepath.Join(dir, l.Name)
			st, err := os.Lstat(p)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			if st.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("refusing to remove non-symlink %s", p)
			}
			if err := os.Remove(p); err != nil {
				return fmt.Errorf("unlink %s: %w", p, err)
			}
		}
	}
	return nil
}

func relinkConfigs(snap *gadgetSnap) {
	// configfs resolves a symlink target from the process cwd, not from the
	// link's directory. Readlink stores a path that is relative to the config
	// directory (often "../../../../usb_gadget/<g>/functions/<fn>"). Creating
	// that link from any other cwd returns ENOENT and the ask gadget comes
	// back with no functions, so the host never enumerates it.
	wd, err := os.Getwd()
	if err != nil {
		log.Printf("relink getwd: %v", err)
		return
	}
	defer func() { _ = os.Chdir(wd) }()
	for _, c := range snap.Configs {
		dir := filepath.Join(snap.Gadget, "configs", c.Name)
		if err := os.Chdir(dir); err != nil {
			log.Printf("relink chdir %s: %v", dir, err)
			continue
		}
		for _, l := range c.Links {
			if st, err := os.Lstat(l.Name); err == nil {
				if st.Mode()&os.ModeSymlink == 0 {
					log.Printf("leave non-symlink %s", filepath.Join(dir, l.Name))
					continue
				}
				_ = os.Remove(l.Name)
			}
			target := l.Target
			if _, err := os.Stat(target); err != nil {
				alt := filepath.Join("..", "..", "functions", l.Name)
				abs := filepath.Join(snap.Gadget, "functions", l.Name)
				switch {
				case statOK(alt):
					target = alt
				case statOK(abs):
					target = abs
				}
			}
			if err := os.Symlink(target, l.Name); err != nil {
				log.Printf("relink %s -> %s: %v", filepath.Join(dir, l.Name), target, err)
			}
		}
	}
}

func statOK(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func writeAccessoryIdentity(gadget string) error {
	if err := os.MkdirAll(filepath.Join(gadget, "strings/0x409"), 0755); err != nil {
		return err
	}
	for _, id := range identityFiles {
		if id.val == "" {
			continue
		}
		p := filepath.Join(gadget, id.rel)
		if err := os.WriteFile(p, []byte(id.val+"\n"), 0644); err != nil {
			return fmt.Errorf("write %s: %w", id.rel, err)
		}
	}
	// The head unit names the USB entry after the gadget product string, which
	// usb-moded builds from hw-release ("Jolla Jolla Phone (2026)"). The caller
	// sets accessoryProductName to the name the user gave the phone (Bluetooth
	// alias) so the wired and wireless entries carry the same, expected name.
	// It stays empty in tests, which keeps the phone's own strings.
	if accessoryProductName != "" {
		// The head unit shows '<manufacturer> <product>' for USB devices, so
		// put the whole name in the product and leave the manufacturer empty:
		// that displays exactly the Bluetooth name, which is what has to match
		// for the head unit to merge its USB and Bluetooth entries.
		prod := filepath.Join(gadget, "strings/0x409/product")
		if err := os.WriteFile(prod, []byte(accessoryProductName+"\n"), 0644); err != nil {
			log.Printf("product string %q: %v", accessoryProductName, err)
		}
		// configfs ignores an empty write, so the manufacturer stays the
		// phone's own ("Jolla") and the head unit shows 'Jolla Manolla': no
		// duplicated name, and the product still carries the user's name.
		man := filepath.Join(gadget, "strings/0x409/manufacturer")
		if accessoryVendor != "" {
			if err := os.WriteFile(man, []byte(accessoryVendor+"\n"), 0644); err != nil {
				log.Printf("manufacturer %q: %v", accessoryVendor, err)
			}
		}
		log.Printf("accessory name: product=%q manufacturer=%q", readTrim(prod), readTrim(man))
	}
	man := readTrim(filepath.Join(gadget, "strings/0x409/manufacturer"))
	prod := readTrim(filepath.Join(gadget, "strings/0x409/product"))
	ser := readTrim(filepath.Join(gadget, "strings/0x409/serialnumber"))
	log.Printf("accessory identity 18d1:2d00 manufacturer=%q product=%q serial=%q", man, prod, ser)
	return nil
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func restoreFiles(snap *gadgetSnap) {
	for _, f := range snap.Files {
		p := filepath.Join(snap.Gadget, f.Rel)
		if !f.Existed {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				log.Printf("remove %s: %v", p, err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(f.Data), 0644); err != nil {
			log.Printf("restore %s: %v", p, err)
		}
	}
}

func unbindGadget(gadget string) {
	_ = os.WriteFile(filepath.Join(gadget, "UDC"), []byte("\n"), 0644)
	time.Sleep(gadgetUnbindSettle)
}

// disconnectBound drops the pullup on the bound gadget with no settle sleep.
// Call it the moment AOA START is observed, before systemctl or FunctionFS
// setup, so the host sees the phone leave the bus inside that exchange.
func disconnectBound() {
	g, _, err := boundGadget()
	if err != nil || g == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(g, "UDC"), []byte("\n"), 0644); err != nil {
		log.Printf("disconnect %s: %v", filepath.Base(g), err)
		return
	}
	log.Printf("disconnected %s on AOA START", filepath.Base(g))
}

func linkFFSFunction(gadget string, snap *gadgetSnap) error {
	src := filepath.Join(gadget, "functions/ffs.aa")
	for _, c := range snap.Configs {
		link := filepath.Join(gadget, "configs", c.Name, "ffs.aa")
		if st, err := os.Lstat(link); err == nil && st.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(link)
		}
		if err := os.Symlink("../../functions/ffs.aa", link); err != nil {
			if err2 := os.Symlink(src, link); err2 != nil {
				return fmt.Errorf("link ffs into %s: %v / %v", c.Name, err, err2)
			}
		}
	}
	return nil
}

// adoptGadget unbinds gadget, parks usb-moded's function links, and attaches
// the accessory FunctionFS instance. The returned snap restores the previous
// identity and links. ep0 must stay open until restore.
func adoptGadget(gadget, ffsDir string) (*os.File, *gadgetSnap, error) {
	snap, err := captureGadget(gadget)
	if err != nil {
		return nil, nil, err
	}
	if err := saveGadgetSnap(snap); err != nil {
		log.Printf("save gadget snap: %v", err)
	}
	fmt.Printf("unbinding %s\n", filepath.Base(gadget))
	unbindGadget(gadget)
	if err := unlinkConfigLinks(gadget, snap.Configs); err != nil {
		return nil, snap, err
	}
	if err := writeAccessoryIdentity(gadget); err != nil {
		return nil, snap, err
	}
	ep0, err := setupFFS(gadget, ffsDir)
	if err != nil {
		return nil, snap, err
	}
	if err := linkFFSFunction(gadget, snap); err != nil {
		_ = ep0.Close()
		return nil, snap, err
	}
	if err := dropNonFFSLinks(gadget); err != nil {
		_ = ep0.Close()
		return nil, snap, err
	}
	fmt.Printf("adopted %s as accessory\n", filepath.Base(gadget))
	return ep0, snap, nil
}

// dropNonFFSLinks removes every function symlink except ffs.aa.
// accessory.gs2 has to stay on the ask gadget so GET_PROTOCOL works, and
// it brings a bulk pair plus an interrupt IN. A car that still sees that
// interface configures USB and then never sends the version request on
// the FunctionFS pipe. Call again immediately before binding: usb-moded
// can put the ask links back while it is stopping.
func dropNonFFSLinks(gadget string) error {
	configs, err := configNames(gadget)
	if err != nil {
		return err
	}
	var left []string
	for _, c := range configs {
		dir := filepath.Join(gadget, "configs", c)
		ents, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("read %s: %w", dir, err)
		}
		for _, e := range ents {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if e.Name() == "ffs.aa" {
				left = append(left, c+"/"+e.Name())
				continue
			}
			p := filepath.Join(dir, e.Name())
			log.Printf("accessory config: remove %s", c+"/"+e.Name())
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("unlink %s: %w", p, err)
			}
		}
	}
	if len(left) == 0 {
		return fmt.Errorf("accessory config has no ffs.aa link")
	}
	log.Printf("accessory config functions: %s", strings.Join(left, ", "))
	return nil
}

func setupFFS(gadget, ffsDir string) (*os.File, error) {
	if err := mustMkdir(filepath.Join(gadget, "functions/ffs.aa")); err != nil {
		return nil, fmt.Errorf("mkdir ffs.aa: %w", err)
	}
	if err := os.MkdirAll(ffsDir, 0755); err != nil {
		return nil, err
	}
	_ = unmountDetach(ffsDir)
	if err := mountFunctionFS("aa", ffsDir); err != nil {
		if _, err2 := os.Stat(filepath.Join(ffsDir, "ep0")); err2 != nil {
			return nil, fmt.Errorf("mount functionfs: %w", err)
		}
	}
	return ffs.SetupAccessory(ffsDir)
}

func restoreAdopted(snap *gadgetSnap, ffsDir string) {
	if snap == nil || snap.Gadget == "" {
		return
	}
	fmt.Printf("restoring gadget %s\n", filepath.Base(snap.Gadget))
	unbindGadget(snap.Gadget)
	for _, c := range snap.Configs {
		p := filepath.Join(snap.Gadget, "configs", c.Name, "ffs.aa")
		st, err := os.Lstat(p)
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(p)
		}
	}
	_ = unmountDetach(ffsDir)
	fn := filepath.Join(snap.Gadget, "functions/ffs.aa")
	if st, err := os.Lstat(fn); err == nil && st.IsDir() && st.Mode()&os.ModeSymlink == 0 {
		if err := os.Remove(fn); err != nil {
			log.Printf("rmdir %s: %v", fn, err)
		}
	}
	restoreFiles(snap)
	relinkConfigs(snap)
	_ = os.Chown(filepath.Join(snap.Gadget, "UDC"), 0, 0)
	_ = os.Remove(usbgadget.AdoptedFile)
	_ = os.Remove(gadgetSnapFile)
}

func saveGadgetSnap(snap *gadgetSnap) error {
	if err := os.MkdirAll(filepath.Dir(gadgetSnapFile), 0755); err != nil {
		return err
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return os.WriteFile(gadgetSnapFile, b, 0600)
}

func loadGadgetSnap() (*gadgetSnap, error) {
	b, err := os.ReadFile(gadgetSnapFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var snap gadgetSnap
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}
	if snap.Gadget == "" {
		return nil, nil
	}
	return &snap, nil
}

func publishAdoptedGadget(path string) error {
	if err := os.MkdirAll(filepath.Dir(usbgadget.AdoptedFile), 0755); err != nil {
		return err
	}
	return os.WriteFile(usbgadget.AdoptedFile, []byte(path+"\n"), 0644)
}
