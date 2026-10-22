"use strict";
// NAVTEX web client: live header, spectrum, options and system log. Plain JavaScript, no build step.
(() => {
  const $ = (id) => document.getElementById(id);
  const N = 4; // decoding channels the server offers (config.Channels)
  const state = { sel: 0, chans: [], role: "viewer", offset: 0, text: "", controls: null, sun: null, meters: null, tab: "status", loc: false, spec: null, yTop: -20, view: { spec: { yTop: -20, yTopLin: 0, geom: null, carX: null }, "spec-mini": { yTop: -20, yTopLin: 0, geom: null, carX: null } } };

  // ---- helpers ----
  const NX = (window.NX = { $, state, tabs: {}, hooks: {} });
  const fire = (n, d) => (NX.hooks[n] || []).forEach((f) => f(d));
  NX.listen = (n, f) => { (NX.hooks[n] = NX.hooks[n] || []).push(f); };
  const FONT = getComputedStyle(document.body).fontFamily;
  NX.FONT = FONT;
  const pad = (n, w = 2) => String(n).padStart(w, "0");
  const hms = (ms) => { const d = new Date(ms); return `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}`; };
  NX.pad = pad; NX.hms = hms;
  NX.hex = (id) => "0x" + Number(id).toString(16).toUpperCase().padStart(8, "0"); // record numbers are shown in hexadecimal
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
      ${d.dataset.two ? `<line id="g2-${id}" x1="${DIAL.cx}" y1="${DIAL.cy}" x2="${dpt(0, DIAL.r - 6)[0]}" y2="${dpt(0, DIAL.r - 6)[1]}" stroke="#ffd166" stroke-width="1.6" stroke-linecap="round" visibility="hidden"/>` : ""}<line id="g-${id}" x1="${DIAL.cx}" y1="${DIAL.cy}" x2="${dpt(0, DIAL.r - 3)[0]}" y2="${dpt(0, DIAL.r - 3)[1]}" stroke="#7fe3c4" stroke-width="2" stroke-linecap="round"/>
      <circle cx="${DIAL.cx}" cy="${DIAL.cy}" r="3.2" fill="#7fe3c4"/>
      <text id="t-${id}" x="40" y="63" class="dv">-</text></svg>`;
  });
  function setBar(id, frac, text) {
    const [x, y] = dpt(Math.max(0, Math.min(1, frac)), DIAL.r - 3);
    const n = $("g-" + id);
    n.setAttribute("x2", x.toFixed(1)); n.setAttribute("y2", y.toFixed(1));
    $("t-" + id).textContent = text;
  }
  function setCpu(m) { // green needle: this program; amber needle: the whole computer
    const f1 = (v) => (v < 10 ? v.toFixed(1) : v.toFixed(0));
    const n2 = $("g2-cpu"), sys = m.sys >= 0 ? m.sys : null;
    setBar("cpu", m.cpu / 100, sys === null ? f1(m.cpu) + "%" : f1(m.cpu) + " / " + f1(sys) + "%");
    if (sys !== null) { const [x, y] = dpt(Math.max(0, Math.min(1, sys / 100)), DIAL.r - 6); n2.setAttribute("x2", x.toFixed(1)); n2.setAttribute("y2", y.toFixed(1)); n2.setAttribute("visibility", "visible"); } else n2.setAttribute("visibility", "hidden");
    const cores = m.cores || 1;
    const now = performance.now(); if (now - (setCpu.t || 0) < 3000) return; setCpu.t = now; // the tooltip text changes with every update; refresh it every three seconds at most
    $("t-cpu").closest(".dial").title = `navtexd: ${m.cpu.toFixed(1)}% of all ${cores} cores (= ${(m.cpu * cores).toFixed(0)}% of one core)` + (sys === null ? "" : `\nWhole computer: ${sys.toFixed(1)}% of all cores`) + "\nGreen needle: navtexd; amber needle: the whole computer";
  }
  const fmtRate = (bps) => (bps < 1000 ? `${bps} B/s` : bps < 1e6 ? `${(bps / 1000).toFixed(1)} kB/s` : `${(bps / 1e6).toFixed(2)} MB/s`);
  function showNet(n) { if (!n) return; $("t-net").textContent = fmtRate(n.bps); $("t-users").textContent = String(n.users); }
  function led(el, mode) { if (el) { el.classList.toggle("on", mode === 2); el.classList.toggle("mid", mode === 1); } } // keeps the other classes (l-lock, l-sync, ...): they are how the LEDs are found

  function showMeters(m) {
    state.meters = m;
    const db = m.peak > 0 ? 20 * Math.log10(m.peak) : -90;
    setBar("level", (db + 60) / 60, m.peak > 0 ? db.toFixed(0) + " dB" : "-", m.clip ? cssVar("--bad") : db > -6 ? cssVar("--warn") : "");
    setBar("q", m.q / 100, m.q.toFixed(0), m.q < 25 ? cssVar("--bad") : m.q < 50 ? cssVar("--warn") : "");
    setCpu(m);
  }
  // ---- per-channel header: the channel line (chip, frequency, input, comment, format, RESYNC, LEDs, UoS, polarity) and the small spectrum's controls ----
  for (let i = 0; i < N; i++) state.chans.push({ controls: null, meters: null, run: null, spec: null, text: "", lines: [], tail: "" });
  // one small spectrum per channel in the header (box 0 is in the page, the others are copies); each has its own LOG/LIN choice
  const miniId = (i) => (i ? "spec-mini" + i : "spec-mini");
  for (let i = 1; i < N; i++) {
    const first = $("grp-spec"), c = first.cloneNode(true); c.id = "grp-spec" + i; c.dataset.ch = i; c.hidden = true;
    c.querySelector("canvas").id = miniId(i); first.parentNode.insertBefore(c, $(i > 1 ? "grp-spec" + (i - 1) : "grp-spec").nextSibling);
    state.view[miniId(i)] = { yTop: -20, yTopLin: 0, geom: null, carX: null };
  }
  const L = (i, sel) => document.querySelector(`.chline[data-ch="${i}"] ${sel}`);
  // box(i): channel i's small spectrum in the header
  const box = (i) => document.querySelector(`.specgrp[data-ch="${i}"]`);
  const M = (i, sel) => box(i).querySelector(sel);
  function showLeds(i) {
    const m = state.chans[i].meters;
    if (!m) return;
    { // the words under the small spectrum
      const c = state.chans[i].controls, auto = !c || c.autotune, adm = state.role === "admin";
      led(M(i, ".m-sync"), m.sync); led(M(i, ".m-data"), m.data ? 2 : 0);
      const lk = M(i, ".m-lock");
      lk.textContent = auto ? "LOCKED" : "MANUAL"; led(lk, auto ? (m.have ? 2 : 0) : 1);
      lk.title = adm ? (auto ? "Press for MANUAL carrier adjust" : "Press for AUTO carrier adjust") : (auto ? "Lit while the carrier is found automatically" : "The carrier is set by hand");
      M(i, ".m-sync").title = adm ? "Press to force RESYNC" : "Lit while the decoder is synchronised";
    }
    led(L(i, ".l-sync"), m.sync);
    led(L(i, ".l-data"), m.data ? 2 : 0);
    const c = state.chans[i].controls, auto = !c || c.autotune;
    const lk = L(i, ".l-lock"); led(lk, auto && m.have ? 2 : 0); lk.classList.toggle("off", !auto); // greyed out while Auto is off
  }
  function showInprog() {
    const el = $("inprog-ch"), c = state.chans[state.sel].controls;
    el.textContent = multiOn() && c ? " \u2013 " + (c.label || c.name || `Channel ${state.sel + 1}`) : "";
  }
  function showChan(i) {
    const c = state.chans[i].controls;
    if (!c) return;
    const set = (el, v) => { if (document.activeElement !== el) { if (el.type === "checkbox") el.checked = v; else el.value = v; } };
    set(L(i, ".c-polarity"), c.polarity);
    set(L(i, ".c-uos"), c.uos);
    set(L(i, ".c-autotune"), c.autotune);
    showLeds(i);
    const chip = L(i, ".chip");
    chip.textContent = c.label || c.name || `Channel ${i + 1}`;
    if (i === state.sel) showInprog();
    chip.classList.toggle("on", i === state.sel);
    const mc = M(i, ".m-chip"); mc.replaceChildren(String(c.channel), Object.assign(document.createElement("span"), { className: "u", textContent: " kHz" })); mc.title = (c.label || c.name || `Channel ${i + 1}`) + " - make this the active channel"; mc.classList.toggle("on", i === state.sel);
    L(i, ".chfreq").textContent = c.channel + " kHz";
    const cm = L(i, ".chcmt"); cm.textContent = c.note || ""; cm.title = c.note ? "Channel comment: " + c.note : "Channel comment (change it in Options)";
  }
  function showInfo(i) {
    const r = state.chans[i].run, el = L(i, ".chinfo");
    if (!r) { el.textContent = ""; return; }
    el.textContent = r.error ? "\u26a0 " + r.error : (r.format || "");
    el.classList.toggle("bad", !!r.error);
    el.title = (r.running ? "" : "Not running. ") + (r.info || "");
    showTips();
  }
  // lines, boxes and the add chip follow the number of channels
  function renderLines() {
    const act = activeList(), many = act.length > 1, admin = state.role === "admin";
    for (let i = 0; i < N; i++) { const ln = document.querySelector(`.chline[data-ch="${i}"]`); ln.hidden = !act.includes(i); ln.querySelector(".chip").classList.toggle("on", i === state.sel); box(i).classList.toggle("sel", i === state.sel); M(i, ".m-chip").classList.toggle("on", i === state.sel); }
    for (let i = 0; i < N; i++) box(i).hidden = !act.includes(i);
    const free = act.length < N;
    $("b-addchan").hidden = !free || !admin;
    $("b-addchan").textContent = act.length < 2 ? "+ Add second channel" : "+ Add channel";
    document.body.classList.toggle("two", many);
    for (const i of act) showChan(i);
    showInprog(); showTips();
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
      state.chans[ch].input = i;
      showInput(ch);
    } catch (e) { /* not available */ }
  }
  // The input of a channel, shown read-only (it is changed in Options)
  function showInput(ch) {
    const i = state.chans[ch].input, el = L(ch, ".chinput");
    if (!i) { el.textContent = ""; return; }
    let t;
    if (i.shared) t = "same input as " + i.shared;
    else if (i.source === "hpsdr") { const r = (NX.knownRadios || []).find((x) => x.addr === i.radio); t = r ? r.label : i.radio ? "Radio " + i.radio : "Radio (searching the network)"; }
    else if (i.source === "file") t = "WAV file";
    else if (i.source === "stdin") t = "Raw stream";
    else t = i.device || "(default audio input)";
    el.textContent = t; el.title = "Input of this channel: " + t + " (change it in Options)";
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
    document.querySelectorAll("#chlines select, #chlines input, #chlines .b-resync, .specgrp input, .m-lock, .m-sync, #b-clip").forEach((el) => { el.disabled = !admin; });
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
    document.querySelectorAll("#hdr .grp:not(.specgrp)").forEach((g) => { g.title = tip; });
    showTips();
  }
  // each small spectrum describes its own channel
  function showTips() {
    for (let i = 0; i < N; i++) {
      const b = box(i), r = state.chans[i].run; if (!b) continue;
      const t = r ? (r.running ? "" : "Not running. ") + (r.error ? "\u26a0 " + r.error : r.info || "") : "";
      b.title = (t ? t + "\n" : "") + `Click: make this the active channel. Double-click: also show it on the Spectrum tab.\nCarrier: found automatically while Auto is on. To set it by hand, switch Auto off and drag the dashed line on the Spectrum tab, or enter it in Options \u203a Channel ${i + 1}.`;
    }
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

  let showScale = () => {};
  // ---- spectrum ----
  // LOG / LIN is chosen separately for the big spectrum and for each small one in the header
  const specPref = (key) => ({ get() { try { return localStorage.getItem(key) === "lin" ? "lin" : "log"; } catch (e) { return "log"; } }, set(v) { try { localStorage.setItem(key, v); } catch (e) { /* no storage */ } } });
  // each channel has its own LOG/LIN choice (kept in this browser); the Spectrum tab sets it and the header spectrum of the channel follows
  const PREF = []; for (let i = 0; i < N; i++) { PREF.push(specPref(i ? "navtex.spec.scale." + i : "navtex.spec.scale")); state.chans[i].scale = PREF[i].get(); }
  state.drag = null; state.specGeom = null;
  const niceTop = (v) => { const e = Math.pow(10, Math.floor(Math.log10(v))); const m = v / e; return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 5 ? 5 : 10) * e; };
  function drawSpectrum() { drawSpec($("spec"), false, state.sel); for (const i of activeList()) drawSpec($(miniId(i)), true, i); }
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
    if (!mini) cv.parentNode.classList.toggle("multi", multiOn()); // the LOG / LIN buttons move down under the channel name
    const s = CH.spec;
    if (!s) return;
    const lin = CH.scale === "lin";
    const L = mini ? 26 : 44, R = mini ? 4 : 8, T = mini ? 4 : 8, B = mini ? 14 : 20, pw = w - L - R, ph = h - T - B;
    const n = s.filt.length, f0 = s.start, f1 = s.start + s.step * (n - 1);
    // the big spectrum can be zoomed and panned (CH.vr = the range shown, Hz); beyond the received slice there is no data
    let d0 = f0, d1 = f1;
    if (CH.vr) { d0 = CH.vr.lo; d1 = CH.vr.hi; }
    V.geom = { L, pw, f0: d0, f1: d1, s0: f0, s1: f1, step: s.step };
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
    const X = (f) => L + (f - d0) / (d1 - d0) * pw;
    g.font = `${mini ? 9 : 11}px ${FONT}`; g.fillStyle = cssVar("--spec-ink"); g.strokeStyle = cssVar("--spec-grid"); g.lineWidth = 1;
    g.textAlign = "right"; g.textBaseline = "middle";
    if (mini) labels = labels.filter((_, i) => i % 2 === 0);
    for (const d of labels) {
      const y = Math.round(Y(d)) + .5; g.beginPath(); g.moveTo(L, y); g.lineTo(L + pw, y); g.stroke();
      g.fillText(lin ? (d >= 0.1 ? d.toFixed(2) : d.toPrecision(2)) : String(d), L - 4, y);
    }
    g.textAlign = "center"; g.textBaseline = "top";
    const gs = d1 - d0 > 1500 ? 200 : d1 - d0 > 600 ? 100 : d1 - d0 > 250 ? 50 : 20;
    for (let f = Math.ceil(d0 / gs) * gs; f <= d1; f += gs) {
      const x = Math.round(X(f)) + .5; g.beginPath(); g.moveTo(x, T); g.lineTo(x, T + ph); g.stroke();
      if (f % (mini ? 800 : gs * 2) === 0) g.fillText(String(f), x, T + ph + 4);
    }
    g.save(); g.beginPath(); g.rect(L, T, pw, ph); g.clip();
    if (d0 < f0 || d1 > f1) { // outside the received slice: no data
      g.fillStyle = cssVar("--spec-grid"); g.globalAlpha = 0.3;
      if (d0 < f0) g.fillRect(L, T, Math.max(0, X(f0) - L), ph);
      if (d1 > f1) g.fillRect(X(f1), T, L + pw - X(f1), ph);
      g.globalAlpha = 1;
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
    g.restore();
    // level of a full-scale single tone: if the signal is far below it, there is gain to spare
    if (fsDB !== null && !mini) {
      const fv = lin ? 1 : fsDB, y = Y(fv), rx = L + 6, below = (fsDB - mxDb).toFixed(0);
      g.fillStyle = cssVar("--spec-ink"); g.textAlign = "left"; g.textBaseline = "bottom"; // left side: the channel name and the LOG / LIN buttons are on the right
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
    if (car >= d0 && car <= d1) {
      const x = Math.round(X(car)) + .5; V.carX = x;
      const searching = CH.controls && CH.controls.autotune && CH.meters && !CH.meters.have;
      g.globalAlpha = searching ? 0.45 : 1;
      g.strokeStyle = cssVar("--spec-car"); g.lineWidth = adminManual ? 2 : 1; g.setLineDash([4, 3]); g.beginPath(); g.moveTo(x, T); g.lineTo(x, T + ph); g.stroke(); g.setLineDash([]); g.lineWidth = 1;
      g.fillStyle = cssVar("--spec-car"); g.textBaseline = "top"; const late = x > L + pw - 150; g.textAlign = late ? "right" : "left"; if (!mini) g.fillText(car.toFixed(0) + " Hz" + (adminManual && !mini ? "  \u2194" : "") + (searching && !mini ? "  searching\u2026" : ""), late ? x - 4 : x + 4, T + 2); g.globalAlpha = 1;
    }
    if (multiOn()) { // which channel this graph shows
      const nm = (CH.controls && (CH.controls.label || CH.controls.name)) || `Channel ${ch + 1}`;
      g.font = (mini ? "bold 10px " : "bold 18px ") + FONT; g.textBaseline = "top"; g.textAlign = "right"; g.fillStyle = cssVar("--spec-ink"); g.globalAlpha = 0.9;
      g.fillText(nm, mini ? L + pw - 4 : w - 12, T + 2); // (big graph: right edge in line with the LOG / LIN buttons) g.globalAlpha = 1; g.textAlign = "left";
    }
  }
  // The big spectrum: drag the carrier marker (administrators, autotune off), drag the background to pan, wheel to zoom,
  // double-click to reset. Zooming and panning inside the received slice are done here. Panning beyond it
  // (administrators) asks the server to center the slice elsewhere; the receiver's window never moves.
  (() => {
    const attach = (cv) => {
      const V = state.view[cv.id];
      const chOf = () => (cv.id === "spec" ? state.sel : Number(cv.closest(".specgrp").dataset.ch));
      const isAdmin = () => state.role === "admin";
      const posHz = (e, clamp) => { const r = cv.getBoundingClientRect(), G = V.geom; if (!G) return null; const f = G.f0 + (e.clientX - r.left - G.L) / G.pw * (G.f1 - G.f0); return clamp ? Math.max(G.f0, Math.min(G.f1, f)) : f; };
      const canDrag = () => { const k = state.chans[chOf()].controls; return isAdmin() && k && k.autotune === false; };
      const cur = () => state.chans[chOf()];
      const range = () => { const G = V.geom; return { lo: G.f0, hi: G.f1 }; };
      const slice = () => { const G = V.geom; return { lo: G.s0, hi: G.s1 }; };
      // keep a view range inside the received slice (everyone) or let administrators go beyond it
      const place = (lo, w) => { const S = slice(); if (!isAdmin()) lo = Math.max(S.lo, Math.min(S.hi - w, lo)); return { lo, hi: lo + w }; };
      let pan = null, lastE = null;
      cv.addEventListener("pointermove", (e) => {
        if (state.drag !== null) {
          lastE = e; state.drag = posHz(e, true); // (the graph stays where it is; the marker stops at its edges)
          scheduleSpec(); return;
        }
        if (pan) {
          const G = V.geom, w = pan.hi - pan.lo, df = -(e.clientX - pan.x) / G.pw * w;
          if (Math.abs(e.clientX - pan.x) > 3) pan.moved = true;
          cur().vr = place(pan.lo + df, w); scheduleSpec(); return;
        }
        const r = cv.getBoundingClientRect();
        cv.style.cursor = canDrag() && V.carX !== null && Math.abs(e.clientX - r.left - V.carX) < 8 ? "ew-resize" : "grab";
      });
      cv.addEventListener("pointerdown", (e) => {
        const r = cv.getBoundingClientRect();
        if (e.button !== 0 || !V.geom) return;
        if (canDrag() && V.carX !== null && Math.abs(e.clientX - r.left - V.carX) < 10) {
          cv.setPointerCapture(e.pointerId); state.dragCh = chOf(); state.drag = posHz(e, true); lastE = e; scheduleSpec(); e.preventDefault(); return;
        }
        const R = range();
        pan = { x: e.clientX, lo: R.lo, hi: R.hi, moved: false };
        cv.setPointerCapture(e.pointerId); cv.style.cursor = "grabbing"; e.preventDefault();
      });
      cv.addEventListener("pointerup", (e) => {
        if (state.drag !== null) {
          const f = Math.round(posHz(e, true) * 10) / 10; state.drag = null;
          control("carrier", f, state.dragCh); // only the carrier moves: neither the graph nor the receiver window follow
          scheduleSpec(); return;
        }
        if (pan) {
          const p = pan; pan = null; cv.style.cursor = "grab";
          const CH = cur(), R = CH.vr, S = slice();
          if (p.moved && R && isAdmin() && (R.lo < S.lo - V.geom.step || R.hi > S.hi + V.geom.step)) {
            control("view", (R.lo + R.hi) / 2, chOf()); // the server sends a slice centered there; the graph stays as it is
          }
          scheduleSpec();
        }
      });
      cv.addEventListener("pointercancel", () => { state.drag = null; pan = null; scheduleSpec(); });
      cv.addEventListener("wheel", (e) => {
        if (!V.geom) return;
        e.preventDefault();
        const G = V.geom, r = cv.getBoundingClientRect(), a = Math.max(0, Math.min(1, (e.clientX - r.left - G.L) / G.pw));
        const S = slice(), full = S.hi - S.lo, w0 = G.f1 - G.f0, w = Math.max(150, Math.min(full, w0 * (e.deltaY < 0 ? 0.8 : 1.25)));
        const at = G.f0 + a * w0, CH = cur();
        if (w >= full * 0.999) { CH.vr = null; } else { const lo = Math.max(S.lo, Math.min(S.hi - w, at - a * w)); CH.vr = { lo, hi: lo + w }; }
        scheduleSpec();
      }, { passive: false });
      cv.addEventListener("dblclick", () => {
        cur().vr = null;
        if (isAdmin()) control("viewreset", undefined, chOf());
        scheduleSpec();
      });
    };
    attach($("spec")); // (the small spectra have no carrier drag)
    // LOG/LIN of the Spectrum tab: kept per channel
    const btns = document.querySelectorAll("#spec-scale button");
    showScale = () => btns.forEach((x) => x.classList.toggle("on", x.dataset.sc === state.chans[state.sel].scale));
    btns.forEach((bt) => { bt.onclick = () => { state.chans[state.sel].scale = bt.dataset.sc; PREF[state.sel].set(bt.dataset.sc); showScale(); scheduleSpec(); }; });
    showScale();
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
      NX.version = h.version; $("about-ver").textContent = "v" + h.version; $("about-ver2").textContent = "v" + h.version;
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
      try { const v = Number(localStorage.getItem("nx-chan")); if (v > 0 && v < N && enabledCh(v)) state.sel = v; } catch (e) { /* no storage */ }
      applySel();
      showNet(h.net); if (!h.net) { $("t-users").textContent = String(h.clients || 1); }
      fire("hello", h);
    });
    on("location", () => { if (state.role !== "admin") { es.close(); connect(); } }); // the operator changed who may see location information
    // Live events carry the decoding channel they belong to (rcv, absent = 0): they are kept per channel, and shown when it is the selected one.
    const rc = (m) => m.rcv || 0;
    on("meters", (m) => { const i = rc(m); state.chans[i].meters = m; showLeds(i); if (i === state.sel) showMeters(m); });
    on("net", showNet);
    on("spectrum", (s) => { const i = rc(s); state.chans[i].spec = s; if (i === state.sel) state.spec = s; if (enabledCh(i)) scheduleSpec(); });
    on("text", (c) => { const i = rc(c); if (i === state.sel) addText(c.x); else addTextTo(state.chans[i], c.x); });
    on("line", (l) => { const i = rc(l); if (i === state.sel) addLine(l); else addLineTo(state.chans[i], l); });
    on("controls", (c) => {
      const i = rc(c), was = state.chans[i].controls;
      state.chans[i].controls = c;
      if (i === state.sel) showControls(c);
      showChan(i); refreshInput(i);
      if (i === state.sel && !c.enabled) selectChan(0); // the selected channel was switched off
      if (!was || was.enabled !== c.enabled) renderLines();
      if (was && was.enabled !== c.enabled) fire("channels", multiOn());
    });
    on("sun", showSun);
    on("run", (r) => { const i = rc(r); state.chans[i].run = r; showInfo(i); if (i === state.sel) showRun(r); });
    on("record", (r) => {
      if (r.action === "new") { const i = rc(r); if (i === state.sel) { state.lines = []; state.tail = ""; scheduleText(); } else { state.chans[i].lines = []; state.chans[i].tail = ""; } }
      fire("record", r);
    });
    on("stations", (r) => fire("stations", r));
    on("log", (e) => { if (state.tab === "syslog") appendLog(e); });
  }

  // ---- decoding channels ----
  // The server decodes up to N channels (config.Channels). state.* holds the selected channel's live data; state.chans[i] holds each
  // channel's own (controls, meters, run, spectrum, text, lines), kept up to date from the events.
  // a click on a small spectrum selects its channel; a double-click also shows the Spectrum tab
  for (let i = 0; i < N; i++) {
    let t = 0; // a click selects the channel and opens Monitor (after a moment, so that a double-click can go to Spectrum instead)
    box(i).addEventListener("click", (e) => {
      if (e.target.closest(".m-lock, .m-sync")) return;
      selectChan(i); clearTimeout(t);
      t = setTimeout(() => { if (state.tab !== "status") document.querySelector("#tabs button[data-tab=status]").click(); }, 250);
    });
    box(i).addEventListener("dblclick", (e) => {
      if (e.target.closest(".m-lock, .m-sync")) return;
      clearTimeout(t); selectChan(i); document.querySelector("#tabs button[data-tab=spectrum]").click();
    });
    M(i, ".m-lock").onclick = (e) => { e.stopPropagation(); const c = state.chans[i].controls; control("autotune", !(c ? c.autotune : true), i); };
    M(i, ".m-sync").onclick = (e) => { e.stopPropagation(); control("resync", undefined, i); };
    for (const b of [M(i, ".m-lock"), M(i, ".m-sync")]) b.ondblclick = (e) => e.stopPropagation();
  }
  // the channel lines: the first is in the page, the others are copies of it
  { const first = document.querySelector('.chline[data-ch="0"]'); for (let i = 1; i < N; i++) { const c = first.cloneNode(true); c.dataset.ch = i; c.hidden = true; first.parentNode.insertBefore(c, $("b-addchan")); } }
  const enabledCh = (i) => i === 0 || !!(state.chans[i].controls && state.chans[i].controls.enabled);
  const activeList = () => { const a = []; for (let i = 0; i < N; i++) if (enabledCh(i)) a.push(i); return a; };
  const multiOn = () => activeList().length > 1;
  NX.multiOn = multiOn; NX.secondOn = multiOn; NX.channels = N; NX.activeList = activeList;
  NX.chanTag = (rcv) => (multiOn() ? String((rcv || 0) + 1) : "");
  const chanName = (i) => { const c = state.chans[i].controls; return c ? (c.name || "") : ""; };
  function saveSel() { const n = state.chans[state.sel]; n.text = state.text; n.lines = state.lines; n.tail = state.tail; n.spec = state.spec; }
  function applySel() {
    const n = state.chans[state.sel];
    state.text = n.text; state.lines = n.lines; state.tail = n.tail; state.spec = n.spec; state.meters = n.meters;
    if (n.controls) showControls(n.controls);
    if (n.run) showRun(n.run);
    if (n.meters) showMeters(n.meters);
    drawText(); renderLines();
    for (let i = 0; i < N; i++) { showChan(i); showLeds(i); showInfo(i); }
    showInprog(); showScale();
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
  for (let i = 0; i < N; i++) {
    L(i, ".c-polarity").onchange = (e) => control("polarity", e.target.value, i);
    L(i, ".c-autotune").onchange = (e) => control("autotune", e.target.checked, i);
    L(i, ".c-uos").onchange = (e) => control("uos", e.target.checked, i);
    L(i, ".b-resync").onclick = () => control("resync", undefined, i);
    // clicking any of the first five columns selects the channel; double-clicking also opens its Options tab
    const ln = document.querySelector(`.chline[data-ch="${i}"]`);
    ln.addEventListener("click", (e) => { if (e.target.closest(".chip, .chfreq, .chinput, .chcmt, .chinfo")) selectChan(i); });
    ln.addEventListener("dblclick", (e) => {
      if (!e.target.closest(".chip, .chfreq, .chinput, .chcmt, .chinfo")) return;
      selectChan(i); optPage = "channel" + (i + 1); document.querySelector("#tabs button[data-tab=options]").click();
    });
  }
  $("b-addchan").onclick = () => { const k = activeList().length, free = []; for (let i = 1; i < N; i++) if (!enabledCh(i)) free.push(i); optPage = "channel" + ((free[0] || 1) + 1); document.querySelector("#tabs button[data-tab=options]").click(); };
  $("b-login").onclick = async () => {
    try { await api("POST", "/api/login", { token: $("tok").value }); location.reload(); } catch (e) { alert(e.message); }
  };
  async function loadDevices() { // the radios used so far (names the inputs of HPSDR channels and feeds the address list in Options)
    if (state.role !== "admin") return;
    try {
      NX.knownRadios = await api("GET", "/api/radios");
      for (let i = 0; i < N; i++) { showInput(i); refreshInput(i); }
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
  let fields = [], optHist = [], optPage = "station", optPages = new Map(), optMsgTimer = 0;
  async function loadOptions(force) {
    if (!force && pendingCount()) return; // keep unsaved edits when coming back to this tab
    const note = $("opt-note"), form = $("opt-form");
    form.replaceChildren(); $("opt-tabs").replaceChildren();
    if (state.role !== "admin") {
      note.hidden = false; note.textContent = "Settings can only be viewed and changed from the local network.";
      $("opt-bar").hidden = true; $("opt-hint").hidden = true; return;
    }
    note.hidden = true; $("opt-bar").hidden = false; $("opt-hint").hidden = false;
    try { const sj = await api("GET", "/api/settings"); fields = sj.fields; optHist = sj.history || []; } catch (e) { note.hidden = false; note.textContent = e.message; return; }
    const title = { operator: "Operator and position", input: "Input source", "input.device": "Audio input (sound card or virtual audio cable)", "input.hpsdr": "Radio (OpenHPSDR / Metis)", "input.stdin": "Raw stream (standard input)", "input.file": "WAV file", channel: "Channel", ddc: "Down-converter", decoder: "Decoder", receiver: "Receiver (FSK demodulator)", segment: "Message segmentation", logging: "Logging", autocorrect: "Word autocorrection", meters: "Meters and spectrum", server: "Network server", output: "Output (command line)" };
    const nameOf = (p, d) => { const f = fields.find((x) => x.path === p); return (f && String(f.value).trim()) || d; };
    const pages = [
      ["station", "Station", ["operator"]],
      ["channel1", nameOf("channel.name", "Channel 1"), ["channel", "input", "input.device", "input.hpsdr", "input.stdin", "input.file", "receiver", "decoder", "segment", "ddc"]],
      ...[0, 1, 2].map((k) => { const p = `extra.${k}.`; return ["channel" + (k + 2), nameOf(p + "channel.name", "Channel " + (k + 2)), ["extra." + k, ...["channel", "input", "input.device", "input.hpsdr", "input.stdin", "input.file", "receiver", "decoder", "segment", "ddc"].map((x) => p + x)]]; }),
      ["logging", "Logging", ["logging", "autocorrect"]],
      ["display", "Display", ["meters"]],
      ["network", "Network", ["server"]],
      ["cli", "Command line", ["output"]],
    ];
    const known = new Set(pages.flatMap((p) => p[2]));
    const gkey = (path) => {
      const m = /^(extra\.\d+)\.(.*)$/.exec(path);
      if (m) return m[2] === "enabled" ? m[1] : m[1] + "." + gkey(m[2]);
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
      const mch = /^channel(\d)$/.exec(id);
      if (mch) {
        const bar2 = document.createElement("div"); bar2.className = "chanact";
        const chn = Number(mch[1]) - 1;
        const rb = document.createElement("button"); rb.type = "button"; rb.className = "reset"; rb.textContent = "Restore default settings (this channel)";
        rb.title = "Puts every setting of this channel back to its default (name, comment and the on/off switch stay). Unsaved edits on this page are lost.";
        rb.onclick = async () => {
          if (!confirm(`Restore the default settings of ${label}?\n\nInput, frequency, filter, receiver, decoder and segmentation settings of this channel are reset. The channel restarts.`)) return;
          try { await api("POST", "/api/settings/reset-channel", { ch: chn }); await loadOptions(true); setMsg("Default settings restored."); }
          catch (e) { setMsg(e.message, 0); }
        };
        const pb = document.createElement("button"); pb.type = "button"; pb.className = "reset"; pb.textContent = "Restore previous settings (this channel)";
        pb.disabled = !(optHist[chn] > 0);
        pb.title = "Undoes the latest bout of changes to this channel's settings (can be repeated, up to " + 10 + " steps). Name, comment and the on/off switch stay. Unsaved edits on this page are lost.";
        pb.onclick = async () => {
          if (!confirm(`Restore the previous settings of ${label}?\n\nThe channel goes back to the settings it had before your latest changes. It may restart.`)) return;
          try { await api("POST", "/api/settings/restore-channel", { ch: chn }); await loadOptions(true); setMsg("Previous settings restored."); }
          catch (e) { setMsg(e.message, 0); }
        };
        bar2.append(pb, rb); page.appendChild(bar2);
      }
      if (id === "logging") {
        const n = document.createElement("p"); n.className = "small"; n.textContent = "These settings are shared by all channels. Dropping fragments (min_fragment_chars, min_fragment_words and the related settings under Message segmentation) is set separately for each channel, on its Channel tab.";
        page.appendChild(n);
        extra.box = document.createElement("div"); extra.box.id = "opt-extra";
      }
      for (const g of gs) {
        if (!groups.has(g)) continue;
        const fs = document.createElement("fieldset"); fs.className = "optgrp"; if (g.startsWith("input.")) fs.dataset.src = g.slice(6);
        const gx = /^extra\.(\d+)\.input\.(.+)$/.exec(g); if (gx) { fs.dataset.srcx = gx[1]; fs.dataset.src2 = gx[2]; }
        const lg = document.createElement("legend"); const gb = g.replace(/^extra\.\d+\.?/, ""); // the group without its channel prefix
        lg.textContent = /^extra\.\d+$/.test(g) ? `Channel ${Number(g.slice(6)) + 2}: switch it on to decode it at the same time, from its own input` : gb === "channel" ? "Channel: frequency, name and comment" : title[gb] || g; fs.appendChild(lg); page.appendChild(fs);
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
          else if (/(^|\.)channel\.frequency_hz$/.test(f.path)) { // only the three NAVTEX channels
            inp = document.createElement("select");
            const hz = [518000, 490000, 424000]; if (!hz.includes(Number(f.value))) hz.push(Number(f.value));
            for (const v of hz) inp.add(new Option(`${v / 1000} kHz`, String(v)));
            inp.value = String(f.value);
          }
          else if (/(^|\.)channel\.name$/.test(f.path)) { inp = document.createElement("input"); inp.type = "text"; inp.value = f.value; inp.readOnly = true; inp.classList.add("ro"); }
          else if (/(^|\.)input\.shared_from$/.test(f.path)) { // which channel's input this channel uses
            const mx = /^extra\.(\d+)\./.exec(f.path), me = mx ? Number(mx[1]) + 2 : 1;
            inp = document.createElement("select"); inp.add(new Option("own input (set below)", "0"));
            for (let n = 1; n <= N; n++) {
              if (n === me) continue;
              const fq = fields.find((x) => x.path === (n === 1 ? "channel.frequency_hz" : `extra.${n - 2}.channel.frequency_hz`));
              inp.add(new Option(`same input as channel ${n}` + (fq ? ` (${(Number(fq.value) / 1000).toFixed(0)} kHz)` : ""), String(n)));
            }
            inp.value = String(f.value);
          }
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
          const doc = document.createElement("div"); doc.className = "doc"; doc.textContent = /(^|\.)channel\.name$/.test(f.path) ? "" : f.doc + (f.unit && !/frequency_hz$/.test(f.path) ? ` (${f.unit})` : "");
          if (/(^|\.)lo_below_hz$/.test(f.path)) { // show the resulting LO next to the offset
            const fq = fields.find((x) => x.path === f.path.replace(/input\.[a-z]+\.lo_below_hz$/, "channel.frequency_hz"));
            const hint = document.createElement("span"); hint.className = "lohint"; doc.append(document.createElement("br"), hint);
            const upd = () => { const v = Number(inp.value); hint.textContent = fq && inp.value !== "" && isFinite(v) ? `Resulting LO: ${((Number(fq.value) - v) / 1000).toFixed(3)} kHz (channel ${(Number(fq.value) / 1000).toFixed(3)} kHz).` : ""; };
            inp.addEventListener("input", upd); upd();
          }
          if (/(^|\.)input\.hpsdr\.address$/.test(f.path)) { // free a radio that stays "in use" after its program was killed
            const rb = document.createElement("button"); rb.type = "button"; rb.textContent = "Release radio\u2026"; rb.className = "reset";
            rb.title = "For a radio that reports being in use although nothing should be using it (for example after a program was killed). Asks twice before doing anything.";
            rb.onclick = async () => {
              const addr = inp.value.trim();
              if (!addr) { setMsg("Enter the radio's IP address first (the box cannot be empty for this).", 8000); return; }
              try {
                const st = await api("GET", "/api/radio/state?addr=" + encodeURIComponent(addr));
                if (!st.found) { setMsg(`The radio at ${addr} did not answer.`, 8000); return; }
                if (st.ours) { setMsg(`The radio at ${addr} is being used by this program itself. Stop the channel that uses it instead.`, 8000); return; }
                if (!st.busy) { setMsg(`The radio at ${addr} does not report being in use; nothing to release.`, 8000); return; }
                if (!confirm(`The radio at ${addr} appears to be in use (it is streaming to another host).\n\nIt may be in use by someone else right now. Do you want to try to reset it?`)) return;
                if (!confirm(`Send the stop command to ${addr}?\n\nAnyone who is receiving from this radio will lose the stream.`)) return;
                const r = await api("POST", "/api/radio/release", { addr });
                setMsg(r.busy ? `Stop command sent, but the radio at ${addr} still reports being in use.` : `The radio at ${addr} is free now.`, 10000);
              } catch (e) { setMsg(e.message, 0); }
            };
            doc.append(document.createElement("br"), rb);
          }
          row.append(name, inp, doc); fs.appendChild(row);
        }
      }
      if (extra.box && id === "logging") page.appendChild(extra.box);
    }
    // only the options of the selected input source are shown
    const srcSel = form.querySelector('[data-path="input.source"]');
    if (srcSel) {
      const shSel = form.querySelector('[data-path="input.shared_from"]');
      const showSrc = () => {
        const shared = shSel && shSel.value !== "0"; srcSel.disabled = shared; srcSel.closest(".opt").classList.toggle("dim", shared);
        form.querySelectorAll("fieldset[data-src]").forEach((fs) => { fs.hidden = fs.dataset.src !== srcSel.value; fs.disabled = shared; fs.classList.toggle("dim", shared); });
      };
      srcSel.addEventListener("change", showSrc); if (shSel) shSel.addEventListener("change", showSrc); showSrc();
    }
    for (let k = 0; k < N - 1; k++) {
      const sx = form.querySelector(`[data-path="extra.${k}.input.source"]`);
      if (!sx) continue;
      const shX = form.querySelector(`[data-path="extra.${k}.input.shared_from"]`);
      const showX = () => {
        const shared = shX && shX.value !== "0"; sx.disabled = shared; sx.closest(".opt").classList.toggle("dim", shared);
        form.querySelectorAll(`fieldset[data-srcx="${k}"]`).forEach((fs) => { fs.hidden = fs.dataset.src2 !== sx.value; fs.disabled = shared; fs.classList.toggle("dim", shared); });
      };
      sx.addEventListener("change", showX); if (shX) shX.addEventListener("change", showX); showX();
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
  setInterval(() => { if (state.role === "admin" && !NX.knownRadios) loadDevices(); }, 5000);
  setInterval(() => { if (state.role === "admin") loadDevices(); }, 20000); // a radio joins the list once it has delivered data
})();

(() => {
  const $ = window.NX.$, dlg = $("about-dlg");
  $("about-ver").onclick = () => { $("about-contact").hidden = window.NX.state.role !== "admin"; dlg.showModal(); };
  $("about-close").onclick = () => dlg.close();
  dlg.addEventListener("mousedown", (e) => { if (e.target === dlg) dlg.close(); });
})();
