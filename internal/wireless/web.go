package wireless

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"
	"sync"
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
		lang := DetectLang(r)
		snap.Localize(lang)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, renderPage(lang, snap))
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		snap := s.Snapshot()
		snap.Localize(DetectLang(r))
		_ = json.NewEncoder(w).Encode(snap)
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
		fmt.Fprintf(w, `<meta http-equiv="refresh" content="2;url=/"><p>%s</p>`,
			html.EscapeString(T(DetectLang(r), "Restarting…")))
	})

	mux.HandleFunc("/bt-capture", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		msg := T(DetectLang(r), "Recording Bluetooth…")
		if capture := s.captureFn(); capture != nil {
			if err := capture(); err != nil {
				msg = err.Error()
			}
		} else {
			msg = T(DetectLang(r), "capture unavailable")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<meta http-equiv="refresh" content="3;url=/"><p>%s</p>`, html.EscapeString(msg))
	})

	mux.HandleFunc("/bt-debug", func(w http.ResponseWriter, r *http.Request) {
		lang := DetectLang(r)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		snap, err := PowerBT(log.Printf)
		s.setBTDebug(renderBTDebug(lang, snap, err))
		http.Redirect(w, r, "/#bluetooth", http.StatusSeeOther)
	})

	mux.HandleFunc("/usb-fix", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := RepairUSB(); err != nil {
			s.Set("USB repair", false, err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s.Set("USB repair", true, "USB service restarted")
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/wifi", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && joinWiFi != nil {
			if err := joinWiFi(); err != nil {
				s.Set("Wi-Fi join", false, err.Error())
			} else {
				s.Set("Wi-Fi join", true, "requested from ConnMan")
			}
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	return mux
}

// usbText renders the wired-path snapshot as three one-line facts, each with
// the dot class the page uses.
func usbText(lang string, u USBSnapshot) (svc, mode, sess, svcCls, modeCls, sessCls string) {
	svc = TF(lang, "USB service: %s", orUnknown(lang, u.ServiceState))
	svcCls = "ko"
	if u.ServiceState == "active" {
		svcCls = "ok"
	}
	switch u.Mode {
	case "accessory":
		mode = TF(lang, "Mode: AOA accessory (%s) — the car can talk", u.VendorID)
		modeCls = "ok"
	case "ask":
		if u.VendorID != "" {
			mode = TF(lang, "Mode: waiting for the cable (%s)", u.VendorID)
		} else {
			mode = T(lang, "Mode: waiting for the cable")
		}
		modeCls = "idle"
	case "inconnu", "unknown":
		mode = T(lang, "Mode: unreadable (configfs is root-only)")
		modeCls = "idle"
	default:
		mode = T(lang, "Mode: no USB gadget")
		modeCls = "ko"
	}
	if u.AccessoryLinked {
		mode += " · " + T(lang, "accessory linked")
	}
	if u.HostPresent {
		mode += " · " + T(lang, "USB host attached")
	}
	sess = T(lang, "USB session: none")
	sessCls = "idle"
	if u.SessionRunning {
		sess = T(lang, "USB session: running")
		sessCls = "ok"
	}
	return svc, mode, sess, svcCls, modeCls, sessCls
}

// orUnknown keeps an empty observed value readable instead of printing
// "USB service: " with nothing after it.
func orUnknown(lang, v string) string {
	if v == "" {
		return T(lang, "unknown")
	}
	return v
}

// phaseLine is the one-line state summary, translated.
func phaseLine(lang string, s Snapshot) string {
	state := T(lang, "stopped")
	if s.Running {
		state = T(lang, "active")
	}
	phase := s.Phase
	if phase == "" {
		phase = "—"
	}
	return TF(lang, "state: %s · step: %s", state, phase)
}

// flowHTML renders the workflow diagram: one row per stage with a mark that
// tells done / current / failed / pending.
func flowHTML(stages []Stage) string {
	var b strings.Builder
	for _, st := range stages {
		mark := "·"
		switch st.State {
		case "done":
			mark = "✔"
		case "current":
			mark = "▶"
		case "failed":
			mark = "✖"
		}
		fmt.Fprintf(&b, `<li class="%s"><span class="mark">%s</span><span><span class="lbl">%s</span>`,
			html.EscapeString(st.State), mark, html.EscapeString(st.Label))
		if st.Detail != "" {
			fmt.Fprintf(&b, `<br><span class="det">%s</span>`, html.EscapeString(st.Detail))
		}
		b.WriteString(`</span></li>`)
	}
	return b.String()
}

func bannerFor(lang string, s Snapshot) (string, string) {
	switch {
	case s.Error != "" && !s.Running:
		// A car that is off is the normal case at home, not a failure.
		if strings.Contains(s.Error, "car absent") || strings.Contains(s.Error, "unreachable") {
			return T(lang, "Waiting for the car"), "wait"
		}
		if strings.Contains(s.Error, "no paired car") {
			return T(lang, "Car not paired"), "wait"
		}
		return T(lang, "Error"), "ko"
	case s.Running && s.Phase == "session":
		return T(lang, "Android Auto connected"), "ok"
	case s.Running:
		return T(lang, "Connecting…"), "wait"
	default:
		return T(lang, "Stopped"), "idle"
	}
}

func renderPage(lang string, snap Snapshot) string {
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
.flow{list-style:none;margin:0 0 12px;padding:0}
.flow li{display:flex;gap:10px;align-items:flex-start;padding:8px 10px;border-left:3px solid #2a2f3a;margin:0}
.flow li .mark{width:22px;flex:0 0 22px;text-align:center;font-weight:700}
.flow li .lbl{font-weight:600}
.flow li .det{color:#9aa3b2;font-size:.82rem;word-break:break-word}
.flow li.done{border-left-color:#2ecc71}.flow li.done .mark{color:#2ecc71}
.flow li.current{border-left-color:#3498db;background:#122534}.flow li.current .mark{color:#3498db}
.flow li.failed{border-left-color:#e74c3c;background:#2a1414}.flow li.failed .mark{color:#e74c3c}
.flow li.pending{border-left-color:#2a2f3a;opacity:.55}
.names{margin:0 0 12px;padding:10px;border-radius:8px;background:#0f1620;border:1px solid #2a2f3a;font-size:.9rem}
.names b{font-weight:700}
details.usb{margin:0 0 12px;padding:10px;border-radius:8px;background:#0f1620;border:1px solid #2a2f3a}
details.usb summary{cursor:pointer;font-weight:700;margin-bottom:8px}
details.usb .row{margin:4px 0;font-size:.92rem}
details.usb button{margin:8px 0;padding:10px;font-size:.95rem;border-radius:8px;border:0;color:#fff;background:#3498db;width:100%}
.btns{display:flex;gap:10px;margin:12px 0;flex-wrap:wrap}
button{flex:1;padding:14px;font-size:1rem;border-radius:10px;border:0;color:#fff}
.start{background:#2ecc71}.stop{background:#e74c3c}.wifi{background:#3498db}.restart{background:#8e44ad}
.tabs{display:flex;gap:6px;margin:0 0 10px}
.tab{flex:1;padding:9px 6px;font-size:.85rem;border:1px solid #2a2f3a;background:#0f1620;color:#9aa3b2;border-radius:8px}
.tab.active{background:#122534;color:#e7e9ee;border-color:#3498db;font-weight:700}
.pane{display:none}.pane.active{display:block}
pre{background:#0b0d11;border:1px solid #2a2f3a;border-radius:8px;padding:10px;overflow:auto;font-size:.78rem;white-space:pre-wrap;word-break:break-word}
#p-log pre{height:62vh;min-height:260px;overflow:auto;-webkit-overflow-scrolling:touch}
#wpp{height:46vh;min-height:200px}

/* First screen: verdict, live chips, big buttons. Touch first: nothing under
   44px tall, colour always doubled by a word. */
.hero{display:flex;gap:14px;align-items:center;padding:18px;border-radius:16px;margin:10px 0 14px;border:1px solid #2a2f3a;background:#0f1620}
.hero.ok{background:#0f2418;border-color:#2ecc71}
.hero.ko{background:#2a1414;border-color:#e74c3c}
.hero.wait{background:#231d0d;border-color:#f1c40f}
.hero .hdot{width:22px;height:22px;border-radius:50%;flex:0 0 22px;background:#7f8c8d}
.hero.ok .hdot{background:#2ecc71}.hero.ko .hdot{background:#e74c3c}.hero.wait .hdot{background:#f1c40f}
.hero h2{margin:0;font-size:1.35rem;line-height:1.25}
.hero p{margin:6px 0 0;font-size:1rem;color:#c7cdd6}
.chips{display:flex;gap:8px;flex-wrap:wrap;margin:0 0 14px}
.chipform{flex:1 1 30%;margin:0}
.chip{display:block;width:100%;padding:14px 10px;border-radius:14px;border:1px solid #2a2f3a;background:#0f1620;color:#e7e9ee;font-size:.95rem;text-align:left;min-height:52px}
.chip.ok{border-color:#2ecc71}.chip.ko{border-color:#e74c3c}.chip.wait{border-color:#f1c40f}
.actions{display:flex;gap:8px;margin:0 0 16px}
.actions form{margin:0;display:flex}
.actions button{width:100%;padding:18px 10px;font-size:1.05rem;font-weight:700;border-radius:14px;border:0;color:#fff}
.go{background:#2ecc71}.relaunch{background:#8e44ad}.halt{background:#e74c3c}
.debug{margin:10px 0 16px;padding:12px;border-radius:12px;background:#0b0d11;border:1px solid #2a2f3a}
.debug .dtitle{font-weight:700;margin-bottom:8px}
.debug .drow{font-size:.9rem;margin:4px 0;color:#c7cdd6}
.debug .dbad{color:#e74c3c;margin:6px 0}
.debug .dim{color:#7f8c8d}
</style></head><body>`)
	fmt.Fprintf(&b, "<h1>%s</h1>", html.EscapeString(T(lang, "Cargadgetd Wireless")))

	banner, cls := snap.Banner, snap.BannerCls
	fmt.Fprintf(&b, `<div id="banner" class="banner %s">%s</div>`, cls, html.EscapeString(banner))

	// Live state, read from the system on this very request: the workflow below
	// is the journal of the last attempt, and on its own it kept showing
	// "Bluetooth link ✓" long after the adapter had been switched off.
	b.WriteString(liveBlockHTML(lang, snap))

	fmt.Fprintf(&b, `<div id="next" class="next %s"><div class="t">%s</div><div id="nextText">%s</div></div>`,
		html.EscapeString(snap.NextKind), html.EscapeString(T(lang, "What to do")), html.EscapeString(snap.Next))

	fmt.Fprintf(&b, `<div class="tabs"><button class="tab active" data-p="state">%s</button><button class="tab" data-p="details">%s</button><button class="tab" data-p="log">%s</button><button class="tab" data-p="usb">%s</button></div>`,
		html.EscapeString(T(lang, "State")), html.EscapeString(T(lang, "Details")),
		html.EscapeString(T(lang, "Log")), html.EscapeString(T(lang, "Cable")))

	// State: the workflow diagram and nothing else.
	b.WriteString(`<div class="pane active" id="p-state">`)
	b.WriteString(`<ol class="flow" id="flow">`)
	b.WriteString(flowHTML(snap.Stages))
	b.WriteString(`</ol>`)
	fmt.Fprintf(&b, `<div class="phase" id="phase">%s</div>`, html.EscapeString(snap.PhaseLine))
	b.WriteString(`</div>`)

	// Details: names, error and the check list.
	b.WriteString(`<div class="pane" id="p-details">`)
	if snap.BluetoothName != "" || snap.USBName != "" {
		b.WriteString(`<div class="names">`)
		if snap.BluetoothName != "" {
			fmt.Fprintf(&b, `<div>`+T(lang, "Wireless, the car sees %s (Bluetooth name).")+`</div>`,
				html.EscapeString(snap.BluetoothName))
		}
		if snap.USBName != "" {
			fmt.Fprintf(&b, `<div>`+T(lang, "With the cable, it sees %s (USB name).")+`</div>`,
				html.EscapeString(snap.USBName))
		}
		b.WriteString(`<div class="detail">` + T(lang, "Same phone.") + `</div></div>`)
	}
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
		b.WriteString(`<tr><td colspan="2" class="detail">` + T(lang, "Waiting") + `</td></tr>`)
	}
	b.WriteString(`</table></div>`)

	// Log: what the daemon discovered and did, plus the WPP exchange.
	b.WriteString(`<div class="pane" id="p-log">`)
	b.WriteString(`<pre id="steps">`)
	for _, line := range snap.Steps {
		b.WriteString(html.EscapeString(line))
		b.WriteByte('\n')
	}
	b.WriteString(`</pre>`)
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
	b.WriteString(`</div>`)

	// Cable: the wired path, observed from the outside.
	b.WriteString(`<div class="pane" id="p-usb">`)
	svc, mode, sess, svcCls, modeCls, sessCls := snap.USBSvc, snap.USBMode, snap.USBSess, snap.USBSvcCls, snap.USBModeCls, snap.USBSessCls
	fmt.Fprintf(&b, `<div class="row"><span class="dot %s" id="usbSvcDot"></span><span id="usbSvc">%s</span></div>`,
		svcCls, html.EscapeString(svc))
	fmt.Fprintf(&b, `<div class="row"><span class="dot %s" id="usbModeDot"></span><span id="usbMode">%s</span></div>`,
		modeCls, html.EscapeString(mode))
	fmt.Fprintf(&b, `<div class="row"><span class="dot %s" id="usbSessDot"></span><span id="usbSess">%s</span></div>`,
		sessCls, html.EscapeString(sess))
	if snap.USB.Transport != "usb" {
		fmt.Fprintf(&b, `<div class="row detail">%s</div>`,
			html.EscapeString(TF(lang, "Configured transport: %s (the cable is ignored).", snap.USB.Transport)))
	}
	fmt.Fprintf(&b, `<button id="usbFix" type="button" data-busy="%s">%s</button>`,
		html.EscapeString(T(lang, "Repairing…")), html.EscapeString(T(lang, "Repair USB")))
	b.WriteString(`<pre id="usbLog" style="margin-top:6px">`)
	for _, line := range snap.USB.Log {
		b.WriteString(html.EscapeString(line))
		b.WriteByte('\n')
	}
	b.WriteString(`</pre></div>`)

	// Actions, always reachable whatever the tab.
	b.WriteString(`<div class="btns">
<form method="post" action="/start" style="flex:1"><button class="start" type="submit">` + T(lang, "Start") + `</button></form>
<form method="post" action="/stop" style="flex:1"><button class="stop" type="submit">` + T(lang, "Stop") + `</button></form>
<form method="post" action="/restart" style="flex:1"><button class="restart" type="submit">` + T(lang, "Restart") + `</button></form>
</div>`)
	// Bluetooth capture: 3 minutes into the home directory, then readable from
	// the PC. It needs root, which the setuid helper provides.
	b.WriteString(`<form method="post" action="/bt-capture" style="margin-top:8px"><button type="submit" style="width:100%;padding:12px;font-size:.95rem;border-radius:10px;border:0;color:#fff;background:#7f5af0">` +
		T(lang, "Record Bluetooth (3 min)") + `</button></form>`)

	if snap.WiFiWant != "" {
		fmt.Fprintf(&b, `<form id="wifiForm" method="post" action="/wifi"><button id="wifiBtn" class="wifi" type="submit" style="width:100%%">`+T(lang, "Join Wi-Fi %s")+`</button></form>`,
			html.EscapeString(snap.WiFiWant))
	} else {
		b.WriteString(`<form id="wifiForm" method="post" action="/wifi" style="display:none"><button id="wifiBtn" class="wifi" type="submit" style="width:100%">` + T(lang, "Join Wi-Fi") + `</button></form>`)
	}

	b.WriteString(pageScript)
	b.WriteString(wireUSBFix)
	b.WriteString("</body></html>")
	return b.String()
}

