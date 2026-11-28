package wireless

import "testing"

func TestResolvePeer(t *testing.T) {
	cases := []struct {
		name    string
		flagNm  string
		flagAdr string
		cfg     string
		want    PeerChoice
	}{
		{
			name:    "bench address beats everything",
			flagNm:  "ignored",
			flagAdr: "AA:BB:CC:DD:EE:FF",
			cfg:     "11:22:33:44:55:66",
			want:    PeerChoice{Name: "ignored", Addr: "AA:BB:CC:DD:EE:FF", Why: "-wireless-addr", Exact: true},
		},
		{
			name: "config address is exact",
			cfg:  " aa:bb:cc:dd:ee:ff ",
			want: PeerChoice{Addr: "aa:bb:cc:dd:ee:ff", Why: "daemon.peer address", Exact: true},
		},
		{
			name: "config name is a substring match",
			cfg:  "CAR BT",
			want: PeerChoice{Name: "CAR BT", Why: "daemon.peer name", Exact: true},
		},
		{
			name: "auto hands over to the ordering",
			cfg:  "AUTO",
			want: PeerChoice{Why: "daemon.peer auto (discovery)"},
		},
		{
			name:   "flag name when the config is empty",
			flagNm: "MULTIVAN",
			want:   PeerChoice{Name: "MULTIVAN", Why: "-wireless-name", Exact: true},
		},
		{
			name: "nothing configured",
			want: PeerChoice{Why: "no selector (discovery)"},
		},
	}
	for _, c := range cases {
		got := ResolvePeer(c.flagNm, c.flagAdr, c.cfg)
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
