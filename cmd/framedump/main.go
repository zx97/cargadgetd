// Command framedump renders one shell frame to a PNG, without a head unit.
//
// The wireless session draws our own shell (the bench unit shows its
// background but no tiles), so the picture we actually produce has to be
// looked at rather than inferred from the encoder's byte counts:
//
//	framedump -out /home/defaultuser/frame.png -w 1280 -h 480
package main

import (
	"flag"
	"image"
	"image/png"
	"log"
	"os"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/shell"
)

func main() {
	w := flag.Int("w", 1280, "frame width (the UI width the unit negotiated)")
	h := flag.Int("h", 480, "frame height")
	wait := flag.Duration("wait", 5*time.Second, "how long to let the apps load before drawing")
	out := flag.String("out", "/home/defaultuser/frame.png", "PNG to write")
	shellID := flag.String("shell", "", "shell plugin id (default: the configured one)")
	app := flag.String("app", "", "open this app before drawing (id from the plugin name)")
	flag.Parse()

	ui := shell.New(*w, *h, 48000, 1)
	ui.SetFramePeriod(33 * time.Millisecond)
	ui.SetDropFrames(0)
	if err := ui.OpenShell(shell.PluginDir, *shellID); err != nil {
		log.Printf("shell: %v", err)
	}
	if ids := shell.Load(ui, []string{shell.PluginDir}); len(ids) > 0 {
		log.Printf("apps: %v", ids)
	} else {
		log.Printf("no app plugin in %s", shell.PluginDir)
	}
	if !ui.WaitLoad(*wait) {
		log.Printf("apps not ready after %v, drawing anyway", *wait)
	}
	log.Printf("ready with: %v", ui.AppIDs())
	// The daemon always starts the apps; without this the shell keeps its
	// "No apps" home screen.
	ui.StartApps()
	if *app != "" {
		time.Sleep(500 * time.Millisecond)
	}

	// Draw, let the plugin paint on its own thread, then draw again: the frame
	// is copied from the plugin, never rendered here.
	img := image.NewRGBA(image.Rect(0, 0, *w, *h))
	for i := 0; i < 3; i++ {
		ui.Draw(img, time.Now())
		time.Sleep(400 * time.Millisecond)
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create %s: %v", *out, err)
	}
	if err := png.Encode(f, img); err != nil {
		log.Fatalf("encode: %v", err)
	}
	f.Close()
	log.Printf("wrote %s (%dx%d)", *out, *w, *h)
}