// pageScript polls /status every second and updates the DOM in place, so the
// page never reloads (a meta refresh flickered badly on the phone). It carries
// no text of its own: every label comes from the JSON, already translated
// server-side, so the same script serves every language.
const pageScript = `<script>
const $=id=>document.getElementById(id);
document.querySelectorAll('.tab').forEach(t=>t.addEventListener('click',()=>{
  document.querySelectorAll('.tab').forEach(x=>x.classList.remove('active'));
  document.querySelectorAll('.pane').forEach(p=>p.classList.remove('active'));
  t.classList.add('active');
  const p=document.getElementById('p-'+t.dataset.p); if(p) p.classList.add('active');
}));
let last='';
async function tick(){
  let txt;
  try { txt = await (await fetch('/status',{cache:'no-store'})).text(); } catch(e) { return; }
  if(txt===last) return;
  last=txt;
  let s;
  try { s = JSON.parse(txt); } catch(e) { return; }
  const b=$('banner');
  if(b){ b.textContent=s.Banner||''; b.className='banner '+(s.BannerCls||'idle'); }
  const h=$('hero');
  if(h){
    h.className='hero '+((s.Hero&&s.Hero.Kind)||'wait');
    const t=$('heroTitle'); if(t) t.textContent=(s.Hero&&s.Hero.Title)||'';
    const hh=$('heroHint'); if(hh) hh.textContent=(s.Hero&&s.Hero.Hint)||'';
  }
  (s.Chips||[]).forEach((c,i)=>{
    const el=$('chip'+i);
    if(el){ el.className='chip '+(c.Kind||'wait'); el.textContent=c.Text||''; }
  });
  const p=$('phase');
  if(p){ p.textContent=s.PhaseLine||''; }
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
      td.colSpan=2; td.className='detail'; td.textContent=s.Waiting||'';
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
    if(s.WiFiWant){ wf.style.display=''; const wb=$('wifiBtn'); if(wb) wb.textContent=s.WiFiLabel||''; }
    else { wf.style.display='none'; }
  }
  const fl=$('flow');
  if(fl){
    const marks={done:'✔',current:'▶',failed:'✖',pending:'·'};
    fl.textContent='';
    for(const st of (s.Stages||[])){
      const li=document.createElement('li'); li.className=st.State||'pending';
      const mk=document.createElement('span'); mk.className='mark'; mk.textContent=marks[st.State]||'·';
      const box=document.createElement('span');
      const lbl=document.createElement('span'); lbl.className='lbl'; lbl.textContent=st.Label;
      box.appendChild(lbl);
      if(st.Detail){ const br=document.createElement('br'); const det=document.createElement('span'); det.className='det'; det.textContent=st.Detail; box.appendChild(br); box.appendChild(det); }
      li.appendChild(mk); li.appendChild(box); fl.appendChild(li);
    }
  }
  const stp=$('steps');
  if(stp){ stp.textContent=(s.Steps||[]).join(String.fromCharCode(10)); }
  const w=$('wpp');
  if(w){
    if(s.WPP && s.WPP.length){ w.textContent=s.WPP.join(String.fromCharCode(10)); w.style.display=''; }
    else { w.style.display='none'; }
  }
  const setRow=(dotId,textId,cls,text)=>{const d=$(dotId); if(d) d.className='dot '+(cls||'idle'); const x=$(textId); if(x) x.textContent=text||'';};
  setRow('usbSvcDot','usbSvc',s.USBSvcCls,s.USBSvc);
  setRow('usbModeDot','usbMode',s.USBModeCls,s.USBMode);
  setRow('usbSessDot','usbSess',s.USBSessCls,s.USBSess);
  const ul=$('usbLog');
  if(ul){ ul.textContent=(s.USB&&s.USB.Log||[]).join(String.fromCharCode(10)); }
}
tick();
setInterval(tick, 1000);
</script>`

