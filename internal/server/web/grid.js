"use strict";
// Shared by the Log list and the station list: a table whose columns the operator moves, resizes and chooses (the choice is kept in this
// browser) and, where the table asks for it, sorts and filters as in a spreadsheet.
//
//   const G = NX.grid({ key, table, head, colgroup, cols, ok, change, source, sortable, filterable });
//
//   cols: [{ id, label, w, min, tip, loc, off, kind, circ (num range wraps when from > to), cell(row), val(row), out(row) }]
//     w: default width in pixels (null: the column takes what is left); off: hidden until chosen; loc: needs the receiver's position (see ok)
//     kind: "text" (default), "num" or "date" (val gives milliseconds): how the column sorts and what its filter offers
//     cell(row) builds the <td>; val(row) is the value for sorting and filtering (null or "" is blank); out(row) the text of copy / export
//   ok(col): false for a column that cannot be shown now.   change(): the rows must be drawn again.
//   source(): all the rows the filters work on (the lists of values in the filter menus come from it)
//
// The table keeps the row height whatever is shown: nothing wraps, long values are cut with an ellipsis.
(() => {
  const NX = window.NX, el = NX.el;

  // ---- one small menu, and one filter panel; each closes when the pointer goes elsewhere or Escape is pressed ----
  const pop = el("div", { class: "ctx", role: "menu" }); pop.hidden = true;
  const panel = el("div", { class: "gridpop" }); panel.hidden = true;
  document.body.append(pop, panel);
  const popClose = () => { if (!pop.hidden) { pop.hidden = true; pop.replaceChildren(); } };
  const panelClose = () => { if (!panel.hidden) { panel.hidden = true; panel.replaceChildren(); } };
  const place = (box, x, y) => {
    box.style.left = Math.max(4, Math.min(x, window.innerWidth - box.offsetWidth - 4)) + "px";
    box.style.top = Math.max(4, Math.min(y, window.innerHeight - box.offsetHeight - 4)) + "px";
  };
  // rows: [{ text, tip, off, sep, fn }]; the menu closes before fn runs
  function popup(x, y, rows) {
    panelClose();
    pop.replaceChildren(...rows.map((r) => {
      if (r.sep) return el("hr", { role: "separator" });
      const b = el("button", { type: "button", role: "menuitem", disabled: !!r.off, title: r.tip || "" }, el("span", { text: r.text }), el("span", { class: "k", text: r.key || "" }));
      b.onclick = () => { popClose(); r.fn(); };
      return b;
    }));
    pop.hidden = false;
    place(pop, x, y);
  }
  document.addEventListener("pointerdown", (e) => {
    if (!pop.hidden && !pop.contains(e.target)) popClose();
    if (!panel.hidden && !panel.contains(e.target) && !e.target.closest(".fb")) panelClose();
  }, true);
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && (!pop.hidden || !panel.hidden)) { e.stopPropagation(); popClose(); panelClose(); } }, true);
  window.addEventListener("blur", () => { popClose(); panelClose(); });
  NX.popup = popup; NX.popupClose = () => { popClose(); panelClose(); };

  const blank = (v) => v == null || v === "" || (typeof v === "number" && Number.isNaN(v));
  const isNum = (c) => c.kind === "num" || c.kind === "date";
  const day = (ms) => new Date(ms).toISOString().slice(0, 10);

  NX.grid = (cfg) => {
    const cols = cfg.cols, byId = new Map(cols.map((c) => [c.id, c]));
    const sortable = !!cfg.sortable, filterable = !!cfg.filterable;
    const CS = { order: cols.map((c) => c.id), hide: new Set(cols.filter((c) => c.off).map((c) => c.id)), w: {} }; // the columns
    const SS = { sort: [], flt: {} }; // sort: [{ id, dir }] (dir 1 or -1); flt[id]: { ex: Set of excluded value keys, min, max, text, op }
    const VKEY = cfg.key + "-view";
    const newF = () => ({ ex: new Set(), min: null, max: null, text: "", op: "has" });
    const flt = (id) => SS.flt[id] || (SS.flt[id] = newF());
    const fActive = (f) => !!f && (f.ex.size > 0 || f.min != null || f.max != null || f.text !== "");

    // what the operator chose; anything unknown or damaged is ignored, a column added later shows up next to its neighbour at its default
    try {
      const j = JSON.parse(localStorage.getItem(cfg.key));
      if (j && Array.isArray(j.order)) {
        const o = [...new Set(j.order.filter((k) => byId.has(k)))];
        for (const c of cols) if (!o.includes(c.id)) { const at = cols.indexOf(c); const after = cols.slice(0, at).map((x) => x.id).reverse().find((k) => o.includes(k)); o.splice(after ? o.indexOf(after) + 1 : 0, 0, c.id); }
        CS.order = o;
        if (Array.isArray(j.hide)) CS.hide = new Set(j.hide.filter((k) => byId.has(k)).concat(cols.filter((c) => c.off && !j.order.includes(c.id)).map((c) => c.id)));
        if (j.w && typeof j.w === "object") for (const [k, v] of Object.entries(j.w)) if (byId.has(k) && Number.isFinite(v) && v >= 24 && v <= 900) CS.w[k] = Math.round(v);
      }
    } catch (e) { /* defaults */ }
    if (sortable || filterable) {
      try {
        const j = JSON.parse(localStorage.getItem(VKEY));
        if (j) {
          if (Array.isArray(j.sort)) SS.sort = j.sort.filter((s) => s && byId.has(s.id) && (s.dir === 1 || s.dir === -1)).slice(0, 4).map((s) => ({ id: s.id, dir: s.dir }));
          if (j.flt && typeof j.flt === "object") for (const [k, v] of Object.entries(j.flt)) {
            if (!byId.has(k) || !v) continue;
            const f = newF();
            if (Array.isArray(v.ex)) f.ex = new Set(v.ex.map(String));
            if (Number.isFinite(v.min)) f.min = v.min;
            if (Number.isFinite(v.max)) f.max = v.max;
            if (typeof v.text === "string") f.text = v.text.slice(0, 80);
            if (v.op === "begins" || v.op === "eq") f.op = v.op;
            if (fActive(f)) SS.flt[k] = f;
          }
        }
      } catch (e) { /* none */ }
    }
    const save = () => {
      try {
        localStorage.setItem(cfg.key, JSON.stringify({ order: CS.order, hide: [...CS.hide], w: CS.w }));
        if (sortable || filterable) {
          const f = {};
          for (const [k, v] of Object.entries(SS.flt)) if (fActive(v)) f[k] = { ex: [...v.ex], min: v.min, max: v.max, text: v.text, op: v.op };
          localStorage.setItem(VKEY, JSON.stringify({ sort: SS.sort, flt: f }));
        }
      } catch (e) { /* private mode */ }
    };
    const okc = (c) => !cfg.ok || cfg.ok(c);
    const visible = () => CS.order.map((k) => byId.get(k)).filter((c) => !CS.hide.has(c.id) && okc(c));
    const wOf = (c) => (CS.w[c.id] !== undefined ? CS.w[c.id] : c.w);
    const keyNow = () => visible().map((c) => c.id + ":" + wOf(c)).join();
    let headKey = "";

    // ---- values: sorting, filtering ----
    const vkey = (c, r) => { const v = c.val(r); return blank(v) ? "" : c.kind === "date" ? day(v) : String(v); };
    function match(c, f, r) {
      if (f.ex.size && f.ex.has(vkey(c, r))) return false;
      const v = c.val(r);
      if (isNum(c)) {
        if (f.min != null || f.max != null) {
          if (blank(v)) return false;
          const n = Number(v);
          // a circular column (bearing) with min > max means the span through north: 350 to 10
          if (c.circ && f.min != null && f.max != null && f.min > f.max) { if (n < f.min && n > f.max) return false; }
          else if ((f.min != null && n < f.min) || (f.max != null && n > f.max)) return false;
        }
      } else if (f.text) {
        const s = blank(v) ? "" : String(v).toLowerCase(), t = f.text.toLowerCase();
        if (f.op === "begins" ? !s.startsWith(t) : f.op === "eq" ? s !== t : !s.includes(t)) return false;
      }
      return true;
    }
    const filters = (skip) => cols.filter((c) => c.id !== skip && fActive(SS.flt[c.id]) && okc(c));
    const passes = (list, skip) => { const fs = filters(skip); return fs.length ? list.filter((r) => fs.every((c) => match(c, SS.flt[c.id], r))) : list; };
    const collate = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });
    function rows(list) { // the rows that pass the filters, in the sort order
      const out = passes(list, null);
      const ss = SS.sort.filter((s) => okc(byId.get(s.id)));
      if (!ss.length) return out;
      const tagged = out.map((r, i) => [r, i]);
      tagged.sort((a, b) => {
        for (const s of ss) {
          const c = byId.get(s.id), va = c.val(a[0]), vb = c.val(b[0]), ba = blank(va), bb = blank(vb);
          if (ba || bb) { if (ba && bb) continue; return ba ? 1 : -1; } // blanks last, whichever way
          const d = isNum(c) ? Number(va) - Number(vb) : collate.compare(String(va), String(vb));
          if (d) return d * s.dir;
        }
        return a[1] - b[1];
      });
      return tagged.map((x) => x[0]);
    }

    // ---- header and widths ----
    const head = cfg.head, cg = cfg.colgroup, table = cfg.table;
    table.classList.add("grid");
    function marks() {
      [...head.children].forEach((th) => {
        const id = th.dataset.c, i = SS.sort.findIndex((s) => s.id === id), sm = th.querySelector(".sm");
        if (sm) sm.textContent = i < 0 ? "" : (SS.sort[i].dir > 0 ? "▲" : "▼") + (SS.sort.length > 1 ? i + 1 : "");
        th.classList.toggle("filtered", fActive(SS.flt[id]));
      });
    }
    const hint = "\nDrag to move, drag the edge to resize (double-click: original width), right-click: choose the columns" + (sortable ? "\nClick: sort (Shift-click: sort by this column as well)" : "") + (filterable ? "\nThe ▾ button: filter" : "");
    function build() {
      const cs = visible();
      headKey = keyNow();
      head.replaceChildren(...cs.map((c) => {
        const inner = el("div", { class: "thc" }, el("span", { class: "lbl", text: c.label }), el("span", { class: "sm" }));
        if (filterable) inner.append(el("button", { type: "button", class: "fb", title: "Filter " + c.label, "aria-label": "Filter " + c.label, text: "▾" }));
        return el("th", { "data-c": c.id, title: c.tip + hint }, inner, el("i", { class: "rz", "aria-hidden": "true" }));
      }));
      cg.replaceChildren(...cs.map((c) => { const k = el("col"); if (wOf(c) != null) k.style.width = wOf(c) + "px"; return k; }));
      const fixed = cs.reduce((n, c) => n + (wOf(c) != null ? wOf(c) : c.min || 100), 0), fill = cs.some((c) => wOf(c) == null);
      table.style.minWidth = fixed + "px";
      table.style.width = fill ? "100%" : fixed + "px";
      marks();
    }
    const changed = () => { save(); build(); cfg.change(); };

    // ---- sorting ----
    function toggleSort(id, add) {
      const i = SS.sort.findIndex((s) => s.id === id);
      if (!add) {
        if (SS.sort.length === 1 && i === 0) { if (SS.sort[0].dir > 0) SS.sort[0].dir = -1; else SS.sort = []; } else SS.sort = [{ id, dir: 1 }];
      } else if (i < 0) { if (SS.sort.length < 4) SS.sort.push({ id, dir: 1 }); } else if (SS.sort[i].dir > 0) SS.sort[i].dir = -1; else SS.sort.splice(i, 1);
      changed();
    }
    const setSort = (id, dir) => { SS.sort = dir ? [{ id, dir }] : SS.sort.filter((s) => s.id !== id); changed(); };

    // ---- header gestures: drag a title to move the column, drag its right edge to resize, click to sort, right-click to choose the columns ----
    head.addEventListener("pointerdown", (e) => {
      const th = e.target.closest("th[data-c]");
      if (!th || e.button !== 0 || e.target.closest(".fb")) return;
      const c = byId.get(th.dataset.c), x0 = e.clientX, shift = e.shiftKey;
      if (e.target.classList.contains("rz")) { // resize
        e.preventDefault();
        const w0 = th.getBoundingClientRect().width, col = cg.children[[...head.children].indexOf(th)];
        let did = false;
        const mv = (m) => {
          if (!did && Math.abs(m.clientX - x0) < 3) return;
          did = true;
          const w = Math.max(c.min ? Math.min(c.min, 40) : 30, Math.min(900, Math.round(w0 + m.clientX - x0)));
          CS.w[c.id] = w; col.style.width = w + "px";
        };
        const up = () => { document.removeEventListener("pointermove", mv); document.removeEventListener("pointerup", up); document.body.classList.remove("colmoving"); if (did) changed(); };
        document.body.classList.add("colmoving");
        document.addEventListener("pointermove", mv); document.addEventListener("pointerup", up);
        return;
      }
      // move: nothing happens until the pointer has gone a few pixels, so a plain click stays a click
      let on = false, to = -1;
      const ths = () => [...head.children];
      const mark = () => ths().forEach((t, i) => { t.classList.toggle("dropl", on && i === to && to < ths().length); t.classList.toggle("dropr", on && to === ths().length && i === ths().length - 1); t.classList.toggle("dragging", on && t === th); });
      const mv = (m) => {
        if (!on && Math.abs(m.clientX - x0) < 6) return;
        on = true; document.body.classList.add("colmoving");
        const all = ths(); to = all.length;
        for (let i = 0; i < all.length; i++) { const r = all[i].getBoundingClientRect(); if (m.clientX < r.left + r.width / 2) { to = i; break; } }
        mark();
      };
      const up = () => {
        document.removeEventListener("pointermove", mv); document.removeEventListener("pointerup", up); document.body.classList.remove("colmoving");
        const all = ths(), from = all.indexOf(th), moved = on && to >= 0 && to !== from && to !== from + 1, wasDrag = on;
        on = false; mark();
        if (!wasDrag) { if (sortable) toggleSort(c.id, shift); return; }
        if (!moved) return;
        const shown = visible().map((k) => k.id); // the order of the visible ones changes; the hidden ones keep their places
        shown.splice(from, 1); shown.splice(to > from ? to - 1 : to, 0, c.id);
        const it = [...shown];
        CS.order = CS.order.map((k) => (shown.includes(k) ? it.shift() : k));
        changed();
      };
      document.addEventListener("pointermove", mv); document.addEventListener("pointerup", up);
    });
    head.addEventListener("dblclick", (e) => { // the edge: back to the original width
      const th = e.target.closest("th[data-c]");
      if (!th || !e.target.classList.contains("rz")) return;
      delete CS.w[th.dataset.c]; changed();
    });
    head.addEventListener("click", (e) => {
      const b = e.target.closest(".fb");
      if (b) openFilter(b.closest("th"), byId.get(b.closest("th").dataset.c));
    });
    head.addEventListener("contextmenu", (e) => { if (e.shiftKey) return; e.preventDefault(); menu(e.clientX, e.clientY); });

    // ---- the menu of columns ----
    function reset() { CS.order = cols.map((c) => c.id); CS.hide = new Set(cols.filter((c) => c.off).map((c) => c.id)); CS.w = {}; SS.sort = []; SS.flt = {}; changed(); }
    function menu(x, y) {
      const shown = visible().length, rs = [];
      for (const k of CS.order) {
        const c = byId.get(k), on = !CS.hide.has(k) && okc(c), off = !okc(c) || (on && shown <= 1);
        rs.push({ text: (on ? "✓ " : "  ") + c.label, off, tip: !okc(c) ? "Needs the receiver's position (Options, Station)" : c.tip, fn: () => { if (CS.hide.has(k)) CS.hide.delete(k); else CS.hide.add(k); changed(); menu(x, y); } });
      }
      rs.push({ sep: true });
      if (filterable) rs.push({ text: "Clear all filters", off: !anyFilter(), fn: () => { clearFilters(); } });
      if (sortable) rs.push({ text: "Clear sorting", off: !SS.sort.length, fn: () => { SS.sort = []; changed(); } });
      rs.push({ text: "Original columns", tip: "The usual set, order and widths" + (sortable || filterable ? ", no sorting, no filters" : ""), fn: reset });
      popup(x, y, rs);
    }

    // ---- filters ----
    const anyFilter = () => filters(null).length > 0;
    const clearFilters = () => { SS.flt = {}; changed(); };
    function distinct(c) { // [{ key, n }]: the values of the rows that pass the other columns' filters
      const m = new Map();
      for (const r of passes(cfg.source ? cfg.source() : [], c.id)) { const k = vkey(c, r); m.set(k, (m.get(k) || 0) + 1); }
      const ks = [...m.keys()].filter((k) => k !== "");
      ks.sort(isNum(c) ? (a, b) => (c.kind === "date" ? (a < b ? -1 : a > b ? 1 : 0) : Number(a) - Number(b)) : (a, b) => collate.compare(a, b));
      if (m.has("")) ks.push("");
      return ks.map((k) => ({ key: k, n: m.get(k) }));
    }
    function openFilter(th, c) {
      popClose();
      if (!panel.hidden && panel.dataset.c === c.id) { panelClose(); return; }
      panelClose();
      panel.dataset.c = c.id;
      const f = flt(c.id);
      const live = () => { save(); marks(); cfg.change(); };
      const kids = [el("div", { class: "gptitle", text: c.label })];
      if (sortable) {
        const sb = (text, dir) => { const b = el("button", { type: "button", class: "gpbtn", text }); b.onclick = () => { setSort(c.id, dir); }; return b; };
        const asc = c.kind === "text" || !c.kind ? "Sort A → Z" : c.kind === "date" ? "Sort oldest first" : "Sort smallest first", dsc = c.kind === "text" || !c.kind ? "Sort Z → A" : c.kind === "date" ? "Sort newest first" : "Sort largest first";
        kids.push(sb(asc, 1), sb(dsc, -1), el("hr"));
      }
      // a range for numbers and dates, a text test for the rest
      if (isNum(c)) {
        const date = c.kind === "date", mk = () => el("input", { type: date ? "date" : "number", step: "any" });
        const lo = mk(), hi = mk();
        const toMs = (v, end) => (v ? Date.parse(v + "T00:00:00Z") + (end ? 86399999 : 0) : null);
        if (date) { lo.value = f.min != null ? day(f.min) : ""; hi.value = f.max != null ? day(f.max) : ""; } else { lo.value = f.min != null ? f.min : ""; hi.value = f.max != null ? f.max : ""; }
        const rd = () => {
          const g = (i, end) => (i.value === "" ? null : date ? toMs(i.value, end) : Number(i.value));
          const a = g(lo, false), b = g(hi, true);
          f.min = a != null && Number.isFinite(a) ? a : null; f.max = b != null && Number.isFinite(b) ? b : null;
          live(); drawList();
        };
        lo.oninput = hi.oninput = rd;
        kids.push(el("div", { class: "gprow" }, el("span", { text: date || c.circ ? "From" : "At least" }), lo), el("div", { class: "gprow" }, el("span", { text: date || c.circ ? "To" : "At most" }), hi));
        if (c.circ) kids.push(el("div", { class: "gphint", text: "From larger than To wraps through north, e.g. 350 to 10." }));
        kids.push(el("hr"));
      } else {
        const op = el("select", {}, el("option", { value: "has", text: "contains" }), el("option", { value: "begins", text: "begins with" }), el("option", { value: "eq", text: "equals" }));
        op.value = f.op;
        const t = el("input", { type: "text", placeholder: "text", maxlength: "80" }); t.value = f.text;
        const rd = () => { f.op = op.value; f.text = t.value; live(); drawList(); };
        op.onchange = t.oninput = rd;
        kids.push(el("div", { class: "gprow" }, op, t), el("hr"));
      }
      // the list of values: every value with how many rows have it; the unticked ones are left out
      const find = el("input", { type: "search", placeholder: "Search the values", class: "gpfind" });
      const list = el("div", { class: "gplist" });
      const all = el("label", { class: "gpitem" }), allBox = el("input", { type: "checkbox" });
      all.append(allBox, el("span", { text: "(Select all)" }));
      function drawList() {
        const q = find.value.trim().toLowerCase();
        const items = distinct(c).filter((it) => !q || (it.key === "" ? "(blanks)" : it.key.toLowerCase()).includes(q));
        const nOff = items.filter((it) => f.ex.has(it.key)).length;
        allBox.checked = nOff === 0; allBox.indeterminate = nOff > 0 && nOff < items.length;
        list.replaceChildren(...items.map((it) => {
          const box = el("input", { type: "checkbox" }); box.checked = !f.ex.has(it.key);
          box.onchange = () => { if (box.checked) f.ex.delete(it.key); else f.ex.add(it.key); live(); drawList(); };
          return el("label", { class: "gpitem" }, box, el("span", { class: "gpv", text: it.key === "" ? "(Blanks)" : it.key }), el("span", { class: "gpn", text: String(it.n) }));
        }));
        if (!items.length) list.append(el("div", { class: "small", text: "No values" }));
        allBox.onchange = () => { for (const it of items) { if (allBox.checked) f.ex.delete(it.key); else f.ex.add(it.key); } live(); drawList(); };
      }
      find.oninput = drawList;
      const clr = el("button", { type: "button", class: "gpbtn", text: "Clear filter" });
      clr.onclick = () => { delete SS.flt[c.id]; save(); marks(); cfg.change(); panelClose(); };
      const done = el("button", { type: "button", class: "gpbtn", text: "Close" }); done.onclick = panelClose;
      kids.push(find, all, list, el("div", { class: "gpfoot" }, clr, done));
      panel.replaceChildren(...kids);
      panel.hidden = false;
      drawList();
      const r = th.getBoundingClientRect();
      place(panel, r.left, r.bottom + 2);
      find.focus({ preventScroll: true });
    }

    // ---- text of the visible columns, for copying and export ----
    function text(list, csv) {
      const cs = visible(), sep = csv ? "," : "\t";
      const q = (s) => { s = String(s == null ? "" : s); return csv ? (/[",\r\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s) : s.replace(/[\t\r\n]+/g, " "); };
      const outv = (c, r) => (c.out ? c.out(r) : blank(c.val(r)) ? "" : c.val(r));
      const lines = [cs.map((c) => q(c.label)).join(sep), ...list.map((r) => cs.map((c) => q(outv(c, r))).join(sep))];
      return lines.join(csv ? "\r\n" : "\n") + (csv ? "\r\n" : "\n");
    }

    build();
    return {
      cols: visible, stale: () => headKey !== keyNow(), build, menu, rows, text,
      active: anyFilter, clearFilters, clearSort: () => { SS.sort = []; changed(); },
      sorted: () => SS.sort.length > 0,
    };
  };
})();
