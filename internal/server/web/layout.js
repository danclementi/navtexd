// Tiled layouts: on a wide screen several tabs are shown at once, each in a tile (a column of tiles, side by side).
// The tabs are the same sections as always; this file only moves them into tiles and back, so every id stays unique and nothing is
// copied. The layout (preset + the sizes you dragged) is kept in this browser. Below MINW pixels the page is always the plain tabs.
(() => {
  "use strict";
  const NX = window.NX, state = NX.state, $ = (id) => document.getElementById(id);
  const KEY = "nx-layout", MINW = 1400, MINC = 240, MINR = 120;
  const PRESETS = {
    wide3: { name: "Wide, 3 columns", cols: [
      { w: 1, rows: [{ tab: "status", h: 1 }, { tab: "schedule", h: 1 }] },
      { w: 1.7, rows: [{ tab: "spectrum", h: 1 }, { tab: "log", h: 2 }] },
      { w: 1, rows: [{ tab: "report", h: 1 }, { tab: "options", h: 1 }] }] },
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
  const valid = (cols) => Array.isArray(cols) && cols.length > 0 && cols.length <= 6 && cols.every((c) => num(c.w) && Array.isArray(c.rows) && c.rows.length > 0 && c.rows.length <= 6 && c.rows.every((r) => typeof r.tab === "string" && num(r.h)));

  function load() {
    try {
      const j = JSON.parse(localStorage.getItem(KEY));
      if (j && j.name !== "tabs" && PRESETS[j.name]) return { name: j.name, cols: valid(j.cols) ? j.cols : clone(PRESETS[j.name].cols) };
    } catch (e) { /* none or damaged: plain tabs */ }
    return { name: "tabs" };
  }
  const S = load();
  const save = () => { try { localStorage.setItem(KEY, JSON.stringify(S)); } catch (e) { /* private mode */ } };

  let on = false, root = null, tiles = [], colEls = [], focusT = null, ro = null, rafOn = false;
  const main = () => document.querySelector("main");
  const avail = () => [...document.querySelectorAll("#tabs button[data-tab]")].filter((b) => !b.hidden).map((b) => ({ tab: b.dataset.tab, label: b.textContent }));

  // a resize of any tile (also while a gutter is dragged) is announced like a window resize: the spectrum, the log list and the
  // other canvases already redraw on that
  function notify() { if (rafOn) return; rafOn = true; requestAnimationFrame(() => { rafOn = false; window.dispatchEvent(new Event("resize")); }); }

  // the tab each tile really shows: the saved one, or (when that tab is not offered right now, or shown in an earlier tile) the first free one
  function pick() {
    const av = avail().map((a) => a.tab), used = new Set(), out = [];
    for (const c of S.cols) for (const r of c.rows) {
      let t = av.includes(r.tab) && !used.has(r.tab) ? r.tab : av.find((x) => !used.has(x));
      if (t) used.add(t);
      out.push(t || null);
    }
    return out;
  }

  function fillSelect(sel, cur) {
    sel.replaceChildren();
    for (const a of avail()) { const o = mk("option"); o.value = a.tab; o.textContent = a.label; sel.append(o); }
    sel.value = cur || "";
  }

  function park(sec) { if (!sec) return; sec.hidden = true; main().append(sec); }

  // (sec is passed in for a swap: replaceChildren takes the old section out of the page, where getElementById no longer finds it)
  function place(tile, tab, sec) {
    tile.tab = tab; tile.el.dataset.tab = tab || "";
    if (sec === undefined) sec = tab ? $("tab-" + tab) : null;
    if (sec) { sec.hidden = false; tile.body.replaceChildren(sec); } else tile.body.replaceChildren();
    fillSelect(tile.sel, tab);
  }

  function updateNav() {
    document.querySelectorAll("#tabs button[data-tab]").forEach((b) => {
      b.classList.toggle("vis", on && tiles.some((t) => t.tab === b.dataset.tab));
      b.classList.toggle("on", b.dataset.tab === state.tab);
    });
  }

  function focusTile(t) {
    if (!t) return;
    focusT = t;
    tiles.forEach((x) => x.el.classList.toggle("focus", x === t));
    if (t.tab) state.tab = t.tab; // the keyboard shortcuts of the Log and the Report act on the tab of the tile you last used
    updateNav();
  }

  function remember(tile) { S.cols[tile.ci].rows[tile.ri].tab = tile.tab; }

  function setTab(tile, t) {
    const old = tile.tab;
    if (!t || t === old) { fillSelect(tile.sel, old); return; }
    const other = tiles.find((x) => x !== tile && x.tab === t);
    if (other) { const sn = $("tab-" + t), so = old ? $("tab-" + old) : null; place(tile, t, sn); place(other, old, so); remember(other); } // the two tiles change places
    else { park(old ? $("tab-" + old) : null); place(tile, t); }
    remember(tile); save();
    focusTile(tile);
    NX.activate(t);
    if (other && old) NX.activate(old);
    notify();
  }

  function teardown() {
    if (ro) { ro.disconnect(); ro = null; }
    tiles.forEach((t) => { const s = t.tab ? $("tab-" + t.tab) : null; if (s) park(s); });
    if (root) root.remove();
    root = null; tiles = []; colEls = []; focusT = null;
  }

  // ---- gutters: drag to give one neighbour more room (the weights are kept as ratios, so a resized window keeps the proportions) ----
  function gutter(kind, ci, ri) {
    const g = mk("div", "gut " + kind);
    g.title = "Drag to resize. Double-click: the sizes of this layout again.";
    g.setAttribute("role", "separator");
    g.onpointerdown = (e) => {
      if (e.button) return;
      e.preventDefault();
      const v = kind === "v";
      const A = v ? { el: colEls[ci - 1], o: S.cols[ci - 1], k: "w" } : { el: tiles.find((t) => t.ci === ci && t.ri === ri - 1).el, o: S.cols[ci].rows[ri - 1], k: "h" };
      const B = v ? { el: colEls[ci], o: S.cols[ci], k: "w" } : { el: tiles.find((t) => t.ci === ci && t.ri === ri).el, o: S.cols[ci].rows[ri], k: "h" };
      const px = (x) => (v ? x.el.offsetWidth : x.el.offsetHeight), a0 = px(A), b0 = px(B), min = v ? MINC : MINR, wsum = A.o[A.k] + B.o[B.k], start = v ? e.clientX : e.clientY;
      if (a0 + b0 < 2 * min) return;
      g.setPointerCapture(e.pointerId); g.classList.add("drag");
      const move = (m) => {
        const a = Math.max(min, Math.min(a0 + b0 - min, a0 + ((v ? m.clientX : m.clientY) - start)));
        A.o[A.k] = wsum * a / (a0 + b0); B.o[B.k] = wsum - A.o[A.k];
        A.el.style.flexGrow = A.o[A.k]; B.el.style.flexGrow = B.o[B.k];
      };
      const up = () => { g.classList.remove("drag"); g.removeEventListener("pointermove", move); g.removeEventListener("pointerup", up); g.removeEventListener("pointercancel", up); save(); };
      g.addEventListener("pointermove", move); g.addEventListener("pointerup", up); g.addEventListener("pointercancel", up);
    };
    g.ondblclick = () => { // the sizes of the preset again; the tabs stay where they are
      const tabs = tiles.map((t) => t.tab); let n = 0;
      S.cols = clone(PRESETS[S.name].cols);
      S.cols.forEach((c) => c.rows.forEach((r) => { r.tab = tabs[n++] || r.tab; }));
      save(); build();
    };
    return g;
  }

  function build() {
    const hadFocus = focusT ? focusT.tab : state.tab;
    teardown();
    root = mk("div"); root.id = "tiles";
    const eff = pick(); let n = 0;
    S.cols.forEach((c, ci) => {
      if (ci) root.append(gutter("v", ci));
      const col = mk("div", "tcol"); col.style.flexGrow = c.w; colEls.push(col);
      c.rows.forEach((r, ri) => {
        if (ri) col.append(gutter("h", ci, ri));
        const el = mk("div", "tile"), head = mk("div", "tilehead"), sel = mk("select", "tilesel"), body = mk("div", "tilebody");
        el.style.flexGrow = r.h; sel.title = "What this tile shows. Choosing a tab that another tile shows swaps the two."; sel.setAttribute("aria-label", "Tab shown in this tile");
        head.append(sel); el.append(head, body); col.append(el);
        const tile = { el, sel, body, ci, ri, tab: null };
        sel.onchange = () => { const v = sel.value; sel.blur(); setTab(tile, v); };
        el.addEventListener("pointerdown", () => { if (focusT !== tile) focusTile(tile); }, true);
        el.addEventListener("focusin", () => { if (focusT !== tile) focusTile(tile); });
        tiles.push(tile);
        place(tile, eff[n++]);
      });
      root.append(col);
    });
    main().querySelectorAll(":scope > section").forEach((s) => { s.hidden = true; });
    main().append(root);
    ro = new ResizeObserver(notify); tiles.forEach((t) => ro.observe(t.body));
    focusTile(tiles.find((t) => t.tab === hadFocus) || tiles[0]);
    notify();
  }

  function enter() {
    on = true; document.body.classList.add("tiled");
    build();
    tiles.forEach((t) => { if (t.tab) NX.activate(t.tab); });
  }
  function leave() {
    const keep = state.tab;
    teardown(); on = false; document.body.classList.remove("tiled");
    const av = avail().map((a) => a.tab);
    if (!av.includes(keep)) state.tab = "status";
    main().querySelectorAll(":scope > section").forEach((s) => { s.hidden = s.id !== "tab-" + state.tab; });
    updateNav(); NX.activate(state.tab); notify();
  }

  // ---- the layout menu in the tab bar ----
  const menu = mk("select", "presel layoutsel"); menu.id = "layoutsel";
  menu.title = "Layout: one tab at a time, or several tabs side by side (screens at least " + MINW + " pixels wide)";
  menu.setAttribute("aria-label", "Layout");
  { const o = mk("option"); o.value = "tabs"; o.textContent = "Layout: tabs"; menu.append(o); }
  for (const k of Object.keys(PRESETS)) { const o = mk("option"); o.value = k; o.textContent = "Layout: " + PRESETS[k].name; menu.append(o); }
  menu.value = S.name;
  { const help = $("helplink"); help.parentNode.insertBefore(menu, help); }
  menu.onchange = () => {
    const v = menu.value; menu.blur();
    if (v === "tabs") { S.name = "tabs"; delete S.cols; } else { S.name = v; S.cols = clone(PRESETS[v].cols); }
    save(); apply();
  };

  function apply() {
    const wide = window.innerWidth >= MINW;
    menu.hidden = !wide;
    const want = S.name !== "tabs" && wide;
    if (want && on) build(); else if (want) enter(); else if (on) leave();
    if (on) tiles.forEach((t) => { if (t.tab) NX.activate(t.tab); });
  }
  window.addEventListener("resize", () => { const wide = window.innerWidth >= MINW; menu.hidden = !wide; if ((S.name !== "tabs" && wide) !== on) apply(); });

  // a tab that appears or disappears (the Ephemeris tab needs the receiver position) changes the offer of every tile
  let mt = 0;
  new MutationObserver(() => { clearTimeout(mt); mt = setTimeout(() => { if (on) { const eff = pick(); if (tiles.some((t, i) => t.tab !== eff[i])) build(); else tiles.forEach((t) => fillSelect(t.sel, t.tab)); } }, 60); })
    .observe($("tabs"), { subtree: true, attributes: true, attributeFilter: ["hidden"] });

  NX.layout = {
    active: () => on,
    visible: (t) => on && tiles.some((x) => x.tab === t),
    show: (t) => { const tile = tiles.find((x) => x.tab === t); if (tile) focusTile(tile); else setTab(focusT || tiles[0], t); },
  };
  apply();
})();
