package wireless

import (
	"fmt"
	"net/http"
	"strings"
)

// The code and every message template are English so the project can be
// shared; this table holds the translations. English is the source language and
// needs no entry. A missing key falls back to the English string, never to an
// empty page, so a partial translation is always safe.
var translations = map[string]map[string]string{
	"fr": {
		"%s is paired and connected":                               "%s est appairé et connecté",
		"%s is paired, not connected":                              "%s est appairé, non connecté",
		"Adapter: %s (%s)":                                         "Adaptateur : %s (%s)",
		"BlueZ is not answering; reboot the phone if it persists.": "BlueZ ne répond pas ; redémarre le téléphone si cela persiste.",
		"Bluetooth is off":                                         "Le Bluetooth est éteint",
		"Bluetooth unavailable":                                    "Bluetooth indisponible",
		"Bluetooth, as BlueZ reports it":                           "Bluetooth, tel que BlueZ le rapporte",
		"Bluetooth: no adapter":                                    "Bluetooth : aucun adaptateur",
		"Bluetooth: off":                                           "Bluetooth : éteint",
		"Bluetooth: on (%d device(s))":                             "Bluetooth : allumé (%d appareil(s))",
		"Bluetooth: unavailable":                                   "Bluetooth : indisponible",
		"Connect":                                                  "Connecter",
		"No Bluetooth adapter":                                     "Aucun adaptateur Bluetooth",
		"No car paired yet":                                        "Aucune voiture appairée",
		"No device known":                                          "Aucun appareil connu",
		"Pair the phone from the car's Bluetooth menu, then touch “Restart”.": "Appaire le téléphone depuis le menu Bluetooth de la voiture, puis touche « Redémarrer ».",
		"Session: active": "Session : active",
		"Session: idle":   "Session : inactive",
		"Switch the ignition on, or touch “Restart”.":                          "Mets le contact, ou touche « Redémarrer ».",
		"The phone reports no Bluetooth controller.":                           "Le téléphone ne signale aucun contrôleur Bluetooth.",
		"Touch the Bluetooth chip to switch the adapter on and read it again.": "Touche la puce Bluetooth pour allumer l'adaptateur et relire son état.",
		"Turn Bluetooth on in the phone settings, then touch “Restart”.":       "Active le Bluetooth dans les réglages du téléphone, puis touche « Redémarrer ».",
		"Use the car's screen. Internet stays on the phone's data.":            "Utilise l'écran de la voiture. Internet reste sur les données du téléphone.",
		"Wi-Fi: %s":                  "Wi-Fi : %s",
		"Wi-Fi: %s (%s)":             "Wi-Fi : %s (%s)",
		"Wi-Fi: not connected":       "Wi-Fi : non connecté",
		"car (audio/video)":          "voiture (audio/vidéo)",
		"none":                       "aucun",
		"not connected":              "non connecté",
		"off":                        "éteint",
		"on":                         "allumé",
		"phone":                      "téléphone",
		"%s answered (%s)":           "%s a répondu (%s)",
		"Car endpoint (default): %s": "Point d'accès voiture (défaut) : %s",
		"valid (%ds)":                "valide (%ds)",
		"expired (expires_in=%ds)":   "expiré (expires_in=%ds)",
		"bluetooth":                  "Bluetooth",
		"profile":                    "Profil",
		"sdp":                        "SDP",
		"wpp":                        "WPP",
		"wifi":                       "Wi-Fi",
		"tcp":                        "TCP",
		"session":                    "Session",
		// Page chrome
		"Cargadgetd Wireless":      "Cargadgetd sans fil",
		"State":                    "État",
		"Details":                  "Détails",
		"Log":                      "Journal",
		"Cable":                    "Câble",
		"What to do":               "Quoi faire",
		"Start":                    "Démarrer",
		"Stop":                     "Arrêter",
		"Restart":                  "Redémarrer",
		"Record Bluetooth (3 min)": "Enregistrer le Bluetooth (3 min)",
		"Recording Bluetooth…":     "Enregistrement Bluetooth en cours…",
		"capture unavailable":      "Enregistrement indisponible",
		"Repair USB":               "Réparer l'USB",
		"Repairing…":               "Réparation…",
		"Restarting…":              "Redémarrage…",
		"Join Wi-Fi %s":            "Rejoindre le Wi-Fi %s",
		"Join Wi-Fi":               "Rejoindre le Wi-Fi",
		"Waiting":                  "En attente",
		"unknown":                  "inconnu",

		// Names
		"Wireless, the car sees %s (Bluetooth name).": "Sans fil, la voiture voit « %s » (nom Bluetooth).",
		"With the cable, it sees %s (USB name).":      "Avec le câble, elle voit « %s » (nom USB).",
		"Same phone.":                                 "C'est le même téléphone.",

		// Banner and phase line
		"Stopped":                "Arrêté",
		"Connecting…":            "Connexion…",
		"Android Auto connected": "Android Auto connecté",
		"Waiting for the car":    "En attente de la voiture",
		"Car not paired":         "Voiture non appairée",
		"Error":                  "Erreur",
		"stopped":                "arrêté",
		"active":                 "actif",
		"state: %s · step: %s":   "état : %s · étape : %s",

		// Workflow stages
		"Bluetooth":             "Bluetooth",
		"Bluetooth paired":      "Bluetooth appairé",
		"paired, not connected": "appairé, non connecté",
		"Android Auto profile":  "Profil Android Auto publié",
		"RFCOMM channel":        "Canal RFCOMM trouvé",
		"WPP credentials":       "WPP : identifiants Wi-Fi",
		"Car Wi-Fi":             "Wi-Fi de la voiture",
		"TCP + TLS":             "TCP + TLS",
		"Android Auto session":  "Session Android Auto",

		// Check names
		"Bluetooth link":  "Lien Bluetooth",
		"Incoming RFCOMM": "RFCOMM entrant",
		"Saved network":   "Wi-Fi auto",
		"Wi-Fi join":      "Wi-Fi join",
		"WPP Wi-Fi":       "WPP Wi-Fi",
		"WPP version":     "Version WPP",
		"WPP handshake":   "WPP handshake",
		"Endpoint":        "Endpoint voiture",
		"Car TCP":         "TCP voiture",
		"Connection":      "Connexion",
		"MD certificate":  "Certificat MD",
		"Devices":         "Appareils",
		"USB repair":      "Réparation USB",
		"RFCOMM":          "RFCOMM",
		"WPP":             "WPP",

		// Check details (static)
		"link up":             "lien monté",
		"link down":           "lien non monté",
		"connected":           "connecté",
		"open (SDR received)": "ouverte (SDR reçu)",
		"TCP connected, the car has not started the session yet": "TCP connecté, la voiture n'a pas encore démarré la session",
		"car connected":                               "voiture connectée",
		"registered, waiting for the car":             "enregistré, attente de la voiture",
		"no incoming connection (passive mode)":       "pas de connexion entrante (mode passif)",
		"no incoming connection, trying as client":    "pas de connexion entrante, essai client",
		"SDP unavailable, probing channels":           "canal SDP indisponible, sonde des canaux",
		"no saved network matches the Bluetooth name": "aucun réseau enregistré ne correspond au nom Bluetooth",
		"TCP connected":                               "TCP connecté",
		"ended":                                       "terminée",
		"requested from ConnMan":                      "demandé à ConnMan",
		"USB service restarted":                       "service USB relancé",

		// Check details (templates)
		"channel %d":                      "canal %d",
		"channel %d (WPP probe)":          "canal %d (sonde WPP)",
		"forced channel %d":               "canal forcé %d",
		"saved network %q (Bluetooth %q)": "réseau enregistré %q (Bluetooth %q)",
		"current=%q want=%q ip=%q":        "actuel=%q attendu=%q ip=%q",
		"ssid=%q":                         "ssid=%q",
		"ssid=%q (version)":               "ssid=%q (version)",
		"%d.%d":                           "%d.%d",
		"%s %s":                           "%s %s",

		// Wired path
		"USB service: %s": "Service USB : %s",
		"Mode: AOA accessory (%s) — the car can talk":      "Mode : accessoire AOA (%s) — la voiture peut parler",
		"Mode: waiting for the cable (%s)":                 "Mode : attente du câble (%s)",
		"Mode: waiting for the cable":                      "Mode : attente du câble",
		"Mode: unreadable (configfs is root-only)":         "Mode : non lisible (configfs réservé à root)",
		"Mode: no USB gadget":                              "Mode : gadget USB absent",
		"accessory linked":                                 "accessoire lié",
		"USB host attached":                                "hôte USB branché",
		"USB session: none":                                "Session USB : aucune",
		"USB session: running":                             "Session USB : en cours",
		"Configured transport: %s (the cable is ignored).": "Transport configuré : %s (le câble est ignoré).",

		// What to do
		"Start the daemon below.": "Touche « Démarrer » ci-dessous.",
		"Turn the car on: the phone retries by itself as soon as it appears.": "Allume le contact de la voiture : le téléphone réessaie tout seul dès qu'elle apparaît.",
		"Start a new attempt.":              "Touche « Redémarrer » pour relancer une tentative.",
		"You are connected. You can drive.": "C'est connecté. Tu peux rouler.",
		"First run: plug the USB cable, let Android Auto show up for ~20 s, then unplug. Wireless is enough afterwards.": "Première fois : branche le câble USB, laisse Android Auto s'afficher ~20 s, puis débranche. Ensuite le sans-fil suffit.",
		"Pair %s with the car (Bluetooth menu on its screen), then press Restart.":                                       "Appaire %s à la voiture (menu Bluetooth de son écran), puis touche « Redémarrer ».",
		"The USB service is stopped: press \"Repair USB\", then plug the cable again.":                                   "Le service USB est arrêté : touche « Réparer l'USB », puis rebranche le câble.",
		"On the car screen: Mobile devices → « %s » → Android Auto. If two identical entries exist, delete the oldest.":  "Sur l'écran de la voiture : Appareils mobiles → « %s » → Android Auto. S'il y a deux entrées identiques, supprime la plus ancienne.",
		"Pair the phone in the car's Bluetooth menu first.":                                                              "Appaire d'abord le téléphone au menu Bluetooth de la voiture.",
		"The phone is joining the car Wi-Fi: hands off.":                                                                 "Le téléphone rejoint le Wi-Fi de la voiture : ne touche à rien.",
		"Hands off: the phone is talking to the car.":                                                                    "Ne touche à rien : le téléphone parle à la voiture.",
		"the phone": "le téléphone",

		// Errors shown on the page
		"no paired car: pair the phone from the car's Bluetooth menu, then press Restart": "Aucune voiture appairée : appaire le téléphone depuis le menu Bluetooth de la voiture, puis touche « Redémarrer ».",
		"car absent: Bluetooth unreachable (ignition on?)":                                "Voiture absente : Bluetooth injoignable (contact allumé ?)",
		"RFCOMM channel busy with a previous session: retrying next cycle":                "Canal RFCOMM occupé par une session précédente : nouvelle tentative au prochain cycle.",
		"no Android Auto service on this channel: retrying next cycle":                    "Aucun service Android Auto sur ce canal : nouvelle tentative au prochain cycle.",

		// Live feed
		"Attempt #%d":                  "Tentative #%d",
		"Failed: %v":                   "Échec : %v",
		"Connected in %s":              "Connecté en %s",
		"Car found: %s (%s)":           "Voiture trouvée : %s (%s)",
		"Bluetooth ready (adapter %s)": "Bluetooth configuré (adaptateur %s)",
		"Android Auto profile published, waiting for the car (max %s)": "Profil Android Auto publié, attente de la voiture (max %s)",
		"The car connects to us":             "La voiture se connecte à nous",
		"The car does not come: we call it":  "La voiture ne vient pas : on l'appelle",
		"RFCOMM channel %d found over SDP":   "Canal RFCOMM %d trouvé par SDP",
		"RFCOMM channel %d found by probing": "Canal RFCOMM %d trouvé par la sonde",
		"WPP version %d.%d":                  "Version WPP %d.%d",
		"Car endpoint: %s":                   "Endpoint voiture : %s",
		"Car Wi-Fi: %q (security %d)":        "Wi-Fi voiture : %q (sécurité %d)",
		"TCP connected to %s":                "TCP connecté à %s",
		"Wi-Fi joined: %q (%s)":              "Wi-Fi connecté : %q (%s)",
	},
}

// DetectLang returns the first language we have a translation for, from the
// explicit ?lang= parameter or Accept-Language, and "en" otherwise.
func DetectLang(r *http.Request) string {
	if r == nil {
		return "en"
	}
	if q := r.URL.Query().Get("lang"); q != "" {
		if code := normLang(q); code != "" {
			return code
		}
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		if code := normLang(strings.SplitN(part, ";", 2)[0]); code != "" {
			return code
		}
	}
	return "en"
}

// normLang keeps the first two letters and returns "" when we cannot translate
// the language, so the caller tries the next preference.
func normLang(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if len(tag) < 2 {
		return ""
	}
	code := tag[:2]
	if code == "en" {
		return "en"
	}
	if _, ok := translations[code]; ok {
		return code
	}
	return ""
}

// T translates one English string.
func T(lang, s string) string {
	if m, ok := translations[lang]; ok {
		if v, ok := m[s]; ok {
			return v
		}
	}
	return s
}

// TF translates an English template and formats it.
func TF(lang, tmpl string, args ...any) string {
	return fmt.Sprintf(T(lang, tmpl), args...)
}
