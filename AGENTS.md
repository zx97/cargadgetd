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
  rooted path available (unlocked bootloader, `boot.img` extracted from the payload
  in `/var/home/manu/captures/sunny-root/crdroid_x/out/`).
- Relay laptop: `EtoileNoire` (elementaryOS), reachable over SSH, drives the phone
  with `~/platform-tools/adb`; `su -c` works there without a password.
- The phone is reached over USB through the relay; Wi-Fi IP changes with the network.
- Never `pkill -f <pattern>` where the pattern matches the running command itself.
