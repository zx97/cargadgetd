// Command gadgetctl brings up the Android Auto USB gadget (ConfigFS + FunctionFS).
//
// Run as root (devel-su, or the Sailfish RPM’s setuid cargadgetd-gadget). It
// stops Sailfish usb-moded (if present), binds the accessory gadget, chmods
// FunctionFS endpoints so a normal user can run cargadgetd, then blocks until
// SIGINT/SIGTERM or USB unplug and restores usb-moded on the way out.
//
//	./gadgetctl                 # root / setuid (RPM: cargadgetd-gadget)
//	./gadgetctl -stop           # tear down a running agent
//	# other terminal, as defaultuser:
//	./cargadgetd -listen ffs:///dev/usb-ffs/aa
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/aoa"
	"github.com/cargadgetd/cargadgetd/internal/ffs"
	"github.com/cargadgetd/cargadgetd/internal/usbcable"
	"github.com/cargadgetd/cargadgetd/internal/wireless"
	"github.com/godbus/dbus/v5"
)

func main() {
	ffsOnly := flag.Bool("ffs-only", false, "only write FunctionFS descriptors (keeps ep0 open until signal)")
	stopFlag := flag.Bool("stop", false, "stop a running gadgetctl / cargadgetd-gadget (needs root/setuid)")
	usbOffFlag := flag.Bool("usb-off", false, "restore a borrowed gadget, start usb-moded, and leave USB alone")
	ffsDir := flag.String("ffs", "/dev/usb-ffs/aa", "FunctionFS mount dir")
	gadget := flag.String("gadget", "/sys/kernel/config/usb_gadget/aa", "configfs gadget path")
	allowUser := flag.String("user", "", "chown FFS nodes for this user (default: real uid / SUDO_USER / defaultuser)")
	noModed := flag.Bool("no-usb-moded", false, "do not touch usb-moded")
	aoaWait := flag.Bool("aoa", false, "wait for AOA START (Android Auto), then bind the accessory gadget")
	aoaService := flag.String("aoa-service", "", "start, stop, or restart cargadgetd-aoa.service (setuid)")
	wifiConnect := flag.String("wifi-connect", "", "connect a saved Wi-Fi service (setuid root) and exit")
	btAdapter := flag.String("bt-adapter", "", "power and expose a BlueZ adapter, e.g. /org/bluez/hci1 (setuid root) and exit")
	btPair := flag.String("bt-pair", "", "pair a head unit by Bluetooth address, answering its PIN (setuid root) and exit")
	btUnpair := flag.String("bt-unpair", "", "forget a Bluetooth bond (setuid root) and exit")
	btPin := flag.String("bt-pin", "0000", "PIN to send when the head unit asks for one")
	flag.Parse()

	// The car names the USB entry after the gadget product string: use the
	// name the user gave the phone (Bluetooth alias), not hw-release's.
	// godbus authenticates with the *real* uid, and a setuid helper still has
	// the user's ruid until becomeRoot, so the bus would reject the query.
	if os.Geteuid() == 0 {
		if err := becomeRoot(); err != nil {
			log.Printf("become root for name lookup: %v", err)
		}
	}
	accessoryVendor := wireless.Vendor()
	if bt, _, _ := wireless.LocalNames(); bt != "" {
		accessoryProductName = wireless.ModelFromAlias(bt, accessoryVendor)
	}
	log.Printf("accessory name: vendor=%q model=%q", accessoryVendor, accessoryProductName)

	if *btAdapter != "" {
		if os.Geteuid() != 0 {
			log.Fatal("gadgetctl -bt-adapter needs root (install setuid RPM or use devel-su)")
		}
		if err := becomeRoot(); err != nil {
			log.Fatalf("become root: %v", err)
		}
		if err := wireless.AdapterSetup(dbus.ObjectPath(*btAdapter)); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *btPair != "" || *btUnpair != "" {
		if os.Geteuid() != 0 {
			log.Fatal("gadgetctl -bt-pair/-bt-unpair needs root (install setuid RPM or use devel-su)")
		}
		if err := becomeRoot(); err != nil {
			log.Fatalf("become root: %v", err)
		}
		if *btUnpair != "" {
			if err := wireless.UnpairDevice(*btUnpair, log.Printf); err != nil {
				log.Fatal(err)
			}
			return
		}
		if err := wireless.PairDevice(*btPair, *btPin, log.Printf); err != nil {
			log.Fatalf("pairing %s: %v", *btPair, err)
		}
		return
	}

	if *wifiConnect != "" {
		if os.Geteuid() != 0 {
			log.Fatal("gadgetctl -wifi-connect needs root (install setuid RPM or use devel-su)")
		}
		if err := becomeRoot(); err != nil {
			log.Fatalf("become root: %v", err)
		}
		if err := wireless.ConnectWiFi(*wifiConnect); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *aoaService != "" {
		switch *aoaService {
		case "start", "stop", "restart":
		default:
			log.Fatalf("unknown -aoa-service %q (start, stop, or restart)", *aoaService)
		}
		if os.Geteuid() != 0 {
			log.Fatal("gadgetctl -aoa-service needs root (install setuid RPM or use devel-su)")
		}
		if err := becomeRoot(); err != nil {
			log.Fatalf("become root: %v", err)
		}
		cmd := exec.Command("systemctl", *aoaService, "cargadgetd-aoa.service")
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Fatalf("systemctl %s cargadgetd-aoa: %v (%s)", *aoaService, err, bytesTrim(out))
		}
		fmt.Printf("cargadgetd-aoa %s\n", *aoaService)
		return
	}

	if *stopFlag {
		if os.Geteuid() != 0 {
			log.Fatal("gadgetctl -stop needs root (install setuid RPM or use devel-su)")
		}
		if err := becomeRoot(); err != nil {
			log.Fatalf("become root: %v", err)
		}
		if err := stopRunningAgents(); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *usbOffFlag {
		if os.Geteuid() != 0 {
			log.Fatal("gadgetctl -usb-off needs root (install setuid RPM or use devel-su)")
		}
		if err := becomeRoot(); err != nil {
			log.Fatalf("become root: %v", err)
		}
		if err := usbOff(*ffsDir); err != nil {
			log.Fatal(err)
		}
		fmt.Println("USB off")
		return
	}

	if *ffsOnly {
		ep0, err := ffs.SetupAccessory(*ffsDir)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("ffs descriptors written; holding ep0")
		waitSignal()
		_ = ep0.Close()
		return
	}

	if os.Geteuid() != 0 {
		log.Fatal("gadgetctl must run as root (setuid package or devel-su); cargadgetd can then run as a normal user")
	}

	// Resolve FFS owner from the invoking user *before* we become full root.
	// Setuid leaves ruid=caller — util-linux mount/systemctl refuse ruid!=0.
	uid, gid, uname := resolveAllowUser(*allowUser)
	if err := becomeRoot(); err != nil {
		log.Fatalf("become root: %v", err)
	}
	fmt.Printf("ffs access for uid=%d gid=%d (%s)\n", uid, gid, uname)

	// usb-moded is stopped for the whole AOA wait, not only after START.
	// On cable insert it rewrites g1 (RNDIS, MTP, …) and drops accessory.gs2.
	// Putting that link back required an unbind, so the car saw the device
	// bounce and never finished GET_PROTOCOL. A Pixel disconnects inside
	// START; we clear the UDC before any further teardown so this does too.
	var stoppedModed string
	if *aoaWait {
		if !*noModed {
			stoppedModed = stopUSBModed()
			if stoppedModed != "" {
				fmt.Println("stopped", stoppedModed)
				time.Sleep(300 * time.Millisecond)
			}
		}
		if _, err := linkAskAccessory(); err != nil {
			log.Printf("ask accessory: %v", err)
		}
		fmt.Println("waiting for Android Auto accessory start")
		stop := make(chan struct{})
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sig
			close(stop)
		}()
		id, err := aoa.Wait(stop)
		signal.Stop(sig)
		if err != nil {
			// This exit is usually systemd stopping us. A blocking
			// systemctl start deadlocks that stop: systemd will not
			// run the start job until we exit, and we will not exit
			// until the start job finishes. --no-block queues it.
			if stoppedModed != "" {
				if serr := startServiceNoBlock(stoppedModed); serr != nil {
					log.Printf("restart %s: %v", stoppedModed, serr)
				} else {
					fmt.Println("restarted", stoppedModed)
				}
			}
			fmt.Println(err)
			os.Exit(0)
		}
		disconnectBound()
		fmt.Printf("AOA start model=%q manufacturer=%q serial=%q\n", id.Model, id.Manufacturer, id.Serial)
	} else if !*noModed {
		stoppedModed = stopUSBModed()
		if stoppedModed != "" {
			fmt.Println("stopped", stoppedModed)
			time.Sleep(300 * time.Millisecond)
		}
	}

	gpath := *gadget
	var snap *gadgetSnap
	owned := false // true only when we created the private aa gadget
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		fmt.Println("tearing down gadget…")
		if snap != nil {
			restoreAdopted(snap, *ffsDir)
		} else if owned {
			_ = cleanupGadget(gpath, *ffsDir)
		}
		if stoppedModed != "" {
			if err := startService(stoppedModed); err != nil {
				log.Printf("restart %s: %v", stoppedModed, err)
			} else {
				fmt.Println("restarted", stoppedModed)
			}
		}
	}
	// log.Fatal skips defers and would leave g1 rewritten and usb-moded stopped.
	fail := func(err error) {
		dump(gpath, *ffsDir)
		log.Printf("%v", err)
		restore()
		os.Exit(1)
	}
	defer restore()

	_ = exec.Command("modprobe", "libcomposite").Run()
	_ = exec.Command("rmmod", "g_ffs").Run()
	_ = mountConfigFS()

	if prev, err := loadGadgetSnap(); err != nil {
		log.Printf("load gadget snap: %v", err)
	} else if prev != nil {
		fmt.Printf("restoring leftover gadget %s\n", filepath.Base(prev.Gadget))
		restoreAdopted(prev, *ffsDir)
	}

	var ep0 *os.File
	var err error
	if adopted, ok := selectAdoptedGadget(*gadget); ok {
		gpath = adopted
		fmt.Printf("using existing gadget %s\n", gpath)
		if adopted != *gadget {
			if err := cleanupGadget(*gadget, *ffsDir); err != nil {
				log.Printf("cleanup leftover: %v", err)
			}
		}
		ep0, snap, err = adoptGadget(gpath, *ffsDir)
	} else {
		owned = true
		releaseForeignUDCs(*gadget)
		if err := cleanupGadget(*gadget, *ffsDir); err != nil {
			log.Printf("cleanup: %v", err)
		}
		ep0, err = prepareGadget(*gadget, *ffsDir)
	}
	if err != nil {
		fail(err)
	}
	defer ep0.Close()

	if err := exposeFFS(*ffsDir, uid, gid); err != nil {
		log.Printf("expose ffs: %v (cargadgetd may need root)", err)
	} else {
		fmt.Printf("ffs nodes mode 0660, owner %s\n", uname)
	}

	fmt.Println("gadget ready")
	if snap != nil {
		if err := dropNonFFSLinks(gpath); err != nil {
			fail(err)
		}
	}
	udc := firstUDC()
	if udc == "" {
		fail(fmt.Errorf("no UDC"))
	}
	_ = os.WriteFile(filepath.Join(gpath, "UDC"), []byte("\n"), 0644)
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(gpath, "UDC"), []byte(udc+"\n"), 0644); err != nil {
		fail(fmt.Errorf("bind UDC: %w", err))
	}
	fmt.Println("bound", udc)
	if *aoaWait {
		// Line-buffered on stderr so usb-aoa-launch.sh can start the MD.
		log.Printf("aoa bound %s", udc)
	}
	if snap != nil {
		if err := publishAdoptedGadget(gpath); err != nil {
			log.Printf("publish gadget path: %v", err)
		}
	}

	// Let cargadgetd (non-root) bounce the UDC on session death — a real USB
	// disconnect is the only way to tell the HU the AA session is gone.
	// JollaPhone2026's g1 is 0770 root:system, so chown of the UDC file alone
	// still returns EACCES (the MD cannot traverse the directory).
	exposeGadgetTraverse(gpath)
	if err := os.Chown(filepath.Join(gpath, "UDC"), uid, gid); err != nil {
		log.Printf("chown UDC: %v", err)
	}

	// Bind can recreate ep nodes as root; re-apply ownership.
	if err := exposeFFS(*ffsDir, uid, gid); err != nil {
		log.Printf("re-expose ffs: %v", err)
	}

	ents, _ := os.ReadDir(*ffsDir)
	fmt.Println("ffs nodes:")
	for _, e := range ents {
		fmt.Println(" ", e.Name())
	}
	fmt.Println()
	fmt.Println("gadget held — run as normal user:")
	fmt.Printf("  cargadgetd-md -listen ffs://%s\n", *ffsDir)
	fmt.Println("Ctrl-C here to unbind and restore usb-moded")

	// Hold ep0 open so FunctionFS stays alive — but do NOT read events here.
	// cargadgetd is the single ep0 event reader; two readers race and each
	// misses ~half the events (DISABLE/SUSPEND went unseen → writes into a
	// reset FIFO → truncated TLS record → DHU BAD_DECRYPT).
	_ = ep0
	waitSignal()
}

func waitSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	unplug := make(chan struct{})
	go func() {
		usbcable.WaitDisconnect(nil)
		close(unplug)
	}()
	select {
	case <-sig:
	case <-unplug:
		fmt.Println("USB cable disconnected — tearing down gadget")
	}
}

// becomeRoot sets ruid/euid to 0. Required for setuid installs: child helpers
// like util-linux mount check the *real* uid and refuse FunctionFS mounts.
func becomeRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("euid=%d", os.Geteuid())
	}
	if os.Getuid() == 0 {
		return nil
	}
	if err := syscall.Setgid(0); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}
	if err := syscall.Setuid(0); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}
	return nil
}

// stopRunningAgents SIGTERM/SIGKILLs other gadgetctl / cargadgetd-gadget
// processes (not this -stop invocation). Used by the setuid UI path.
func stopRunningAgents() error {
	self := os.Getpid()
	out, err := exec.Command("pgrep", "-af", `cargadgetd-gadget|gadgetctl`).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			fmt.Println("no gadget agent running")
			return nil
		}
		return fmt.Errorf("pgrep: %w", err)
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid == self {
			continue
		}
		joined := strings.Join(fields[1:], " ")
		if strings.Contains(joined, " -stop") {
			continue
		}
		if !strings.Contains(joined, "cargadgetd-gadget") && !strings.Contains(joined, "gadgetctl") {
			continue
		}
		pids = append(pids, pid)
	}
	if len(pids) == 0 {
		fmt.Println("no gadget agent running")
		return nil
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		alive := 0
		for _, pid := range pids {
			if err := syscall.Kill(pid, 0); err == nil {
				alive++
			}
		}
		if alive == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	fmt.Printf("stopped %d gadget agent(s)\n", len(pids))
	return nil
}

