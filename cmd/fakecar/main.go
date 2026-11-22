// Command fakecar impersonates a head unit for the wireless Android Auto
// bootstrap, so the whole chain can be exercised without a car:
//
//	phone (MD)                    PC (fakecar)
//	  probe RFCOMM ch7  ─────────►  AA profile registered with BlueZ
//	  WifiInfoRequest   ─────────►  WifiInfoResponse (ssid/bssid/key)
//	                    ◄─────────  WifiStartRequest (ip:port)
//	  ConnectStatus     ─────────►
//	  TCP + TLS server  ◄────────►  TCP listener + TLS client + AA session
//
// The phone must be paired with this machine's Bluetooth adapter first, and the
// daemon must target it (-wireless-name <alias>). The AA SDP record this tool
// publishes is what makes the phone pick it.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/session/hupeer"
	aassl "github.com/cargadgetd/cargadgetd/internal/ssl"
	"github.com/cargadgetd/cargadgetd/internal/wireless"
)

func main() {
	channel := flag.Int("channel", 7, "RFCOMM channel to publish (VW MIB3 uses 7)")
	name := flag.String("name", "Fake VW", "service name in the SDP record")
	ssid := flag.String("ssid", "", "SSID to hand to the phone (default: current wlan)")
	key := flag.String("key", "", "Wi-Fi key to hand to the phone (optional)")
	port := flag.Int("port", 5288, "TCP port for the GAL session")
	iface := flag.String("iface", "", "interface whose IPv4 address is advertised (default: route to the peer)")
	phone := flag.String("phone", "", "phone IPv4, only used to pick the local address")
	wait := flag.Duration("wait", 3*time.Minute, "how long to wait for the phone")
	flag.Parse()

	logf := log.Printf

	// The phone opens the TCP session to the address we advertise. Pick the
	// local address it can reach (route towards the phone, or the default route).
	ip := *iface
	if ip != "" {
		addr, err := interfaceIPv4(ip)
		if err != nil {
			log.Fatalf("fakecar: %v", err)
		}
		ip = addr
	} else {
		ip = localIPv4(*phone)
	}
	if ip == "" {
		log.Fatalf("fakecar: cannot determine the address to advertise (use -iface or -phone)")
	}
	if *ssid == "" {
		*ssid = currentSSID()
	}
	logf("fakecar: advertising %s:%d, ssid=%q key_len=%d", ip, *port, *ssid, len(*key))

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("fakecar: listen :%d: %v", *port, err)
	}
	defer ln.Close()

	conns, err := wireless.RegisterAAProfileOnce(wireless.AAProfileOptions{
		Channel: uint16(*channel),
		Name:    *name,
	}, logf)
	if err != nil {
		log.Fatalf("fakecar: %v", err)
	}
	logf("fakecar: waiting for the phone (pair it to this machine's adapter first, then run the daemon with -wireless-name <alias>)")

	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			c, cerr := ln.Accept()
			if cerr != nil {
				return
			}
			accepted <- c
		}
	}()

	deadline := time.After(*wait)
	for {
		select {
		case f := <-conns:
			logf("fakecar: RFCOMM connected, running WPP")
			go runWPP(f, *ssid, *key, ip, *port, logf)
		case c := <-accepted:
			logf("fakecar: TCP session from %s, starting the HU half", c.RemoteAddr())
			runHU(c, logf)
		case <-deadline:
			logf("fakecar: %s elapsed, leaving", *wait)
			return
		}
	}
}

// runWPP plays the head unit on an RFCOMM stream: answer the info request with
// the Wi-Fi credentials, then tell the phone where to open the GAL session.
func runWPP(f *os.File, ssid, key, ip string, port int, logf func(string, ...any)) {
	defer f.Close()
	_ = f.SetDeadline(time.Now().Add(2 * time.Minute))
	for {
		pkt, err := wireless.ReadWPPPacket(f)
		if err != nil {
			logf("fakecar: wpp read: %v", err)
			return
		}
		logf("fakecar: wpp rx %s", wireless.DescribeWPP(pkt))
		switch pkt.ID {
		case wireless.WPPInfoRequest:
			if err := wireless.WriteWPPPacket(f, wireless.WPPInfoResponse,
				wireless.BuildWifiInfoResponse(ssid, "", key, 8, 1)); err != nil {
				logf("fakecar: wpp info response: %v", err)
				return
			}
			logf("fakecar: wpp tx id=%d ssid=%q", wireless.WPPInfoResponse, ssid)
			if err := wireless.WriteWPPPacket(f, wireless.WPPStartRequest,
				wireless.BuildStartRequest(ip, port)); err != nil {
				logf("fakecar: wpp start request: %v", err)
				return
			}
			logf("fakecar: wpp tx id=%d endpoint=%s:%d", wireless.WPPStartRequest, ip, port)
		case wireless.WPPVersionRequest:
			if err := wireless.WriteWPPPacket(f, wireless.WPPVersionResponse,
				wireless.BuildVersionResponse(1, 5)); err != nil {
				logf("fakecar: wpp version response: %v", err)
				return
			}
			logf("fakecar: wpp tx id=%d version 1.5", wireless.WPPVersionResponse)
		case wireless.WPPPingRequest:
			if err := wireless.WriteWPPPacket(f, wireless.WPPPingResponse,
				wireless.BuildPingResponse(0)); err != nil {
				logf("fakecar: wpp ping response: %v", err)
				return
			}
		case wireless.WPPConnectStatus:
			logf("fakecar: phone reports Wi-Fi connected; waiting for its TLS session")
		}
	}
}

// runHU runs the head unit half of the GAL session on an accepted TCP socket.
func runHU(c net.Conn, logf func(string, ...any)) {
	defer c.Close()
	certPEM, keyPEM, err := selfSigned()
	if err != nil {
		logf("fakecar: certificate: %v", err)
		return
	}
	ssl, err := aassl.NewClientPEM(certPEM, keyPEM)
	if err != nil {
		logf("fakecar: tls: %v", err)
		return
	}
	defer ssl.Close()
	hu := hupeer.New(c, ssl, log.New(os.Stderr, "hu: ", 0))
	if err := hu.Run(); err != nil {
		logf("fakecar: session ended: %v", err)
		return
	}
	if n, addr, methods := hu.BluetoothPairing(); n > 0 {
		logf("fakecar: session done, %d BluetoothPairingRequest, address=%s methods=%v", n, addr, methods)
		return
	}
	logf("fakecar: session done")
}

func interfaceIPv4(name string) (string, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return "", err
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.String(), nil
		}
	}
	return "", fmt.Errorf("no IPv4 on %s", name)
}

// localIPv4 returns the local address the kernel would use to reach peer, or
// the first non-loopback IPv4 when peer is empty.
func localIPv4(peer string) string {
	if peer != "" {
		if c, err := net.Dial("udp", net.JoinHostPort(peer, "9")); err == nil {
			addr := c.LocalAddr().(*net.UDPAddr).IP.String()
			c.Close()
			return addr
		}
	}
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}
