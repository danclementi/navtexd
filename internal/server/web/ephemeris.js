"use strict";
// Ephemeris tab: daylight at the receiver and at a station against the station's transmission slots.
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el;
  const E = { data: null, err: "" };
  const css = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
  const today = () => new Date(Date.now() + NX.state.offset).toISOString().slice(0, 10);
  const hhmm = (m) => `${NX.pad(Math.floor(m / 60) % 24)}:${NX.pad(m % 60)}`;
  const label = (s) => `${s.b1}:${s.call} ${s.name},${s.cc}`;

  function fillStations() {
    const f = Number($("ep-freq").value), sel = $("ep-station"), cur = sel.value;
    sel.replaceChildren();
    const by = new Map();
    for (const s of NX.stationList()) if (s.freq === f) { if (!by.has(s.area)) by.set(s.area, []); by.get(s.area).push(s); }
    for (const a of [...by.keys()].sort((x, y) => x - y)) {
      const g = el("optgroup", { label: "NAVAREA " + a });
      for (const s of by.get(a).sort((x, y) => x.b1.localeCompare(y.b1))) g.append(new Option(label(s), String(s.id)));
      sel.append(g);
    }
    if ([...sel.options].some((o) => o.value === cur)) { sel.value = cur; return; }
    // default: the station closest to the receiver (distances are only known to clients that may see them)
    let best = null;
    for (const s of NX.stationList()) if (s.freq === f && s.km && (!best || s.km < best.km)) best = s;
    if (best) sel.value = String(best.id);
  }

  async function load() {
    const id = $("ep-station").value, date = $("ep-date").value || today();
    $("ep-note").hidden = true;
    if (!id) return;
    try { E.data = await NX.api("GET", `/api/ephemeris?station=${id}&date=${date}`); E.err = ""; }
    catch (e) { E.data = null; E.err = e.message; $("ep-note").hidden = false; $("ep-note").textContent = e.message; }
    draw(); info();
  }

  function draw() {
    const cv = $("ep-canvas");
    const dpr = window.devicePixelRatio || 1, w = cv.clientWidth, h = cv.clientHeight;
    cv.width = Math.round(w * dpr); cv.height = Math.round(h * dpr);
    const g = cv.getContext("2d");
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, w, h);
    const d = E.data;
    if (!d) return;
    const L = 120, R = 16, T = 44, B = 34, pw = w - L - R;
    const X = (m) => L + (m / 1440) * pw;
    const rowH = 54, rows = [{ y: T + 8, name: "Receiver", iv: d.local, col: css("--day-local") }, { y: T + 8 + rowH + 16, name: d.station.call, iv: d.dx, col: css("--day-dx") }];
    g.fillStyle = css("--ink"); g.font = `600 13px ${NX.FONT}`; g.textAlign = "left"; g.textBaseline = "top";
    g.fillText(`Ephemeris comparison for ${d.station.call}, ${d.station.name} ${d.station.cc}` + (d.station.km ? ` (${d.station.km} km)` : "") + ` on ${d.date}`, L, 10);
    g.font = `12px ${NX.FONT}`; g.fillStyle = css("--dim");
    g.textAlign = "center";
    for (let hr = 0; hr <= 24; hr += 2) g.fillText(String(hr), X(hr * 60), T + 8 + 2 * rowH + 16 + 6);
    g.textAlign = "right"; g.textBaseline = "middle";
    for (const r of rows) {
      g.fillStyle = css("--night"); g.fillRect(X(0), r.y, pw, rowH);
      g.fillStyle = r.col;
      for (const [a, b] of r.iv) g.fillRect(X(a), r.y, X(b) - X(a), rowH);
      g.fillStyle = css("--ink"); g.fillText(r.name, L - 8, r.y + rowH / 2);
    }
    g.strokeStyle = css("--line"); g.lineWidth = 1;
    for (let hr = 0; hr <= 24; hr++) { const x = Math.round(X(hr * 60)) + .5; g.beginPath(); g.moveTo(x, T + 4); g.lineTo(x, rows[1].y + rowH + 4); g.stroke(); }
    g.fillStyle = css("--slot");
    for (const s of d.slots) g.fillRect(X(s), T + 4, Math.max(2, X(10) - X(0)), rows[1].y + rowH - T);
    if (d.date === today()) {
      const n = new Date(Date.now() + NX.state.offset), m = n.getUTCHours() * 60 + n.getUTCMinutes() + n.getUTCSeconds() / 60, x = X(m);
      g.strokeStyle = css("--now"); g.lineWidth = 2; g.beginPath(); g.moveTo(x, T - 2); g.lineTo(x, rows[1].y + rowH + 6); g.stroke();
      g.fillStyle = css("--now"); g.textAlign = "center"; g.textBaseline = "bottom"; g.fillText(hhmm(Math.floor(m)), x, T - 3);
    }
    // legend
    g.textAlign = "left"; g.textBaseline = "middle"; g.font = `11px ${NX.FONT}`;
    let x = L; const y = h - 10;
    for (const [c, t] of [[css("--day-local"), "daytime at the receiver"], [css("--day-dx"), "daytime at the station"], [css("--slot"), "station's slots"], [css("--now"), "now"]]) {
      g.fillStyle = c; g.fillRect(x, y - 5, 10, 10); g.fillStyle = css("--dim"); g.fillText(t, x + 14, y); x += 24 + g.measureText(t).width + 12;
    }
  }

  function info() {
    const dl = $("ep-info");
    dl.replaceChildren();
    const d = E.data;
    if (!d) return;
    const s = d.station, add = (k, v) => dl.append(el("dt", { text: k }), el("dd", { text: v }));
    const iv = (a) => a.length ? a.map(([x, y]) => `${hhmm(x)}–${hhmm(y)}`).join(", ") : "none";
    add("Station", `${s.call} ${s.name}, ${s.country}  (${s.freq} kHz, NAVAREA ${s.area}, B1 ${s.b1})`);
    add("Position", `latitude ${s.lat.toFixed(3)}, longitude ${s.lon.toFixed(3)}`);
    if (s.km) add("From the receiver", `${s.km} km, bearing ${s.brg}°`);
    add("Transmission slots", s.times.join("  ") + "  UTC");
    add("Daylight at the receiver", iv(d.local) + " UTC");
    add("Daylight at the station", iv(d.dx) + " UTC");
  }

  $("ep-freq").onchange = () => { fillStations(); load(); };
  $("ep-station").onchange = load;
  $("ep-date").onchange = load;
  $("ep-today").onclick = () => { $("ep-date").value = today(); load(); };
  window.addEventListener("resize", () => { if (!$("tab-ephemeris").hidden) draw(); });
  NX.tabs.ephemeris = async () => {
    await NX.loadStations();
    if (!$("ep-date").value) $("ep-date").value = today();
    if (!$("ep-station").options.length) { $("ep-freq").value = String(NX.state.controls ? NX.state.controls.channel : 518); fillStations(); }
    load();
  };
  setInterval(() => { if (!$("tab-ephemeris").hidden) draw(); }, 30000);
})();
