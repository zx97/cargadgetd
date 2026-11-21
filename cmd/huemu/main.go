// Command huemu is a tiny Android Auto head unit emulator: it connects to a
// phone-side MD over TCP, runs the HU half of the protocol (TLS client, version
// exchange, service discovery, channel opens) and answers the Bluetooth
// pairing request.
//
// It reuses the same peer the unit tests use, so it validates the real MD
// without a car, without adb and without the Android SDK:
//
//	# on the phone
//	cargadgetd-md -listen tcp://127.0.0.1:5277 -control tcp://127.0.0.1:9279 \
//	    -certs /usr/lib/cargadgetd/plugins/reversegearhead.so
//	# on the PC, tunnel the port and run the emulator
//	ssh -N -L 5277:127.0.0.1:5277 defaultuser@<phone>
//	huemu -addr 127.0.0.1:5277 -wait 30s
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/session/hupeer"
	aassl "github.com/cargadgetd/cargadgetd/internal/ssl"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5277", "MD address (host:port)")
	wait := flag.Duration("wait", 30*time.Second, "how long to keep the session open")
	flag.Parse()

	conn, err := net.DialTimeout("tcp", *addr, 5*time.Second)
	if err != nil {
		log.Fatalf("huemu: dial %s: %v", *addr, err)
	}
	defer conn.Close()
	log.Printf("huemu: connected to %s", *addr)

	certPEM, keyPEM, err := selfSigned()
	if err != nil {
		log.Fatalf("huemu: certificate: %v", err)
	}
	ssl, err := aassl.NewClientPEM(certPEM, keyPEM)
	if err != nil {
		log.Fatalf("huemu: tls: %v", err)
	}
	defer ssl.Close()

	hu := hupeer.New(conn, ssl, log.New(os.Stderr, "hu: ", 0))
	done := make(chan error, 1)
	go func() { done <- hu.Run() }()

	deadline := time.After(*wait)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last := 0
	for {
		select {
		case err := <-done:
			if err != nil {
				// A closed socket at the end of the session is normal.
				log.Printf("huemu: session ended: %v", err)
			}
			return
		case <-deadline:
			if n, address, methods := hu.BluetoothPairing(); n > 0 {
				log.Printf("huemu: %d BluetoothPairingRequest, address=%s methods=%v", n, address, methods)
			}
			log.Printf("huemu: %s elapsed, leaving", *wait)
			return
		case <-tick.C:
			if n, address, methods := hu.BluetoothPairing(); n != last {
				last = n
				log.Printf("huemu: BluetoothPairingRequest #%d address=%s methods=%v (answered 0x8002 already_paired=true)",
					n, address, methods)
			}
		}
	}
}

// selfSigned builds an in-memory certificate for the HU side of the TLS
// handshake; the MD does not require a specific issuer.
func selfSigned() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "huemu"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
	return certPEM, keyPEM, nil
}
