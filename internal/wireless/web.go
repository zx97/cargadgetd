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

func bannerFor(s Snapshot) (string, string) {
	switch {
	case s.Error != "" && !s.Running:
		return "Erreur", "ko"
	case s.Running && s.Phase == "session":
		return "Android Auto connecté", "ok"
	case s.Running:
		return "Connexion…", "wait"
	default:
		return "Arrêté", "idle"
	}
}

func renderPage(snap Snapshot) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
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

	banner, cls := bannerFor(snap)
	fmt.Fprintf(&b, `<div id="banner" class="banner %s">%s</div>`, cls, html.EscapeString(banner))

	state := "arrêté"
	if snap.Running {
		state = "actif"
	}
	phase := snap.Phase
	if phase == "" {
		phase = "—"
	}
	fmt.Fprintf(&b, `<div class="phase" id="phase">état : <b>%s</b> · étape : <b>%s</b></div>`,
		html.EscapeString(state), html.EscapeString(phase))

	if snap.Error != "" {
		fmt.Fprintf(&b, `<div id="err" class="err">%s</div>`, html.EscapeString(snap.Error))
	} else {
		b.WriteString(`<div id="err" class="err" style="display:none"></div>`)
	}

	b.WriteString(`<table id="checks">`)
	for _, c := range snap.Checks {
		dot := "ko"
		if c.OK {
			dot = "ok"
		}
		fmt.Fprintf(&b, `<tr><td style="width:22px"><span class="dot %s"></span></td><td><div class="name">%s</div><div class="detail">%s</div></td></tr>`,
			dot, html.EscapeString(c.Name), html.EscapeString(c.Detail))
	}
	if len(snap.Checks) == 0 {
		b.WriteString(`<tr><td colspan="2" class="detail">En attente</td></tr>`)
	}
	b.WriteString(`</table>`)

	b.WriteString(`<div class="btns">
<form method="post" action="/start" style="flex:1"><button class="start" type="submit">Démarrer</button></form>
<form method="post" action="/stop" style="flex:1"><button class="stop" type="submit">Arrêter</button></form>
<form method="post" action="/restart" style="flex:1"><button class="restart" type="submit">Redémarrer</button></form>
</div>`)

	if snap.WiFiWant != "" {
		fmt.Fprintf(&b, `<form id="wifiForm" method="post" action="/wifi"><button id="wifiBtn" class="wifi" type="submit" style="width:100%%">Rejoindre le Wi-Fi %s</button></form>`,
			html.EscapeString(snap.WiFiWant))
	} else {
		b.WriteString(`<form id="wifiForm" method="post" action="/wifi" style="display:none"><button id="wifiBtn" class="wifi" type="submit" style="width:100%">Rejoindre le Wi-Fi</button></form>`)
	}

	if len(snap.WPP) > 0 {
		b.WriteString(`<pre id="wpp">`)
		for _, line := range snap.WPP {
			b.WriteString(html.EscapeString(line))
			b.WriteByte('\n')
		}
		b.WriteString(`</pre>`)
	} else {
		b.WriteString(`<pre id="wpp" style="display:none"></pre>`)
	}

	b.WriteString(pageScript)
	b.WriteString("</body></html>")
	return b.String()
}

// pageScript polls /status every second and updates the DOM in place, so the
// page never reloads (a meta refresh flickered badly on the phone).
const pageScript = `<script>
const $=id=>document.getElementById(id);
async function tick(){
  let s;
  try { s = await (await fetch('/status',{cache:'no-store'})).json(); } catch(e) { return; }
  let text='Arrêté', cls='idle';
  if (s.Error && !s.Running) { text='Erreur'; cls='ko'; }
  else if (s.Running && s.Phase==='session') { text='Android Auto connecté'; cls='ok'; }
  else if (s.Running) { text='Connexion…'; cls='wait'; }
  const b=$('banner');
  if(b){ b.textContent=text; b.className='banner '+cls; }
  const p=$('phase');
  if(p){ p.textContent='état : '+(s.Running?'actif':'arrêté')+' · étape : '+(s.Phase||'—'); }
  const e=$('err');
  if(e){ if(s.Error){ e.textContent=s.Error; e.style.display=''; } else { e.style.display='none'; } }
  const t=$('checks');
  if(t){
    t.textContent='';
    const checks=s.Checks||[];
    if(!checks.length){
      const tr=document.createElement('tr');
      const td=document.createElement('td');
      td.colSpan=2; td.className='detail'; td.textContent='En attente';
      tr.appendChild(td); t.appendChild(tr);
    }
    for(const c of checks){
      const tr=document.createElement('tr');
      const td1=document.createElement('td'); td1.style.width='22px';
      const dot=document.createElement('span'); dot.className='dot '+(c.OK?'ok':'ko'); td1.appendChild(dot);
      const td2=document.createElement('td');
      const n=document.createElement('div'); n.className='name'; n.textContent=c.Name;
      const d=document.createElement('div'); d.className='detail'; d.textContent=c.Detail;
      td2.appendChild(n); td2.appendChild(d); tr.appendChild(td1); tr.appendChild(td2); t.appendChild(tr);
    }
  }
  const wf=$('wifiForm');
  if(wf){
    if(s.WiFiWant){ wf.style.display=''; const wb=$('wifiBtn'); if(wb) wb.textContent='Rejoindre le Wi-Fi '+s.WiFiWant; }
    else { wf.style.display='none'; }
  }
  const w=$('wpp');
  if(w){
    if(s.WPP && s.WPP.length){ w.textContent=s.WPP.join('\n'); w.style.display=''; }
    else { w.style.display='none'; }
  }
}
tick();
setInterval(tick, 1000);
</script>`
