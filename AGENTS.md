# AGENTS.md — cargadgetd

Project steering rules. These override any general habit, including the global
instructions in `~/.config/opencode/AGENTS.md`.

## Never propose to stop

- **The owner decides when work stops. Never ask "should we stop?", never suggest
  pausing, wrapping up, calling it a night, or that "the bench has given what it
  could".** Do not weigh fatigue, hour, or how much has already been done.
- When a path is exhausted, state the facts and **immediately continue on the next
  concrete path** — never end a turn with a choice between "continue" and "stop".
- "Continue at all costs" is the default. If something is genuinely impossible,
  prove it with a measurement, then move to the next approach without ceremony.

## Hard-won facts

- **The head unit validates the phone's MD certificate.** A rejected certificate makes
  the unit answer `MsgAuthComplete` (id 4) with a protobuf status of **-3**
  (`hex=000408fdffffffffffffffff01`) and then ignore the service discovery request
  for ever: the TCP connection stays open, TLS completes, and the session simply
  never starts. Our old `reversegearhead` pair expired on **2026-10-28**, which is
  exactly when the wireless runs stopped working while the wired VW kept working
  (it does not validate).
  With a valid pair (`carservice` issued by `Google Automotive Link`, valid to
  2027-03-04) the same unit answers `hex=00040800` (status 0), sends the service
  discovery response and the session opens at 1280x720.
  Current pairs: https://github.com/iscle/android-auto-certificates
  (`android-auto/latest`: certificate.pem + private-key.pem for AA 18.1.664444).
  The plugin is rebuilt from those PEMs by `captures/sunny-root/certplugin`
  (exports `reversegearhead_android_auto_crt/_key/_ca_crt`, `_probe`,
  `_expires_in`) and pointed at with `-certs`, so no root is needed.
  AA 18's own APK ships no usable private key: do not go looking for one in the
  APK or in GMS, it is not there.

- **The bench head unit (Melia 4.0, `CAR BT_603F`, `41:00:00:00:9E:F0`) works.**
  A OnePlus Nord 2 connects to it wirelessly, fast and reliably. Any failure is on
  the phone side or in our code — never blame the unit.
- Android Auto on that unit runs with the same release (`18.0.664264-release`,
  targetSdk 37) on both phones, both in `/data/app` over a privileged stub.
- A user-installed Android Auto **cannot** work: it needs
  `COMPANION_APPROVE_WIFI_CONNECTIONS` (privileged) and `FOREGROUND_SERVICE_LOCATION`
  from the background. It must be a privileged system app (NikGapps stub + allowlist).
- WPP ordering, measured on the commercial stack: the head unit speaks first
  (version request, up to ~5.4 s after RFCOMM), then `id=1`, the phone answers
  `id=7`, sends `id=2`, receives `id=3`, sends `id=6` after the Wi-Fi join, then TCP
  to `192.168.43.1:6730` where the phone is the passive TLS side.
- `cargadgetd` opens the exchange itself (version request + info request) — this is
  what makes it work on this unit; Android Auto is passive there.

- **The phone pings, and only on wireless.** The official Android Auto sends a
  `PingRequest` about once a second on a Wi-Fi link and merely answers the head
  unit's on USB; the reply carries the round trip. We never sent one, and an
  incoming `PingResponse` fell into `UNHANDLED` (a session failure in strict
  mode). `MD.EnablePings(1s)` is wired for every non-USB session.

- **The key binding must be exactly the keycodes the unit advertises.** Binding
  every keycode we know made this unit answer `STATUS_KEYCODE_NOT_BOUND` (-18,
  `800308eeffffffffffffffff01`) on the input channel: 19 of our 56 codes are not
  in its SDR list (1, 2, 24, 25, 79, 81, 91, 164, 219, 231, 260-263, 268-271,
  65539) and one binding covers the whole set, so touch was refused outright.
  The real phone binds the advertised list and nothing else.

- **Keyframes must keep flowing.** Our stream carried a single IDR, at its start:
  a hardware decoder that loses its reference then waits for ever and the head
  unit stops consuming the channel. `RequestIDR()` every 2 s goes through
  Codec2's input sync flag (no codec reopen); the x264 fallback keeps a 4 s GOP
  because x264-go cannot be told to emit a keyframe on demand.