// wireUSBFix makes the repair button a background POST: the page then updates
// from the next /status poll instead of reloading. The busy label is a data
// attribute written by the server, so this script stays language-neutral.
const wireUSBFix = `<script>
(function(){
  const b=document.getElementById('usbFix');
  if(!b) return;
  b.addEventListener('click', async function(){
    b.disabled=true; const old=b.textContent;
    b.textContent=b.dataset.busy||old;
    try { await fetch('/usb-fix',{method:'POST'}); } catch(e) {}
    setTimeout(function(){ b.disabled=false; b.textContent=old; }, 1500);
  });
})();
</script>`

// liveBlockHTML is the first screen: one verdict, three live facts, three big
// buttons. Everything is translated here; the page script only swaps texts it
// receives from /status, so it stays language-free.
func liveBlockHTML(lang string, snap Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<section class="hero %s" id="hero"><span class="hdot"></span><div class="htx"><h2 id="heroTitle">%s</h2><p id="heroHint">%s</p></div></section>`,
		html.EscapeString(snap.Hero.Kind), html.EscapeString(snap.Hero.Title), html.EscapeString(snap.Hero.Hint))

	b.WriteString(`<div class="chips" id="chips">`)
	for i, c := range snap.Chips {
		if c.Action != "" {
			fmt.Fprintf(&b, `<form method="post" action="%s" class="chipform"><button type="submit" class="chip %s" id="chip%d">%s</button></form>`,
				html.EscapeString(c.Action), html.EscapeString(c.Kind), i, html.EscapeString(c.Text))
			continue
		}
		fmt.Fprintf(&b, `<div class="chip %s" id="chip%d">%s</div>`, html.EscapeString(c.Kind), i, html.EscapeString(c.Text))
	}
	b.WriteString(`</div>`)

	b.WriteString(`<div class="actions">
<form method="post" action="/start" style="flex:2"><button class="go" type="submit">` + T(lang, "Connect") + `</button></form>
<form method="post" action="/restart" style="flex:1"><button class="relaunch" type="submit">` + T(lang, "Restart") + `</button></form>
<form method="post" action="/stop" style="flex:1"><button class="halt" type="submit">` + T(lang, "Stop") + `</button></form>
</div>`)

	if dbg := debugHTML(); dbg != "" {
		b.WriteString(dbg)
	}
	return b.String()
}

// renderBTDebug is what the Bluetooth chip returns: the adapter as BlueZ sees
// it, and the devices with the class that decides who gets dialled.
func renderBTDebug(lang string, snap BTSnapshot, err error) string {
	var b strings.Builder
	b.WriteString(`<div class="debug" id="btdebug">`)
	fmt.Fprintf(&b, `<div class="dtitle">%s</div>`, html.EscapeString(T(lang, "Bluetooth, as BlueZ reports it")))
	if err != nil {
		fmt.Fprintf(&b, `<div class="dbad">%s</div>`, html.EscapeString(err.Error()))
	}
	state := T(lang, "off")
	if snap.Powered {
		state = T(lang, "on")
	}
	adapter := snap.Adapter
	if adapter == "" {
		adapter = T(lang, "none")
	}
	fmt.Fprintf(&b, `<div class="drow">%s</div>`, html.EscapeString(TF(lang, "Adapter: %s (%s)", adapter, state)))
	if snap.Err != "" {
		fmt.Fprintf(&b, `<div class="dbad">%s</div>`, html.EscapeString(snap.Err))
	}
	if len(snap.Devices) == 0 {
		fmt.Fprintf(&b, `<div class="drow">%s</div>`, html.EscapeString(T(lang, "No device known")))
	}
	for _, d := range snap.Devices {
		fmt.Fprintf(&b, `<div class="drow">• %s</div>`, html.EscapeString(DeviceLabel(lang, d)))
	}
	fmt.Fprintf(&b, `<div class="drow dim">%s</div>`, html.EscapeString(T(lang, "Touch the Bluetooth chip to switch the adapter on and read it again.")))
	b.WriteString(`</div>`)
	return b.String()
}

// btDebugHTML is the last Bluetooth diagnostic, kept until the next one. A
// package variable rather than a Status field: it belongs to the page, not to
// the bootstrap state machine.
var btDebug struct {
	sync.Mutex
	html string
}

func (s *Status) setBTDebug(h string) {
	btDebug.Lock()
	btDebug.html = h
	btDebug.Unlock()
}

func debugHTML() string {
	btDebug.Lock()
	defer btDebug.Unlock()
	return btDebug.html
}
