# Wireless Android Auto

cargadgetd normally talks to the head unit over USB (FunctionFS). This document
covers the Wireless Android Auto path: Bluetooth for the setup handshake, the
car's Wi-Fi access point, then TCP + TLS for the normal GAL session.

## Flow

```mermaid
sequenceDiagram
    participant MD as cargadgetd (phone)
    participant BT as Bluetooth (BlueZ)
    participant HU as Head unit (car)
    participant WiFi as Wi-Fi (ConnMan)
    MD->>BT: find paired device with AA UUID
    MD->>BT: register AA RFCOMM server profile (root helper)
    Note over HU,MD: the HU's list now shows the Android Auto entry
    HU->>MD: RFCOMM connect to the AA channel
    MD->>HU: WPP version / info request
    HU->>MD: WPP version / Wi-Fi credentials / TCP endpoint
    MD->>WiFi: join the car AP (SSID from WPP)
    MD->>HU: TCP connect (phone is still the TLS server)
    Note over MD,HU: normal GAL framing, same as USB
```

The phone is the **RFCOMM client** and the **TCP client**, but the **TLS
server**. That is the Android Auto wireless layout; do not swap the TLS roles.

Two RFCOMM directions exist in the wild. The daemon registers an Android Auto
**server** profile (UUID `4de17a00-…`, RFCOMM channel 8, profile version 1.0) so
the head unit discovers the phone as an Android Auto device and connects to it;
if nothing arrives within 20 s it falls back to the classic client-only path
(dial the HU channel learned over SDP).

## Running

```sh
cargadgetd-md -wireless \
  -wireless-name "" \
  -wireless-channel 0 \
  -wireless-iface wlan0 \
  -certs /usr/lib/cargadgetd/plugins/reversegearhead.so
```

- `-wireless-name` matches the paired head unit name (empty = pick the paired
  device advertising the Android Auto UUID `4de17a00-52cb-11e6-bdf4-0800200c9a66`).
- `-wireless-addr` selects the device by Bluetooth address.
- `-wireless-channel 0` discovers the RFCOMM channel over SDP; `-wireless-ssid`
  forces a Wi-Fi SSID instead of the one received from the car.

The daemon serves a status page on `http://127.0.0.1:9288/` (`-wireless-web`).

## First pairing

The first wireless setup normally needs one **USB** session: plug the phone,
let the car run wired Android Auto once (the head unit registers the phone and
its wireless trust), then unplug. Without that step many head units do not list
the phone over Bluetooth, or cannot complete the WPP handshake. The daemon
powers the Bluetooth adapter and makes it discoverable, but it cannot perform
the car's first-time trust step.

The status page shows the adapter state (Powered / Discoverable / Pairable) and
the device properties (Bonded / Trusted / ServicesResolved).

### Two names for one phone

The head unit names the phone differently in each mode, and the page says so
rather than leaving the user to guess:

| Mode | Name source | Example |
| --- | --- | --- |
| Wireless | BlueZ adapter alias (`Adapter1.Alias`) | `Manolla` |
| USB cable | gadget product string, built from `/etc/machine-info` `PRETTY_HOSTNAME` | `Jolla Phone (2026)` |

Both are the same phone. The car lists the **Bluetooth** name for Wireless
Android Auto; the USB name only appears while the cable is plugged in.

### How the head unit links the USB session to the Bluetooth bond

Names are labels; they are never the link. Three independent identities exist:

| Identity | Value | Where it comes from |
| --- | --- | --- |
| USB device | `18d1:2d00` + descriptor strings | the phone's gadget |
| Bluetooth bond | `BD_ADDR` + link key | BlueZ / the car |
| AA session | reversegearhead TLS certificate | the same in USB and Wi-Fi |

