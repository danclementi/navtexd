"use strict";
// Log tab: filters, record grid, record text, manual B1 / NAVAREA override, delete, shift reverser.
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el, api = NX.api;
  const CH = 100, ROW = 22, HDR = 26; // fetch chunk size, row height and header height in pixels
  const L = { total: 0, all: 0, cache: new Map(), chunks: new Set(), gen: 0, sel: new Set(), selId: 0, anchor: -1, rec: null, loaded: false, timer: 0, q: "", hid0: 0, la: 0, lt: 0 };
  const admin = () => NX.state.role === "admin";
  const sc = $("logscroll");

  for (let i = 1; i <= 21; i++) $("f-area").add(new Option(String(i), String(i)));
  for (const c of "ABCDEFGHIJKLMNOPQRSTUVWXYZ?") $("f-b1").add(new Option(c, c));

  const ids = ["freq", "rcv", "area", "b1", "kind", "text", "from", "to", "kmin", "kmax"];
  // dates are typed and shown as yyyy-mm-dd (like the UTC column) with a calendar button for picking
  const isoOk = (v) => /^\d{4}-\d{2}-\d{2}$/.test(v) && !isNaN(Date.parse(v + "T00:00:00Z")) && new Date(v + "T00:00:00Z").toISOString().slice(0, 10) === v;
  function isoField(inp) {
    const wrap = document.createElement("span"); wrap.className = "isodate";
    const btn = document.createElement("button"); btn.type = "button"; btn.className = "cal"; btn.title = "Pick a date from a calendar"; btn.setAttribute("aria-label", "Pick a date");
    btn.innerHTML = '<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.4" d="M2.5 3.5h11v10h-11zM2.5 6.5h11M5 2v3M11 2v3"/></svg>';
    const pick = document.createElement("input"); pick.type = "date"; pick.className = "pick"; pick.tabIndex = -1; pick.setAttribute("aria-hidden", "true");
    inp.parentNode.insertBefore(wrap, inp); wrap.append(inp, btn, pick);
    btn.onclick = () => { pick.value = isoOk(inp.value.trim()) ? inp.value.trim() : ""; try { pick.showPicker(); } catch (e) { pick.focus(); pick.click(); } };
    pick.onchange = () => { if (pick.value) { inp.value = pick.value; inp.classList.remove("bad"); inp.dispatchEvent(new Event("input", { bubbles: true })); inp.dispatchEvent(new Event("change", { bubbles: true })); } };
    inp.addEventListener("input", () => { const d = inp.value.replace(/[^\d-]/g, ""); if (d !== inp.value) inp.value = d; inp.classList.toggle("bad", inp.value !== "" && inp.value.length >= 10 && !isoOk(inp.value)); });
  }
  for (const id of ["n-date", "f-from", "f-to"]) isoField($(id));
  function params() {
    const p = new URLSearchParams();
    for (const k of ids) { let v = $("f-" + k).value.trim(); if ((k === "from" || k === "to") && !isoOk(v)) v = ""; if (v) p.set(k === "freq" ? "freq" : k === "area" ? "navarea" : k === "kmin" ? "minkm" : k === "kmax" ? "maxkm" : k, v); }
    if ($("f-unid").checked) p.set("unid", "1");
    return $("f-on").checked ? p : new URLSearchParams(); // switched off: the settings stay in the boxes but nothing is filtered
  }
  const filterOn = () => L.q !== ""; // the filter that is applied (not what is typed in the boxes)
  const newestFirst = () => $("n-order").value === "newest";

  async function fetchRange(offset, limit) {
    const p = params();
    p.set("limit", limit); p.set("offset", offset); p.set("order", $("n-order").value);
    return api("GET", "/api/messages?" + p);
  }

  // ---- the continuous list: only the rows in view are in the table; the rest is spacer ----
  async function ensure(first, last) {
    const gen = L.gen;
    for (let c = Math.floor(Math.max(0, first) / CH); c <= Math.floor(Math.max(0, last) / CH); c++) {
      if (L.chunks.has(c) || c * CH >= L.total) continue;
      L.chunks.add(c);
      fetchRange(c * CH, CH).then((j) => {
        if (gen !== L.gen) return;
        j.records.forEach((r, k) => L.cache.set(c * CH + k, r));
        L.total = j.total; L.all = j.all;
        render();
      }).catch(() => { L.chunks.delete(c); });
    }
  }
  const idxOf = (id) => { for (const [i, r] of L.cache) if (r.id === id) return i; return -1; };
  const visRows = () => Math.max(1, Math.floor((sc.clientHeight - HDR) / ROW));

  function render() {
    $("n-total").textContent = L.total;
    $("n-all").textContent = L.total !== L.all ? `(of ${L.all})` : "";
    $("f-km-wrap").hidden = !NX.state.loc;
    $("f-rcv-wrap").hidden = !NX.secondOn();
    indicate();
    const first = Math.max(0, Math.floor(sc.scrollTop / ROW)), vis = visRows() + 2;
    const start = Math.max(0, first - 3), end = Math.min(L.total, first + vis + 3);
    const n = L.sel.size;
    $("n-selinfo").textContent = n > 1 ? `${n} selected` : "";
    $("n-open").disabled = !L.selId;
    if (dlg.open) updateNav();
    $("n-report").hidden = n < 1;
    $("n-report").textContent = n > 1 ? `Add ${n} to report` : "Add to report";
    $("n-delete").disabled = !admin() || n < 1;
    $("n-delete").textContent = n > 1 ? `\u2212 Delete ${n} records` : "\u2212 Delete record";
    const tb = document.querySelector("#log-table tbody");
    const rows = [];
    const spacer = (h) => { const tr = el("tr", { class: "spacer" }); tr.style.height = h + "px"; tr.append(el("td", { colspan: "10" })); return tr; };
    if (start > 0) rows.push(spacer(start * ROW));
    for (let i = start; i < end; i++) {
      const r = L.cache.get(i);
      if (!r) { const tr = el("tr", { class: "ph" }); tr.append(el("td", { colspan: "10", text: "\u2026" })); rows.push(tr); continue; }
      const tr = el("tr", { "data-id": r.id, "data-i": i, class: (L.sel.has(r.id) ? "sel " : "") + (r.id === L.selId ? "cur " : "") + (r.st === "UNID" ? "unid" : "") });
      tr.append(el("td", { text: NX.stamp(r.rx), "data-f": "utc", }), el("td", { text: r.ch }, NX.secondOn() ? el("span", { class: "chb", text: String((r.rcv || 0) + 1), title: "Received on channel " + ((r.rcv || 0) + 1) }) : null), el("td", { text: r.q.toFixed(0) }),
        el("td", { text: r.b1 || "?", class: r.man ? "man" : "" }), el("td", { text: r.b2 }), el("td", { text: r.ser || "?" }), el("td", { text: String(r.area), class: r.man ? "man" : "" }),
        el("td", { text: r.st + (r.cmt ? ` [${r.cmt}]` : "") }), el("td", { class: "km", text: r.km ? r.km.toFixed(0) : "" }), el("td", { text: r.kind }));
      rows.push(tr);
    }
    if (end < L.total) rows.push(spacer((L.total - end) * ROW));
    const top = sc.scrollTop;
    tb.replaceChildren(...rows);
    if (sc.scrollTop !== top) sc.scrollTop = top; // the browser may clamp it while the rows are replaced
    ensure(start, end - 1);
  }
  NX.listen("channels", () => { if (L.loaded) render(); });
  sc.addEventListener("scroll", () => { if (!L.gotoAt || performance.now() - L.gotoAt > 600) $("n-date").value = ""; requestAnimationFrame(render); }); // scrolling by hand forgets the "go to date"
  window.addEventListener("resize", () => { if (L.loaded) render(); });

  function scrollToIndex(i) {
    const top = i * ROW, bottom = top + ROW;
    if (top < sc.scrollTop) sc.scrollTop = top;
    else if (bottom > sc.scrollTop + sc.clientHeight - HDR) sc.scrollTop = bottom - sc.clientHeight + HDR;
    render();
  }
  const toEnd = (latest) => { sc.scrollTop = latest === "bottom" ? L.total * ROW + HDR : 0; render(); };
  const jumpLatest = () => toEnd(newestFirst() ? "top" : "bottom");

  // a fresh query (filters or order changed): forget everything and show the latest records
  async function reload() {
    L.gen++; L.cache.clear(); L.chunks.clear();
    const q = params().toString();
    try { const j = await fetchRange(0, 1); L.total = j.total; L.all = j.all; } catch (e) { $("n-total").textContent = "error: " + e.message; return; }
    L.q = q; L.lt = L.total; L.la = L.all; L.hid0 = L.all - L.total; // what the filter hid when it was applied
    L.sel = new Set(); L.selId = 0; L.anchor = -1;
    jumpLatest();
    select(0);
  }
  // new or changed records arrived: the view, the selection and an open edit stay exactly where they are. Rows are kept by
  // position in the list; when the newest records come first, new ones push everything down by the change in the total,
  // so cached rows, the edit cell, the anchor and the scroll position are shifted by that amount (scrolled to the top or to
  // the end of the list the view follows the new rows instead). The cache is only marked stale and refilled in place.
  async function refresh() {
    // with anything selected or being edited the view is frozen: it never follows the new rows, at the top or at the end
    const frozen = L.sel.size > 0 || L.selId > 0;
    const atEnd = !frozen && !newestFirst() && sc.scrollTop + sc.clientHeight >= sc.scrollHeight - 4, atTop = !frozen && newestFirst() && sc.scrollTop < 4;
    const old = L.total, ord = $("n-order").value, qs = params().toString();
    let j;
    try { j = await fetchRange(0, 1); } catch (e) { return; }
    if (ord !== $("n-order").value || qs !== params().toString()) return; // the query changed meanwhile: reload() handles it
    L.total = j.total; L.all = j.all; L.lt = j.total; L.la = j.all;
    const d = newestFirst() ? L.total - old : 0;
    L.gen++; L.chunks.clear();
    if (d) {
      const moved = new Map();
      for (const [i, r] of L.cache) if (i + d >= 0) moved.set(i + d, r);
      L.cache = moved;
      if (L.anchor >= 0) L.anchor += d;
      if (!atTop) sc.scrollTop += d * ROW;
    }
    if (atEnd) sc.scrollTop = L.total * ROW + HDR;
    render();
    if (L.selId && dlg.open) reloadRecord(L.selId);
  }
  // re-read the current record; the text pane is rebuilt only when it really changed
  async function reloadRecord(id) {
    let rec;
    try { rec = await api("GET", "/api/messages/" + id); } catch (e) { return; }
    if (L.selId !== id) return;
    if (L.rec && L.rec.id === id && JSON.stringify(L.rec) === JSON.stringify(rec)) return;
    L.rec = rec; showRecord();
  }

  // ---- go to a date: binary search on the (time ordered) list ----
  async function atIndex(i) {
    if (L.cache.has(i)) return L.cache.get(i);
    const j = await fetchRange(i, 1);
    return j.records[0];
  }
  async function gotoDate() {
    const v = $("n-date").value.trim();
    if (!isoOk(v) || !L.total) return;
    const t0 = Date.parse(v + "T00:00:00Z"), t1 = t0 + 86400000;
    let lo = 0, hi = L.total;
    try {
      while (lo < hi) {
        const mid = (lo + hi) >> 1, r = await atIndex(mid);
        const hit = newestFirst() ? r.rx < t1 : r.rx >= t0;
        if (hit) hi = mid; else lo = mid + 1;
      }
    } catch (e) { return; }
    const i = Math.min(lo, L.total - 1);
    L.gotoAt = performance.now(); // the scroll that follows is ours: it must not clear the date
    sc.scrollTop = i * ROW;
    render();
    selectIndex(i);
    if (document.activeElement && document.activeElement !== sc) document.activeElement.blur();
    sc.focus({ preventScroll: true }); // the arrow keys now move through the records
  }

  // ---- selection and record text ----
  // show one record (the "current" one) in the text pane; `keep` leaves the selection set alone
  async function select(id, keep) {
    L.selId = id;
    if (!keep) L.sel = id ? new Set([id]) : new Set();
    render();
    if (!id) { $("rec-text").textContent = ""; $("rev-sug").hidden = true; $("rec-info").replaceChildren(); $("rec-title").textContent = ""; L.rec = null; return; }
    let rec;
    try { rec = await api("GET", "/api/messages/" + id); } catch (e) { $("rec-text").textContent = e.message; return; }
    if (L.selId !== id) return; // the user has moved on
    L.rec = rec;
    showRecord();
  }
  async function selectIndex(i) {
    i = Math.max(0, Math.min(L.total - 1, i));
    if (i < 0) return;
    L.anchor = i;
    scrollToIndex(i);
    const r = L.cache.get(i) || await atIndex(i);
    if (r) select(r.id);
  }

  // The record's lines for display: the effective text (raw with the operator's <revisions>), with the autocorrected words
  // in [brackets] (hover: what was received). Revised stretches get their own background (hover: what was received).
  // Every line is a span.ln with the quality prefix in span.pfx and the text in span.tx, so a selection can be traced
  // back to a line.
  NX.fillRecordText = (pre, r, opt) => {
    const cor = opt.cor && r.cor ? r.cor.split("\n") : null;
    pre.replaceChildren();
    r.lines.forEach((l, i) => {
      if (i) pre.append("\n");
      const base = r.eff ? r.eff[i] : l.x;
      const c = cor && cor[i] !== undefined ? cor[i] : base;
      const revs = (r.rev || []).filter((v) => v.l === i).sort((a, b) => a.s - b.s);
      const ln = el("span", { class: "ln", "data-i": i }), tx = el("span", { class: "tx" });
      ln.append(el("span", { class: "pfx", text: NX.lineText({ t: Date.parse(l.t), q: l.q, x: "" }) }), tx);
      const baseTok = base.split(/(\s+)/);
      let into = tx, rv = 0, open = null;
      c.split(/(\s+)/).forEach((tok, k) => {
        let node = tok;
        if (c !== base && tok.includes("[") && baseTok[k] !== undefined) node = el("span", { class: "fix", title: "received: " + baseTok[k], text: tok });
        if (!open && tok.includes("<") && revs[rv]) { open = el("span", { class: "revd", title: "replaced by the operator; received: " + revs[rv].o }); tx.append(open); into = open; }
        into.append(node);
        if (open && tok.includes(">")) { open = null; into = tx; rv++; }
      });
      pre.append(ln);
    });
  };

  function showText() {
    const r = L.rec;
    NX.fillRecordText($("rec-text"), r, { cor: $("rec-cor").checked });
    // stretches that look as if they were decoded in the wrong shift
    const box = $("rev-sug");
    box.replaceChildren();
    const sug = [];
    (r.rv || []).forEach((list, i) => (list || []).forEach((s) => sug.push({ s, i })));
    box.hidden = !sug.length;
    if (sug.length) {
      box.append("Possibly decoded in the wrong shift: ");
      for (const { s, i } of sug.slice(0, 8)) {
        const b = el("button", { type: "button", class: "linkbtn", title: "Put it into the shift reverser", text: `${r.lines[i].x.slice(s.s, s.e)} \u2192 ${s.t}` });
        b.onclick = () => { setRevSource({ l: i, s: s.s, e: s.e }); $("rev-in").value = r.lines[i].x.slice(s.s, s.e); $("rev-go").click(); };
        box.append(b, " ");
      }
    }
    // the operator's revisions, with a way back
    const rl = $("rev-list");
    rl.replaceChildren();
    rl.hidden = !(r.rev && r.rev.length);
    if (!rl.hidden) {
      rl.append("Revised by the operator: ");
      r.rev.forEach((v, n) => {
        const b = el("button", { type: "button", class: "linkbtn", disabled: !admin(), title: "Put the received text back", text: `<${v.n}> (received ${v.o})  \u21b6 undo` });
        b.onclick = () => patch({ rev: { op: "del", i: n } });
        rl.append(b, " ");
      });
      if (r.rev.length > 1) { const b = el("button", { type: "button", class: "linkbtn", disabled: !admin(), text: "undo all" }); b.onclick = () => patch({ rev: { op: "clear" } }); rl.append(b); }
    }
  }
  $("rec-cor").checked = (() => { try { return localStorage.getItem("nx-cor") !== "0"; } catch { return true; } })();
  $("rec-cor").onchange = () => { try { localStorage.setItem("nx-cor", $("rec-cor").checked ? "1" : "0"); } catch {} if (L.rec) showText(); };

  function showRecord() {
    const r = L.rec;
    updateNav();
    showText();
    const dl = el("dl", { class: "dlginfo" });
    const add = (k, v) => { dl.append(el("dt", { text: k }), el("dd", {}, v)); };
    add("Received", NX.stamp(Date.parse(r.rx)) + " UTC");
    add("Frequency", r.ch + " kHz");
    add("Header", (r.b1 || "?") + (r.b2 || "?") + (r.ser || "??"));
    add("Quality", r.q.toFixed(1));
    add("Station", r.st + (r.im ? `  (${r.im}, score ${r.is.toFixed(1)})` : ""));
    if (r.km) add("Distance", `${r.km.toFixed(0)} km, bearing ${r.brg.toFixed(0)}\u00b0`);
    // B1 and NAVAREA can be set by the operator here (and only here)
    const fieldIn = (f, val, len, ok, title) => {
      const inp = el("input", { type: "text", class: "mini", maxlength: String(len), value: val, disabled: !admin(), title });
      inp.onkeydown = (e) => { if (e.key === "Enter") inp.blur(); e.stopPropagation(); };
      inp.onchange = () => {
        const v = inp.value.trim();
        if (!ok(v)) { inp.classList.add("bad"); return; }
        if (v === val.trim()) return;
        patch({ [f]: f === "b1" ? v.toUpperCase() : Number(v) });
      };
      return inp;
    };
    add("B1 / NAVAREA", el("span", { class: "nowrap" }, fieldIn("b1", r.b1 || "", 1, (v) => /^[A-Za-z?]$/.test(v), "Station ID letter (B1) set by the operator"), " / ", fieldIn("area", String(r.area || ""), 2, (v) => /^\d{1,2}$/.test(v), "NAVAREA number set by the operator")));
    if (r.man) add("Set by", "operator (B1 / NAVAREA)");
    if (r.rev && r.rev.length) add("Revised", `${r.rev.length} stretch${r.rev.length > 1 ? "es" : ""} replaced by the operator (shown in <angle brackets>)`);
    const cm = el("input", { type: "text", value: r.cmt || "", maxlength: "120", disabled: !admin(), placeholder: "comment" });
    cm.onchange = () => patch({ cmt: cm.value });
    cm.onkeydown = (e) => { if (e.key === "Enter") cm.blur(); e.stopPropagation(); };
    add("Comment", cm);
    const box = $("rec-info");
    box.replaceChildren(dl);
    if (r.cand && r.cand.length) {
      const cands = el("div", { class: "cands" }, el("span", { class: "small", text: "Candidate stations:" }));
      box.append(cands);
      for (const c of r.cand) {
        const b = el("button", { text: "Use", disabled: !admin(), title: "Identify the message as this station (sets B1 and NAVAREA to its own)" });
        b.onclick = () => useCandidate(c.id);
        cands.append(el("span", { class: "cand" }, `${c.label}  (${c.score.toFixed(1)}) `, b));
      }
    }
  }

  // The server sets B1 and NAVAREA from the station and remembers the choice (several stations can share all three)
  function useCandidate(sid) { patch({ station: sid }); }

  // a record as returned by PATCH (the full record) in the shape of a list row
  const rowOf = (r, old) => ({ ...old, id: r.id, v: r.v, ch: r.ch, rx: Date.parse(r.rx) || old.rx, kind: r.kind, b1: r.b1, b2: r.b2, ser: r.ser, area: r.area, sid: r.sid, st: r.st, q: r.q, km: r.km || 0, brg: r.brg || 0, cmt: r.cmt, man: r.man, im: r.im });
  async function patch(fields) {
    if (!L.rec) return;
    try {
      const saved = await api("PATCH", "/api/messages/" + L.rec.id, { v: L.rec.v, ...fields });
      const i = idxOf(saved.id);
      if (i >= 0) L.cache.set(i, rowOf(saved, L.cache.get(i)));
    } catch (e) { alert(e.message); }
    select(L.selId, true);
  }

  // ---- the record pop-up: opened by double-click or Enter, the arrows step through the (filtered) list as shown ----
  const dlg = $("log-dlg");
  const curIndex = () => { const i = idxOf(L.selId); return i >= 0 ? i : L.anchor; };
  function updateNav() {
    const i = curIndex(), n = L.total;
    const pos = i >= 0 && n ? ` (${i + 1} of ${n}${filterOn() ? ", filtered" : ""})` : "";
    $("rec-title").textContent = L.rec ? `#${L.rec.id}  ${L.rec.kind}${pos}` : "";
    $("ld-prev").disabled = i <= 0;
    $("ld-next").disabled = i < 0 || i + 1 >= n;
    $("ld-report").hidden = false;
    $("ld-delete").disabled = !admin();
  }
  async function openDialog() {
    if (!L.selId) return;
    if (!dlg.open) dlg.showModal();
    updateNav();
    if (!L.rec || L.rec.id !== L.selId) select(L.selId, true); // the pane may still show an older record
  }
  let stepping = false;
  async function step(d) {
    if (stepping) return;
    const i = curIndex();
    if (i < 0) return;
    const to = i + d;
    if (to < 0 || to >= L.total) return;
    stepping = true;
    try { await selectIndex(to); } finally { stepping = false; }
  }
  $("ld-prev").onclick = () => step(-1);
  $("ld-next").onclick = () => step(1);
  $("ld-close").onclick = () => dlg.close();
  dlg.addEventListener("close", () => { sc.focus({ preventScroll: true }); render(); });
  dlg.addEventListener("keydown", (e) => {
    if (e.ctrlKey || e.altKey || e.metaKey || /INPUT|SELECT|TEXTAREA/.test(e.target.tagName)) return;
    if (e.key === "ArrowLeft") { e.preventDefault(); step(-1); }
    else if (e.key === "ArrowRight") { e.preventDefault(); step(1); }
  });
  dlg.addEventListener("mousedown", (e) => { if (e.target === dlg) dlg.close(); }); // a click on the backdrop
  $("n-open").onclick = openDialog;

  const tbody = document.querySelector("#log-table tbody");
  // ---- clicking rows: select, ctrl-click for several, shift-click for a range ----
  tbody.addEventListener("click", (e) => {
    const tr = e.target.closest("tr[data-id]");
    if (!tr) return;
    const id = Number(tr.dataset.id), i = Number(tr.dataset.i);
    sc.focus({ preventScroll: true });
    // a double-click opens the record. (The rows are rebuilt on the first click, so the browser's dblclick event is not used.)
    if (e.detail === 2 && !e.shiftKey && !e.ctrlKey && !e.metaKey) {
      window.getSelection().removeAllRanges();
      L.anchor = i; L.sel = new Set([id]); L.selId = id;
      openDialog();
      return;
    }
    if (e.ctrlKey || e.metaKey) {
      if (L.sel.has(id)) L.sel.delete(id); else L.sel.add(id);
      L.anchor = i; L.selId = L.sel.has(id) ? id : [...L.sel].pop() || 0;
      render(); if (L.selId) select(L.selId, true); else select(0);
      return;
    }
    if (e.shiftKey && L.anchor >= 0) {
      const a = Math.min(L.anchor, i), b = Math.max(L.anchor, i);
      L.sel = new Set();
      for (let k = a; k <= b; k++) { const r = L.cache.get(k); if (r) L.sel.add(r.id); }
      L.selId = id; render(); select(id, true);
      return;
    }
    L.anchor = i; select(id);
  });

  // ---- keyboard: one line, a page, the ends ----
  document.addEventListener("keydown", (e) => {
    if (NX.state.tab !== "log" || dlg.open || /INPUT|SELECT|TEXTAREA|BUTTON/.test(document.activeElement.tagName) || e.ctrlKey || e.altKey || e.metaKey) return;
    if (e.key === "Enter") { if (L.selId) { e.preventDefault(); openDialog(); } return; }
    const cur = idxOf(L.selId), page = Math.max(1, visRows() - 1);
    const to = { ArrowDown: cur + 1, ArrowUp: cur - 1, PageDown: cur + page, PageUp: cur - page, Home: 0, End: L.total - 1 }[e.key];
    if (to === undefined) return;
    e.preventDefault();
    if (cur < 0) { selectIndex(Math.floor(sc.scrollTop / ROW) + 1); return; }
    selectIndex(to);
  });

  // ---- navigation and filters ----
  $("n-order").onchange = () => reload();
  $("n-latest").onclick = () => { $("n-date").value = ""; jumpLatest(); if (L.total) selectIndex(newestFirst() ? 0 : L.total - 1); };
  $("n-date").onchange = gotoDate; // picking or typing a date goes there at once
  $("n-date").onkeydown = (e) => { if (e.key === "Enter") gotoDate(); };
  // the filter is edited in a pop-up: Apply (or Enter) uses the settings, Cancel (or Esc) puts the old ones back, Reset empties the boxes
  const fdlg = $("f-dlg");
  const snap = () => ({ v: ids.map((k) => $("f-" + k).value), u: $("f-unid").checked });
  let before = snap();
  const restore = (s) => { ids.forEach((k, i) => { $("f-" + k).value = s.v[i]; }); $("f-unid").checked = s.u; };
  const applyDlg = () => { if (!$("f-on").checked && summaryText(true)) $("f-on").checked = true; fdlg.close(); reload(); indicate(); };
  $("f-edit").onclick = () => { before = snap(); fdlg.showModal(); };
  fdlg.addEventListener("cancel", (e) => { e.preventDefault(); $("f-cancel").click(); });
  $("f-cancel").onclick = () => { restore(before); fdlg.close(); indicate(); };
  $("f-apply").onclick = applyDlg;
  $("f-on").onchange = () => { reload().then(indicate); };
  $("f-clear").onclick = () => { for (const k of ids) $("f-" + k).value = ""; $("f-unid").checked = false; indicate(); };
  // what the filter boxes say, in one line (also used when the filter is switched off)
  function summaryText() {
    const v = (k) => $("f-" + k).value.trim(), parts = [];
    if (v("freq")) parts.push("Frequency " + v("freq"));
    if (v("rcv")) { const c = NX.state.chans[Number(v("rcv")) - 1], n = c && c.controls && (c.controls.label || c.controls.name); parts.push(n && n !== "Channel " + v("rcv") ? "Channel " + v("rcv") + " (" + n + ")" : "Channel " + v("rcv")); }
    if (v("area")) parts.push("NAVAREA " + v("area"));
    if (v("b1")) parts.push("ID letter " + v("b1"));
    if (v("kind")) parts.push("Kind " + v("kind"));
    if (v("from") || v("to")) parts.push("Dates " + (v("from") || "\u2026") + " to " + (v("to") || "\u2026"));
    if (v("kmin") || v("kmax")) parts.push("Distance " + (v("kmin") || "0") + "\u2013" + (v("kmax") || "\u221e") + " km");
    if ($("f-unid").checked) parts.push("UNID only");
    if (v("text")) parts.push("Body text \u201c" + v("text") + "\u201d");
    return parts.join(" \u00b7 ");
  }
  for (const k of [...ids, "unid"]) {
    const f = $("f-" + k);
    f.addEventListener("keydown", (e) => { if (e.key === "Enter") applyDlg(); });
    f.addEventListener("input", indicate);
    f.addEventListener("change", indicate);
  }
  // the filter indicator: what is applied, how much it hides, and what arrived since that the filter hides; the Log tab gets a dot
  function indicate() {
    const on = filterOn();
    const hid = Math.max(0, (L.la - L.lt) - L.hid0);
    $("f-ind").classList.toggle("on", on);
    $("f-stat").textContent = on ? ` \u00b7 showing ${L.lt} of ${L.la}` + (hid ? ` \u00b7 +${hid} new hidden` : "") : "";
    const sum = summaryText();
    $("f-sum").textContent = !sum ? "No filters selected; all records are shown." : $("f-on").checked ? sum : ""; // switched off: do not show settings that are not in use
    $("f-sum").title = sum;
    $("filters").classList.toggle("active", on);
    $("tab-log").classList.toggle("filtering", on);
    const tab = document.querySelector("nav button[data-tab=log]");
    if (tab) { tab.classList.toggle("filtered", on); tab.title = on ? `A filter is applied in the Log tab (${L.lt} of ${L.la} records shown${hid ? `, ${hid} new hidden` : ""})` : ""; }
  }
  // delete records; from the pop-up the next record in the list is shown afterwards (or the pop-up closes)
  async function deleteSelected() {
    if (!admin() || !L.sel.size) return;
    const list = [...L.sel], at = curIndex();
    if (!confirm(list.length === 1 && L.rec ? `Delete record #${L.rec.id} (${NX.stamp(Date.parse(L.rec.rx))}, ${L.rec.st})?` : `Delete ${list.length} records?`)) return;
    for (const id of list) { try { await api("DELETE", "/api/messages/" + id); } catch (e) { alert(e.message); break; } }
    L.sel = new Set(); L.selId = 0; L.rec = null;
    const top = sc.scrollTop;
    L.gen++; L.cache.clear(); L.chunks.clear();
    try { const j = await fetchRange(0, 1); L.total = j.total; L.all = j.all; L.lt = j.total; L.la = j.all; } catch (e) { return; }
    sc.scrollTop = top; select(0);
    if (dlg.open) { if (L.total && at >= 0) selectIndex(Math.min(at, L.total - 1)); else dlg.close(); }
  }
  $("n-delete").onclick = deleteSelected;
  $("ld-delete").onclick = deleteSelected;
  $("ld-report").onclick = () => { if (L.selId) addToReport([L.selId]); };

  // ---- the report list ----
  let reportNote = 0;
  function addToReport(list) {
    if (!list.length) return;
    const n = NX.replist.add(list);
    const msg = n === list.length ? `${n} added to your report` : n ? `${n} added to your report (the rest were already there)` : "already in your report";
    $("n-selinfo").textContent = msg;
    if (dlg.open) $("rec-title").textContent += "  \u2014 " + (n ? "added to your report" : "already in your report");
    clearTimeout(reportNote); reportNote = setTimeout(render, 2500);
  }
  $("n-report").onclick = () => addToReport([...L.sel]);

  // ---- shift reverser: reverse a stretch of the message and, if it reads right, put it into the message ----
  // revSrc says where the text in the reverser came from: {l, s, e} (a suggestion) or {l, find, occ} (a selection)
  let revSrc = null;
  function setRevSource(src) { revSrc = src; updateApply(); }
  function updateApply() {
    const ok = admin() && L.rec && revSrc && $("rev-out").value.trim() !== "";
    $("rev-apply").disabled = !ok;
    $("rev-apply").title = ok ? "Replace the received text in the message by this text. The received text is kept (undo is possible)." : "Select text in the message (then \"Use selection\") or click a suggestion above, press Reverse, correct the result if needed, then use this.";
  }
  $("rev-in").addEventListener("input", () => setRevSource(null));
  $("rev-out").addEventListener("input", updateApply);
  $("rev-sel").onclick = () => {
    const sel = window.getSelection(), t = String(sel);
    if (!t) return;
    const node = (n) => (n && n.nodeType === 3 ? n.parentElement : n);
    const a = node(sel.anchorNode) && node(sel.anchorNode).closest(".ln"), b = node(sel.focusNode) && node(sel.focusNode).closest(".ln");
    $("rev-in").value = t;
    revSrc = null;
    if (a && a === b && $("rec-text").contains(a) && !t.includes("\n")) {
      // which occurrence of the text in its line: count the ones before the selection
      const tx = a.querySelector(".tx"), r0 = sel.getRangeAt(0), pre = document.createRange();
      pre.setStart(tx, 0); pre.setEnd(r0.startContainer, r0.startOffset);
      const before = pre.toString();
      let occ = 0;
      for (let i = before.indexOf(t); i >= 0 && i + t.length <= before.length; i = before.indexOf(t, i + 1)) occ++;
      revSrc = { l: Number(a.dataset.i), find: t, occ };
    }
    updateApply();
  };
  $("rev-go").onclick = async () => {
    try { $("rev-out").value = (await api("POST", "/api/reverse", { text: $("rev-in").value })).text; } catch (e) { $("rev-out").value = e.message; }
    updateApply();
  };
  $("rev-apply").onclick = async () => {
    if (!revSrc || !L.rec) return;
    const op = { op: "add", l: revSrc.l, t: $("rev-out").value.trim() };
    if (revSrc.find !== undefined) { op.find = revSrc.find; op.occ = revSrc.occ; } else { op.s = revSrc.s; op.e = revSrc.e; }
    await patch({ rev: op });
    revSrc = null; $("rev-in").value = ""; $("rev-out").value = ""; updateApply();
  };

  // ---- live updates ----
  NX.listen("record", () => {
    clearTimeout(L.timer);
    if (NX.state.tab === "log") { L.timer = setTimeout(refresh, 400); return; }
    if (L.loaded && filterOn()) L.timer = setTimeout(liveCounts, 1000); // keep the "new hidden" count and the tab dot current
  });
  async function liveCounts() {
    try { const j = await fetchRange(0, 1); L.lt = j.total; L.la = j.all; indicate(); } catch (e) { /* ignore */ }
  }
  NX.listen("role", () => { if (L.loaded) { render(); if (L.rec) showRecord(); } updateApply(); });
  NX.tabs.log = () => { if (!L.loaded) { L.loaded = true; reload(); } else refresh(); };
})();