func resolveAllowUser(explicit string) (uid, gid int, name string) {
	name = explicit
	if name == "" {
		name = os.Getenv("SUDO_USER")
	}
	// setuid: prefer the invoking user's real uid over USER=root.
	if (name == "" || name == "root") && os.Getuid() != 0 {
		if u, err := user.LookupId(strconv.Itoa(os.Getuid())); err == nil {
			name = u.Username
		}
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	if name == "" || name == "root" {
		// Sailfish devel-su often leaves USER=root; prefer defaultuser if present.
		if _, err := user.Lookup("defaultuser"); err == nil {
			name = "defaultuser"
		}
	}
	if name != "" && name != "root" {
		if u, err := user.Lookup(name); err == nil {
			uid, _ = strconv.Atoi(u.Uid)
			gid, _ = strconv.Atoi(u.Gid)
			return uid, gid, name
		}
	}
	if v := os.Getenv("SUDO_UID"); v != "" {
		uid, _ = strconv.Atoi(v)
		gid, _ = strconv.Atoi(os.Getenv("SUDO_GID"))
		return uid, gid, name
	}
	return 0, 0, "root"
}

func stopUSBModed() string {
	for _, unit := range []string{"usb-moded", "usb_moded"} {
		if !serviceActive(unit) {
			continue
		}
		cmd := exec.Command("systemctl", "stop", unit)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("stop %s: %v (%s)", unit, err, bytesTrim(out))
			continue
		}
		return unit
	}
	return ""
}