The missing piece on a plain Linux MD is the bridge between the first two.
Android Auto carries a **Bluetooth channel** (service 10) inside the normal
session: the MD opens it and sends `BluetoothPairingRequest` with its
`phone_address`. The head unit stores "this USB/Wi-Fi session belongs to that
Bluetooth device", which is what lets it offer Wireless Android Auto later.

cargadgetd implements it:

```mermaid
sequenceDiagram
    participant MD as cargadgetd
    participant HU as Head unit
    HU->>MD: ServiceDiscoveryResponse (channel 10 = Bluetooth, adapter 10:32:2C:…)
    MD->>HU: ChannelOpenRequest (channel 10)
    HU-->>MD: ChannelOpenResponse (OK)
    MD->>HU: BluetoothPairingRequest phone_address=50:56:A8:06:03:39, methods A2DP/HFP
    HU-->>MD: BluetoothPairingResponse already_paired=true, status=OK
```

Log lines to look for:

```
SDR bluetooth channel=10 adapter="10:32:2C:50:5D:5E" methods=[2 4]
bluetooth: pairing request sent on channel 10 address=50:56:A8:06:03:39 name="Manolla" method=2
bluetooth: pairing response already_paired=true status=1 (0 none, 1 ok, 2 fail)
```

`already_paired=true` confirms the car recognised the phone's Bluetooth bond.
The address comes from `Adapter1.Address`, never from a configured name.


## Root-only D-Bus calls (Sailfish)

Sailfish answers `org.freedesktop.DBus.Error.AuthFailed` (with an empty message)
to `Adapter1` property writes coming from a normal user session, and ConnMan
refuses `Service.Connect` the same way. The daemon therefore asks the setuid
`cargadgetd-gadget` helper for both:

```sh
cargadgetd-gadget -bt-adapter /org/bluez/hci1   # Powered, no timeouts, Pairable, Discoverable
cargadgetd-gadget -wifi-connect MULTIVAN        # ConnMan Service.Connect as root
```

`DiscoverableTimeout` is set to 0 ("stay discoverable") because BlueZ otherwise
clears the flag after its timeout and the phone disappears from the head unit's
list mid-session. Both helpers exit immediately; the daemon keeps running as the
normal user.

To check from another machine that the phone advertises the service:

```
bluetoothctl --timeout 12 scan on
bluetoothctl info <phone-address>   # expects 4de17a00-… under "Vendor specific"
```

## Status page

The page shows one green/red line per prerequisite:

- Bluetooth device paired (AA UUID present)
- Bluetooth link up
- SDP RFCOMM channel
- RFCOMM connected
- WPP version / Wi-Fi credentials / TCP endpoint
- Wi-Fi: current SSID vs expected SSID and IPv4 address
- TCP endpoint and session state

Buttons: **Démarrer** / **Arrêter** control the connection attempt; the
**Rejoindre le Wi-Fi** button asks ConnMan to join the SSID received from the
car; **Redémarrer** re-execs the daemon from the installed binary (reloads an
updated build and resets the controller) without a shell. The daemon keeps
running so the page stays available.

