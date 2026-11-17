// Tiled layouts: on a wide screen several tabs are shown at once. The page is a few columns, each a stack of tiles, and a tile holds
// one tab or a stack of tabs (a tab strip at its top). The tabs are the same sections as always; this file only moves them into tiles
// and back, so every id stays unique and nothing is copied. A layout is the preset you picked or "my layout" (a preset you changed);
// both are kept in this browser. Below MINW pixels the page is always the plain tabs.
(() => {
  "use strict";
  const NX = window.NX, state = NX.state, $ = (id) => document.getElementById(id);
  const KEY = "nx-layout", KEYM = "nx-layout-mine", MINW = 1400, MINC = 240, MINR = 120, MAXC = 6, MAXR = 6;
  const PRESETS = {
    wide3: { name: "Wide, 3 columns", cols: [
      { w: 1, rows: [{ tab: "status", h: 1 }, { tab: "schedule", h: 1 }] },
      { w: 1.7, rows: [{ tab: "spectrum", h: 1 }, { tab: "log", h: 2 }] },
      { w: 1, rows: [{ tab: "report", h: 1 }, { tab: "options", h: 1 }] }] },
    instr3: { name: "Instruments, 3 columns", cols: [
      { w: 1, rows: [{ tab: "header", h: 1.1 }, { tab: "status", h: 1 }, { tab: "schedule", h: 1 }] },
      { w: 1.7, rows: [{ tab: "spectrum", h: 1 }, { tab: "log", h: 2 }] },
      { w: 1, rows: [{ tab: "report", h: 1 }, { tab: "options", h: 1 }] }] },
    stack3: { name: "Stacks, 3 columns", cols: [
      { w: 1, rows: [{ tab: "header", h: 1.1 }, { tabs: ["status", "schedule", "database"], h: 2 }] },
      { w: 1.7, rows: [{ tab: "spectrum", h: 1 }, { tab: "log", h: 2 }] },
      { w: 1, rows: [{ tabs: ["report", "options", "syslog"], h: 1 }] }] },
    monlog: { name: "Monitor + Log", cols: [
      { w: 1, rows: [{ tab: "status", h: 1 }, { tab: "spectrum", h: 1 }] },
      { w: 1.6, rows: [{ tab: "log", h: 1 }] }] },
    logrep: { name: "Log + Report", cols: [
      { w: 1.6, rows: [{ tab: "log", h: 1 }] },
      { w: 1, rows: [{ tab: "report", h: 1 }] }] },
    grid4: { name: "2 × 2", cols: [
      { w: 1, rows: [{ tab: "status", h: 1 }, { tab: "log", h: 1 }] },
      { w: 1, rows: [{ tab: "spectrum", h: 1 }, { tab: "report", h: 1 }] }] },
  };
  const clone = (o) => JSON.parse(JSON.stringify(o));
  const mk = (tag, cls, attrs) => { const e = document.createElement(tag); if (cls) e.className = cls; if (attrs) Object.assign(e, attrs); return e; };
  const num = (x) => typeof x === "number" && isFinite(x) && x > 0;
  const rowOk = (r) => num(r.h) && ((Array.isArray(r.tabs) && r.tabs.length <= 16 && r.tabs.every((t) => typeof t === "string")) || typeof r.tab === "string");
  const valid = (cols) => Array.isArray(cols) && cols.length > 0 && cols.length <= MAXC && cols.every((c) => num(c.w) && Array.isArray(c.rows) && c.rows.length > 0 && c.rows.length <= MAXR && c.rows.every(rowOk));
  // every row becomes {tabs, act, h}; a tab is in one row at most
  function norm(cols) {
    const seen = new Set();
    return cols.map((c) => ({ w: c.w, rows: c.rows.map((r) => {
      const tabs = (Array.isArray(r.tabs) ? r.tabs : [r.tab]).filter((t) => typeof t === "string" && !seen.has(t) && seen.add(t));
      return { tabs, act: tabs.includes(r.act) ? r.act : tabs[0] || null, h: r.h };
    }) }));
  }
  const preset = (k) => norm(clone(PRESETS[k].cols));

  function load() {
    try {
      const j = JSON.parse(localStorage.getItem(KEY));
      if (j && PRESETS[j.name]) return { name: j.name, cols: valid(j.cols) ? norm(j.cols) : preset(j.name) };
      if (j && j.name === "mine" && valid(j.cols)) return { name: "mine", cols: norm(j.cols) };
    } catch (e) { /* none or damaged: plain tabs */ }
    return { name: "tabs" };
  }
  const S = load();
  function mine() { try { const j = JSON.parse(localStorage.getItem(KEYM)); return j && valid(j.cols) ? norm(j.cols) : null; } catch (e) { return null; } }
  function save() {
    try {
      localStorage.setItem(KEY, JSON.stringify(S.name === "tabs" ? { name: "tabs" } : { name: S.name, cols: S.cols }));
      if (S.name === "mine") localStorage.setItem(KEYM, JSON.stringify({ cols: S.cols }));
    } catch (e) { /* private mode */ }
  }

  const secs = {}; // the sections of all tabs, by tab name
  document.querySelectorAll("main > section[id^=tab-]").forEach((s) => { secs[s.id.slice(4)] = s; });
  const mainEl = document.querySelector("main");
  let on = false, root = null, tiles = [], colEls = [], focusT = null, ro = null, rafOn = false, shownPrev = new Set();
  const avail = () => [...document.querySelectorAll("#tabs button[data-tab]")].filter((b) => !b.hidden).map((b) => ({ tab: b.dataset.tab, label: b.textContent }));
  const labelOf = (k) => { const b = document.querySelector(`#tabs button[data-tab="${k}"]`); return b ? b.textContent : k; };

  // a resize of any tile (also while a gutter is dragged) is announced like a window resize: the spectrum, the log list and the
  // other canvases already redraw on that
  function notify() { if (rafOn) return; rafOn = true; requestAnimationFrame(() => { rafOn = false; window.dispatchEvent(new Event("resize")); }); }

  // ---- a small menu (same look as the Log's right-click menu) ----
  const pop = mk("div", "ctx"); pop.setAttribute("role", "menu"); pop.hidden = true; document.body.append(pop);
  let popAnchor = null, closedFor = null, closedAt = 0;
  const popClose = () => { if (!pop.hidden) { pop.hidden = true; pop.replaceChildren(); } };
  function popOpen(anchor, rows) {
    if (closedFor === anchor && performance.now() - closedAt < 400) { closedFor = null; return; } // the click that closed it
    popAnchor = anchor;
    pop.replaceChildren(...rows.map((r) => {
      if (r.sep) return mk("hr");
      const b = mk("button"); b.type = "button"; b.setAttribute("role", "menuitem"); b.disabled = !!r.off; b.title = r.tip || "";
      const a = mk("span"); a.textContent = r.text; const k = mk("span", "k"); k.textContent = r.key || ""; b.append(a, k);
      b.onclick = () => { popClose(); r.fn(); };
      return b;
    }));
    pop.hidden = false;
    const r = anchor.getBoundingClientRect(), w = pop.offsetWidth, h = pop.offsetHeight;
    pop.style.left = Math.max(4, Math.min(r.left, window.innerWidth - w - 4)) + "px";
    pop.style.top = Math.max(4, Math.min(r.bottom + 2, window.innerHeight - h - 4)) + "px";
    const f = pop.querySelector("button:not(:disabled)"); if (f) f.focus({ preventScroll: true });
  }
  document.addEventListener("pointerdown", (e) => { if (!pop.hidden && !pop.contains(e.target)) { closedFor = popAnchor; closedAt = performance.now(); popClose(); } }, true);
  document.addEventListener("keydown", (e) => { if (!pop.hidden && e.key === "Escape") { e.preventDefault(); e.stopPropagation(); popClose(); } }, true);
  window.addEventListener("blur", popClose);

  // ---- the model <-> the page ----
  // what each tile really shows: its tabs that are offered right now and not already in an earlier tile (a tab the tile lists but that
  // is not offered, such as Ephemeris without a position, is kept in the layout and shows up when it is offered again)
  function resolve() {
    const av = new Set(avail().map((a) => a.tab)), used = new Set();
    for (const t of tiles) {
      t.tabs = t.r.tabs.filter((x) => av.has(x) && !used.has(x)); t.tabs.forEach((x) => used.add(x));
      t.act = t.tabs.includes(t.r.act) ? t.r.act : t.tabs[0] || null;
    }
  }

  // the top bar is the Instruments tab: when a tile shows it, the bar leaves the top of the page and sits in that tile
  const hdrBody = $("hdrbody"), hdrHome = $("hdr"), instrBtn = document.querySelector('#tabs button[data-tab="header"]');
  function syncHeader() {
    const inTile = on && tiles.some((t) => t.act === "header");
    const to = inTile ? secs.header : hdrHome;
    if (hdrBody.parentNode !== to) to.append(hdrBody);
    hdrHome.hidden = inTile;
  }

  function updateNav() {
    document.querySelectorAll("#tabs button[data-tab]").forEach((b) => {
      b.classList.toggle("vis", on && tiles.some((t) => t.act === b.dataset.tab));
      b.classList.toggle("on", b.dataset.tab === state.tab);
    });
  }

  function focusTile(t) {
    if (!t) return;
    focusT = t;
    tiles.forEach((x) => x.el.classList.toggle("focus", x === t));
    if (t.act) state.tab = t.act; // the keyboard shortcuts of the Log and the Report act on the tab of the tile you last used
    updateNav();
  }

  function renderHead(t) {
    t.strip.replaceChildren(...t.tabs.map((k) => {
      const it = mk("div", "tabitem" + (k === t.act ? " on" : "")), b = mk("button", "tabbtn"), x = mk("button", "tabx");
      b.type = "button"; b.textContent = labelOf(k); b.title = "Show " + labelOf(k) + " in this tile";
      b.onclick = () => pickTab(t, k);
      x.type = "button"; x.textContent = "×"; x.title = "Take " + labelOf(k) + " out of this tile (the + button or the tab bar brings it back)"; x.setAttribute("aria-label", "Remove " + labelOf(k));
      x.onclick = (e) => { e.stopPropagation(); removeTab(t, k); };
      it.append(b, x);
      return it;
    }));
    const on_ = t.strip.querySelector(".tabitem.on"); if (on_) on_.scrollIntoView({ block: "nearest", inline: "nearest" });
    t.empty.hidden = !!t.act;
  }

  // make the page match the model: each tile shows its active tab; the other tabs wait, hidden, in <main>
  function sync() {
    resolve();
    const want = new Set(tiles.map((t) => t.act).filter(Boolean));
    for (const k in secs) if (!want.has(k)) { secs[k].hidden = true; if (secs[k].parentNode !== mainEl) mainEl.append(secs[k]); }
    for (const t of tiles) if (t.act) { const s = secs[t.act]; s.hidden = false; if (s.parentNode !== t.body) t.body.insertBefore(s, t.empty); }
    tiles.forEach(renderHead);
    syncHeader(); updateNav();
    if (focusT && focusT.act) state.tab = focusT.act;
    const fresh = [...want].filter((k) => !shownPrev.has(k));
    shownPrev = want;
    fresh.forEach((k) => NX.activate(k));
    notify();
  }

  // ---- editing: a preset that is changed becomes "my layout" (the presets themselves stay as they are) ----
  function touch() { if (S.name !== "mine") { S.name = "mine"; menuSync(); } save(); }
  const hasVisible = (r) => { const av = new Set(avail().map((a) => a.tab)); return r.tabs.some((x) => av.has(x)); };

  function pickTab(t, k) { t.r.act = k; save(); focusTile(t); sync(); }

  function addTab(t, k) { // a tab is in one tile only: it moves here from the tile that had it
    const from = tiles.find((x) => x !== t && x.r.tabs.includes(k));
    S.cols.forEach((c) => c.rows.forEach((r) => { if (r !== t.r) r.tabs = r.tabs.filter((x) => x !== k); }));
    if (!t.r.tabs.includes(k)) t.r.tabs.push(k);
    t.r.act = k;
    touch();
    if (from && !hasVisible(from.r) && tiles.length > 1) { dropRow(from); build(t.r); } else { focusTile(t); sync(); }
  }

  function removeTab(t, k) {
    t.r.tabs = t.r.tabs.filter((x) => x !== k);
    if (t.r.act === k) t.r.act = null;
    touch();
    if (!hasVisible(t.r) && tiles.length > 1) { dropRow(t); build(); } else sync();
  }

  function dropRow(t) { const c = S.cols[t.ci]; c.rows.splice(t.ri, 1); if (!c.rows.length) S.cols.splice(t.ci, 1); }

  function addRowBelow(t) {
    const c = S.cols[t.ci]; if (c.rows.length >= MAXR) return;
    const h = t.r.h / 2, r = { tabs: [], act: null, h }; t.r.h = h; c.rows.splice(t.ri + 1, 0, r);
    touch(); build(r);
  }
  function addColRight(t) {
    if (S.cols.length >= MAXC) return;
    const c = S.cols[t.ci], w = c.w / 2, r = { tabs: [], act: null, h: 1 }; c.w = w; S.cols.splice(t.ci + 1, 0, { w, rows: [r] });
    touch(); build(r);
  }
  function closeTile(t) { if (tiles.length < 2) return; dropRow(t); touch(); build(); }
  function equalize() { S.cols.forEach((c) => { c.w = 1; c.rows.forEach((r) => { r.h = 1; }); }); touch(); colEls.forEach((e) => { e.style.flexGrow = 1; }); tiles.forEach((t) => { t.el.style.flexGrow = 1; }); }

  function plusMenu(t) {
    const rows = avail().filter((a) => !t.tabs.includes(a.tab)).map((a) => {
      const elsewhere = tiles.some((x) => x !== t && x.tabs.includes(a.tab));
      return { text: a.label, key: elsewhere ? "from another tile" : "", fn: () => addTab(t, a.tab) };
    });
    if (!rows.length) rows.push({ text: "Every tab is in this tile", off: true });
    return rows;
  }
  function tileMenu(t) {
    return [
      { text: "Add a tile below", off: S.cols[t.ci].rows.length >= MAXR, fn: () => addRowBelow(t) },
      { text: "Add a column to the right", off: S.cols.length >= MAXC, fn: () => addColRight(t) },
      { text: "Close this tile", off: tiles.length < 2, tip: "Its tabs wait; the + button or the tab bar brings them back", fn: () => closeTile(t) },
      { sep: true },
      { text: "Equal sizes", fn: equalize },
    ];
  }

  // ---- gutters: drag to give one neighbour more room (the weights are kept as ratios, so a resized window keeps the proportions) ----
  function gutter(kind, ci, ri) {
    const g = mk("div", "gut " + kind);
    g.title = "Drag to resize. Double-click: share the space of the two neighbours equally.";
    g.setAttribute("role", "separator");
    const pair = () => {
      const v = kind === "v";
      return v ? [{ el: colEls[ci - 1], o: S.cols[ci - 1], k: "w" }, { el: colEls[ci], o: S.cols[ci], k: "w" }]
        : [{ el: tiles.find((t) => t.ci === ci && t.ri === ri - 1).el, o: S.cols[ci].rows[ri - 1], k: "h" }, { el: tiles.find((t) => t.ci === ci && t.ri === ri).el, o: S.cols[ci].rows[ri], k: "h" }];
    };
    g.onpointerdown = (e) => {
      if (e.button) return;
      e.preventDefault();
      const v = kind === "v", [A, B] = pair();
      const px = (x) => (v ? x.el.offsetWidth : x.el.offsetHeight), a0 = px(A), b0 = px(B), min = v ? MINC : MINR, wsum = A.o[A.k] + B.o[B.k], start = v ? e.clientX : e.clientY;
      if (a0 + b0 < 2 * min) return;
      g.setPointerCapture(e.pointerId); g.classList.add("drag");
      let moved = false;
      const move = (m) => {
        const a = Math.max(min, Math.min(a0 + b0 - min, a0 + ((v ? m.clientX : m.clientY) - start)));
        A.o[A.k] = wsum * a / (a0 + b0); B.o[B.k] = wsum - A.o[A.k];
        A.el.style.flexGrow = A.o[A.k]; B.el.style.flexGrow = B.o[B.k]; moved = true;
      };
      const up = () => { g.classList.remove("drag"); g.removeEventListener("pointermove", move); g.removeEventListener("pointerup", up); g.removeEventListener("pointercancel", up); if (moved) touch(); };
      g.addEventListener("pointermove", move); g.addEventListener("pointerup", up); g.addEventListener("pointercancel", up);
    };
    g.ondblclick = () => { const [A, B] = pair(), s = A.o[A.k] + B.o[B.k]; A.o[A.k] = B.o[B.k] = s / 2; A.el.style.flexGrow = B.el.style.flexGrow = s / 2; touch(); };
    return g;
  }

  // flex-grow values that add up to less than 1 fill only that fraction of the space, so after splits and closings the weights are scaled
  // to add up to the number of tiles in the column (and the number of columns); the proportions stay
  function fit() {
    const ws = S.cols.reduce((a, c) => a + c.w, 0);
    S.cols.forEach((c) => { c.w *= S.cols.length / ws; const hs = c.rows.reduce((a, r) => a + r.h, 0); c.rows.forEach((r) => { r.h *= c.rows.length / hs; }); });
  }

  // the whole grid again (a preset was chosen, or a tile was added or closed); focusRow: the row to focus afterwards
  function build(focusRow) {
    fit();
    const had = focusRow || (focusT && focusT.r);
    if (ro) { ro.disconnect(); ro = null; }
    if (root) root.remove();
    root = mk("div"); root.id = "tiles"; tiles = []; colEls = [];
    S.cols.forEach((c, ci) => {
      if (ci) root.append(gutter("v", ci));
      const col = mk("div", "tcol"); col.style.flexGrow = c.w; colEls.push(col);
      c.rows.forEach((r, ri) => {
        if (ri) col.append(gutter("h", ci, ri));
        const el = mk("div", "tile"), head = mk("div", "tilehead"), strip = mk("div", "tilestrip"), btns = mk("div", "tilebtns"), body = mk("div", "tilebody");
        const plus = mk("button", "tilebtn"), more = mk("button", "tilebtn"), empty = mk("div", "tileempty");
        plus.type = more.type = "button"; plus.textContent = "+"; more.textContent = "⋮";
        plus.title = "Add a tab to this tile"; more.title = "Tiles: add, close, equal sizes"; plus.setAttribute("aria-label", "Add a tab to this tile"); more.setAttribute("aria-label", "Tile menu");
        empty.textContent = "Empty tile. Add a tab with the + button, or close the tile in the ⋮ menu.";
        el.style.flexGrow = r.h;
        btns.append(plus, more); head.append(strip, btns); body.append(empty); el.append(head, body); col.append(el);
        const t = { el, strip, body, empty, c, r, ci, ri, tabs: [], act: null };
        plus.onclick = () => popOpen(plus, plusMenu(t));
        more.onclick = () => popOpen(more, tileMenu(t));
        el.addEventListener("pointerdown", () => { if (focusT !== t) focusTile(t); }, true);
        el.addEventListener("focusin", () => { if (focusT !== t) focusTile(t); });
        tiles.push(t);
      });
      root.append(col);
    });
    mainEl.append(root);
    focusT = tiles.find((t) => t.r === had) || null;
    resolve(); focusTile(focusT || tiles.find((t) => t.act === state.tab) || tiles[0]);
    sync();
    ro = new ResizeObserver(notify); tiles.forEach((t) => ro.observe(t.body));
  }

  function enter() {
    on = true; document.body.classList.add("tiled"); instrBtn.hidden = false; shownPrev = new Set(); focusT = null;
    build();
  }
  function leave() {
    const keep = state.tab;
    if (ro) { ro.disconnect(); ro = null; }
    for (const k in secs) { secs[k].hidden = true; if (secs[k].parentNode !== mainEl) mainEl.append(secs[k]); }
    if (root) root.remove();
    root = null; tiles = []; colEls = []; focusT = null; shownPrev = new Set();
    on = false; document.body.classList.remove("tiled"); instrBtn.hidden = true; syncHeader();
    const av = avail().map((a) => a.tab);
    if (!av.includes(keep)) state.tab = "status";
    mainEl.querySelectorAll(":scope > section").forEach((s) => { s.hidden = s.id !== "tab-" + state.tab; });
    updateNav(); NX.activate(state.tab); notify();
  }

  // ---- the layout menu in the tab bar ----
  const menu = mk("select", "presel layoutsel"); menu.id = "layoutsel";
  menu.title = "Layout: one tab at a time, or several tabs side by side (screens at least " + MINW + " pixels wide).\nThe + and ⋮ buttons of a tile change a layout; the result is \"my layout\".";
  menu.setAttribute("aria-label", "Layout");
  function menuSync() {
    menu.replaceChildren();
    const add = (v, t) => { const o = mk("option"); o.value = v; o.textContent = "Layout: " + t; menu.append(o); };
    add("tabs", "tabs");
    for (const k of Object.keys(PRESETS)) add(k, PRESETS[k].name);
    if (S.name === "mine" || mine()) add("mine", "my layout");
    menu.value = S.name;
  }
  menuSync();
  { const help = $("helplink"); help.parentNode.insertBefore(menu, help); }
  menu.onchange = () => {
    const v = menu.value; menu.blur();
    if (v === "tabs") { S.name = "tabs"; delete S.cols; }
    else if (v === "mine") { S.name = "mine"; S.cols = mine() || preset("wide3"); }
    else { S.name = v; S.cols = preset(v); }
    save(); apply();
  };

  function apply() {
    const wide = window.innerWidth >= MINW;
    menu.hidden = !wide;
    const want = S.name !== "tabs" && wide;
    if (want && on) build(); else if (want) enter(); else if (on) leave();
  }
  window.addEventListener("resize", () => { const wide = window.innerWidth >= MINW; menu.hidden = !wide; if ((S.name !== "tabs" && wide) !== on) apply(); });

  // a tab that appears or disappears (the Ephemeris tab needs the receiver position) changes what every tile can show
  let mt = 0;
  new MutationObserver(() => { clearTimeout(mt); mt = setTimeout(() => { if (on) sync(); }, 60); })
    .observe($("tabs"), { subtree: true, attributes: true, attributeFilter: ["hidden"] });

  NX.layout = {
    active: () => on,
    visible: (t) => on && tiles.some((x) => x.act === t),
    show: (k) => {
      const tile = tiles.find((x) => x.tabs.includes(k));
      if (tile) pickTab(tile, k); else addTab(focusT || tiles[0], k); // a tab that is in no tile joins the stack of the tile used last
    },
  };
  apply();
})();
