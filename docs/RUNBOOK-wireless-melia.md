# Procédure vérifiée — sans-fil sur l'autoradio de lab (Melia 4.0)

État validé le **26/11/2026 à 02:36** : session sans fil complète, écran du
téléphone affiché sur l'autoradio, tactile fonctionnel, **Link → Waze** lancé
depuis l'unité. Ne pas modifier cette configuration sans faire un A/B.

## Ce qui doit être en place

| Élément | Valeur | Vérification |
|---|---|---|
| Certificat MD | paire Google `carservice` (valide jusqu'au 2027-03-04) | `cargadgetd-md -certs … -cert-info` |
| Config | `audio.enabled: true`, `video.live: true`, `shell.drop_frames: 20` | `~/.config/cargadgetd/config.yaml` |
| Plugin Link | **activé** (`.so`, pas `.so.off`) | `ls /usr/lib/cargadgetd/plugins/` |
| Bluetooth Jolla | allumé **depuis l'UI** (SSH → `AuthFailed`) | Réglages → Bluetooth |
| Autoradio | allumé, en mode sans fil (sa fenêtre WPP s'ouvre au démarrage) | son écran affiche son habillage |

## Lancement (exactement ceci)

```sh
ssh defaultuser@10.0.1.18
cd ~/cargadgetd-deploy/bin
GLIBC_TUNABLES=glibc.rtld.optional_static_tls=65536 \
  setsid nohup ./cargadgetd-md \
  -wireless -wireless-addr 41:00:00:00:9E:F0 \
  -log-file ~/wireless.log \
  -control tcp://127.0.0.1:9278 \
  -certs /home/defaultuser/cargadgetd-deploy/reversegearhead.so \
  >> ~/wireless.log 2>&1 < /dev/null &
```

Build vérifié : **`8362aac5bf89308e`** (`md5sum ~/cargadgetd-deploy/bin/cargadgetd-md`).
Les builds suivants (`c4c3e614…` et après) ajoutent le balayage des ports AAP et
l'attente de profil allongée : compatibles, mais non re-testés sur cette unité.

## La séquence à retrouver dans `~/wireless.log`

```
bluetooth: link up
probe: channel 1 answered the WPP info request after ~1s
wpp rx: id=4 …                         (l'unité parle en premier)
wpp tx: id=5 (version response) ; wpp tx: id=2 (info request)
wpp rx: id=1 … f1="192.168.43.1" f2=6730      ← endpoint
wpp rx: id=3 … f1="carplay_wifi_XXXX"          ← SSID + clé
wifi: wlan0 ssid="carplay_wifi_XXXX" ip=192.168.43.2
wpp tx: id=6 connect status SUCCESS
tcp: connected to 192.168.43.1:6730
version request 1.4 → version response 1.4
ssl handshake complete (md)
auth complete (from the unit)          ← 00040800 = statut 0
got service discovery response
session ready (SDR received) video=1 audio=4 input=2 sensor=7 size=1280x720 ui=1280x480 pad=0,120
channel 8 / 1 / 2 / 7 open ok
video focus ind focus=1 ; video StartInd session=0 config_index=0
video focus request (projected)
shell apps ready: maps, hello, link, settings
channel 4 open ok (audio système 48 kHz)
video CODEC_CONFIG sent
video_tx #1 … video MediaAck #1 …
```

## Ce qui doit être visible sur l'autoradio

Barre maison à gauche, horloge en bas, pagineur, tuiles **Maps, Hello, Settings,
Link**. Taper une tuile l'ouvre ; Link donne accès aux applis du téléphone
(Waze). L'habillage « constellation » de l'unité doit disparaître.

## Les trois pièges (chacun mesuré)

1. **Ne pas ouvrir les canaux qu'on ne parle pas.** Micro (3), audio média (5),
   speech (6), phone status (9) → l'unité acquitte des milliers de trames et
   garde son habillage. Seulement **5 canaux** : 8, 1, 2, 7 et un audio.
2. **Le certificat est vérifié.** Une paire auto-signée ou expirée donne
   `auth complete` = `-3` et la session ne démarre jamais.
3. **Les applis du shell doivent démarrer après leur chargement** (`StartApps`
   après `WaitLoad`), sinon l'écran affiche « No apps » sans tuiles.

## Fin de session

À l'arrêt (côté unité ou application fermée) : `session: ended after …`, puis
`wifi: forgot head unit AP`, puis retour automatique sur le réseau de la maison
(`iw dev wlan0 link` → SSID de la maison). Vérifié deux fois, sans intervention.