Above the buttons, the **Quoi faire** box gives exactly one instruction for the
current state ("allume le contact", "sur l'écran de la voiture : Appareils
mobiles → « Manolla » → Android Auto", "tu peux rouler"), so the driver never
has to read the check list. The daemon never gives up: after a failed attempt it
watches the Bluetooth link, the ConnMan state and the association, and starts a
new cycle as soon as one of them changes (or after 15 s at most).

### Wired path monitoring

The page also observes the USB daemon, which it does not own:

| Line | Source |
| --- | --- |
| Service USB | `systemctl is-active cargadgetd-aoa.service` |
| Mode | live gadget `idVendor` (`0x0e8d` ask, `0x18d1` accessory) |
| Session USB | a `cargadgetd-md` process without `-wireless` |
| Log | tail of `~/cargadgetd.log` |

The state that looks like "the car refuses the phone" is the launcher unit
stopped: with `daemon.transport: usb` the waiter must run *before* the cable is
plugged, otherwise the phone enumerates as plain mass storage. The
**Réparer l'USB** button runs `cargadgetd-gadget -aoa-service start` (setuid) and
the page then follows the unit, so no shell is needed.

Configfs gadget directories are `0771 root:system`: `filepath.Glob` cannot be
used there (Go opens every intermediate directory), only `ReadDir` on the parent
plus a direct `ReadFile` of `idVendor`.



Logs: `~/wireless.log` (wireless daemon) and `~/cargadgetd.log` (USB daemon).
The status page lists the failing check and the raw WPP exchange. It polls
`/status` once a second and patches the DOM in place, so the page never reloads
or flickers (a `meta refresh` did, badly, on the phone).

### WPP, verified against Android Auto 18.1

The stock client (from the gearhead APK) uses exactly the framing cargadgetd
does, and the same ids:

| Framing | value |
| --- | --- |
| header | 2-byte big-endian payload length, 2-byte big-endian message type |
| max length | 1024 |
| first packet (phone → HU) | `WifiInfoRequest` (2), sent as soon as RFCOMM connects |
| ids | 1 Start, 2 InfoRequest, 3 InfoResponse, 4 VersionRequest, 5 VersionResponse, 6 ConnectStatus, 7 StartResponse, 8 Ping, 9 PingResponse, 10 ConnectionRejection, 11 SetupInfo |

The only behavioural difference: the stock client retries `WifiInfoRequest`
**twice** and then closes the socket and restarts WPP, while cargadgetd used to
retry forever. Silent retries hid the real cause — a car that answers nothing is
usually on another RFCOMM service than the one we dialled — so the attempt now
fails with `no WifiInfoResponse after 2 retries`.

`pairing_method` on the Bluetooth channel is not a profile: the real enum is
OOB=1, NUMERIC_COMPARISON=2, PASSKEY_ENTRY=3, PIN=4, and stock Android picks
NUMERIC_COMPARISON then PIN (never OOB).

### The RFCOMM channel is 7 on a VW MIB3

The phone's BlueZ SDP cache (`/var/lib/bluetooth/<adapter>/cache/<car>`) contains
the head unit's records in clear text. Decoding a VW MIB3:

```
0x0001000A  ServiceClassIDList = 4DE17A00-52CB-11E6-BDF4-0800200C9A66   ← Android Auto
            ProtocolDescriptorList = L2CAP + RFCOMM channel 7
            ServiceName = "VW SPP Service"

0x0001000B  UUID 00001101 (generic SPP) — RFCOMM channel 8
```

Channel 8, the old hard-coded fallback, is the car's **generic SPP** service: it
accepts the connection and then ignores WPP, which looked like "the car never
answers". When SDP fails (the unit refuses the L2CAP PSD 1 connect while its
baseband link is not fully up), the daemon now probes the likely channels by
connecting and sending a `WifiInfoRequest`, and keeps the one that answers.

## Reading the log
Every attempt is bounded and self-documenting: timestamps carry milliseconds and
each phase logs its duration, so one attempt can be read top to bottom without a
sniffer.

```mermaid
flowchart LR
    A["attempt: start"] --> B["dial: begin"]
    B --> C["bluetooth: device found in …"]
    C --> D["bluetooth: adapter configured / by the root helper"]
    D --> D2["bluetooth: N paired candidate(s)"]
    D --> E["profile: waiting up to 30s"]
    E -->|car connects| F["profile: NewConnection / rfcomm: incoming"]
    E -->|timeout| G["bluetooth: Device1.Connect / link up?"]
    G --> H["sdp: attempt N failed after …"]
    H --> I["rfcomm: connected … fd="]
    F --> J["wpp tx / wpp rx (len + hex)"]
    I --> J
    J --> K["wpp: end after … rx=… tx=… last_rx_age=…"]
    K --> L["dial: FAILED after … / dial: connected after …"]
```

