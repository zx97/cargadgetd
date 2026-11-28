package aaextract

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSignifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID := []byte("testid01")
	message := []byte(`{"time":1792550001,"packages":{}}`)
	sig := ed25519.Sign(priv, message)
	pubB64 := signifyBlob(append(append([]byte("Ed"), keyID...), pub...))
	sigB64 := signifyBlob(append(append([]byte("Ed"), keyID...), sig...))
	blob := append(append(append([]byte{}, message...), '\n'), []byte(sigB64+"\n")...)

	gotMsg, gotSig, err := splitSJSON(blob)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySignify(pubB64, gotSig, gotMsg); err != nil {
		t.Fatal(err)
	}
	gotMsg[0] ^= 0x01
	if err := verifySignify(pubB64, gotSig, gotMsg); err == nil {
		t.Fatal("tampered metadata verified")
	}
}

func TestGearheadFromMetadata(t *testing.T) {
	raw := []byte(`{"time":1792550001,"packages":{"com.google.android.projection.gearhead":{"variants":{
		"1":{"versionCode":1,"versionName":"old","channel":"stable","apks":["base.apk"],"apkHashes":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"apkSizes":[10]},
		"9":{"versionCode":9,"versionName":"new","channel":"beta","apks":["base.apk"],"apkHashes":["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"],"apkSizes":[11]},
		"5":{"versionCode":5,"versionName":"mid","channel":"stable","apks":["split.apk","base.apk"],"apkHashes":["cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"],"apkSizes":[2,20]}
	}}}}`)
	g, err := gearheadFromMetadata(raw, grapheneBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if g.VersionName != "mid" || g.VersionCode != 5 || g.Size != 20 {
		t.Fatalf("%+v", g)
	}
	if !strings.HasSuffix(g.URL, "/packages/com.google.android.projection.gearhead/5/base.apk") {
		t.Fatal(g.URL)
	}
}

func TestLiveGrapheneMetadata(t *testing.T) {
	if os.Getenv("CARGADGETD_LIVE_GEARHEAD") != "1" {
		t.Skip("set CARGADGETD_LIVE_GEARHEAD=1 to fetch GrapheneOS metadata")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g, err := LatestGearhead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g.Channel != "stable" || g.VersionCode <= 0 || g.VersionName == "" {
		t.Fatalf("%+v", g)
	}
	if !strings.Contains(g.URL, "/packages/com.google.android.projection.gearhead/") || !strings.HasSuffix(g.URL, "/base.apk") {
		t.Fatal(g.URL)
	}
	t.Logf("%s %d %s %d", g.VersionName, g.VersionCode, g.SHA256, g.Size)
}

func signifyBlob(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
