package wireless

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
)

// Handler serves the wireless status page and its start/stop controls.
func (s *Status) Handler(start, stop func(), joinWiFi func() error, restart func()) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		snap := s.Snapshot()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, renderPage(snap))
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Snapshot())
	})

	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && start != nil {
			start()
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && stop != nil {
			stop()
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && restart != nil {
			restart()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<meta http-equiv="refresh" content="2;url=/"><p>Redémarrage…</p>`)
	})

	mux.HandleFunc("/wifi", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && joinWiFi != nil {
			if err := joinWiFi(); err != nil {
				s.Set("Wi-Fi join", false, err.Error())
			} else {
				s.Set("Wi-Fi join", true, "demandé à ConnMan")
			}
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	return mux
}

func renderPage(snap Snapshot) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="1">
<title>Cargadgetd Wireless</title>
<style>
body{font-family:sans-serif;margin:0;padding:12px;background:#12141a;color:#e7e9ee}
h1{font-size:1.25rem;margin:0 0 10px}
.phase{color:#9aa3b2;margin-bottom:10px}
table{width:100%;border-collapse:collapse;margin-bottom:12px}
td{padding:7px 6px;border-bottom:1px solid #2a2f3a;vertical-align:top}
.dot{display:inline-block;width:14px;height:14px;border-radius:50%;margin-right:8px;vertical-align:middle}
.ok{background:#2ecc71}.ko{background:#e74c3c}.idle{background:#7f8c8d}
.banner{font-size:1.5rem;font-weight:700;padding:18px;border-radius:12px;margin-bottom:12px;text-align:center}
.wait{background:#f39c12}
.name{font-weight:600}
.detail{color:#9aa3b2;font-size:.85rem}
.err{background:#4a1d1d;border:1px solid #e74c3c;padding:9px;border-radius:8px;margin-bottom:12px;word-break:break-word}
.btns{display:flex;gap:10px;margin:12px 0;flex-wrap:wrap}
button{flex:1;padding:14px;font-size:1rem;border-radius:10px;border:0;color:#fff}
.start{background:#2ecc71}.stop{background:#e74c3c}.wifi{background:#3498db}.restart{background:#8e44ad}
pre{background:#0b0d11;border:1px solid #2a2f3a;border-radius:8px;padding:10px;overflow:auto;font-size:.78rem;white-space:pre-wrap;word-break:break-word}
</style></head><body>`)
	b.WriteString("<h1>Cargadgetd Wireless</h1>")

	banner, cls := "Arrêté", "idle"
	switch {
	case snap.Error != "" && !snap.Running:
		banner, cls = "Erreur", "ko"
	case snap.Running && snap.Phase == "session":
		banner, cls = "Android Auto connecté", "ok"
	case snap.Running:
		banner, cls = "Connexion…", "wait"
	}
	fmt.Fprintf(&b, `<div class="banner %s">%s</div>`, cls, html.EscapeString(banner))

	state := "arrêté"
	if snap.Running {
		state = "actif"
	}
	phase := snap.Phase
	if phase == "" {
		phase = "—"
	}
	fmt.Fprintf(&b, `<div class="phase">état : <b>%s</b> · étape : <b>%s</b></div>`,
		html.EscapeString(state), html.EscapeString(phase))

	if snap.Error != "" {
		fmt.Fprintf(&b, `<div class="err">%s</div>`, html.EscapeString(snap.Error))
	}

	b.WriteString("<table>")
	for _, c := range snap.Checks {
		cls := "ko"
		if c.OK {
			cls = "ok"
		}
		fmt.Fprintf(&b, `<tr><td style="width:22px"><span class="dot %s"></span></td><td><div class="name">%s</div><div class="detail">%s</div></td></tr>`,
			cls, html.EscapeString(c.Name), html.EscapeString(c.Detail))
	}
	if len(snap.Checks) == 0 {
		b.WriteString(`<tr><td><span class="dot idle"></span></td><td><div class="name">En attente</div><div class="detail">Appuie sur Démarrer</div></td></tr>`)
	}
	b.WriteString("</table>")

	b.WriteString(`<div class="btns">
<form method="post" action="/start" style="flex:1"><button class="start" type="submit">Démarrer</button></form>
<form method="post" action="/stop" style="flex:1"><button class="stop" type="submit">Arrêter</button></form>
<form method="post" action="/restart" style="flex:1"><button class="restart" type="submit">Redémarrer</button></form>
</div>`)
	if snap.WiFiWant != "" {
		fmt.Fprintf(&b, `<form method="post" action="/wifi"><button class="wifi" type="submit" style="width:100%%">Rejoindre le Wi-Fi %s</button></form>`, html.EscapeString(snap.WiFiWant))
	}

	if len(snap.WPP) > 0 {
		b.WriteString("<pre>")
		for _, line := range snap.WPP {
			b.WriteString(html.EscapeString(line))
			b.WriteByte('\n')
		}
		b.WriteString("</pre>")
	}
	b.WriteString("</body></html>")
	return b.String()
}