func startService(unit string) error {
	return systemctl("start", unit)
}

// startServiceNoBlock queues the start and returns. Use it when this
// process is itself being stopped by systemd.
func startServiceNoBlock(unit string) error {
	return systemctl("start", "--no-block", unit)
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v (%s)", err, bytesTrim(out))
	}
	return nil
}

func serviceActive(unit string) bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", unit)
	return cmd.Run() == nil
}

func bytesTrim(b []byte) string {
	return strings.TrimSpace(string(b))
}

func releaseForeignUDCs(keepGadget string) {
	root := "/sys/kernel/config/usb_gadget"
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	keep := filepath.Base(keepGadget)
	for _, e := range ents {
		if !e.IsDir() || e.Name() == keep {
			continue
		}
		udcPath := filepath.Join(root, e.Name(), "UDC")
		cur, _ := os.ReadFile(udcPath)
		if strings.TrimSpace(string(cur)) == "" {
			continue
		}
		fmt.Printf("unbinding %s from UDC\n", e.Name())
		_ = os.WriteFile(udcPath, []byte("\n"), 0644)
	}
}

// exposeGadgetTraverse adds other-execute on the gadget directory so the
// unprivileged MD can open <gadget>/UDC after we chown that file.
func exposeGadgetTraverse(gadget string) {
	st, err := os.Stat(gadget)
	if err != nil {
		log.Printf("stat %s: %v", gadget, err)
		return
	}
	mode := st.Mode().Perm()
	if mode&0001 != 0 {
		return
	}
	if err := os.Chmod(gadget, mode|0001); err != nil {
		log.Printf("chmod %s: %v", gadget, err)
	}
}

