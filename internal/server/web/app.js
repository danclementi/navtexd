"use strict";
// NAVTEX web client: live header, spectrum, options and system log. Plain JavaScript, no build step.
(() => {
  const $ = (id) => document.getElementById(id);
  const state = { sel: 0, chans: [], role: "viewer", offset: 0, text: "", controls: null, sun: null, meters: null, tab: "status", loc: false, spec: null, yTop: -20, view: { spec: { yTop: -20, yTopLin: 0, geom: null, carX: null }, "spec-mini": { yTop: -20, yTopLin: 0, geom: null, carX: null }, "spec-mini2": { yTop: -20, yTopLin: 0, geom: null, carX: null } } };

  // ---- helpers ----
  const NX = (window.NX = { $, state, tabs: {}, hooks: {} });
  const fire = (n, d) => (NX.hooks[n] || []).forEach((f) => f(d));
  NX.listen = (n, f) => { (NX.hooks[n] = NX.hooks[n] || []).push(f); };
  const FONT = getComputedStyle(document.body).fontFamily;
  NX.FONT = FONT;
  const pad = (n, w = 2) => String(n).padStart(w, "0");
  const hms = (ms) => { const d = new Date(ms); return `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}`; };
  NX.pad = pad; NX.hms = hms;
  NX.stamp = (ms) => { const d = new Date(ms); return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${hms(ms)}`; };
  NX.el = (tag, props, ...kids) => {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(props || {})) { if (k === "class") e.className = v; else if (k === "text") e.textContent = v; else if (k.startsWith("on")) e[k] = v; else if (v === true) e.setAttribute(k, ""); else if (v !== false && v != null) e.setAttribute(k, v); }
    for (const c of kids.flat()) if (c != null) e.append(c);
    return e;
  };
  const cssVar = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();

  async function api(method, path, body) {
    const opt = { method, headers: { "X-Navtex": "1" }, credentials: "same-origin" };
    if (body !== undefined) { opt.headers["Content-Type"] = "application/json"; opt.body = JSON.stringify(body); }
    const r = await fetch(path, opt);
    let j = null;
    try { j = await r.json(); } catch (e) { /* no body */ }
    if (!r.ok) { const err = new Error((j && j.error) || r.statusText); err.needToken = !!(j && j.need_token); throw err; }
    return j;
  }
  NX.api = api;
  // Controls act on the selected decoding channel (state.sel: 0 = the first, 1 = the second).
  const control = (name, value, ch = state.sel) => api("POST", "/api/control/" + name + (ch ? "?ch=" + ch : ""), value === undefined ? undefined : { value }).catch((e) => alert(e.message));

  // ---- header ----
  // round dial gauges in the style of YaND's: dark green face, needle, red end of the scale
  const DIAL = { cx: 40, cy: 42, r: 27, a0: 205, a1: -25 };
  const dpt = (frac, r) => { const a = (DIAL.a0 + (DIAL.a1 - DIAL.a0) * frac) * Math.PI / 180; return [DIAL.cx + r * Math.cos(a), DIAL.cy - r * Math.sin(a)]; };
  const arc = (f0, f1, r) => { const [x0, y0] = dpt(f0, r), [x1, y1] = dpt(f1, r); return `M${x0.toFixed(1)} ${y0.toFixed(1)}A${r} ${r} 0 ${(f1 - f0) * (DIAL.a0 - DIAL.a1) > 180 ? 1 : 0} 1 ${x1.toFixed(1)} ${y1.toFixed(1)}`; };
  document.querySelectorAll(".dial").forEach((d) => {
    const id = d.dataset.id, red = Number(d.dataset.red);
    let ticks = "";
    for (let i = 0; i <= 10; i++) { const [x0, y0] = dpt(i / 10, DIAL.r - 1), [x1, y1] = dpt(i / 10, DIAL.r + (i % 5 ? 2 : 4)); ticks += `<path d="M${x0.toFixed(1)} ${y0.toFixed(1)}L${x1.toFixed(1)} ${y1.toFixed(1)}" stroke="${red && i / 10 >= red ? "#ff5a4f" : "#dfe9e3"}" stroke-width="1"/>`; }
    d.innerHTML = `<svg viewBox="0 0 80 68" role="img" aria-label="${d.dataset.label}"><rect x=".5" y=".5" width="79" height="67" rx="7" fill="#0a3524" stroke="#000"/>
      <text x="40" y="10" class="dl">${d.dataset.label}</text>
      <path d="${arc(0, 1, DIAL.r)}" fill="none" stroke="#dfe9e3" stroke-width="1.6"/>${red ? `<path d="${arc(red, 1, DIAL.r)}" fill="none" stroke="#ff5a4f" stroke-width="2.6"/>` : ""}${ticks}
      <line id="g-${id}" x1="${DIAL.cx}" y1="${DIAL.cy}" x2="${dpt(0, DIAL.r - 3)[0]}" y2="${dpt(0, DIAL.r - 3)[1]}" stroke="#7fe3c4" stroke-width="2" stroke-linecap="round"/>
      <circle cx="${DIAL.cx}" cy="${DIAL.cy}" r="3.2" fill="#7fe3c4"/>
      <text id="t-${id}" x="40" y="63" class="dv">-</text></svg>`;
  });
  function setBar(id, frac, text) {
    const [x, y] = dpt(Math.max(0, Math.min(1, frac)), DIAL.r - 3);
    const n = $("g-" + id);
    n.setAttribute("x2", x.toFixed(1)); n.setAttribute("y2", y.toFixed(1));
    $("t-" + id).textContent = text;
  }
  const fmtRate = (bps) => (bps < 1000 ? `${bps} B/s` : bps < 1e6 ? `${(bps / 1000).toFixed(1)} kB/s` : `${(bps / 1e6).toFixed(2)} MB/s`);
  function showNet(n) { if (!n) return; $("t-net").textContent = fmtRate(n.bps); $("t-users").textContent = String(n.users); }
  function led(el, mode) { if (el) el.className = "led" + (mode === 2 ? " on" : mode === 1 ? " mid" : ""); }

  function showMeters(m) {
    state.meters = m;
    const db = m.peak > 0 ? 20 * Math.log10(m.peak) : -90;
    setBar("level", (db + 60) / 60, m.peak > 0 ? db.toFixed(0) + " dB" : "-", m.clip ? cssVar("--bad") : db > -6 ? cssVar("--warn") : "");
    setBar("q", m.q / 100, m.q.toFixed(0), m.q < 25 ? cssVar("--bad") : m.q < 50 ? cssVar("--warn") : "");
    setBar("cpu", m.cpu / 100, m.cpu.toFixed(0) + "%", m.cpu > 80 ? cssVar("--bad") : "");
  }
  // ---- per-channel header: the channel line (chip, frequency, input, comment, format, RESYNC, LEDs, UoS, polarity) and the small spectrum's controls ----
  const L = (i, sel) => document.querySelector(`.chline[data-ch="${i}"] ${sel}`);
  const S = (i, sel) => document.querySelector(`.specgrp[data-ch="${i}"] ${sel}`);
  function showLeds(i) {
    const m = state.chans[i].meters;
    if (!m) return;
    led(L(i, ".l-sync"), m.sync);
    led(L(i, ".l-data"), m.data ? 2 : 0);
    led(S(i, ".l-lock"), m.have ? 2 : 0);
    const cr = S(i, ".c-carrier");
    if (document.activeElement !== cr && m.audio) cr.placeholder = m.audio.toFixed(0);
  }
  function showChan(i) {
    const c = state.chans[i].controls;
    if (!c) return;
    const set = (el, v) => { if (document.activeElement !== el) { if (el.type === "checkbox") el.checked = v; else el.value = v; } };
    set(L(i, ".c-polarity"), c.polarity);
    set(L(i, ".c-uos"), c.uos);
    set(S(i, ".c-autotune"), c.autotune);
    const cr = S(i, ".c-carrier");
    if (!c.autotune) set(cr, c.carrier); else if (document.activeElement !== cr) cr.value = "";
    const chip = L(i, ".chip");
    chip.textContent = c.label || c.name || `Channel ${i + 1}`;
    chip.classList.toggle("on", i === state.sel);
    L(i, ".chfreq").textContent = c.channel + " kHz";
    const cm = L(i, ".chcmt"); cm.textContent = c.note || ""; cm.title = c.note ? "Channel comment: " + c.note : "Channel comment (change it in Options)";
  }
  function showInfo(i) {
    const r = state.chans[i].run, el = L(i, ".chinfo");
    if (!r) { el.textContent = ""; return; }
    el.textContent = r.error ? "\u26a0 " + r.error : (r.format || "");
    el.classList.toggle("bad", !!r.error);
    el.title = (r.running ? "" : "Not running. ") + (r.info || "");
  }
  // lines, boxes and the add chip follow the number of channels
  function renderLines() {
    const two = secondOn(), admin = state.role === "admin";
    L(1, ".chip").closest(".chline").hidden = !two;
    $("grp-spec2").hidden = !two;
    $("b-addchan").hidden = two || !admin;
    for (let i = 0; i < 2; i++) { const ch = L(i, ".chip"); if (ch) ch.classList.toggle("on", i === state.sel); }
    document.body.classList.toggle("two", two);
    scheduleSpec();
  }

  function showControls(c) {
    state.controls = c;
    scheduleText();
  }
  // Which input is in use: asked of the server (administrators only) whenever the controls change.
  async function refreshInput(ch) {
    if (state.role !== "admin") return;
    const k = state.chans[ch].controls;
    if (!k || !k.enabled) return;
    try {
      const i = await api("GET", "/api/input" + (ch ? "?ch=" + ch : ""));
      state.chans[ch].inputWanted = i.source === "hpsdr" ? "r:" + i.radio : "a:" + i.device;
      showInput(ch);
    } catch (e) { /* not available */ }
  }
  // The Input box: audio inputs ("a:name") and radios ("r:address"; "r:" searches the network).
  function showInput(ch) {
    const sel = L(ch, ".c-device"), want = state.chans[ch].inputWanted;
    if (want === undefined || document.activeElement === sel) return;
    if (![...sel.options].some((o) => o.value === want)) { // a radio that has not delivered data yet, or an unlisted device
      const o = new Option(want.startsWith("r:") ? want.slice(2) + "  (not confirmed yet)" : want.slice(2), want);
      (want.startsWith("r:") ? sel.querySelector("optgroup.radios") : sel.querySelector("optgroup.audio"))?.prepend(o);
    }
    sel.value = want;
  }

  function showSun(s) {
    state.sun = s;
    $("sun").hidden = !state.loc; // sunrise/sunset reveal the receiver's position
    $("sun").textContent = s ? `Sunrise: ${s.rise} / Sunset: ${s.set}` : "Sunrise: --:-- / Sunset: --:--";
  }

  function showRole(role, needToken) {
    state.role = role;
    const admin = role === "admin";
    const r = $("role");
    r.textContent = admin ? "admin" : "view only";
    r.className = "pill " + (admin ? "ok" : "");
    document.querySelectorAll("#chlines select, #chlines input, #chlines .b-resync, .specgrp input, #b-clip").forEach((el) => { el.disabled = !admin; });
    $("login").hidden = !needToken;
    if (state.tab === "options") loadOptions(true);
    if (state.tab === "syslog") loadLog();
    renderLines();
    fire("role", role);
  }

  function showRun(r) {
    let t = r.info || "";
    if (r.error) t = "⚠ " + r.error;
    const tip = (r.running ? "" : "Not running. ") + t;
    document.querySelectorAll("#hdr .grp").forEach((g) => { g.title = tip; });
  }

  function clock() {
    const now = Date.now() + state.offset;
    const d = new Date(now);
    $("clock").textContent = hms(now) + " UTC";
    $("date").textContent = `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`;
  }
  setInterval(clock, 250);

  // ---- text ----
  const TEXT_KEEP = 8000, LINE_KEEP = 300;
  state.lines = []; state.tail = "";
  NX.lineText = (l) => {
    const q = state.controls && state.controls.lineq === false ? "" : `${String(Math.round(l.q)).padStart(2, "0")}> `;
    return `${NX.stamp(l.t)} ${q}${l.x}`;
  };
  // o is the selected channel's live text (state) or another channel's stored one
  function addTextTo(o, x) {
    o.text += x;
    if (o.text.length > TEXT_KEEP) o.text = o.text.slice(o.text.length - TEXT_KEEP);
    const i = x.lastIndexOf("\n");
    o.tail = (i >= 0 ? x.slice(i + 1) : o.tail + x).replace(/\r/g, "");
    if (o.tail.length > 200) o.tail = "";
  }
  function addText(x) { addTextTo(state, x); scheduleText(); }
  function addLineTo(o, l) { o.lines.push(l); if (o.lines.length > LINE_KEEP) o.lines.shift(); }
  function addLine(l) { addLineTo(state, l); scheduleText(); }
  let textPending = false;
  function scheduleText() { if (!textPending) { textPending = true; requestAnimationFrame(drawText); } }
  function drawText() {
    textPending = false;
    const pre = $("text");
    const atEnd = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 8;
    let txt = state.lines.map(NX.lineText).join("\n");
    if (state.tail.trim()) txt += (txt ? "\n" : "") + state.tail;
    pre.textContent = txt;
    if (atEnd) pre.scrollTop = pre.scrollHeight;
    $("ticker").textContent = state.text.replace(/\r/g, "").replace(/\n+/g, " \u21b5 ").slice(-160);
  }

  // ---- draggable split between the in-progress message and the recent records ----
  (() => {
    const sp = $("st-split"), cols = sp.parentElement, root = document.documentElement;
    const KEY = "nx-rec-w";
    const set = (px) => {
      const total = cols.clientWidth;
      px = Math.max(200, Math.min(total - 240, px));
      root.style.setProperty("--rec-w", px + "px");
      return px;
    };
    try { const v = Number(localStorage.getItem(KEY)); if (v > 0) root.style.setProperty("--rec-w", v + "px"); } catch {}
    const save = () => { try { localStorage.setItem(KEY, String(Math.round(document.querySelector("#tab-status .reccol").getBoundingClientRect().width))); } catch {} };
    sp.addEventListener("pointerdown", (ev) => {
      sp.setPointerCapture(ev.pointerId); sp.classList.add("drag");
      const move = (e) => set(cols.getBoundingClientRect().right - e.clientX - 6);
      const up = () => { sp.classList.remove("drag"); sp.removeEventListener("pointermove", move); sp.removeEventListener("pointerup", up); save(); };
      sp.addEventListener("pointermove", move); sp.addEventListener("pointerup", up);
      ev.preventDefault();
    });
    sp.addEventListener("dblclick", () => { root.style.removeProperty("--rec-w"); try { localStorage.removeItem(KEY); } catch {} });
    sp.addEventListener("keydown", (e) => {
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      const w = document.querySelector("#tab-status .reccol").getBoundingClientRect().width;
      set(w + (e.key === "ArrowLeft" ? 30 : -30)); save(); e.preventDefault();
    });
    window.addEventListener("resize", () => { if (root.style.getPropertyValue("--rec-w")) set(parseFloat(root.style.getPropertyValue("--rec-w"))); });
  })();

  // ---- recent records ----
  function loadRecent() {
    fetch("/api/messages?limit=60&order=newest").then((r) => r.json()).then((j) => {
      const tb = document.querySelector("#recent tbody");
      tb.replaceChildren();
      j.records.forEach((r, i) => {
        const tr = document.createElement("tr");
        tr.dataset.i = String(i); tr.dataset.id = String(r.id); tr.title = "Double-click to open";
        const cells = [NX.stamp(r.rx).slice(5, 16) + (NX.secondOn() ? " \u00b7" + ((r.rcv || 0) + 1) : ""), r.kind, r.b1 + r.b2 + r.ser, r.st, r.q.toFixed(0)];
        for (const c of cells) { const td = document.createElement("td"); td.textContent = c; tr.appendChild(td); }
        tb.appendChild(tr);
      });
    }).catch(() => {});
  }

  // ---- spectrum ----
  // LOG / LIN is chosen separately for the big spectrum and the small one in the header
  const specPref = (key) => ({ get() { try { return localStorage.getItem(key) === "lin" ? "lin" : "log"; } catch (e) { return "log"; } }, set(v) { try { localStorage.setItem(key, v); } catch (e) { /* no storage */ } } });
  const PREF = { spec: specPref("navtex.spec.scale"), "spec-mini": specPref("navtex.mini.scale") };
  state.view.spec.scale = PREF.spec.get(); state.view["spec-mini"].scale = state.view["spec-mini2"].scale = PREF["spec-mini"].get();
  state.drag = null; state.specGeom = null;
  const niceTop = (v) => { const e = Math.pow(10, Math.floor(Math.log10(v))); const m = v / e; return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 5 ? 5 : 10) * e; };
  function drawSpectrum() { drawSpec($("spec"), false, state.sel); drawSpec($("spec-mini"), true, 0); if (secondOn()) drawSpec($("spec-mini2"), true, 1); }
  function drawSpec(cv, mini, ch) {
    const CH = state.chans[ch];
    const V = state.view[cv.id];
    if (!mini && state.tab !== "spectrum") return;
    const dpr = window.devicePixelRatio || 1;
    const w = cv.clientWidth, h = cv.clientHeight;
    if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(h * dpr)) { cv.width = Math.round(w * dpr); cv.height = Math.round(h * dpr); }
    const g = cv.getContext("2d");
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.fillStyle = cssVar("--spec-bg"); g.fillRect(0, 0, w, h);
    const s = CH.spec;
    if (!s) return;
    const lin = V.scale === "lin";
    const L = mini ? 26 : 44, R = mini ? 4 : 8, T = mini ? 4 : 8, B = mini ? 14 : 20, pw = w - L - R, ph = h - T - B;
    const n = s.filt.length, f0 = s.start, f1 = s.start + s.step * (n - 1);
    V.geom = { L, pw, f0, f1 };
    const fsDB = s.fs === undefined ? null : s.fs;
    // values in dB (tenths on the wire); linear mode shows amplitude as a fraction of a full-scale tone
    const dbv = (arr, i) => arr[i] / 10;
    const lv = (arr, i) => (fsDB === null ? 0 : Math.pow(10, (dbv(arr, i) - fsDB) / 20));
    let mx = -200, mxDb = -200;
    for (let i = 0; i < n; i++) { if (dbv(s.filt, i) > mxDb) mxDb = dbv(s.filt, i); }
    for (let i = 0; i < n; i++) { const v = lin ? lv(s.filt, i) : dbv(s.filt, i); if (v > mx) mx = v; }
    let top, range, Y, labels;
    if (lin) {
      const want = niceTop(Math.max(mx * 1.15, 1e-4));
      V.yTopLin = V.yTopLin ? V.yTopLin + (want - V.yTopLin) * 0.15 : want;
      top = V.yTopLin; range = top;
      Y = (v) => T + (top - v) / range * ph;
      labels = []; for (let k = 0; k <= 5; k++) labels.push(top * k / 5);
    } else {
      V.yTop += ((Math.ceil((mx + 6) / 10) * 10) - V.yTop) * 0.1;
      top = V.yTop; range = 80;
      Y = (d) => T + (top - d) / range * ph;
      labels = []; for (let d = Math.floor(top / 10) * 10; d > top - range; d -= 10) labels.push(d);
    }
    const X = (f) => L + (f - f0) / (f1 - f0) * pw;
    g.font = `${mini ? 9 : 11}px ${FONT}`; g.fillStyle = cssVar("--spec-ink"); g.strokeStyle = cssVar("--spec-grid"); g.lineWidth = 1;
    g.textAlign = "right"; g.textBaseline = "middle";
    if (mini) labels = labels.filter((_, i) => i % 2 === 0);
    for (const d of labels) {
      const y = Math.round(Y(d)) + .5; g.beginPath(); g.moveTo(L, y); g.lineTo(L + pw, y); g.stroke();
      g.fillText(lin ? (d >= 0.1 ? d.toFixed(2) : d.toPrecision(2)) : String(d), L - 4, y);
    }
    g.textAlign = "center"; g.textBaseline = "top";
    for (let f = Math.ceil(f0 / 200) * 200; f <= f1; f += 200) {
      const x = Math.round(X(f)) + .5; g.beginPath(); g.moveTo(x, T); g.lineTo(x, T + ph); g.stroke();
      if (f % (mini ? 800 : 400) === 0) g.fillText(String(f), x, T + ph + 4);
    }
    const trace = (arr, colour, width) => {
      g.beginPath();
      let pen = false; // a value of -300 dB or lower means "no data here": lift the pen
      for (let i = 0; i < n; i++) {
        if (arr[i] <= -3000) { pen = false; continue; }
        const v = lin ? lv(arr, i) : dbv(arr, i); const x = X(f0 + i * s.step), y = Math.min(T + ph, Math.max(T, Y(v)));
        if (pen) g.lineTo(x, y); else g.moveTo(x, y);
        pen = true;
      }
      g.strokeStyle = colour; g.lineWidth = width; g.stroke();
    };
    if (s.raw && s.raw.length) trace(s.raw, cssVar("--spec-raw"), 1);
    trace(s.filt, cssVar("--spec-filt"), 1.5);
    if (s.chan && s.chan.length === n) {
      trace(s.chan, cssVar("--spec-chan"), 1.5);
      if (!mini) { g.font = `11px ${FONT}`; g.fillStyle = cssVar("--spec-chan"); g.textAlign = "left"; g.textBaseline = "bottom"; g.fillText("\u2014 after channel filter", L + 6, T + ph - 4); g.fillStyle = cssVar("--spec-filt"); g.fillText("\u2014 input", L + 6, T + ph - 18); }
    }
    // level of a full-scale single tone: if the signal is far below it, there is gain to spare
    if (fsDB !== null && !mini) {
      const fv = lin ? 1 : fsDB, y = Y(fv), rx = L + pw - 84, below = (fsDB - mxDb).toFixed(0);
      g.fillStyle = cssVar("--spec-ink"); g.textAlign = "right"; g.textBaseline = "bottom";
      if (y >= T && y <= T + ph) {
        g.save(); g.globalAlpha = 0.55; g.strokeStyle = cssVar("--spec-ink"); g.setLineDash([1, 4]); g.lineCap = "round";
        g.beginPath(); g.moveTo(L, Math.round(y) + .5); g.lineTo(L + pw, Math.round(y) + .5); g.stroke(); g.restore();
        g.globalAlpha = 0.7; g.fillText(`full-scale tone (signal peak ${below} dB below)`, rx, y - 2); g.globalAlpha = 1;
      } else if (y < T) {
        g.globalAlpha = 0.7; g.textBaseline = "top";
        g.fillText(lin ? `\u25b2 full-scale tone = 1.0 (signal peak ${below} dB below)` : `\u25b2 full-scale tone ${fsDB.toFixed(0)} dB (signal peak ${below} dB below)`, rx, T + 2); g.globalAlpha = 1;
      }
    }
    let car = s.carrier;
    const adminManual = state.role === "admin" && CH.controls && CH.controls.autotune === false;
    if (state.drag !== null && state.dragCh === ch) car = state.drag;
    V.carX = null;
    if (car >= f0 && car <= f1) {
      const x = Math.round(X(car)) + .5; V.carX = x;
      const searching = CH.controls && CH.controls.autotune && CH.meters && !CH.meters.have;
      g.globalAlpha = searching ? 0.45 : 1;
      g.strokeStyle = cssVar("--spec-car"); g.lineWidth = adminManual ? 2 : 1; g.setLineDash([4, 3]); g.beginPath(); g.moveTo(x, T); g.lineTo(x, T + ph); g.stroke(); g.setLineDash([]); g.lineWidth = 1;
      g.fillStyle = cssVar("--spec-car"); g.textBaseline = "top"; g.textAlign = "left"; g.fillText(car.toFixed(0) + " Hz" + (adminManual && !mini ? "  \u2194" : "") + (searching && !mini ? "  searching\u2026" : ""), x + 4, T + 2); g.globalAlpha = 1;
    }
  }
  // dragging the carrier marker (only while autotune is off)
  (() => {
    const attach = (cv) => {
      const V = state.view[cv.id];
      const chOf = () => (cv.id === "spec" ? state.sel : cv.id === "spec-mini" ? 0 : 1);
      const freqAt = (e) => { const r = cv.getBoundingClientRect(), G = V.geom; if (!G) return null; const f = G.f0 + (e.clientX - r.left - G.L) / G.pw * (G.f1 - G.f0); return Math.max(G.f0, Math.min(G.f1, f)); };
      const canDrag = () => { const k = state.chans[chOf()].controls; return state.role === "admin" && k && k.autotune === false; };
      cv.addEventListener("pointermove", (e) => {
        if (state.drag !== null) { state.drag = freqAt(e); scheduleSpec(); return; }
        const r = cv.getBoundingClientRect();
        cv.style.cursor = canDrag() && V.carX !== null && Math.abs(e.clientX - r.left - V.carX) < 8 ? "ew-resize" : "";
      });
      cv.addEventListener("pointerdown", (e) => {
        const r = cv.getBoundingClientRect();
        if (!canDrag() || e.button !== 0 || V.carX === null || Math.abs(e.clientX - r.left - V.carX) >= 10) return;
        cv.setPointerCapture(e.pointerId); state.dragCh = chOf(); state.drag = freqAt(e); scheduleSpec(); e.preventDefault();
      });
      cv.addEventListener("pointerup", (e) => {
        if (state.drag === null) return;
        const f = Math.round(freqAt(e) * 10) / 10; state.drag = null;
        control("carrier", f, state.dragCh); scheduleSpec();
      });
      cv.addEventListener("pointercancel", () => { state.drag = null; scheduleSpec(); });
    };
    attach($("spec")); attach($("spec-mini")); attach($("spec-mini2"));
    // LOG/LIN: one choice for the big spectrum, one shared by the small ones
    const bind = (btns, cids, pref) => {
      btns.forEach((bt) => {
        bt.classList.toggle("on", bt.dataset.sc === state.view[cids[0]].scale);
        bt.onclick = () => { for (const c of cids) state.view[c].scale = bt.dataset.sc; pref.set(bt.dataset.sc); btns.forEach((x) => x.classList.toggle("on", x.dataset.sc === bt.dataset.sc)); scheduleSpec(); };
      });
    };
    bind(document.querySelectorAll("#spec-scale button"), ["spec"], PREF.spec);
    bind(document.querySelectorAll(".mini-scale button"), ["spec-mini", "spec-mini2"], PREF["spec-mini"]);
  })();
  let specPending = false;
  function scheduleSpec() { if (!specPending) { specPending = true; requestAnimationFrame(() => { specPending = false; drawSpectrum(); }); } }
  window.addEventListener("resize", scheduleSpec);

  // ---- events ----
  let es, lostTimer = 0, byeShown = false;
  function srvMsg(text) { $("srvmsg-text").textContent = text; $("srvmsg").hidden = !text; }
  $("srvmsg-x").onclick = () => srvMsg("");
  function connect() {
    es = new EventSource("/api/events");
    es.onopen = () => { $("conn").textContent = "live"; $("conn").className = "pill ok"; };
    es.onerror = () => {
      $("conn").textContent = "reconnecting"; $("conn").className = "pill bad";
      // the browser keeps retrying by itself; if it has not worked for a while, say so (a closing server announces itself with "bye")
      if (!lostTimer && !byeShown) lostTimer = setTimeout(() => { lostTimer = 0; if (!byeShown) srvMsg("The connection to the server has been lost. This page keeps trying to reconnect; if it does not recover, check that the server is running and reload the page with Ctrl+F5."); }, 12000);
    };
    const on = (n, f) => es.addEventListener(n, (e) => f(JSON.parse(e.data)));
    on("bye", (m) => { byeShown = true; clearTimeout(lostTimer); lostTimer = 0; srvMsg((m.msg || "The server has been shut down.") + " You have been disconnected. When the server is running again this page reconnects by itself; if it does not, reload it with Ctrl+F5."); });
    on("hello", (h) => {
      clearTimeout(lostTimer); lostTimer = 0; byeShown = false; srvMsg(""); // (re)connected: any "server closed" message is stale
      state.offset = h.now - Date.now();
      state.loc = !!h.loc;
      document.body.classList.toggle("noloc", !state.loc);
      document.querySelector("#tabs button[data-tab=ephemeris]").hidden = !state.loc;
      showRole(h.role, h.need_token);
      showSun(h.sun);
      const hc = h.chans || [{ controls: h.controls, run: h.run, meters: h.meters, text: h.text, lines: h.lines }];
      hc.forEach((c, i) => {
        const n = state.chans[i];
        n.controls = c.controls; n.run = c.run; n.meters = c.meters || null;
        n.text = (c.text || []).map((x) => x.x).join(""); n.lines = c.lines || []; n.tail = "";
      });
      state.sel = 0; // the saved choice, if that channel is still on
      try { const v = Number(localStorage.getItem("nx-chan")); if (v === 1 && secondOn()) state.sel = 1; } catch (e) { /* no storage */ }
      applySel();
      showNet(h.net); if (!h.net) { $("t-users").textContent = String(h.clients || 1); }
      loadRecent();
      fire("hello", h);
    });
    on("location", () => { if (state.role !== "admin") { es.close(); connect(); } }); // the operator changed who may see location information
    // Live events carry the decoding channel they belong to (rcv, absent = 0): they are kept per channel, and shown when it is the selected one.
    const rc = (m) => m.rcv || 0;
    on("meters", (m) => { const i = rc(m); state.chans[i].meters = m; showLeds(i); if (i === state.sel) showMeters(m); });
    on("net", showNet);
    on("spectrum", (s) => { const i = rc(s); state.chans[i].spec = s; if (i === state.sel) { state.spec = s; scheduleSpec(); } });
    on("text", (c) => { const i = rc(c); if (i === state.sel) addText(c.x); else addTextTo(state.chans[i], c.x); });
    on("line", (l) => { const i = rc(l); if (i === state.sel) addLine(l); else addLineTo(state.chans[i], l); });
    on("controls", (c) => {
      const i = rc(c), was = state.chans[i].controls;
      state.chans[i].controls = c;
      if (i === state.sel) showControls(c);
      showChan(i); refreshInput(i);
      if (i === state.sel && !c.enabled) selectChan(0); // the selected channel was switched off
      if (!was || was.enabled !== c.enabled) renderLines();
      if (was && was.enabled !== c.enabled) fire("channels", secondOn());
    });
    on("sun", showSun);
    on("run", (r) => { const i = rc(r); state.chans[i].run = r; showInfo(i); if (i === state.sel) showRun(r); });
    on("record", (r) => {
      if (r.action === "new") { const i = rc(r); if (i === state.sel) { state.lines = []; state.tail = ""; scheduleText(); } else { state.chans[i].lines = []; state.chans[i].tail = ""; } }
      loadRecent(); fire("record", r);
    });
    on("stations", (r) => fire("stations", r));
    on("log", (e) => { if (state.tab === "syslog") appendLog(e); });
  }

  // ---- decoding channels ----
  // The server decodes one or two channels. state.* holds the selected channel's live data; state.chans[i] holds each
  // channel's own (controls, meters, run, spectrum, text, lines), kept up to date from the events.
  for (let i = 0; i < 2; i++) state.chans.push({ controls: null, meters: null, run: null, spec: null, text: "", lines: [], tail: "" });
  const secondOn = () => !!(state.chans[1].controls && state.chans[1].controls.enabled);
  NX.secondOn = secondOn;
  NX.chanTag = (rcv) => (secondOn() ? String((rcv || 0) + 1) : "");
  const chanName = (i) => { const c = state.chans[i].controls; return c ? (c.name || "") : ""; };
  function saveSel() { const n = state.chans[state.sel]; n.text = state.text; n.lines = state.lines; n.tail = state.tail; n.spec = state.spec; }
  function applySel() {
    const n = state.chans[state.sel];
    state.text = n.text; state.lines = n.lines; state.tail = n.tail; state.spec = n.spec; state.meters = n.meters;
    if (n.controls) showControls(n.controls);
    if (n.run) showRun(n.run);
    if (n.meters) showMeters(n.meters);
    drawText(); renderLines();
    for (let i = 0; i < 2; i++) { showChan(i); showLeds(i); showInfo(i); }
    fire("channel", state.sel);
  }
  function selectChan(i) {
    if (i === state.sel) return;
    saveSel(); state.sel = i;
    try { localStorage.setItem("nx-chan", String(i)); } catch (e) { /* no storage */ }
    applySel();
  }
  NX.selectChan = selectChan;
  // ---- controls ----
  for (let i = 0; i < 2; i++) {
    L(i, ".c-polarity").onchange = (e) => control("polarity", e.target.value, i);
    L(i, ".c-uos").onchange = (e) => control("uos", e.target.checked, i);
    L(i, ".b-resync").onclick = () => control("resync", undefined, i);
    L(i, ".c-device").onchange = (e) => {
      const v = e.target.value;
      if (v.startsWith("r:")) control("radio", v.slice(2), i);
      else if (v.startsWith("a:")) control("device", v.slice(2), i);
    };
    S(i, ".c-autotune").onchange = (e) => control("autotune", e.target.checked, i);
    S(i, ".c-carrier").onchange = (e) => { const v = Number(e.target.value); if (v) control("carrier", v, i); };
    L(i, ".chip").onclick = () => selectChan(i);
  }
  $("b-addchan").onclick = () => { optPage = "channel2"; document.querySelector("#tabs button[data-tab=options]").click(); };
  $("b-login").onclick = async () => {
    try { await api("POST", "/api/login", { token: $("tok").value }); location.reload(); } catch (e) { alert(e.message); }
  };
  async function loadDevices() {
    if (state.role !== "admin") return;
    try {
      const [devs, radios] = await Promise.all([api("GET", "/api/audio-devices"), api("GET", "/api/radios")]);
      if (document.activeElement && document.activeElement.classList.contains("c-device")) return;
      const audio = document.createElement("optgroup"); audio.label = "Audio input"; audio.className = "audio";
      audio.append(new Option("(default)", "a:"));
      for (const d of devs) audio.append(new Option(d.name, "a:" + d.name));
      const rg = document.createElement("optgroup"); rg.label = "Radio (OpenHPSDR)"; rg.className = "radios";
      for (const r of radios) rg.append(new Option(r.label, "r:" + r.addr));
      rg.append(new Option("Radio: search the network", "r:"));
      for (let i = 0; i < 2; i++) { const sel = L(i, ".c-device"); sel.replaceChildren(audio.cloneNode(true), rg.cloneNode(true)); }
      NX.knownRadios = radios;
      refreshInput(0); refreshInput(1);
    } catch (e) { /* not available */ }
  }

  // ---- tabs ----
  document.querySelectorAll("#tabs button").forEach((b) => {
    b.onclick = () => {
      document.querySelectorAll("#tabs button").forEach((x) => x.classList.toggle("on", x === b));
      state.tab = b.dataset.tab;
      document.querySelectorAll("main > section").forEach((x) => { x.hidden = x.id !== "tab-" + state.tab; });
      if (NX.tabs[state.tab]) NX.tabs[state.tab]();
      if (state.tab === "spectrum") scheduleSpec();
      if (state.tab === "options") loadOptions();
      if (state.tab === "syslog") loadLog();
    };
  });

  // ---- options ----
  let fields = [], optPage = "station", optPages = new Map(), optMsgTimer = 0;
  async function loadOptions(force) {
    if (!force && pendingCount()) return; // keep unsaved edits when coming back to this tab
    const note = $("opt-note"), form = $("opt-form");
    form.replaceChildren(); $("opt-tabs").replaceChildren();
    if (state.role !== "admin") {
      note.hidden = false; note.textContent = "Settings can only be viewed and changed from the local network.";
      $("opt-bar").hidden = true; $("opt-hint").hidden = true; return;
    }
    note.hidden = true; $("opt-bar").hidden = false; $("opt-hint").hidden = false;
    try { fields = (await api("GET", "/api/settings")).fields; } catch (e) { note.hidden = false; note.textContent = e.message; return; }
    const title = { operator: "Operator and position", input: "Input source", "input.device": "Audio input (sound card or virtual audio cable)", "input.hpsdr": "Radio (OpenHPSDR / Metis)", "input.stdin": "Raw stream (standard input)", "input.file": "WAV file", channel: "Channel", ddc: "Down-converter", decoder: "Decoder", receiver: "Receiver (FSK demodulator)", segment: "Message segmentation", logging: "Logging", autocorrect: "Word autocorrection", meters: "Meters and spectrum", server: "Network server", output: "Output (command line)" };
    const nameOf = (p, d) => { const f = fields.find((x) => x.path === p); return (f && String(f.value).trim()) || d; };
    const pages = [
      ["station", "Station", ["operator"]],
      ["channel1", nameOf("channel.name", "Channel 1"), ["channel", "input", "input.device", "input.hpsdr", "input.stdin", "input.file", "receiver", "decoder", "segment", "ddc"]],
      ["channel2", nameOf("second.channel.name", "Channel 2"), ["second", "second.channel", "second.input", "second.input.device", "second.input.hpsdr", "second.input.stdin", "second.input.file", "second.receiver", "second.decoder", "second.segment", "second.ddc"]],
      ["logging", "Logging", ["logging", "autocorrect"]],
      ["display", "Display", ["meters"]],
      ["network", "Network", ["server"]],
      ["cli", "Command line", ["output"]],
    ];
    const known = new Set(pages.flatMap((p) => p[2]));
    const gkey = (path) => {
      if (path.startsWith("second.")) { const r = path.slice(7); return r === "enabled" ? "second" : "second." + gkey(r); }
      const a = path.split("."); return a[0] === "input" && a.length > 2 ? a[0] + "." + a[1] : a[0];
    };
    const others = [...new Set(fields.map((f) => gkey(f.path)))].filter((g) => !known.has(g));
    if (others.length) pages.push(["other", "Other", others]);
    const groups = new Map();
    for (const f of fields) { const g = gkey(f.path); if (!groups.has(g)) groups.set(g, []); groups.get(g).push(f); }
    const bar = $("opt-tabs");
    const pageEls = new Map(), extra = { box: null }; optPages = pageEls;
    const showPage = (id) => { optPage = id; for (const [k, p] of pageEls) { p.hidden = k !== id; } for (const b of bar.children) b.classList.toggle("on", b.dataset.page === id); };
    for (const [id, label, gs] of pages) {
      if (!gs.some((g) => groups.has(g)) && id !== "logging") continue;
      const btn = document.createElement("button"); btn.type = "button"; btn.textContent = label; btn.dataset.page = id; btn.onclick = () => showPage(id); bar.appendChild(btn);
      const page = document.createElement("div"); page.className = "optpage"; page.hidden = true; form.appendChild(page); pageEls.set(id, page);
      if (id === "logging") { extra.box = document.createElement("div"); extra.box.id = "opt-extra"; }
      for (const g of gs) {
        if (!groups.has(g)) continue;
        const fs = document.createElement("fieldset"); fs.className = "optgrp"; if (g.startsWith("input.")) fs.dataset.src = g.slice(6);
        if (g.startsWith("second.input.")) fs.dataset.src2 = g.slice(13);
        const lg = document.createElement("legend"); lg.textContent = g === "second" ? "Channel 2: switch it on to decode a second channel from its own input" : g === "channel" || g === "second.channel" ? "Channel: frequency, name and comment" : title[g.startsWith("second.") ? g.slice(7) : g] || g; fs.appendChild(lg); page.appendChild(fs);
        for (const f of groups.get(g)) {
          const row = document.createElement("div"); row.className = "opt";
          const name = document.createElement("div"); const code = document.createElement("code"); code.textContent = f.path.slice(g.length + 1);
          name.appendChild(code);
          if (f.apply !== "live") { const b = document.createElement("span"); b.className = "badge"; b.textContent = f.apply === "server" ? "restart navtexd" : "restarts channel"; name.appendChild(b); }
          let inp;
          if (f.kind === "bool") { inp = document.createElement("input"); inp.type = "checkbox"; inp.checked = f.value; }
          else if (f.kind === "enum" || (f.choices && f.choices.length)) {
            inp = document.createElement("select");
            for (const c of f.choices || [f.value]) inp.add(new Option(c, c));
            inp.value = f.value;
          } else if (f.path === "operator.info") { inp = document.createElement("textarea"); inp.rows = 5; inp.value = f.value; row.classList.add("wide"); }
          else if (f.path === "input.hpsdr.address") {
            inp = document.createElement("input"); inp.type = "text"; inp.value = f.value; inp.placeholder = "empty = search the network";
            inp.setAttribute("list", "radio-list");
            let dl = document.getElementById("radio-list"); if (!dl) { dl = document.createElement("datalist"); dl.id = "radio-list"; document.body.append(dl); }
            dl.replaceChildren(...(NX.knownRadios || []).map((r) => { const o = document.createElement("option"); o.value = r.addr; o.label = r.name || ""; o.textContent = r.label; return o; }));
          }
          else { inp = document.createElement("input"); inp.type = f.kind === "int" || f.kind === "float" ? "number" : "text"; if (inp.type === "number") inp.step = f.kind === "int" ? "1" : "any"; inp.value = f.value; }
          inp.dataset.path = f.path; inp.dataset.orig = String(f.value);
          const live = f.apply === "live";
          if (live) {
            // applies (and is saved) as soon as the edit is committed; no restart, no Save button
            inp.onchange = async () => {
              const ch = {}; ch[f.path] = typed(f, inp);
              row.classList.remove("applied", "failed"); row.title = "";
              try {
                const r = await api("PUT", "/api/settings", { changes: ch });
                inp.dataset.orig = valueOf(inp); row.classList.add("applied");
                if (r.restarted) setMsg("The channel was restarted for this change.");
                setTimeout(() => row.classList.remove("applied"), 1500);
              } catch (e) { row.classList.add("failed"); row.title = e.message; setMsg(f.path + ": " + e.message, 0); inp.value = inp.dataset.orig; if (inp.type === "checkbox") inp.checked = inp.dataset.orig === "true"; }
            };
          } else inp.oninput = inp.onchange = () => { row.classList.toggle("changed", valueOf(inp) !== inp.dataset.orig); clearMsg(); updatePending(); };
          const doc = document.createElement("div"); doc.className = "doc"; doc.textContent = f.doc + (f.unit ? ` (${f.unit})` : "");
          row.append(name, inp, doc); fs.appendChild(row);
        }
      }
      if (extra.box && id === "logging") page.appendChild(extra.box);
    }
    // only the options of the selected input source are shown
    const srcSel = form.querySelector('[data-path="input.source"]');
    if (srcSel) {
      const showSrc = () => form.querySelectorAll("fieldset[data-src]").forEach((fs) => { fs.hidden = fs.dataset.src !== srcSel.value; });
      srcSel.addEventListener("change", showSrc); showSrc();
    }
    const srcSel2 = form.querySelector('[data-path="second.input.source"]');
    if (srcSel2) {
      const showSrc2 = () => form.querySelectorAll("fieldset[data-src2]").forEach((fs) => { fs.hidden = fs.dataset.src2 !== srcSel2.value; });
      srcSel2.addEventListener("change", showSrc2); showSrc2();
    }
    showPage(pageEls.has(optPage) ? optPage : "station");
    updatePending();
    if (NX.optionsExtra && extra.box) NX.optionsExtra(extra.box);
  }
  const typed = (f, inp) => (f.kind === "bool" ? inp.checked : f.kind === "int" || f.kind === "float" ? Number(inp.value) : inp.value);
  const valueOf = (inp) => (inp.type === "checkbox" ? String(inp.checked) : inp.value);
  // pending (unsaved) changes: counted per sub-tab; the sub-tab gets a dot, the Save button the total
  const pendingCount = () => { let n = 0; for (const p of optPages.values()) n += p.querySelectorAll(".opt.changed").length; return n; };
  function updatePending() {
    let n = 0;
    for (const b of $("opt-tabs").children) {
      const p = optPages.get(b.dataset.page), c = p ? p.querySelectorAll(".opt.changed").length : 0;
      b.classList.toggle("changed", c > 0); b.title = c ? `${c} unsaved change${c > 1 ? "s" : ""}` : ""; n += c;
    }
    const admin = state.role === "admin";
    $("opt-save").textContent = "Save changes" + (n ? ` (${n})` : ""); $("opt-save").disabled = !admin || !n; $("opt-revert").disabled = !admin || !n;
  }
  function clearMsg() { clearTimeout(optMsgTimer); $("opt-msg").textContent = ""; }
  function setMsg(text, ms = 6000) { clearMsg(); $("opt-msg").textContent = text; if (ms) optMsgTimer = setTimeout(clearMsg, ms); }
  $("opt-revert").onclick = () => {
    for (const inp of $("opt-form").querySelectorAll("[data-path]")) {
      if (valueOf(inp) === inp.dataset.orig) continue;
      if (inp.type === "checkbox") inp.checked = inp.dataset.orig === "true"; else inp.value = inp.dataset.orig;
      inp.dispatchEvent(new Event("change"));
    }
    clearMsg(); updatePending();
  };
  $("opt-save").onclick = async () => {
    clearMsg();
    const changes = {};
    for (const inp of $("opt-form").querySelectorAll("[data-path]")) {
      if (valueOf(inp) === inp.dataset.orig) continue;
      const f = fields.find((x) => x.path === inp.dataset.path);
      changes[f.path] = typed(f, inp);
    }
    const n = Object.keys(changes).length;
    if (!n) { updatePending(); return; }
    $("opt-save").disabled = $("opt-revert").disabled = true; // greyed out at once; loadOptions() re-evaluates
    try {
      const r = await api("PUT", "/api/settings", { changes });
      await loadOptions(true); loadDevices();
      setMsg(`Saved ${n} change${n > 1 ? "s" : ""}.` + (r.restarted ? " The channel was restarted." : "") + (r.server_restart ? " Restart navtexd for the server settings to take effect." : ""), r.server_restart ? 15000 : 6000);
    } catch (e) { setMsg("Not saved: " + e.message, 0); updatePending(); }
  };

  // ---- system log ----
  const LOG = { entries: [], max: 5000, pending: false };
  const logText = (e) => { const d = new Date(e.t); return `${NX.stamp(d.getTime())}.${pad(d.getUTCMilliseconds(), 3)}  ${e.l === "info" ? "" : e.l.toUpperCase() + " "}${e.x}`; };
  function renderLog() {
    LOG.pending = false;
    const lvl = $("log-level").value, q = $("log-filter").value.trim().toLowerCase();
    const rows = LOG.entries.filter((e) => (lvl === "all" || e.l !== "info") && (!q || e.x.toLowerCase().includes(q))).map(logText);
    const pre = $("log");
    pre.textContent = rows.join("\n");
    $("log-count").textContent = `${rows.length} of ${LOG.entries.length} lines`;
    if ($("log-follow").checked) pre.scrollTop = pre.scrollHeight;
  }
  function scheduleLog() { if (!LOG.pending && state.tab === "syslog") { LOG.pending = true; setTimeout(renderLog, 200); } }
  function appendLog(e) {
    LOG.entries.push(e);
    if (LOG.entries.length > LOG.max) LOG.entries.splice(0, LOG.entries.length - LOG.max);
    scheduleLog();
  }
  async function loadLog() {
    const note = $("log-note");
    if (state.role !== "admin") { note.hidden = false; note.textContent = "The system log can only be viewed from the local network."; $("log").textContent = ""; return; }
    note.hidden = true;
    try { LOG.entries = await api("GET", "/api/syslog?n=2000"); } catch (e) { note.hidden = false; note.textContent = e.message; return; }
    renderLog();
  }
  $("log-level").onchange = $("log-follow").onchange = renderLog;
  $("log-filter").oninput = renderLog;

  // colour of the message text (ticker, current text, log text): a per-browser choice
  (() => {
    const inp = $("msg-color"), key = "nx-msgcolor";
    let c = "#8cf0a0";
    try { c = localStorage.getItem(key) || c; } catch (e) { /* no storage */ }
    const apply = (v) => { document.documentElement.style.setProperty("--msg-ink", v); inp.value = v; };
    apply(c);
    inp.oninput = () => { apply(inp.value); try { localStorage.setItem(key, inp.value); } catch (e) { /* no storage */ } };
    inp.ondblclick = () => { apply("#8cf0a0"); try { localStorage.removeItem(key); } catch (e) { /* no storage */ } };
    inp.title = "Colour of the message text (double-click for the default)";
  })();

  connect();
  setTimeout(loadDevices, 800);
  setInterval(() => { if (state.role === "admin" && L(0, ".c-device").options.length <= 1) loadDevices(); }, 5000);
  setInterval(() => { if (state.role === "admin") loadDevices(); }, 20000); // a radio joins the list once it has delivered data
})();
