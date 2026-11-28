package aaextract

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

const (
	grapheneBaseURL    = "https://apps.grapheneos.org"
	graphenePackage    = "com.google.android.projection.gearhead"
	grapheneKeyVersion = "0"
	graphenePublicKey  = "RWQtZwEu1br1lMh911L3yPOs97cQb9LOks/ALBbqGl21ul695ocWR/ir"
	grapheneMinTime    = 1_792_550_000
	graphenePkgFile    = "base.apk"
)

type Gearhead struct {
	VersionName string
	VersionCode int64
	Channel     string
	SHA256      string
	Size        int64
	URL         string
}

func LatestGearhead(ctx context.Context) (*Gearhead, error) {
	return latestGearhead(ctx, grapheneBaseURL, graphenePublicKey)
}

func latestGearhead(ctx context.Context, baseURL, pubKey string) (*Gearhead, error) {
	list, err := listGearheads(ctx, baseURL, pubKey)
	if err != nil {
		return nil, err
	}
	return &list[0], nil
}

func listGearheads(ctx context.Context, baseURL, pubKey string) ([]Gearhead, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	url := fmt.Sprintf("%s/metadata.1.%s.sjson", baseURL, grapheneKeyVersion)
	blob, err := httpGet(ctx, url, 8<<20)
	if err != nil {
		return nil, err
	}
	message, sig, err := splitSJSON(blob)
	if err != nil {
		return nil, err
	}
	if err := verifySignify(pubKey, sig, message); err != nil {
		return nil, fmt.Errorf("metadata signature: %w", err)
	}
	return stableGearheads(message, baseURL)
}

func (g *Gearhead) Download(ctx context.Context) ([]byte, error) {
	if g.Size <= 0 || g.Size > 80<<20 {
		return nil, fmt.Errorf("unexpected base.apk size %d", g.Size)
	}
	body, err := httpGet(ctx, g.URL, g.Size)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != g.Size {
		return nil, fmt.Errorf("base.apk is %d bytes, metadata says %d", len(body), g.Size)
	}
	sum := sha256.Sum256(body)
	want, err := hex.DecodeString(g.SHA256)
	if err != nil || len(want) != sha256.Size {
		return nil, fmt.Errorf("metadata sha256 %q", g.SHA256)
	}
	if subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return nil, errors.New("base.apk sha256 does not match signed metadata")
	}
	return body, nil
}

func gearheadFromMetadata(message []byte, baseURL string) (*Gearhead, error) {
	list, err := stableGearheads(message, baseURL)
	if err != nil {
		return nil, err
	}
	return &list[0], nil
}

func stableGearheads(message []byte, baseURL string) ([]Gearhead, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	var idx struct {
		Time     int64 `json:"time"`
		Packages map[string]struct {
			Variants map[string]struct {
				VersionCode int64    `json:"versionCode"`
				VersionName string   `json:"versionName"`
				Channel     string   `json:"channel"`
				APKs        []string `json:"apks"`
				APKHashes   []string `json:"apkHashes"`
				APKSizes    []int64  `json:"apkSizes"`
			} `json:"variants"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(message, &idx); err != nil {
		return nil, fmt.Errorf("metadata json: %w", err)
	}
	if idx.Time < grapheneMinTime {
		return nil, fmt.Errorf("metadata timestamp %d is older than %d", idx.Time, grapheneMinTime)
	}
	pkg, ok := idx.Packages[graphenePackage]
	if !ok {
		return nil, fmt.Errorf("metadata has no %s", graphenePackage)
	}
	var list []Gearhead
	for _, v := range pkg.Variants {
		if v.Channel != "stable" {
			continue
		}
		hash, size, err := baseAPK(v.APKs, v.APKHashes, v.APKSizes)
		if err != nil {
			return nil, err
		}
		list = append(list, Gearhead{
			VersionName: v.VersionName,
			VersionCode: v.VersionCode,
			Channel:     v.Channel,
			SHA256:      hash,
			Size:        size,
			URL: fmt.Sprintf("%s/packages/%s/%d/%s",
				baseURL, graphenePackage, v.VersionCode, graphenePkgFile),
		})
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no stable %s in metadata", graphenePackage)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].VersionCode > list[j].VersionCode
	})
	return list, nil
}

func baseAPK(names, hashes []string, sizes []int64) (string, int64, error) {
	if len(names) != len(hashes) || len(names) != len(sizes) {
		return "", 0, errors.New("metadata apk lists have different lengths")
	}
	for i, name := range names {
		if name != graphenePkgFile {
			continue
		}
		if len(hashes[i]) != sha256.Size*2 {
			return "", 0, fmt.Errorf("base.apk hash %q", hashes[i])
		}
		return hashes[i], sizes[i], nil
	}
	return "", 0, errors.New("metadata variant has no base.apk")
}

func splitSJSON(b []byte) ([]byte, string, error) {
	const sigLen = 100
	const tail = 1 + sigLen + 1
	if len(b) <= tail || b[len(b)-1] != '\n' || b[len(b)-tail] != '\n' {
		return nil, "", errors.New("metadata is not signed sjson")
	}
	sig := string(b[len(b)-sigLen-1 : len(b)-1])
	if strings.Contains(sig, "\n") {
		return nil, "", errors.New("metadata signature is not a single line")
	}
	return b[:len(b)-tail], sig, nil
}

func verifySignify(pubB64, sigB64 string, message []byte) error {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	const (
		pubSize = 42
		sigSize = 74
		idEnd   = 10
	)
	if len(pub) != pubSize || string(pub[:2]) != "Ed" {
		return errors.New("public key is not a signify Ed25519 key")
	}
	if len(sig) != sigSize || string(sig[:2]) != "Ed" {
		return errors.New("signature is not a signify Ed25519 signature")
	}
	if subtle.ConstantTimeCompare(pub[2:idEnd], sig[2:idEnd]) != 1 {
		return errors.New("signature key id does not match the pinned public key")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub[idEnd:]), message, sig[idEnd:]) {
		return errors.New("rejected")
	}
	return nil
}

func httpGet(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", url, limit)
	}
	return body, nil
}