func exposeFFS(ffsDir string, uid, gid int) error {
	_ = os.Chmod(ffsDir, 0755)
	// Sailfish (esp. Xperia) mounts /dev/usb-ffs as 0770 shell:shell — defaultuser
	// is not in that group, so they cannot traverse to aa/ep* even after chown.
	// Walk parents up to /dev and open execute bit for others.
	for p := filepath.Dir(ffsDir); p != "/" && p != "." && p != ""; p = filepath.Dir(p) {
		st, err := os.Stat(p)
		if err != nil {
			break
		}
		mode := st.Mode().Perm()
		if mode&0001 == 0 {
			_ = os.Chmod(p, mode|0005) // o+rx; keep existing owner/group bits
		}
		if p == "/dev" {
			break
		}
	}
	var first error
	ents, err := os.ReadDir(ffsDir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		p := filepath.Join(ffsDir, e.Name())
		if err := os.Chmod(p, 0660); err != nil && first == nil {
			first = err
		}
		if uid != 0 || gid != 0 {
			if err := os.Chown(p, uid, gid); err != nil && first == nil {
				first = err
			}
		}
	}
	// ep nodes can appear after descriptors; chmod again after a beat.
	time.Sleep(50 * time.Millisecond)
	for _, name := range []string{"ep0", "ep1", "ep2"} {
		p := filepath.Join(ffsDir, name)
		_ = os.Chmod(p, 0660)
		if uid != 0 || gid != 0 {
			_ = os.Chown(p, uid, gid)
		}
	}
	return first
}

