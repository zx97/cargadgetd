package aaextract

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/rgh"
)

func TestExtractRejectsGarbage(t *testing.T) {
	if _, err := Extract([]byte("not an apk")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestOpenFollowsVersionDir(t *testing.T) {
	res, err := Extract(syntheticAPK(t))
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	dest := filepath.Join(base, "17.9.664044-release")
	if err := res.writeDir(dest); err != nil {
		t.Fatal(err)
	}
	if err := writeGearheadVersion(dest, Gearhead{VersionCode: 664044, VersionName: "17.9.664044-release"}); err != nil {
		t.Fatal(err)
	}
	m, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}
	if m.Path != dest {
		t.Fatalf("opened %s", m.Path)
	}

	empty := t.TempDir()
	_, err = Open(empty)
	if err == nil || !strings.Contains(err.Error(), empty) {
		t.Fatalf("missing cert: %v", err)
	}
}

func TestInstallAPKRejectsOtherNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"classes.dex", "base.apk.xz", "base.apk.xz.apk.bak"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("nope"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallAPK(path, filepath.Join(dir, "out")); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestInstallAPKAndDownload(t *testing.T) {
	apk := syntheticAPK(t)
	src := filepath.Join(t.TempDir(), "gearhead.apk")
	if err := os.WriteFile(src, apk, 0o644); err != nil {
		t.Fatal(err)
	}

	fromFile := t.TempDir()
	if _, err := InstallAPK(src, fromFile); err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, fromFile)

	so := filepath.Join(t.TempDir(), "reversegearhead.so")
	if err := os.WriteFile(so, []byte("plugin"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	got, err := installAPK(src, so, base)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(base, "local") {
		t.Fatalf("plugin path installed to %s", got)
	}
	assertInstalled(t, got)

	st, err := os.Stat(filepath.Join(fromFile, "android_auto.key"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %o", st.Mode().Perm())
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(apk)
	meta := metadataJSON(t, hex.EncodeToString(sum[:]), int64(len(apk)))
	good := signedSJSON(t, priv, meta)
	badSig := signedSJSON(t, mustKey(t), meta)
	badHash := signedSJSON(t, priv, metadataJSON(t, strings.Repeat("ab", 32), int64(len(apk))))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/metadata.1.0.sjson"):
			switch {
			case strings.Contains(r.URL.Path, "/bad-sig/"):
				w.Write(badSig)
			case strings.Contains(r.URL.Path, "/bad-hash/"):
				w.Write(badHash)
			default:
				w.Write(good)
			}
		case strings.HasSuffix(r.URL.Path, "/base.apk"):
			w.Write(apk)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	pubB64 := signifyBlob(append(append([]byte("Ed"), []byte("testid01")...), pub...))
	fromNet := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err = installDownload(ctx, fromNet, srv.URL, pubB64)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "test" {
		t.Fatalf("version dir %s", got)
	}
	assertInstalled(t, got)

	if _, err := installDownload(ctx, t.TempDir(), srv.URL+"/bad-sig", pubB64); err == nil {
		t.Fatal("bad signature was accepted")
	}
	if _, err := installDownload(ctx, t.TempDir(), srv.URL+"/bad-hash", pubB64); err == nil {
		t.Fatal("hash mismatch was accepted")
	}
}

func TestDownloadUsesNewerAPK(t *testing.T) {
	good := syntheticAPK(t)
	bad := zipWithoutDex(t)
	goodSum := sha256.Sum256(good)
	badSum := sha256.Sum256(bad)
	oldMeta := metadataReleases(t, []release{
		{code: 5, name: "old", hash: hex.EncodeToString(goodSum[:]), size: int64(len(good))},
	})
	both := metadataReleases(t, []release{
		{code: 9, name: "new", hash: hex.EncodeToString(goodSum[:]), size: int64(len(good))},
		{code: 5, name: "old", hash: hex.EncodeToString(goodSum[:]), size: int64(len(good))},
	})
	brokenNew := metadataReleases(t, []release{
		{code: 9, name: "new", hash: hex.EncodeToString(badSum[:]), size: int64(len(bad))},
		{code: 5, name: "old", hash: hex.EncodeToString(goodSum[:]), size: int64(len(good))},
	})

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var meta []byte
	var apkHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/metadata.1.0.sjson"):
			w.Write(signedSJSON(t, priv, meta))
		case strings.Contains(r.URL.Path, "/9/base.apk"):
			apkHits++
			if bytes.Equal(meta, brokenNew) {
				w.Write(bad)
				return
			}
			w.Write(good)
		case strings.Contains(r.URL.Path, "/5/base.apk"):
			apkHits++
			w.Write(good)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	pubB64 := signifyBlob(append(append([]byte("Ed"), []byte("testid01")...), pub...))
	ctx := context.Background()
	dir := t.TempDir()

	meta = oldMeta
	got, err := installDownload(ctx, dir, srv.URL, pubB64)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "old" {
		t.Fatalf("path %s", got)
	}
	if code, ok := readGearheadVersion(got); !ok || code != 5 {
		t.Fatalf("installed %d ok=%v", code, ok)
	}
	hits := apkHits
	again, err := installDownload(ctx, dir, srv.URL, pubB64)
	if err == nil || !strings.Contains(err.Error(), "already extracted") || again != got {
		t.Fatalf("second install: %s %v", again, err)
	}
	if apkHits != hits {
		t.Fatalf("downloaded the apk that was already extracted (%d -> %d)", hits, apkHits)
	}

	meta = both
	got, err = installDownload(ctx, dir, srv.URL, pubB64)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "new" {
		t.Fatalf("path %s", got)
	}
	if code, _ := readGearheadVersion(got); code != 9 {
		t.Fatalf("version %d", code)
	}

	meta = brokenNew
	if err := os.RemoveAll(filepath.Join(dir, "new")); err != nil {
		t.Fatal(err)
	}
	kept, err := installDownload(ctx, dir, srv.URL, pubB64)
	if err == nil || !strings.Contains(err.Error(), "new (9)") {
		t.Fatalf("broken newer: %v", err)
	}
	if filepath.Base(kept) != "old" {
		t.Fatalf("kept %s", kept)
	}
	assertInstalled(t, kept)

	fresh := t.TempDir()
	got, err = installDownload(ctx, fresh, srv.URL, pubB64)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "old" {
		t.Fatalf("fell back to %s", got)
	}
	if code, _ := readGearheadVersion(got); code != 5 {
		t.Fatalf("fell back to %d", code)
	}
}

type release struct {
	code int64
	name string
	hash string
	size int64
}

func metadataReleases(t *testing.T, rels []release) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"time":1792550001,"packages":{"com.google.android.projection.gearhead":{"variants":{`)
	for i, r := range rels {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"%d":{"versionCode":%d,"versionName":%q,"channel":"stable","apks":["base.apk"],"apkHashes":[%q],"apkSizes":[%d]}`,
			r.code, r.code, r.name, r.hash, r.size)
	}
	b.WriteString(`}}}}`)
	return []byte(b.String())
}

func zipWithoutDex(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("README")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("no dex")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func assertInstalled(t *testing.T, dir string) {
	t.Helper()
	text, err := Probe(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "CarService") || !strings.Contains(text, "key: matches leaf") {
		t.Fatalf("probe: %q", text)
	}
	if !Ready(dir) {
		t.Fatal("not ready")
	}
	m, err := rgh.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Probe != text {
		t.Fatalf("rgh probe %q\naaextract %q", m.Probe, text)
	}
	if m.ExpiresIn <= 0 {
		t.Fatalf("expires %d", m.ExpiresIn)
	}
	if !bytes.Contains(m.CA, []byte("BEGIN CERTIFICATE")) {
		t.Fatalf("ca missing")
	}
}

func syntheticAPK(t *testing.T) []byte {
	t.Helper()
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafPEM := certPEM(t, &leafKey.PublicKey, leafKey, false)
	caPEM := certPEM(t, &caKey.PublicKey, caKey, true)
	der, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	salt := bytes.Repeat([]byte{7}, 256)
	enc := wrapKey(t, leafPEM, caPEM, salt, der)
	if len(enc) < encMin || len(enc) > encMax {
		t.Fatalf("ciphertext length %d outside %d-%d", len(enc), encMin, encMax)
	}
	dex := appendAligned(nil, salt)
	dex = appendAligned(dex, enc)
	dex = append(dex, leafPEM...)
	dex = append(dex, caPEM...)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("classes.dex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(dex); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func certPEM(t *testing.T, pub any, priv any, isCA bool) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"CarService"}, CommonName: "aa-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if isCA {
		tmpl.Subject.CommonName = "aa-ca"
		tmpl.KeyUsage = x509.KeyUsageCertSign
		tmpl.SerialNumber = big.NewInt(2)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func wrapKey(t *testing.T, leafPEM, caPEM, salt, der []byte) []byte {
	t.Helper()
	mime := base64.StdEncoding.EncodeToString(der)
	plain := append(bytes.Repeat([]byte{0x11}, 28), []byte(mime)...)
	plain = append(plain, bytes.Repeat([]byte{0x22}, 26)...)
	plain = pkcs7Pad(plain, aes.BlockSize)
	state := make([]byte, 48)
	tweak(leafPEM, state, salt)
	tweak(caPEM, state, salt)
	for i := 0; i < 7; i++ {
		tweak(state, state, salt)
	}
	block, err := aes.NewCipher(state[:32])
	if err != nil {
		t.Fatal(err)
	}
	enc := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, state[32:48]).CryptBlocks(enc, plain)
	return enc
}

func pkcs7Pad(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	if n == 0 {
		n = blockSize
	}
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}

func appendAligned(buf, payload []byte) []byte {
	for len(buf)%4 != 0 {
		buf = append(buf, 0)
	}
	buf = append(buf, fillArrayHeader...)
	size := make([]byte, 4)
	binary.LittleEndian.PutUint32(size, uint32(len(payload)))
	buf = append(buf, size...)
	return append(buf, payload...)
}

func metadataJSON(t *testing.T, hash string, size int64) []byte {
	t.Helper()
	return []byte(`{"time":1792550001,"packages":{"com.google.android.projection.gearhead":{"variants":{"5":{` +
		`"versionCode":5,"versionName":"test","channel":"stable",` +
		`"apks":["base.apk"],"apkHashes":["` + hash + `"],"apkSizes":[` + strconv.FormatInt(size, 10) + `]}}}}}`)
}

func signedSJSON(t *testing.T, priv ed25519.PrivateKey, message []byte) []byte {
	t.Helper()
	sig := ed25519.Sign(priv, message)
	sigB64 := signifyBlob(append(append([]byte("Ed"), []byte("testid01")...), sig...))
	if len(sigB64) != 100 {
		t.Fatalf("signature line is %d bytes", len(sigB64))
	}
	out := append(append([]byte{}, message...), '\n')
	out = append(out, sigB64...)
	return append(out, '\n')
}

func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}
