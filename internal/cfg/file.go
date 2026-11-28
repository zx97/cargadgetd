package cfg

import (
	"os"
	"strings"

	"github.com/cargadgetd/cargadgetd/internal/gps"
	"gopkg.in/yaml.v3"
)

type fileConfig struct {
	Daemon *fileDaemon `yaml:"daemon"`
	Video  *fileVideo  `yaml:"video"`
	Audio  *fileAudio  `yaml:"audio"`
	USB    *fileUSB    `yaml:"usb"`
	Gadget *fileGadget `yaml:"gadget"`
	Apps   *fileApps   `yaml:"apps"`
	Music  *fileMusic  `yaml:"music"`
	Shell  *fileShell  `yaml:"shell"`
	GPS    *fileGPS    `yaml:"gps"`
}

type fileDaemon struct {
	Transport *string `yaml:"transport"`
	Listen    *string `yaml:"listen"`
	Control   *string `yaml:"control"`
	Certs     *string `yaml:"certs"`
	Peer      *string `yaml:"peer"`
}

type fileVideo struct {
	Encoder     *string `yaml:"encoder"`
	Size        *string `yaml:"size"`
	ConfigIndex *int    `yaml:"config_index"`
	FrameMs     *int    `yaml:"frame_ms"`
	Live        *bool   `yaml:"live"`
	BitrateKbps *int    `yaml:"bitrate_kbps"`
}

type fileAudio struct {
	Enabled     *bool   `yaml:"enabled"`
	ForcePrime  *bool   `yaml:"force_prime"`
	TickMs      *int    `yaml:"tick_ms"`
	Channels    *int    `yaml:"channels"`
	ConfigIndex *int    `yaml:"config_index"`
	Type        *string `yaml:"type"`
}

type fileUSB struct {
	MediaGapMs *int    `yaml:"media_gap_ms"`
	PCMDump    *string `yaml:"pcm_dump"`
	FFSDiag    *string `yaml:"ffs_diag"`
}

type fileGadget struct {
	Path *string `yaml:"path"`
	Bin  *string `yaml:"bin"`
}

type fileApps struct {
	Strict       *bool   `yaml:"strict"`
	UnhandledLog *string `yaml:"unhandled_log"`
}

type fileShell struct {
	ID         *string `yaml:"id"`
	DropFrames *int    `yaml:"drop_frames"`
}

type fileGPS struct {
	Backend *string `yaml:"backend"`
}

type fileMusic struct {
	Loopback *bool   `yaml:"loopback"`
	Sink     *string `yaml:"sink"`
}

func applyFile(dst *Config, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var f fileConfig
	if err := yaml.Unmarshal(b, &f); err != nil {
		return err
	}
	f.merge(dst)
	return nil
}

func (f fileConfig) merge(dst *Config) {
	if f.Daemon != nil {
		d := f.Daemon
		if d.Transport != nil {
			dst.Daemon.Transport = NormalizeTransport(*d.Transport)
		}
		if d.Listen != nil {
			dst.Daemon.Listen = *d.Listen
		}
		if d.Control != nil {
			dst.Daemon.Control = *d.Control
		}
		if d.Certs != nil {
			dst.Daemon.Certs = *d.Certs
		}
		if d.Peer != nil {
			dst.Daemon.Peer = strings.TrimSpace(*d.Peer)
		}
	}
	if f.Video != nil {
		v := f.Video
		if v.Encoder != nil {
			dst.Video.Encoder = *v.Encoder
		}
		if v.Size != nil {
			dst.Video.Size = *v.Size
		}
		if v.ConfigIndex != nil {
			dst.Video.ConfigIndex = *v.ConfigIndex
		}
		if v.FrameMs != nil {
			dst.Video.FrameMs = *v.FrameMs
		}
		if v.Live != nil {
			dst.Video.Live = *v.Live
		}
		if v.BitrateKbps != nil {
			dst.Video.BitrateKbps = *v.BitrateKbps
		}
	}
	if f.Audio != nil {
		a := f.Audio
		if a.Enabled != nil {
			dst.Audio.Enabled = *a.Enabled
		}
		if a.ForcePrime != nil {
			dst.Audio.ForcePrime = *a.ForcePrime
		}
		if a.TickMs != nil {
			dst.Audio.TickMs = *a.TickMs
		}
		if a.Channels != nil {
			dst.Audio.Channels = *a.Channels
		}
		if a.ConfigIndex != nil {
			dst.Audio.ConfigIndex = *a.ConfigIndex
		}
		if a.Type != nil {
			dst.Audio.Type = *a.Type
		}
	}
	if f.USB != nil {
		u := f.USB
		if u.MediaGapMs != nil {
			dst.USB.MediaGapMs = *u.MediaGapMs
		}
		if u.PCMDump != nil {
			dst.USB.PCMDump = *u.PCMDump
		}
		if u.FFSDiag != nil {
			dst.USB.FFSDiag = *u.FFSDiag
		}
	}
	if f.Gadget != nil {
		g := f.Gadget
		if g.Path != nil {
			dst.Gadget.Path = *g.Path
		}
		if g.Bin != nil {
			dst.Gadget.Bin = *g.Bin
		}
	}
	if f.Apps != nil {
		a := f.Apps
		if a.Strict != nil {
			dst.Apps.Strict = *a.Strict
		}
		if a.UnhandledLog != nil {
			dst.Apps.UnhandledLog = *a.UnhandledLog
		}
	}
	if f.Music != nil {
		m := f.Music
		if m.Loopback != nil {
			dst.Music.Loopback = *m.Loopback
		}
		if m.Sink != nil {
			dst.Music.Sink = *m.Sink
		}
	}
	if f.Shell != nil {
		if f.Shell.ID != nil {
			id := strings.TrimSpace(*f.Shell.ID)
			if id == "" {
				id = "basic"
			}
			dst.Shell.ID = id
		}
		if f.Shell.DropFrames != nil {
			n := *f.Shell.DropFrames
			if n < 0 {
				n = 0
			}
			dst.Shell.DropFrames = n
		}
	}
	if f.GPS != nil && f.GPS.Backend != nil && strings.TrimSpace(*f.GPS.Backend) != "" {
		dst.GPS.Backend = gps.Normalize(*f.GPS.Backend)
	}
}

