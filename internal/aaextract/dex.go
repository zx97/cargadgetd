package aaextract

import (
	"bytes"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
)

const (
	saltMin = 240
	saltMax = 280
	encMin  = 1400
	encMax  = 2200
)

var fillArrayHeader = []byte{0x00, 0x03, 0x01, 0x00}

var (
	pemBegin = []byte("-----BEGIN CERTIFICATE-----")
	pemEnd   = []byte("-----END CERTIFICATE-----")
	caMarker = []byte("AMFO56WkVE1C") // donotstealoklol
)

func findPEMs(data []byte) [][]byte {
	var out [][]byte
	off := 0
	for {
		i := bytes.Index(data[off:], pemBegin)
		if i < 0 {
			break
		}
		start := off + i
		rel := bytes.Index(data[start:], pemEnd)
		if rel < 0 {
			break
		}
		end := start + rel + len(pemEnd)
		if end < len(data) && data[end] == '\r' {
			end++
		}
		if end < len(data) && data[end] == '\n' {
			end++
		}
		pemBytes := append([]byte(nil), data[start:end]...)
		off = end
		if _, err := parseCert(pemBytes); err != nil {
			continue
		}
		out = append(out, pemBytes)
	}
	return out
}

func findByteArrays(data []byte, min, max int) [][]byte {
	var out [][]byte
	seen := map[string]struct{}{}
	off := 0
	for off+8 <= len(data) {
		i := bytes.Index(data[off:], fillArrayHeader)
		if i < 0 {
			break
		}
		pos := off + i
		off = pos + 1
		if pos%4 != 0 || pos+8 > len(data) {
			continue
		}
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		if size < min || size > max {
			continue
		}
		end := pos + 8 + size
		if end > len(data) {
			continue
		}
		blob := data[pos+8 : end]
		if _, ok := seen[string(blob)]; ok {
			continue
		}
		seen[string(blob)] = struct{}{}
		out = append(out, append([]byte(nil), blob...))
	}
	return out
}

func uniqueBytes(in [][]byte) [][]byte {
	var out [][]byte
	seen := map[string]struct{}{}
	for _, b := range in {
		if _, ok := seen[string(b)]; ok {
			continue
		}
		seen[string(b)] = struct{}{}
		out = append(out, b)
	}
	return out
}

func parseCert(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errNoPEM
	}
	return x509.ParseCertificate(block.Bytes)
}
