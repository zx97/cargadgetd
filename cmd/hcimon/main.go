// Command hcimon records the Bluetooth HCI traffic into a btsnoop file.
//
// Sailfish ships neither btmon nor hcidump, and the interesting question —
// what a head unit actually sends when it tries to reach the phone's Android
// Auto RFCOMM profile — can only be answered from the controller's own view.
// This opens the kernel's HCI monitor channel (root) and writes btsnoop v1, the
// format captures/cargadgetd/btsnoop_dump.py reads:
//
//	sudo ./hcimon -seconds 120 -out /home/defaultuser/hci.btsnoop
//
// The daemon exposes the same thing as a button on its status page, through the
// setuid helper, so no shell is needed on the phone.
package main

import (
	"flag"
	"log"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/btcapture"
)

func main() {
	seconds := flag.Int("seconds", 120, "how long to record")
	out := flag.String("out", "/home/defaultuser/hci.btsnoop", "btsnoop file to write")
	flag.Parse()

	start := time.Now()
	n, err := btcapture.Record(*out, time.Duration(*seconds)*time.Second)
	if err != nil {
		log.Fatalf("%v", err)
	}
	log.Printf("%d records in %s -> %s", n, time.Since(start).Round(time.Millisecond), *out)
}
