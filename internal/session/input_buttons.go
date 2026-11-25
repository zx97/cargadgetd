package session

// huInput is a named GAL key dispatched to plugins as
//
//	void cargadgetd_app_<id>_huinput_<Name>(int pressed); // 1 down, 0 up
//
// Unknown scan codes stay in apps.unhandled_log.
var huInputs = []struct {
	Code int
	Name string
}{
	{1, "mic"},
	{2, "menu"},
	{3, "home"},
	{4, "back"},
	{5, "call"},
	{6, "endcall"},
	{7, "key0"},
	{8, "key1"},
	{9, "key2"},
	{10, "key3"},
	{11, "key4"},
	{12, "key5"},
	{13, "key6"},
	{14, "key7"},
	{15, "key8"},
	{16, "key9"},
	{17, "star"},
	{18, "pound"},
	{19, "dpad_up"},
	{20, "dpad_down"},
	{21, "dpad_left"},
	{22, "dpad_right"},
	{23, "dpad_click"},
	{24, "volume_up"},
	{25, "volume_down"},
	{79, "headsethook"},
	{81, "plus"},
	{82, "menu"}, // Android KEYCODE_MENU. Code 2 stays the DHU soft-right menu.
	{84, "search"},
	{85, "playpause"},
	{86, "stop"},
	{87, "next"},
	{88, "prev"},
	{89, "rewind"},
	{90, "fast_forward"},
	{91, "mute"},
	{126, "play"},
	{127, "pause"},
	{164, "volume_mute"},
	{219, "assist"},
	{231, "voice_assist"},
	{260, "navigate_previous"},
	{261, "navigate_next"},
	{262, "navigate_in"},
	{263, "navigate_out"},
	{268, "dpad_up_left"},
	{269, "dpad_down_left"},
	{270, "dpad_up_right"},
	{271, "dpad_down_right"},
	{65536, "rotary"},
	{65537, "media"},
	{65538, "navigation"},
	{65539, "radio"},
	{65540, "tel"},
}

func huInputCodes() []int {
	out := make([]int, len(huInputs))
	for i, h := range huInputs {
		out[i] = h.Code
	}
	return out
}

// HuInputName is the plugin ABI suffix for a GAL scan_code.
func HuInputName(code int) (string, bool) {
	for _, h := range huInputs {
		if h.Code == code {
			return h.Name, true
		}
	}
	return "", false
}

// handshakeButtons is the set we bind: exactly the keycodes the head unit listed
// in its service discovery, when it listed any.
//
// Claiming more than that is refused. The Melia bench answers the whole binding
// with STATUS_KEYCODE_NOT_BOUND (-18, `800308eeffffffffffffffff01`) as soon as
// one requested code is unknown to it, and the real phone only ever binds the
// advertised list — so a "every key we know" list costs the touch/keys channel
// outright. The known list stays as the fallback for a unit that lists none.
func handshakeButtons(hu []int) []int {
	if len(hu) == 0 {
		return huInputCodes()
	}
	seen := make(map[int]bool, len(hu))
	out := make([]int, 0, len(hu))
	for _, c := range hu {
		if c <= 0 || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return huInputCodes()
	}
	return out
}

// handshakeBody is KeyBindingRequest.keycodes in the packed form LIVI's
// proto declares (repeated int32 keycodes = 1 [packed = true]).
func handshakeBody(codes []int) []byte {
	var inner []byte
	for _, c := range codes {
		if c <= 0 {
			continue
		}
		inner = appendVarint(inner, uint64(c))
	}
	return encodeLenDelim(1, inner)
}

// KnobEvent is the plugin name for one detent of a relative key (LIVI sends
// the scroll wheel as RotaryController plus a signed delta, not a press).
func KnobEvent(code, delta int) (string, bool) {
	name, ok := HuInputName(code)
	if !ok || delta == 0 {
		return "", false
	}
	if delta > 0 {
		return name + "_cw", true
	}
	return name + "_ccw", true
}

func appendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

func encodeLenDelim(field int, inner []byte) []byte {
	out := appendVarint(nil, uint64(field<<3|2))
	out = appendVarint(out, uint64(len(inner)))
	return append(out, inner...)
}
