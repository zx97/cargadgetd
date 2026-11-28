package session

import "testing"

// A VW MIB3 advertises three modes, largest first, and sends touch=unset
// (touch=0x0). "auto" then skips both match steps and takes video_configs[0]:
// 1920x1080, 2.25x the pixels, 60% of the frame black letterbox, for a content
// area only 6% wider than 1280x576. That is why video.size is pinned on the
// phone, and why the fallback must not change silently.
func TestAutoWithNoTouchSizeFallsBackToConfigZero(t *testing.T) {
	cfgs := []videoCfg{
		{W: 1920, H: 1080, MarginW: 559, MarginH: 468},
		{W: 1280, H: 720, MarginW: 0, MarginH: 144},
		{W: 800, H: 480, MarginW: 0, MarginH: 120},
	}
	// touch=unset -> inputW/H = 0, video.size=auto -> wantW/H = 0
	c, idx := selectVideoConfig(cfgs, 0, 0, -1, 0, 0)
	t.Logf("auto with touch unset -> config %d %dx%d (content %dx%d)", idx, c.W, c.H, c.W-c.MarginW, c.H-c.MarginH)
	if c.W != 1920 || c.H != 1080 {
		t.Fatalf("expected the fallback to config 0 (1920x1080), got %dx%d", c.W, c.H)
	}
	// And with a pinned size it is deterministic:
	c2, idx2 := selectVideoConfig(cfgs, 0, 0, -1, 1280, 720)
	t.Logf("size=1280x720 -> config %d %dx%d", idx2, c2.W, c2.H)
	if c2.W != 1280 || c2.H != 720 || idx2 != 1 {
		t.Fatalf("pinned 1280x720 got %dx%d index %d", c2.W, c2.H, idx2)
	}
}