| Log line | Meaning |
| --- | --- |
| `attempt: FAILED after 1m34s: voiture absente …` | whole attempt summary; the duration matters |
| `bluetooth: Device1.Connect failed after 7.7s: br-connection-page-timeout` | the car did not answer the page |
| `bluetooth: link never came up within 10s (20 polls, last=down)` | ACL link never established |
| `sdp: attempt 1 failed after 7.7s: … host is down` | SDP L2CAP connect timed out (7.7 s is the baseband page timeout) |
| `profile: NewConnection from … fd=` | the head unit connected to the phone's AA service |
| `rfcomm: connected … (fd=)` | client path connected; the channel and fd identify the service |
| `wpp tx/rx: id=… len=… hex=…` | exact WPP bytes, both directions |
| `wpp: end after … rx=0 tx=1 last_rx_age=…` | the car never answered the info request |
| `wifi: join #N … hors de portée` | ConnMan cannot see the car AP yet |

A head unit that never answers `wpp tx: id=2` is the signature of a wrong
RFCOMM channel (connected to another service) or of a car that wants the phone
to wait: the SDP channel lookup above it tells which case applies.


### Discovery, not a fixed target

Every bonded device is a candidate, tried in turn, so a head unit that is
switched off does not stop the daemon from finding one that is on. A device is
only reported as present once its ACL link comes up; "candidate" in the log
means "read from the BlueZ cache", not "in range".

`~/.cache/cargadgetd-headunit` (one line: Bluetooth address, or a name) pins a
single head unit when several are around and the order is not what you want.
Empty means discovery over all bonded devices.

### A deleted log is recreated

The daemon is started with `-log-file` and owns its log: if the file is removed
or rotated while a session runs, it reopens it. A shell redirect would keep
writing into the unlinked inode, which is how a whole session can leave no trace
(a deleted `wireless.log` looked like "nothing happened").

## When the car ends the session

A projection can end in four ways, and the log names which one happened. Only
the first is ours to fix; the others are the car deciding.

```mermaid
flowchart TD
    S["session ready (SDR received)"] --> P{"who stops?"}
    P -->|car leaves AA| B["bye-bye (reason=1, user selection)"]
    B --> B2["ByeByeResponse sent, session closed"]
    P -->|car reboots / link dies| Q["no frame for SilentHU (20 s)"]
    Q --> Q2["session: head unit silent — ending the session"]
    P -->|phone stops| R["media tx error / stop"]
    P -->|socket closes| T["session: EOF / read: connection …"]
```

| Log line | Meaning |
| --- | --- |
| `head unit bye-bye (reason=N, …) — acknowledging` | the car asked to end the projection (`ByeByeRequest`, msg `0x000f`); reasons are `1 user selection`, `2 device switch`, `3/4 not supported`, `5 probe`. The daemon answers with `ByeByeResponse` (`0x0010`) before closing, which is what a real phone does — an unanswered bye-bye leaves the car waiting |
| `session: head unit silent for 20s — ending the session` | the car stopped sending anything (it rebooted, or the link dropped). Without this check the socket stays half-open and a phone pushes frames into it for minutes, never retrying: the "it worked once and never again" state |
| `session: read unblocked after N frames: …` | the reason above, reported instead of the deadline error used to unblock the read |
| `video catch-up: skipped N ticks` | the send pipe is stalled; with the above it should no longer be seen for minutes |

`session.SilentHU` (20 s) is the silence budget: the head unit pings about once
a second, so only a dead car trips it.

## Video bitrate

`video.bitrate_kbps` pins the CBR target. `0` keeps the frame-size budget
(about 4.8 Mbps at 30 fps, keyframes around 450 KB). A head unit whose decoder
reboots under a heavy stream needs a lower rate; `1500` is the project's own
"crisp" floor and cuts the keyframe bursts to roughly a third.

```yaml
video:
    encoder: droidmedia
    size: 1280x720
    bitrate_kbps: 1500
```

