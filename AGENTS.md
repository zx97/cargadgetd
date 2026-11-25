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