// outConfig is the on-disk shape. Value fields (not pointers) so zeros and
// empty strings are written. Reading still uses the pointer types above, so a
// missing key does not clobber the compiled default.
type outConfig struct {
	Daemon outDaemon `yaml:"daemon"`
	Video  outVideo  `yaml:"video"`
	Audio  outAudio  `yaml:"audio"`
	USB    outUSB    `yaml:"usb"`
	Gadget outGadget `yaml:"gadget"`
	Shell  outShell  `yaml:"shell"`
	GPS    outGPS    `yaml:"gps"`
	Apps   outApps   `yaml:"apps"`
	Music  outMusic  `yaml:"music"`
}

type outDaemon struct {
	Transport string `yaml:"transport"`
	Listen    string `yaml:"listen"`
	Control   string `yaml:"control"`
	Certs     string `yaml:"certs"`
	Peer      string `yaml:"peer"`
}

type outVideo struct {
	Encoder     string `yaml:"encoder"`
	Size        string `yaml:"size"`
	ConfigIndex int    `yaml:"config_index"`
	FrameMs     int    `yaml:"frame_ms"`
	Live        bool   `yaml:"live"`
}

type outAudio struct {
	Enabled     bool   `yaml:"enabled"`
	ForcePrime  bool   `yaml:"force_prime"`
	TickMs      int    `yaml:"tick_ms"`
	Channels    int    `yaml:"channels"`
	ConfigIndex int    `yaml:"config_index"`
	Type        string `yaml:"type"`
}

type outUSB struct {
	MediaGapMs int    `yaml:"media_gap_ms"`
	PCMDump    string `yaml:"pcm_dump"`
	FFSDiag    string `yaml:"ffs_diag"`
}

type outGadget struct {
	Path string `yaml:"path"`
	Bin  string `yaml:"bin"`
}

type outShell struct {
	ID         string `yaml:"id"`
	DropFrames int    `yaml:"drop_frames"`
}

type outGPS struct {
	Backend string `yaml:"backend"`
}

type outApps struct {
	Strict       bool   `yaml:"strict"`
	UnhandledLog string `yaml:"unhandled_log"`
}

type outMusic struct {
	Loopback bool   `yaml:"loopback"`
	Sink     string `yaml:"sink"`
}

func encodeYAML(c *Config) ([]byte, error) {
	doc := outConfig{
		Daemon: outDaemon{
			Transport: c.Daemon.Transport,
			Listen:    c.Daemon.Listen,
			Control:   c.Daemon.Control,
			Certs:     c.Daemon.Certs,
			Peer:      c.Daemon.Peer,
		},
		Video: outVideo{
			Encoder:     c.Video.Encoder,
			Size:        c.Video.Size,
			ConfigIndex: c.Video.ConfigIndex,
			FrameMs:     c.Video.FrameMs,
			Live:        c.Video.Live,
		},
		Audio: outAudio{
			Enabled:     c.Audio.Enabled,
			ForcePrime:  c.Audio.ForcePrime,
			TickMs:      c.Audio.TickMs,
			Channels:    c.Audio.Channels,
			ConfigIndex: c.Audio.ConfigIndex,
			Type:        c.Audio.Type,
		},
		USB: outUSB{
			MediaGapMs: c.USB.MediaGapMs,
			PCMDump:    c.USB.PCMDump,
			FFSDiag:    c.USB.FFSDiag,
		},
		Gadget: outGadget{Path: c.Gadget.Path, Bin: c.Gadget.Bin},
		Shell:  outShell{ID: c.Shell.ID, DropFrames: c.Shell.DropFrames},
		GPS:    outGPS{Backend: c.GPS.Backend},
		Apps: outApps{
			Strict:       c.Apps.Strict,
			UnhandledLog: c.Apps.UnhandledLog,
		},
		Music: outMusic{Loopback: c.Music.Loopback, Sink: c.Music.Sink},
	}
	body, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, err
	}
	const header = "# cargadgetd. Omitted keys keep the compiled default.\n"
	return append([]byte(header), body...), nil
}
