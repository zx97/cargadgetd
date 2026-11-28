package aaextract

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

var errNoPEM = errors.New("no PEM block")

func unwrapCandidates(pems, salts, encs [][]byte) (*Result, error) {
	if len(pems) == 0 {
		return nil, errors.New("no certificate PEM found in apk")
	}
	if len(salts) == 0 || len(encs) == 0 {
		return nil, errors.New("no salt/ciphertext byte arrays found in apk")
	}
	cas := caPEMs(pems)
	if len(cas) == 0 {
		return nil, errors.New("CA certificate not found in apk")
	}

	var got *Result
	tried := 0
	for _, salt := range salts {
		for _, enc := range encs {
			for _, ca := range cas {
				for _, leaf := range pems {
					if bytes.Equal(leaf, ca) {
						continue
					}
					tried++
					der, cert, err := unwrap(leaf, ca, salt, enc)
					if err != nil {
						continue
					}
					keyPEM := encodeKeyPEM(der)
					if got != nil {
						if bytes.Equal(got.KeyPEM, keyPEM) {
							continue
						}
						return nil, errors.New("multiple different private keys matched leaf certificates")
					}
					got = &Result{
						LeafPEM:      append([]byte(nil), leaf...),
						CAPEM:        append([]byte(nil), ca...),
						KeyPEM:       keyPEM,
						Salt:         salt,
						EncryptedKey: enc,
						Certificate:  cert,
					}
				}
			}
		}
	}
	if got == nil {
		return nil, fmt.Errorf("decrypted key did not match any leaf certificate (%d candidates)", tried)
	}
	return got, nil
}

func caPEMs(pems [][]byte) [][]byte {
	var marked, isCA [][]byte
	for _, p := range pems {
		if bytes.Contains(p, caMarker) {
			marked = append(marked, p)
			continue
		}
		c, err := parseCert(p)
		if err == nil && c.IsCA {
			isCA = append(isCA, p)
		}
	}
	if len(marked) > 0 {
		return marked
	}
	return isCA
}

func unwrap(leafPEM, caPEM, salt, enc []byte) ([]byte, *x509.Certificate, error) {
	if len(salt) == 0 || len(enc) == 0 || len(enc)%aes.BlockSize != 0 {
		return nil, nil, errors.New("bad salt or ciphertext")
	}
	state := make([]byte, 48)
	tweak(leafPEM, state, salt)
	tweak(caPEM, state, salt)
	for i := 0; i < 7; i++ {
		tweak(state, state, salt)
	}

	block, err := aes.NewCipher(state[:32])
	if err != nil {
		return nil, nil, err
	}
	plain := make([]byte, len(enc))
	cipher.NewCBCDecrypter(block, state[32:48]).CryptBlocks(plain, enc)
	plain, err = pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return nil, nil, err
	}
	if len(plain) <= 54 {
		return nil, nil, fmt.Errorf("decrypted blob too short: %d", len(plain))
	}
	der, err := decodeMIME(plain[28 : len(plain)-26])
	if err != nil {
		return nil, nil, err
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, nil, err
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("PKCS#8 is %T, not RSA", keyAny)
	}
	cert, err := parseCert(leafPEM)
	if err != nil {
		return nil, nil, err
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, nil, fmt.Errorf("leaf public key is %T, not RSA", cert.PublicKey)
	}
	if pub.N.Cmp(key.N) != 0 {
		return nil, nil, errors.New("modulus mismatch")
	}
	return der, cert, nil
}

func tweak(data, state, salt []byte) {
	for i := 0; i < len(data); i++ {
		for j := 0; j < 48; j++ {
			b := int(state[j])
			mixed := (((b >> 7) | (b + b)) + 33) ^ int(salt[j%len(salt)]) ^ int(data[i])
			state[j] = byte(mixed)
		}
	}
}

func pkcs7Unpad(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 || len(b)%blockSize != 0 {
		return nil, errors.New("invalid PKCS#7 padding")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > blockSize || n > len(b) {
		return nil, errors.New("invalid PKCS#7 padding")
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, errors.New("invalid PKCS#7 padding")
		}
	}
	return b[:len(b)-n], nil
}

func decodeMIME(b []byte) ([]byte, error) {
	var buf strings.Builder
	buf.Grow(len(b))
	for _, c := range b {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/', c == '=':
			buf.WriteByte(c)
		}
	}
	s := buf.String()
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.StdEncoding.DecodeString(s)
}

func encodeKeyPEM(der []byte) []byte {
	s := base64.StdEncoding.EncodeToString(der)
	var b strings.Builder
	b.Grow(len(s) + len(s)/64 + 64)
	b.WriteString("-----BEGIN PRIVATE KEY-----\n")
	line := 0
	for i := 0; i < len(s); i++ {
		b.WriteByte(s[i])
		line++
		if line == 64 {
			b.WriteByte('\n')
			line = 0
		}
	}
	b.WriteString("\n-----END PRIVATE KEY-----\n")
	return []byte(b.String())
}
