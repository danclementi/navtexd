"use strict";
// Database tab: station list and keyword editor, station table import / export.
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el, api = NX.api;
  const D = { stations: [], byId: new Map(), max: 300, sel: null, kw: [], orig: [], loaded: false };
  const admin = () => NX.state.role === "admin";

  NX.stationList = () => D.stations;
  NX.reloadStations = () => load();
  NX.loadStations = async () => { if (!D.stations.length) await load(); return D.byId; };

  async function load() {
    const j = await api("GET", "/api/stations");
    D.stations = j.stations; D.max = j.max_keyword_chars; D.byId = new Map(j.stations.map((s) => [s.id, s]));
    renderStations();
    if (D.sel) loadKeywords();
    (NX.hooks.stationsLoaded || []).forEach((f) => f());
  }

  // the columns of the station list: moved, resized, chosen, sorted and filtered by the operator (grid.js)
  const td = (v) => el("td", { text: v == null ? "" : String(v) });
  const COLS = [
    { id: "area", label: "Area", w: 66, kind: "num", tip: "NAVAREA", val: (s) => s.area, cell: (s) => td(s.area) },
    { id: "freq", label: "Freq", w: 74, kind: "num", tip: "Frequency in kHz", val: (s) => s.freq, cell: (s) => td(s.freq) },
    { id: "b1", label: "B1", w: 56, tip: "Station (ID) letter", val: (s) => s.b1, cell: (s) => td(s.b1) },
    { id: "call", label: "Call", w: 92, tip: "Call sign (___: none)", val: (s) => (/^_*$/.test(s.call) ? null : s.call), out: (s) => s.call, cell: (s) => td(s.call) },
    { id: "name", label: "Name", w: null, min: 160, tip: "Station name", val: (s) => s.name, cell: (s) => el("td", { text: s.name, title: s.name }) },
    { id: "cc", label: "CC", w: 58, tip: "Country code", val: (s) => s.cc, cell: (s) => td(s.cc) },
    { id: "country", label: "Country", w: 130, off: true, tip: "Country", val: (s) => s.country, cell: (s) => el("td", { text: s.country, title: s.country }) },
    { id: "km", label: "Km", w: 76, kind: "num", loc: true, tip: "Distance from your receiver", val: (s) => (s.km ? s.km : null), cell: (s) => td(s.km || "") },
    { id: "brg", label: "Brg", w: 74, kind: "num", circ: true, loc: true, tip: "Bearing from your receiver, in degrees", val: (s) => (s.km ? s.brg || 0 : null), cell: (s) => td(s.km ? (s.brg || 0) + "\u00b0" : "") },
    { id: "gid", label: "GID", w: 66, kind: "num", tip: "How many geographical keywords the station has (0: none yet)", val: (s) => s.gid.length, cell: (s) => td(s.gid.length) },
    { id: "uid", label: "UID", w: 66, kind: "num", tip: "How many unique keywords the station has (0: none yet)", val: (s) => s.uid.length, cell: (s) => td(s.uid.length) },
    { id: "heard", label: "Heard", w: 78, kind: "num", tip: "How many messages of the log were identified as this station", val: (s) => s.heard || 0, cell: (s) => td(s.heard || 0) },
    { id: "last", label: "Last heard", w: 150, off: true, kind: "date", tip: "When the latest message identified as this station began (UTC)", val: (s) => s.last || null, cell: (s) => td(s.last ? NX.stamp(s.last) : ""), out: (s) => (s.last ? NX.stamp(s.last) : "") },
  ];
  // the stations the frequency choice and the search leave; the filters of the columns work on these
  const base = () => {
    const f = $("s-freq").value, q = $("s-text").value.trim().toLowerCase();
    return D.stations.filter((s) => (!f || String(s.freq) === f) && (!q || `${s.call} ${s.name} ${s.country} ${s.cc}`.toLowerCase().includes(q)));
  };
  const G = NX.grid({ key: "nx-stcols", table: $("st-table"), head: $("st-head"), colgroup: $("st-cols"), cols: COLS, sortable: true, filterable: true, source: base,
    ok: (c) => !c.loc || !!NX.state.loc, change: () => renderStations() });
  let shown = []; // the rows as listed (what copy and export give)

  function renderStations() {
    if (G.stale()) G.build();
    const tb = document.querySelector("#st-table tbody"), cs = G.cols();
    shown = G.rows(base());
    tb.replaceChildren(...shown.map((s) => {
      const tr = el("tr", { "data-id": s.id, class: s.id === D.sel ? "sel" : "" });
      tr.append(...cs.map((c) => c.cell(s)));
      return tr;
    }));
    $("s-count").textContent = `${shown.length} of ${D.stations.length} stations`;
    $("s-clr").hidden = !G.active();
    const sel = tb.querySelector("tr.sel"); if (sel) sel.scrollIntoView({ block: "nearest" });
  }
  $("s-clr").onclick = () => G.clearFilters();
  $("s-copy").onclick = async () => {
    const t = G.text(shown, false);
    try {
      if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(t);
      else { const ta = el("textarea", { style: "position:fixed;opacity:0" }); ta.value = t; document.body.append(ta); ta.select(); const ok = document.execCommand("copy"); ta.remove(); if (!ok) throw new Error("the browser refused"); }
      $("s-count").textContent = `Copied ${shown.length} stations.`;
    } catch (e) { $("s-count").textContent = "Not copied: " + e.message; }
    setTimeout(renderStations, 2500);
  };
  $("s-csv").onclick = () => {
    const url = URL.createObjectURL(new Blob(["\ufeff" + G.text(shown, true)], { type: "text/csv;charset=utf-8" }));
    const a = el("a", { href: url, download: "navtexd_stations_view.csv" }); document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 5000);
  };

  const kind = () => document.querySelector('input[name=kwtype]:checked').value;
  const show = (k) => k.replace(/^ +| +$/g, (m) => "␣".repeat(m.length)); // visible marks for significant spaces

  function loadKeywords() {
    const s = D.byId.get(D.sel);
    if (!s) return;
    $("kw-title").textContent = `${s.freq} ${s.b1} ${s.call} ${s.name}`;
    D.orig = (kind() === "GID" ? s.gid : s.uid).slice();
    D.kw = D.orig.slice();
    renderKeywords();
  }

  function renderKeywords() {
    const ul = $("kw-list");
    ul.replaceChildren();
    D.kw.forEach((k, i) => {
      const b = el("button", { text: "✕", title: "Delete this keyword", disabled: !admin() });
      b.onclick = () => { if (confirm(`Delete keyword "${k}"?`)) { D.kw.splice(i, 1); renderKeywords(); } };
      ul.append(el("li", { class: D.orig.includes(k) ? "" : "new" }, el("span", { text: show(k) }), b));
    });
    const n = D.kw.join(",").length;
    $("kw-count").textContent = `${n} / ${D.max} characters`;
    $("kw-count").className = "small" + (n > D.max ? " badq" : "");
    const dirty = D.kw.join("\n") !== D.orig.join("\n");
    $("kw-save").disabled = !admin() || !D.sel || !dirty || n > D.max;
    $("kw-revert").disabled = !dirty;
    $("kw-add").disabled = !admin() || !D.sel;
    $("kw-new").disabled = !admin() || !D.sel;
  }

  document.querySelector("#st-table tbody").addEventListener("click", (e) => {
    const tr = e.target.closest("tr");
    if (!tr) return;
    D.sel = Number(tr.dataset.id);
    $("kw-msg").textContent = "";
    renderStations(); loadKeywords();
  });
  document.querySelectorAll("input[name=kwtype]").forEach((r) => { r.onchange = () => { $("kw-msg").textContent = ""; loadKeywords(); }; });
  $("s-freq").onchange = $("s-text").oninput = renderStations;

  $("kw-add").onclick = () => {
    let k = $("kw-new").value.toUpperCase().replace(/[#,]/g, "");
    if (!k.trim()) return;
    if ($("kw-word").checked) k = " " + k.trim() + " ";
    if (D.kw.includes(k)) { $("kw-msg").textContent = "That keyword is already in the list."; return; }
    D.kw.push(k); $("kw-new").value = ""; $("kw-msg").textContent = ""; renderKeywords();
  };
  $("kw-new").onkeydown = (e) => { if (e.key === "Enter") $("kw-add").onclick(); };
  $("kw-revert").onclick = () => { D.kw = D.orig.slice(); renderKeywords(); };
  $("kw-save").onclick = async () => {
    try { await api("PUT", `/api/stations/${D.sel}/keywords`, { type: kind(), keywords: D.kw }); $("kw-msg").textContent = "Saved."; await load(); }
    catch (e) { $("kw-msg").textContent = "Not saved: " + e.message; }
  };

  // ---- station table ----
  $("st-export").onclick = () => { const a = document.createElement("a"); a.href = "/api/stations/export"; a.download = "navtexd_stations.txt"; a.click(); };
  $("st-import").onclick = () => $("st-file").click(); // the button opens the file chooser; choosing a file continues below
  $("st-file").onchange = async () => {
    const f = $("st-file").files[0]; $("st-file").value = "";
    if (!f) return;
    const mode = $("st-mode").value;
    const ask = {
      "merge": `Merge ${f.name} into the station table?\n\nStations that are missing are added; for the stations you have, the keywords of the file that you lack are added to your lists (yours stay).`,
      "merge-replace": `Merge ${f.name} into the station table, with its rows replacing yours?\n\nStations that are missing are added; every station that the file has replaces your row of it, with your own keywords.`,
      "replace": `Replace the whole station table with ${f.name}?`,
    }[mode] + "\n\nThe current table is saved first in the backups folder, with the date and time it was last saved in the file name.";
    if (!confirm(ask)) return;
    try {
      const text = await f.text();
      const r = await fetch("/api/stations/import?mode=" + encodeURIComponent(mode), { method: "POST", headers: { "X-Navtex": "1", "Content-Type": "text/plain" }, body: text, credentials: "same-origin" });
      const j = await r.json();
      if (!r.ok) throw new Error(j.error);
      if (j.merged) {
        $("st-msg").textContent = !j.added && !j.changed ? "Nothing to merge: the table already has everything in the file."
          : `${j.added} stations added, ${j.changed} changed (${j.keywords} keywords added), ${j.stations} stations now.` + (j.skipped ? ` ${j.skipped} keywords did not fit in the ${300} characters of a list and were left out.` : "") + (j.backup ? ` The previous table is saved as backups/${j.backup}.` : "");
      } else {
        $("st-msg").textContent = `${j.stations} stations imported.` + (j.backup ? ` The previous table is saved as backups/${j.backup}.` : "");
      }
      D.sel = null; await load();
    } catch (e) { $("st-msg").textContent = "Not imported: " + e.message; }
  };

  function roles() {
    const a = admin();
    document.querySelectorAll("#tableio input, #tableio select, #tableio button:not(#st-export)").forEach((x) => { x.disabled = !a; });
    renderKeywords();
  }
  NX.listen("role", roles);
  NX.listen("stations", () => { if (D.stations.length) load(); });
  NX.listen("role", () => { if (D.stations.length) load(); });
  NX.tabs.database = () => { D.loaded = true; load().then(roles).catch((e) => { $("s-count").textContent = e.message; }); };
})();
