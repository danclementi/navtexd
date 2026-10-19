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

  function renderStations() {
    const f = $("s-freq").value, q = $("s-text").value.trim().toLowerCase();
    const tb = document.querySelector("#st-table tbody");
    tb.replaceChildren();
    let n = 0;
    for (const s of D.stations) {
      if (f && String(s.freq) !== f) continue;
      if (q && !(`${s.call} ${s.name} ${s.country} ${s.cc}`.toLowerCase().includes(q))) continue;
      const tr = el("tr", { "data-id": s.id, class: s.id === D.sel ? "sel" : "" });
      tr.append(el("td", { text: s.area }), el("td", { text: s.freq }), el("td", { text: s.b1 }), el("td", { text: s.call }), el("td", { text: s.name }), el("td", { text: s.cc }), el("td", { text: s.km || "" }), el("td", { text: s.brg ? s.brg + "°" : "" }));
      tb.appendChild(tr); n++;
    }
    $("s-count").textContent = `${n} of ${D.stations.length} stations`;
  }

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
  $("st-import").onclick = () => $("st-file").click(); // the button opens the file chooser; choosing a file continues below
  $("st-file").onchange = async () => {
    const f = $("st-file").files[0]; $("st-file").value = "";
    if (!f) return;
    if (!confirm(`Replace the whole station table with ${f.name}?\n\nThe current table is saved first in the backups folder, with the date and time it was last saved in the file name.`)) return;
    try {
      const text = await f.text();
      const r = await fetch("/api/stations/import", { method: "POST", headers: { "X-Navtex": "1", "Content-Type": "text/plain" }, body: text, credentials: "same-origin" });
      const j = await r.json();
      if (!r.ok) throw new Error(j.error);
      $("st-msg").textContent = `${j.stations} stations imported.` + (j.backup ? ` The previous table is saved as backups/${j.backup}.` : "");
      D.sel = null; await load();
    } catch (e) { $("st-msg").textContent = "Not imported: " + e.message; }
  };

  function roles() {
    const a = admin();
    document.querySelectorAll("#tableio input, #tableio button").forEach((x) => { x.disabled = !a; });
    $("st-export").hidden = false;
    renderKeywords();
  }
  NX.listen("role", roles);
  NX.listen("stations", () => { if (D.stations.length) load(); });
  NX.listen("role", () => { if (D.stations.length) load(); });
  NX.tabs.database = () => { D.loaded = true; load().then(roles).catch((e) => { $("s-count").textContent = e.message; }); };
})();