The daemon logs the value it actually uses on every session:

```
video: CBR target 1.5 Mbps (video.bitrate_kbps=1500)
large video AU 143210B
```

`large video AU` appears above 48 KB, i.e. for keyframes and scene changes: it
is the quickest way to see whether the burst is still heavy.

## Wi-Fi selection

Two sources, in order:

1. **Saved network heuristic**: once the Bluetooth link is up, the daemon looks
   for a ConnMan Wi-Fi service whose SSID matches the Bluetooth device name
   (case-insensitive, or one containing the other). Only networks already saved
   on the phone are considered, so it never joins an unknown AP.
2. **WPP credentials**: `WifiInfoResponse` / `WifiSetupInfo` (and the Wi-Fi
   Direct group name in `WifiVersionRequest`) are authoritative and override the
   heuristic.

## Optional car apps

The ftuna aarch64 repository ships two app plugins:

| Package | Purpose |
|---|---|
| `cargadgetd-plugin-link` | Mirrors the phone screen to the car (Waze/Google Maps), plus two setuid helpers |
| `cargadgetd-plugin-maps` | MapLibre map app for the car shell |

```sh
devel-su rpm -Uvh cargadgetd-plugin-link-*.rpm cargadgetd-plugin-maps-*.rpm
```

The plugins are `.so` files in `/usr/lib/cargadgetd/plugins/`; the shell loads
them on the next session and lists them in the app grid.

AppSupport, microG and Aurora Store are already installed on a stock Jolla
Phone 2026. Install Waze (and Google Maps if your AppSupport has the Play
Services it needs) from Aurora Store, open the app on the phone, then open
**Link** in the cargadgetd shell.

## Waze / Google Maps

cargadgetd does **not** project Android apps: it streams its own Sailfish shell
and plugins. Waze and Google Maps are Android apps running in AppSupport, so
they can only reach the car screen through the **Link** plugin, which mirrors
the phone screen:

1. Install Waze (and Google Maps if your AppSupport has Play Services) from
   Aurora Store / your APK source.
2. Open the app on the phone.
3. In the cargadgetd shell, open **Link**.
4. The phone screen is mirrored to the car; touches are forwarded back.

The Link package ships two **setuid root** helpers,
`cargadgetd-linkcap` (Wayland screen capture) and `cargadgetd-linktouch`
(`/dev/uinput` touch injection). The upstream README warns that they can
potentially be used to escalate privileges. Remove the package
(`rpm -e cargadgetd-plugin-link`) if you do not need the mirror.

## Navit maps

Maps live in a dedicated directory:

```sh
mkdir -p ~/Documents/navit-maps
```

Build them from an OSM extract with Navit's `maptool` (Debian package
`maptool`):

```sh
maptool --protobuf -i luxembourg-latest.osm.pbf luxemburg.bin
maptool --protobuf -i belgium-latest.osm.pbf belgium.bin
maptool --protobuf -i france-latest.osm.pbf france.bin
```

Then declare them in `/usr/share/harbour-navit/navit.xml` inside the enabled
`<mapset>`:

```xml
<map type="binfile" enabled="yes" data="~/Documents/navit-maps/luxemburg.bin"/>
<map type="binfile" enabled="yes" data="~/Documents/navit-maps/belgium.bin"/>
<map type="binfile" enabled="yes" data="~/Documents/navit-maps/france.bin"/>
```

`maptool` produces large files: France is about 11 GB from the 5.5 GB PBF.

## Known limitations

- The `libreversegearhead` certificate expired on 2026-10-28; at least one car
  accepts the TLS handshake anyway, but this must be refreshed for others.
- The WPP field numbers are implemented for both documented layouts (A and B)
  and logged raw; the VW MIB3 exchange still needs a capture to confirm which
  one it uses.
- The RFCOMM channel is discovered over SDP; if SDP fails the daemon falls back
  to channel 8 and logs it.