- **ConnMan's agent must outlive the join.** ConnMan keeps the passphrase of an
  *unsaved* service (a head unit's AP — Sailfish cannot store it) only while the
  agent that supplied it is registered. Unregistering at the end of
  `JoinWithAgent` deauthenticated the station 200 ms later
  (`01:47:48.675 join accepted` → `01:47:48.881 link: LOST` → `01:47:49.890
  associated again`), and on the run before that it landed 24 s into the
  projection: both MediaAck streams stopped at once, the TCP socket stayed
  half-open and the writes began to time out. Keep one agent for the whole
  session and release it only after the head unit's service is removed. A second
  `Service.Connect` on an already-ready service is skipped for the same reason.
  `link:` lines carry ConnMan's own state beside the driver's so the actor is
  visible, and a lost link now ends the session at once instead of waiting out
  the 20 s silence watchdog.

- **The unit's AP disappears when it sleeps.** A join then fails in about 0.5 s
  with ConnMan's `No carrier`: the cheap way to tell "the unit is off" from "the
  unit is on but silent".

- **Do not open channels we do not speak.** Opening every service the unit lists
  (microphone `ch3`, media audio `ch5`, speech audio `ch6`, phone status `ch9`)
  makes this unit answer MediaAck after MediaAck for minutes — 4505 frames over
  2m52s — while it keeps its own animated wallpaper on screen. The A/B is exact:
  the sessions that showed our picture (2026-11-25 01:29 and 01:34) opened five
  channels (video, input, sensor, Bluetooth, one audio sink) and nothing else,
  with the same SDR, the same `StartInd` (`session=0 config_index=0 1280x720`)
  and the same audio sink as the failing ones. The passive opens sit behind
  `CARGADGETD_OPEN_ALL_CHANNELS=1` now.
  A unit that acks a stream is therefore no proof that it displays it: the ack
  is receipt, not decode. Screenshots and the unit's own screen are the only
  honest signals.

- **The working wireless configuration (2026-11-26)**: five channels (video,
  input, sensor, Bluetooth, one audio sink), phone pings at 1 s, a
  `VIDEO_FOCUS_REQUEST` after START, SPS/PPS in a `CODEC_CONFIG` message as well
  as in-band, an IDR every 2 s, and the shell's apps started *after* their
  plugins have loaded. With that the Melia shows our shell, the touch works, and
  the **Link** app projects the phone's own apps (Waze) onto the unit.

- **`libcargadgetd_app_link.so` must not ship disabled.** The package installs
  it as `libcargadgetd_app_link.so.off`, so the shell loads only Maps, Hello and
  Settings. Renaming it to `.so` adds the Link tile, and Link is what brings the
  phone's real apps to the car. Anyone testing "the apps do nothing" should check
  that suffix first.
- Reference capture and analysis: `/var/home/manu/captures/sunny-root/AA-REFERENCE-HANDSHAKE.md`
  (kept outside the repository, with the HCI/logcat artifacts).

## Test bench (2026-11)

- Test phone: Redmi Note 10 (`sunny`/`mojito`, `M2101K7AI`), crDroid 11 (Android 15),
  rooted (Magisk 31.1 on the patched crDroid boot image), unlocked bootloader,
  `boot.img` extracted from the payload in `/var/home/manu/captures/sunny-root/crdroid_x/out/`.
  A Magisk module `bthcilog` exports `/data/misc/bluetooth/logs/btsnoop_hci.log`
  to `/sdcard/Download/btsnoop_hci.log` every 15 s: that is how the payload-level
  WPP reference was captured. Android Auto is a privileged system app there.
- Relay laptop: `EtoileNoire` (elementaryOS) at `10.0.1.2` (Wi-Fi interface `wls1`),
  reachable over SSH as `manu`, drives the phone with `~/platform-tools/adb`;
  `su -c` works there without a password (no sudo).
- **Second bench, independent of the real head unit**: `cmd/fakecar` runs on the
  relay laptop as root and impersonates a head unit (AA SDP record, channel 7,
  WPP, TCP/TLS, full GAL session). `-iface` takes an **interface name**, not an
  IP. It is started from `/tmp/start-fc.sh`; log in `/tmp/fc.log`.
- The phone is reached over USB through the relay; Wi-Fi IP changes with the network.
- Never `pkill -f <pattern>` where the pattern matches the running command itself
  (it killed an SSH session twice: `pkill -f /tmp/fakecar`, `pkill -f pair-watch.sh`).

## Sailfish / Jolla constraints

- **`/cores` fills `/` silently.** Crash dumps (pulseaudio, cargadgetd-md,
  CodecLooper, qmlscene) accumulate there: 13 files, 4.8 GB, which took `/` to
  100% and broke unrelated things (USB mode switch, ConnMan state). They belong to
  `defaultuser` and `/cores` is `drwxrwxrwt`, so `rm -f /cores/core.*` works
  without root. Check `df -h /` (9.5 GB total) and `du -x -h -d1 /` when anything
  behaves oddly; `/home` is a separate 218 GB LUKS volume, always roomy.
  On 2026-11-27 eight **pulseaudio** cores (1.4 GB, 898 MB, 886 MB, …) took the
  root filesystem to 100% again. Three defences are on the phone now: a
  `LimitCore=0` drop-in for `pulseaudio.service`, `prlimit --core=0` applied to the
  running daemon, and `~/bin/core-guard.sh` under a user timer (`core-guard.timer`)
  that deletes all but the two newest cores once free space drops below 1 GiB.
  **`/cores` is bind-mounted onto `/home/defaultuser/.cores`** (ext4, 218 GB):
  `cores-on-home.service` does the bind, so `core_pattern` needs no change and a
  core can never eat the root filesystem. The SD card was tried first and is
  wrong: FAT32 cannot store a file owner, the kernel aborts the dump
  (`cannot preserve file owner`) and leaves a 0-byte file — which is exactly how
  the connmand core of 2026-11-28 00:13 was lost. Verified after the change: a
  deliberate SIGSEGV writes a 442 KB core owned by `defaultuser`.

- `defaultuser@10.0.1.18`, deploy dir `~/cargadgetd-deploy`, binary
  `~/cargadgetd-deploy/bin/cargadgetd-md`, launcher `wireless-toggle.sh`,
  log `~/wireless.log`.
- **Bluetooth cannot be powered on from an SSH session**: `org.bluez.Adapter1.Powered`
  returns `org.freedesktop.DBus.Error.AuthFailed` (polkit has no active local
  session over SSH), `systemd-run --user` does not help, there is no `bluetoothctl`
  and no `sudo`; `devel-su` needs the device password. Only the UI toggle (or a
  root shell) can do it. Without it every attempt logs
  `br-connection-adapter-not-powered`.
- Cross-compiling needs the container recipe: Debian 12 + `gcc-aarch64-linux-gnu`
  + `libssl-dev:arm64` (multiarch) + Go 1.27, then
  `go build -tags "netgo osusergo" -ldflags "-s -w -linkmode external -extldflags '-ldl'"`.
  Scripts: `captures/sunny-root/build-md.sh` (arm64) and `build-fakecar.sh` (amd64).

