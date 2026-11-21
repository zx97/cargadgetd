// Command sdpprobe asks a Bluetooth device for its Android Auto SDP record and
// prints the RFCOMM channel it advertises.
//
// It needs an AF_BLUETOOTH socket, so run it as root on a Linux host with a
// Bluetooth adapter. Useful to learn a head unit's channel when the phone
// cannot reach it.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/cargadgetd/cargadgetd/internal/wireless"
)

func main() {
	addr := flag.String("addr", "", "Bluetooth address of the device (aa:bb:cc:dd:ee:ff)")
	flag.Parse()
	if *addr == "" {
		flag.Usage()
		os.Exit(2)
	}
	ch, err := wireless.SDPAAChannel(*addr, log.Printf)
	if err != nil {
		log.Fatalf("sdp: %v", err)
	}
	fmt.Printf("Android Auto RFCOMM channel: %d\n", ch)
}
