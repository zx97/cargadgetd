package aaextract

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cargadgetd/cargadgetd/internal/rgh"
)

func InstallDownload(ctx context.Context, dir string) (string, error) {
	return installDownload(ctx, rgh.ResolvePath(dir), grapheneBaseURL, graphenePublicKey)
}

func ActiveDir(base string) string {
	base = rgh.ResolvePath(base)
	_, dir := newestExtracted(base)
	if dir != "" {
		return dir
	}
	if Ready(base) {
		return base
	}
	return ""
}

func Open(base string) (*rgh.Material, error) {
	dir := ActiveDir(base)
	if dir == "" {
		dir = base
	}
	return rgh.Open(dir)
}

func installDownload(ctx context.Context, base, baseURL, pubKey string) (string, error) {
	list, err := listGearheads(ctx, baseURL, pubKey)
	if err != nil {
		return "", err
	}
	have, haveDir := newestExtracted(base)
	var newer []Gearhead
	for _, g := range list {
		if haveDir == "" || g.VersionCode > have {
			newer = append(newer, g)
		}
	}
	if len(newer) == 0 {
		cur := list[0]
		return haveDir, fmt.Errorf("Android Auto %s (%d) is already extracted", cur.VersionName, cur.VersionCode)
	}
	var last error
	for _, g := range newer {
		dest := filepath.Join(base, versionDirName(g))
		apk, err := g.Download(ctx)
		if err != nil {
			last = err
			continue
		}
		res, err := Extract(apk)
		if err != nil {
			last = fmt.Errorf("%s (%d): %w", g.VersionName, g.VersionCode, err)
			continue
		}
		if err := res.writeDir(dest); err != nil {
			return "", err
		}
		if err := writeGearheadVersion(dest, g); err != nil {
			return "", err
		}
		return dest, nil
	}
	return haveDir, last
}

func InstallAPK(path, dir string) (string, error) {
	return installAPK(path, dir, rgh.ResolvePath(rgh.ConfigDefault))
}

func installAPK(path, dir, base string) (string, error) {
	if !strings.HasSuffix(strings.ToLower(path), ".apk") {
		return "", fmt.Errorf("not an apk: %s", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !isZip(b) {
		return "", fmt.Errorf("not an apk: %s", path)
	}
	res, err := Extract(b)
	if err != nil {
		return "", err
	}
	dest := dir
	if !certDirUsable(dir) {
		dest = filepath.Join(base, "local")
	}
	if err := res.writeDir(dest); err != nil {
		return "", err
	}
	if err := writeGearheadVersion(dest, Gearhead{}); err != nil {
		return "", err
	}
	return dest, nil
}

func certDirUsable(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" || strings.HasSuffix(strings.ToLower(dir), ".so") {
		return false
	}
	st, err := os.Stat(dir)
	return err != nil || st.IsDir()
}

func versionDirName(g Gearhead) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.' || r == '_' || r == '+' || r == '-':
			return r
		default:
			return -1
		}
	}, g.VersionName)
	name = strings.Trim(name, "._-")
	if name == "" || name == "." || name == ".." {
		return strconv.FormatInt(g.VersionCode, 10)
	}
	return name
}

func newestExtracted(base string) (int64, string) {
	var best int64
	var bestDir string
	consider := func(dir string) {
		code, ok := readGearheadVersion(dir)
		if !ok || !Ready(dir) {
			return
		}
		if bestDir == "" || code > best || (code == best && bestDir == base && dir != base) {
			best = code
			bestDir = dir
		}
	}
	consider(base)
	entries, err := os.ReadDir(base)
	if err != nil {
		return best, bestDir
	}
	for _, e := range entries {
		if e.IsDir() {
			consider(filepath.Join(base, e.Name()))
		}
	}
	return best, bestDir
}

func Probe(dir string) (string, error) {
	leaf, key, _, err := readDir(dir)
	if err != nil {
		return "", err
	}
	text, _, err := describe(leaf, key)
	return text, err
}

func writeGearheadVersion(dir string, g Gearhead) error {
	text := fmt.Sprintf("versionCode: %d\nversionName: %s\n", g.VersionCode, g.VersionName)
	return os.WriteFile(filepath.Join(dir, "gearhead.version"), []byte(text), 0o644)
}

func readGearheadVersion(dir string) (int64, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "gearhead.version"))
	if err != nil {
		return 0, false
	}
	var code int64
	var name string
	_, err = fmt.Sscanf(string(b), "versionCode: %d\nversionName: %s\n", &code, &name)
	if err != nil || code <= 0 {
		return 0, false
	}
	return code, true
}

func Ready(dir string) bool {
	_, err := Probe(dir)
	return err == nil
}

func (r *Result) writeDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"android_auto.crt", r.LeafPEM, 0o644},
		{"android_auto.key", r.KeyPEM, 0o600},
		{"ca.crt", r.CAPEM, 0o644},
	}
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, f.data, f.mode); err != nil {
			return err
		}
	}
	return nil
}

func readDir(dir string) (leaf, key, ca []byte, err error) {
	leaf, err = os.ReadFile(filepath.Join(dir, "android_auto.crt"))
	if err != nil {
		return nil, nil, nil, err
	}
	key, err = os.ReadFile(filepath.Join(dir, "android_auto.key"))
	if err != nil {
		return nil, nil, nil, err
	}
	ca, err = os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, nil, err
	}
	return leaf, key, ca, nil
}

func describe(certPEM, keyPEM []byte) (string, int64, error) {
	cert, err := parseCert(certPEM)
	if err != nil {
		return "", 0, fmt.Errorf("android_auto.crt: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return "", 0, errors.New("android_auto.key is not PEM")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", 0, fmt.Errorf("android_auto.key: %w", err)
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
	text := fmt.Sprintf("subject: %s\nnotAfter: %s\nkey: matches leaf\n",
		cert.Subject.String(), cert.NotAfter.UTC().Format(time.RFC3339))
	return text, exp, nil
}
