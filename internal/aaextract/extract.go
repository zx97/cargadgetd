package aaextract

import (
	"archive/zip"
	"bytes"
	"crypto/x509"
	"fmt"
	"io"
)

type Result struct {
	LeafPEM      []byte
	CAPEM        []byte
	KeyPEM       []byte
	Salt         []byte
	EncryptedKey []byte
	Certificate  *x509.Certificate
}

func Extract(b []byte) (*Result, error) {
	switch {
	case isZip(b):
		return extractZip(b)
	case isDex(b):
		return fromDexes([][]byte{b})
	default:
		return nil, fmt.Errorf("not an apk")
	}
}

func extractZip(b []byte) (*Result, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	var dexes [][]byte
	for _, f := range zr.File {
		if !isDexName(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		buf, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		dexes = append(dexes, buf)
	}
	if len(dexes) == 0 {
		return nil, fmt.Errorf("apk contains no classes.dex")
	}
	return fromDexes(dexes)
}

func fromDexes(dexes [][]byte) (*Result, error) {
	var pems [][]byte
	var salts, encs [][]byte
	seenPEM := map[string]struct{}{}
	for _, dex := range dexes {
		for _, pem := range findPEMs(dex) {
			if _, ok := seenPEM[string(pem)]; ok {
				continue
			}
			seenPEM[string(pem)] = struct{}{}
			pems = append(pems, pem)
		}
		salts = append(salts, findByteArrays(dex, saltMin, saltMax)...)
		encs = append(encs, findByteArrays(dex, encMin, encMax)...)
	}
	salts = uniqueBytes(salts)
	encs = uniqueBytes(encs)
	return unwrapCandidates(pems, salts, encs)
}

func isDexName(name string) bool {
	return len(name) >= 4 && name[len(name)-4:] == ".dex"
}

func isZip(b []byte) bool {
	return len(b) >= 4 && bytes.Equal(b[:4], []byte{'P', 'K', 0x03, 0x04})
}

func isDex(b []byte) bool {
	return len(b) >= 8 && bytes.Equal(b[:4], []byte{'d', 'e', 'x', '\n'})
}
