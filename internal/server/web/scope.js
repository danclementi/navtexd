"use strict";
// Scope tab: a software oscilloscope for the demodulator's intermediate signals. The server keeps the last seconds of
// every signal on one time axis (each shifted for its own processing delay, so decided bits sit under the signal that
// produced them) and serves sweeps on request; this file triggers, draws and paces them.
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el;
  const LANES = [
    { id: "audio", label: "Audio (baseband)", title: "Real part of the baseband input to the demodulator" },
    { id: "filt", label: "Channel filter", title: "Output of the narrow channel filter (real part) with its envelope" },
    { id: "tones", label: "Tone filters", title: "One-symbol matched filters at the high tone (green) and the low tone (amber)" },
    { id: "disc", label: "Discriminator (Hz)", title: "Instantaneous frequency from the carrier; the dashed lines are the two tones" },
    { id: "soft", label: "Soft symbols (Hz)", title: "Integrate-and-dump value of each symbol" },
    { id: "bit", label: "Bits", title: "Decided bits, high tone = 1; blank when symbol timing is not locked" },
  ];
  const pref = {
    get(k, d) { try { const v = localStorage.getItem("navtex.scope." + k); return v === null ? d : JSON.parse(v); } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem("navtex.scope." + k, JSON.stringify(v)); } catch (e) { /* no storage */ } },
  };
  const S = { sweep: null, after: 0, armed: false, timer: 0, busy: false };
  const colour = (n, d) => getComputedStyle(document.documentElement).getPropertyValue(n).trim() || d;

  function build() {
    const box = $("sc2-lanes");
    for (const l of LANES) {
      const cb = el("input", { type: "checkbox", checked: pref.get("lane." + l.id, true) });
      cb.onchange = () => { pref.set("lane." + l.id, cb.checked); draw(); };
      cb.dataset.lane = l.id;
      box.append(el("label", { class: "check", title: l.title }, cb, l.label));
    }
    $("sc2-span").value = String(pref.get("span", 0.5));
    $("sc2-trig").value = pref.get("trig", "rise");
    $("sc2-on").checked = pref.get("on", true);
    $("sc2-free").checked = pref.get("free", true);
    $("sc2-clock").checked = pref.get("clock", true);
    $("sc2-span").onchange = () => { pref.set("span", $("sc2-span").value); S.after = 0; kick(); };
    $("sc2-trig").onchange = () => { pref.set("trig", $("sc2-trig").value); S.after = 0; kick(); };
    $("sc2-clock").onchange = () => { pref.set("clock", $("sc2-clock").checked); draw(); };
    $("sc2-on").onchange = () => { pref.set("on", $("sc2-on").checked); S.after = 0; kick(); draw(); };
    $("sc2-free").onchange = () => { pref.set("free", $("sc2-free").checked); if ($("sc2-free").checked) S.armed = false; S.after = 0; kick(); };
    $("sc2-one").onclick = async () => {
      $("sc2-free").checked = false; pref.set("free", false);
      S.armed = true; S.after = 1e9; // asking beyond the end returns just the head time: wait for the next sweep after it
      $("sc2-state").textContent = "armed";
      const r = await fetchSweep(); if (r && r.head !== undefined) S.after = r.head;
      kick();
    };
    window.addEventListener("resize", draw);
  }

  async function fetchSweep() {
    const q = new URLSearchParams({ span: $("sc2-span").value, trig: $("sc2-trig").value === "none" ? "none" : $("sc2-trig").value, pre: "0.1", after: String(S.after), points: "1000" });
    try { return await (await fetch("/api/scope?" + q)).json(); } catch (e) { return null; }
  }

  function kick() {
    clearTimeout(S.timer);
    S.timer = setTimeout(tick, 0);
  }
  async function tick() {
    if ($("tab-scope").hidden || !$("sc2-on").checked) { $("sc2-state").textContent = $("sc2-on").checked ? "" : "off"; return; }
    const free = $("sc2-free").checked;
    if (!free && !S.armed) { $("sc2-state").textContent = "stopped"; return; }
    let r = null;
    if (!S.busy) { S.busy = true; r = await fetchSweep(); S.busy = false; }
    let wait = 300;
    if (r) {
      if (r.sweep) {
        S.sweep = r.sweep; S.after = r.sweep.t0 + r.sweep.span; draw();
        if (S.armed) { S.armed = false; $("sc2-state").textContent = "done"; return; }
        wait = 120;
      } else if (r.head !== undefined && r.head < S.after - 0.5 && S.after < 1e8) { S.after = 0; } // the stream restarted
      if (!r.sweep && !S.sweep && r.head === 0) $("sc2-state").textContent = "no data yet";
    }
    if (S.armed) $("sc2-state").textContent = "armed - waiting for trigger";
    else if (free) $("sc2-state").textContent = r && r.sweep ? "free-run" : "free-run (waiting)";
    S.timer = setTimeout(tick, wait);
  }

  const niceStep = (span) => { const e = Math.pow(10, Math.floor(Math.log10(span / 6))); const m = span / 6 / e; return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 5 ? 5 : 10) * e; };

  function draw() {
    const cv = $("sc2-canvas");
    const dpr = window.devicePixelRatio || 1;
    const w = cv.clientWidth, h = cv.clientHeight;
    if (!w) return;
    if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(h * dpr)) { cv.width = Math.round(w * dpr); cv.height = Math.round(h * dpr); }
    const g = cv.getContext("2d");
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    const bg = colour("--spec-bg", "#0f1722"), grid = colour("--spec-grid", "#263447"), ink = colour("--spec-ink", "#9fb0c6");
    g.fillStyle = bg; g.fillRect(0, 0, w, h);
    g.font = `11px ${NX.FONT}`;
    const sw = S.sweep;
    if (!$("sc2-on").checked || !sw) {
      g.fillStyle = ink; g.textAlign = "center"; g.textBaseline = "middle";
      g.fillText($("sc2-on").checked ? "waiting for a sweep (needs a locked signal for bit triggers)" : "Scope display off", w / 2, h / 2); return;
    }
    const on = LANES.filter((l) => pref.get("lane." + l.id, true));
    const L = 8, R = 8, T = 6, B = 22, pw = w - L - R, ph = h - T - B;
    if (!on.length) return;
    const lh = ph / on.length;
    const n = sw.traces.disc.length;
    const X = (i) => L + i / (n - 1) * pw;
    const XT = (t) => L + t / sw.span * pw;
    // time grid
    const step = niceStep(sw.span);
    g.strokeStyle = grid; g.lineWidth = 1; g.fillStyle = ink; g.textAlign = "center"; g.textBaseline = "top";
    for (let t = 0; t <= sw.span + 1e-9; t += step) {
      const x = Math.round(XT(t)) + .5; g.beginPath(); g.moveTo(x, T); g.lineTo(x, T + ph); g.stroke();
      g.textAlign = t < 1e-9 ? "left" : t + step > sw.span + 1e-9 ? "right" : "center";
      g.fillText(`${(t * 1000).toFixed(step < 0.01 ? 1 : 0)} ms`, x, T + ph + 5);
    }
    if ($("sc2-clock").checked && sw.clock) {
      g.save(); g.globalAlpha = 0.35; g.setLineDash([1, 3]); g.strokeStyle = ink;
      for (const t of sw.clock) { const x = Math.round(XT(t - 0.005)) + .5; g.beginPath(); g.moveTo(x, T); g.lineTo(x, T + ph); g.stroke(); } // symbol boundaries
      g.restore();
    }
    const line = (arr, y0, scale, col, wd, off) => {
      g.beginPath();
      for (let i = 0; i < n; i++) { const x = X(i), y = y0 - (arr[i] - (off || 0)) * scale; if (i) g.lineTo(x, y); else g.moveTo(x, y); }
      g.strokeStyle = col; g.lineWidth = wd; g.stroke();
    };
    const maxAbs = (...arrs) => { let m = 1e-9; for (const a of arrs) for (const v of a) { const q = Math.abs(v); if (q > m) m = q; } return m; };
    const cA = colour("--spec-raw", "#4b78b8"), cB = colour("--spec-filt", "#59d68c"), cC = "#e6b422";
    on.forEach((l, k) => {
      const top = T + k * lh, mid = top + lh / 2, half = lh / 2 - 6;
      g.strokeStyle = grid; g.beginPath(); g.moveTo(L, Math.round(top + lh) + .5); g.lineTo(L + pw, Math.round(top + lh) + .5); g.stroke();
      g.fillStyle = ink; g.textAlign = "left"; g.textBaseline = "top"; g.globalAlpha = 0.85; g.fillText(l.label, L + 4, top + 3); g.globalAlpha = 1;
      const tr = sw.traces;
      switch (l.id) {
        case "audio": { const m = maxAbs(tr.audio); line(tr.audio, mid, half / m, cA, 1); g.fillStyle = ink; g.textAlign = "right"; g.fillText(`±${m.toPrecision(2)}`, L + pw - 4, top + 3); break; }
        case "filt": {
          const m = maxAbs(tr.filt, tr.env); line(tr.env, mid, half / m, cC, 1); line(tr.env, mid, -half / m, cC, 1); line(tr.filt, mid, half / m, cB, 1);
          g.fillStyle = ink; g.textAlign = "right"; g.fillText(`±${m.toPrecision(2)}`, L + pw - 4, top + 3); break; }
        case "tones": {
          const m = maxAbs(tr.hi, tr.lo); const y0 = top + lh - 6, sc = (lh - 14) / m;
          line(tr.lo, y0, sc, cC, 1); line(tr.hi, y0, sc, cB, 1.2); break; }
        case "disc": {
          const m = 150; line(tr.disc, mid, half / m, cA, 1);
          g.save(); g.setLineDash([2, 4]); g.strokeStyle = ink; g.globalAlpha = 0.5;
          for (const f of [85, -85]) { const y = Math.round(mid - f * half / m) + .5; g.beginPath(); g.moveTo(L, y); g.lineTo(L + pw, y); g.stroke(); } g.restore(); break; }
        case "soft": { const m = 130; line(tr.soft, mid, half / m, cB, 1.5); break; }
        case "bit": {
          const hi = mid - half * 0.55, lo = mid + half * 0.55;
          g.beginPath(); let pen = false;
          for (let i = 0; i < n; i++) {
            const b = sw.bit[i];
            if (b === 0) { pen = false; continue; }
            const x = X(i), y = b > 0 ? hi : lo;
            if (!pen) { g.moveTo(x, y); pen = true; } else g.lineTo(x, y);
            if (i + 1 < n && sw.bit[i + 1] !== b && sw.bit[i + 1] !== 0) g.lineTo(X(i + 1), sw.bit[i + 1] > 0 ? hi : lo);
          }
          g.strokeStyle = cB; g.lineWidth = 2; g.stroke();
          g.fillStyle = ink; g.textAlign = "right"; g.textBaseline = "middle"; g.fillText("1", L + pw - 4, hi); g.fillText("0", L + pw - 4, lo); break; }
      }
    });
    if (sw.trigger >= 0) {
      const x = Math.round(XT(sw.trigger)) + .5;
      g.save(); g.strokeStyle = colour("--spec-car", "#ff6b6b"); g.setLineDash([4, 3]); g.beginPath(); g.moveTo(x, T); g.lineTo(x, T + ph); g.stroke(); g.restore();
    }
  }

  build();
  NX.tabs.scope = () => { draw(); kick(); };
  setInterval(() => { if ($("tab-scope").hidden && S.timer) { clearTimeout(S.timer); S.timer = 0; } }, 2000);
})();
