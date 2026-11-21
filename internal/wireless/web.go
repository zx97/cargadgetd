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

	mux.HandleFunc("/usb-fix", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := RepairUSB(); err != nil {
			s.Set("USB réparation", false, err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s.Set("USB réparation", true, "service USB relancé")
		w.WriteHeader(http.StatusNoContent)
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

// usbText renders the wired-path snapshot as three one-line facts, each with
// the dot class the page uses.
func usbText(u USBSnapshot) (svc, mode, sess, svcCls, modeCls, sessCls string) {
	svc = "Service USB : " + u.ServiceState
	svcCls = "ko"
	if u.ServiceState == "active" {
		svcCls = "ok"
	}
	switch u.Mode {
	case "accessory":
		mode = fmt.Sprintf("Mode : accessoire AOA (%s) — la voiture peut parler", u.VendorID)
		modeCls = "ok"
	case "ask":
		if u.VendorID != "" {
			mode = fmt.Sprintf("Mode : attente du câble (%s)", u.VendorID)
		} else {
			mode = "Mode : attente du câble"
		}
		modeCls = "idle"
	case "inconnu":
		mode = "Mode : non lisible (configfs root)"
		modeCls = "idle"
	default:
		mode = "Mode : gadget USB absent"
		modeCls = "ko"
	}
	if u.AccessoryLinked {
		mode += " · accessoire lié"
	}
	if u.HostPresent {
		mode += " · hôte USB branché"
	}
	sess = "Session USB : aucune"
	sessCls = "idle"
	if u.SessionRunning {
		sess = "Session USB : en cours"
		sessCls = "ok"
	}
	return svc, mode, sess, svcCls, modeCls, sessCls
}

func bannerFor(s Snapshot) (string, string) {
	switch {
	case s.Error != "" && !s.Running:
		// A car that is off is the normal case at home, not a failure.
		if strings.Contains(s.Error, "voiture absente") {
			return "En attente de la voiture", "wait"
		}
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
.next{padding:14px;border-radius:12px;margin:0 0 12px;border:1px solid #2f4a63;background:#16202c}
.next .t{font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;color:#9aa3b2;margin-bottom:6px}
#nextText{font-size:1.15rem;font-weight:700;line-height:1.3}
.next.ok{background:#12351f;border-color:#2ecc71}
.next.wait{background:#3a2b10;border-color:#f39c12}
.next.action{background:#122534;border-color:#3498db}
.names{margin:0 0 12px;padding:10px;border-radius:8px;background:#0f1620;border:1px solid #2a2f3a;font-size:.9rem}
.names b{font-weight:700}
details.usb{margin:0 0 12px;padding:10px;border-radius:8px;background:#0f1620;border:1px solid #2a2f3a}
details.usb summary{cursor:pointer;font-weight:700;margin-bottom:8px}
details.usb .row{margin:4px 0;font-size:.92rem}
details.usb button{margin:8px 0;padding:10px;font-size:.95rem;border-radius:8px;border:0;color:#fff;background:#3498db;width:100%}
.btns{display:flex;gap:10px;margin:12px 0;flex-wrap:wrap}
button{flex:1;padding:14px;font-size:1rem;border-radius:10px;border:0;color:#fff}
.start{background:#2ecc71}.stop{background:#e74c3c}.wifi{background:#3498db}.restart{background:#8e44ad}
pre{background:#0b0d11;border:1px solid #2a2f3a;border-radius:8px;padding:10px;overflow:auto;font-size:.78rem;white-space:pre-wrap;word-break:break-word}
</style></head><body>`)
	b.WriteString("<h1>Cargadgetd Wireless</h1>")

	banner, cls := bannerFor(snap)
	fmt.Fprintf(&b, `<div id="banner" class="banner %s">%s</div>`, cls, html.EscapeString(banner))

	fmt.Fprintf(&b, `<div id="next" class="next %s"><div class="t">Quoi faire</div><div id="nextText">%s</div></div>`,
		html.EscapeString(snap.NextKind), html.EscapeString(snap.Next))

	if snap.BluetoothName != "" || snap.USBName != "" {
		b.WriteString(`<div class="names">`)
		if snap.BluetoothName != "" {
			fmt.Fprintf(&b, `<div>Sans fil, la voiture voit <b>« %s »</b> (nom Bluetooth).</div>`,
				html.EscapeString(snap.BluetoothName))
		}
		if snap.USBName != "" {
			fmt.Fprintf(&b, `<div>Avec le câble, elle voit <b>« %s »</b> (nom USB).</div>`,
				html.EscapeString(snap.USBName))
		}
		b.WriteString(`<div class="detail">C'est le même téléphone. Le câble ne sert qu'à la toute première association.</div></div>`)
	}

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

	// Wired path: observed from the outside. The launcher being stopped is the
	// state that looks like "the car refuses the phone", so it is visible here
	// with a one-tap repair.
	svc, mode, sess, svcCls, modeCls, sessCls := usbText(snap.USB)
	b.WriteString(`<details class="usb" open><summary>Câble USB</summary>`)
	fmt.Fprintf(&b, `<div class="row"><span class="dot %s" id="usbSvcDot"></span><span id="usbSvc">%s</span></div>`,
		svcCls, html.EscapeString(svc))
	fmt.Fprintf(&b, `<div class="row"><span class="dot %s" id="usbModeDot"></span><span id="usbMode">%s</span></div>`,
		modeCls, html.EscapeString(mode))
	fmt.Fprintf(&b, `<div class="row"><span class="dot %s" id="usbSessDot"></span><span id="usbSess">%s</span></div>`,
		sessCls, html.EscapeString(sess))
	if snap.USB.Transport != "usb" {
		fmt.Fprintf(&b, `<div class="row detail">Transport configuré : %s (le câble est ignoré).</div>`,
			html.EscapeString(snap.USB.Transport))
	}
	b.WriteString(`<button id="usbFix" type="button">Réparer l'USB</button>`)
	b.WriteString(`<pre id="usbLog" style="margin-top:6px">`)
	for _, line := range snap.USB.Log {
		b.WriteString(html.EscapeString(line))
		b.WriteByte('\n')
	}
	b.WriteString(`</pre></details>`)

	b.WriteString(pageScript)
	b.WriteString(wireUSBFix)
	b.WriteString("</body></html>")
	return b.String()
}

// pageScript polls /status every second and updates the DOM in place, so the
// page never reloads (a meta refresh flickered badly on the phone).
const pageScript = `<script>
const $=id=>document.getElementById(id);
let last='';
async function tick(){
  let txt;
  try { txt = await (await fetch('/status',{cache:'no-store'})).text(); } catch(e) { return; }
  if(txt===last) return;
  last=txt;
  let s;
  try { s = JSON.parse(txt); } catch(e) { return; }
  let text='Arrêté', cls='idle';
  if (s.Error && !s.Running) { if (s.Error.indexOf('voiture absente')>=0) { text='En attente de la voiture'; cls='wait'; } else { text='Erreur'; cls='ko'; } }
  else if (s.Running && s.Phase==='session') { text='Android Auto connecté'; cls='ok'; }
  else if (s.Running) { text='Connexion…'; cls='wait'; }
  const b=$('banner');
  if(b){ b.textContent=text; b.className='banner '+cls; }
  const p=$('phase');
  if(p){ p.textContent='état : '+(s.Running?'actif':'arrêté')+' · étape : '+(s.Phase||'—'); }
  const nx=$('next');
  if(nx){
    nx.className='next '+(s.NextKind||'wait');
    const nt=$('nextText'); if(nt) nt.textContent=s.Next||'';
  }
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
  const u=s.USB||{};
  const setRow=(dotId,textId,cls,text)=>{const d=$(dotId); if(d) d.className='dot '+cls; const t=$(textId); if(t) t.textContent=text;};
  const svcText='Service USB : '+(u.ServiceState||'inconnu');
  const svcCls=u.ServiceState==='active'?'ok':'ko';
  let modeText='Mode : gadget USB absent', modeCls='ko';
  if(u.Mode==='accessory'){ modeText='Mode : accessoire AOA ('+(u.VendorID||'')+') — la voiture peut parler'; modeCls='ok'; }
  else if(u.Mode==='ask'){ modeText=u.VendorID?('Mode : attente du câble ('+u.VendorID+')'):'Mode : attente du câble'; modeCls='idle'; }
  else if(u.Mode==='inconnu'){ modeText='Mode : non lisible (configfs root)'; modeCls='idle'; }
  if(u.AccessoryLinked) modeText+=' · accessoire lié';
  if(u.HostPresent) modeText+=' · hôte USB branché';
  const sessText=u.SessionRunning?'Session USB : en cours':'Session USB : aucune';
  setRow('usbSvcDot','usbSvc',svcCls,svcText);
  setRow('usbModeDot','usbMode',modeCls,modeText);
  setRow('usbSessDot','usbSess',u.SessionRunning?'ok':'idle',sessText);
  const ul=$('usbLog');
  if(ul){ ul.textContent=(u.Log||[]).join('\n'); }
}
tick();
setInterval(tick, 1000);
</script>`

// wireUSBFix makes the repair button a background POST: the page then updates
// from the next /status poll instead of reloading.
const wireUSBFix = `<script>
(function(){
  const b=document.getElementById('usbFix');
  if(!b) return;
  b.addEventListener('click', async function(){
    b.disabled=true; const old=b.textContent; b.textContent='Réparation…';
    try { await fetch('/usb-fix',{method:'POST'}); } catch(e) {}
    setTimeout(()=>{ b.disabled=false; b.textContent=old; }, 1500);
  });
})();
</script>`
