//go:build linux

package rgh

import (
	"crypto/rsa"
	"crypto/x509"
	pemx "encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// openDir loads a pair extracted from the Android Auto APK: the layout
// aaextract installs, one directory per AA version, with android_auto.crt,
// android_auto.key and (optionally) the CA beside them.
//
// Linux only because the .so path needs dlopen; this half is plain Go.
func openDir(path string) (*Material, error) {
	certPEM, err := os.ReadFile(filepath.Join(path, "android_auto.crt"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, missingErr(path)
		}
		return nil, &LoadError{Path: path, Err: err}
	}
	keyPEM, err := os.ReadFile(filepath.Join(path, "android_auto.key"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, missingErr(path)
		}
		return nil, &LoadError{Path: path, Err: err}
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, missingErr(path)
	}
	probe, exp, err := describe(certPEM, keyPEM)
	if err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}
	m := &Material{Path: path, Cert: certPEM, Key: keyPEM, Probe: probe, ExpiresIn: exp}
	if ca, err := os.ReadFile(filepath.Join(path, "ca.crt")); err == nil {
		m.CA = ca
	}
	return m, nil
}

// describe checks that the key matches the certificate and reports the pair the
// way the status page shows it.
func describe(certPEM, keyPEM []byte) (string, int64, error) {
	block, _ := pemx.Decode(certPEM)
	if block == nil {
		return "", 0, errors.New("android_auto.crt is not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", 0, fmt.Errorf("android_auto.crt: %w", err)
	}
	kblock, _ := pemx.Decode(keyPEM)
	if kblock == nil {
		return "", 0, errors.New("android_auto.key is not PEM")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(kblock.Bytes)
	if err != nil {
		// Some extractions are PKCS#1; accept both so a hand-made pair works.
		if k1, e1 := x509.ParsePKCS1PrivateKey(kblock.Bytes); e1 == nil {
			keyAny = k1
		} else {
			return "", 0, fmt.Errorf("android_auto.key: %w", err)
		}
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return "", 0, fmt.Errorf("android_auto.key is %T, not RSA", keyAny)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return "", 0, fmt.Errorf("leaf public key is %T, not RSA", cert.PublicKey)
	}
	if pub.N.Cmp(key.N) != 0 {
		return "", 0, errors.New("private key does not match the leaf certificate")
	}
	exp := int64(time.Until(cert.NotAfter).Seconds())
	if exp < 0 {
		exp = 0
	}
	// Same shape as the plugin's reversegearhead_probe, so the status page and
	// the tests see one format whatever the source.
	text := fmt.Sprintf("subject: %s\nnotAfter: %s\nkey: matches leaf\n",
		cert.Subject.String(), cert.NotAfter.UTC().Format(time.RFC3339))
	return text, exp, nil
}

// isDir reports whether path is a directory holding an extracted pair.
func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// looksLikePair reports whether a directory has the files openDir wants.
func looksLikePair(path string) bool {
	for _, name := range []string{"android_auto.crt", "android_auto.key"} {
		if _, err := os.Stat(filepath.Join(path, name)); err != nil {
			return false
		}
	}
	return true
}