func dump(gadget, ffsDir string) {
	fmt.Println("==== gadget dump ====")
	cmd := exec.Command("sh", "-c",
		"find "+gadget+" -maxdepth 4 -print 2>/dev/null; ls -la "+ffsDir+" 2>/dev/null; "+
			"echo UDC=$(cat "+gadget+"/UDC 2>/dev/null); ls /sys/class/udc; mount | grep -E 'configfs|functionfs'; cat /sys/kernel/debug/usb/devices 2>/dev/null | head -80")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func mustMkdir(path string) error {
	if err := os.Mkdir(path, 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	return nil
}

func cleanupGadget(gadget, ffsDir string) error {
	// Kill any leftover ep0 holders from older gadgetctl builds.
	_ = exec.Command("pkill", "-f", "gadgetctl.*-hold-ep0").Run()
	if _, err := os.Stat(gadget); err == nil {
		_ = os.WriteFile(filepath.Join(gadget, "UDC"), []byte("\n"), 0644)
		time.Sleep(100 * time.Millisecond)
		_ = os.Remove(filepath.Join(gadget, "configs/c.1/ffs.aa"))
		_ = os.Remove(filepath.Join(gadget, "configs/c.1/strings/0x409/configuration"))
		_ = os.Remove(filepath.Join(gadget, "configs/c.1/strings/0x409"))
		_ = os.Remove(filepath.Join(gadget, "configs/c.1/strings"))
		_ = os.Remove(filepath.Join(gadget, "configs/c.1/MaxPower"))
		_ = os.Remove(filepath.Join(gadget, "configs/c.1/bmAttributes"))
		_ = os.Remove(filepath.Join(gadget, "configs/c.1"))
		_ = os.Remove(filepath.Join(gadget, "functions/ffs.aa"))
		for _, s := range []string{"serialnumber", "manufacturer", "product"} {
			_ = os.Remove(filepath.Join(gadget, "strings/0x409", s))
		}
		_ = os.Remove(filepath.Join(gadget, "strings/0x409"))
		_ = os.Remove(filepath.Join(gadget, "strings"))
		_ = os.Remove(gadget)
	}
	_ = unmountDetach(ffsDir)
	return nil
}

func prepareGadget(gadget, ffsDir string) (*os.File, error) {
	if err := mustMkdir(gadget); err != nil {
		return nil, err
	}
	write := func(rel, val string) error {
		p := filepath.Join(gadget, rel)
		if err := os.WriteFile(p, []byte(val+"\n"), 0644); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
		return nil
	}
	for _, kv := range [][2]string{
		{"idVendor", "0x18d1"},
		{"idProduct", "0x2d00"},
		{"bcdDevice", "0x0100"},
		{"bcdUSB", "0x0200"},
	} {
		if err := write(kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	if err := mustMkdir(filepath.Join(gadget, "strings/0x409")); err != nil {
		return nil, err
	}
	_ = write("strings/0x409/serialnumber", "cargadgetd")
	_ = write("strings/0x409/manufacturer", "cargadgetd")
	_ = write("strings/0x409/product", "Android Auto MD")

	ep0, err := setupFFS(gadget, ffsDir)
	if err != nil {
		return nil, err
	}
	fmt.Println("ffs descriptors ok")

	if err := mustMkdir(filepath.Join(gadget, "configs/c.1")); err != nil {
		_ = ep0.Close()
		return nil, err
	}
	if err := mustMkdir(filepath.Join(gadget, "configs/c.1/strings/0x409")); err != nil {
		_ = ep0.Close()
		return nil, err
	}
	if err := write("configs/c.1/strings/0x409/configuration", "AA"); err != nil {
		_ = ep0.Close()
		return nil, err
	}
	if err := write("configs/c.1/MaxPower", "500"); err != nil {
		_ = ep0.Close()
		return nil, err
	}

	link := filepath.Join(gadget, "configs/c.1/ffs.aa")
	_ = os.Remove(link)
	cmd := exec.Command("ln", "-s", filepath.Join(gadget, "functions/ffs.aa"), link)
	if out, err := cmd.CombinedOutput(); err != nil {
		cmd2 := exec.Command("ln", "-s", "../../functions/ffs.aa", link)
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			_ = ep0.Close()
			return nil, fmt.Errorf("ln ffs into config: %v (%s) / %v (%s)", err, out, err2, out2)
		}
	}
	fmt.Println("config link ok")
	return ep0, nil
}

var udcClassPath = "/sys/class/udc"

func firstUDC() string {
	ents, err := os.ReadDir(udcClassPath)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		return e.Name()
	}
	return ""
}
