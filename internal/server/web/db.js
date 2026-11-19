"use strict";
// Database tab: station list and keyword editor, station table import / export.
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el, api = NX.api;
  const D = { stations: [], byId: new Map(), sel: null, kind: "GID", kw: [], orig: [], ship: [], own: [], extra: false, loaded: false };
  const admin = () => NX.state.role === "admin";

  NX.stationList = () => D.stations;
  NX.reloadStations = () => load();
  NX.loadStations = async () => { if (!D.stations.length) await load(); return D.byId; };

  async function load() {
    const j = await api("GET", "/api/stations");
    D.changes = j.changes || { own: 0, deleted: 0, edited: 0 }; D.stations = j.stations; D.byId = new Map(j.stations.map((s) => [s.id, s]));
    renderStations();
    if (D.sel && !D.byId.has(D.sel)) { D.sel = null; D.kw = D.orig = []; renderKeywords(); } // (the station was removed)
    if (D.sel) { if (isDirty()) renderKeywords(); else loadKeywords(); } // edits that are not saved stay
    if (!D.adding) fillForm();
    tableState();
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
    { id: "edits", label: "Mine", w: 62, kind: "num", tip: "Changes of yours to the table shipped with the program: own = a station of your own, del = a shipped station you deleted (not used), a number = keywords you added or removed (empty: as shipped)", val: (s) => s.edits || 0, cell: (s) => el("td", { class: s.edits ? "mine" : "", text: s.own ? "own" : s.deleted ? "del" : s.edits ? "\u25cf " + s.edits : "" }) },
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
      const tr = el("tr", { "data-id": s.id, class: [s.id === D.sel ? "sel" : "", s.own ? "own" : "", s.deleted ? "dead" : "", !s.own && !s.deleted && s.edits ? "mod" : ""].join(" ").trim(), title: s.deleted ? "Deleted by you: shipped with the program, not used. Restore original brings it back." : s.own ? "A station of your own" : "" });
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

  const isDirty = () => !!D.sel && D.kw.join("\n") !== D.orig.join("\n");
  const kwTitle = () => { const s = D.byId.get(D.sel); return s ? `${s.freq} ${s.b1} ${s.call} ${s.name}` : ""; };

  function loadKeywords() {
    const s = D.byId.get(D.sel);
    if (!s) return;
    D.kind = kind();
    $("kw-title").textContent = kwTitle();
    D.orig = (D.kind === "GID" ? s.gid : s.uid).slice();
    // what the program ships and what is yours: the list as it is, without your additions, and with the ones you removed
    const own = (D.kind === "GID" ? s.own_gid : s.own_uid) || [], del = (D.kind === "GID" ? s.del_gid : s.del_uid) || [];
    D.extra = !!s.own; D.dead = !!s.deleted; D.own = D.extra ? D.orig.slice() : own.slice();
    D.ship = D.extra ? [] : D.orig.filter((k) => !own.includes(k)).concat(del);
    D.kw = D.orig.slice();
    renderKeywords();
  }

  function renderKeywords() {
    const ul = $("kw-list");
    ul.replaceChildren();
    D.kw.forEach((k, i) => {
      const b = el("button", { text: "✕", title: "Delete this keyword", disabled: !admin() || D.dead });
      b.onclick = () => { if (confirm(`Delete keyword "${k}"?`)) { D.kw.splice(i, 1); renderKeywords(); } };
      const cls = !D.orig.includes(k) || (!D.ship.includes(k) && !D.own.includes(k)) ? "new" : D.ship.includes(k) ? "" : "own";
      ul.append(el("li", { class: cls, title: cls === "own" ? "Yours: not in the table shipped with the program" : cls === "new" ? "Not saved yet" : "" }, el("span", { text: show(k) }), b));
    });
    D.ship.filter((k) => !D.kw.includes(k)).forEach((k) => { // shipped keywords that are not in the list (removed by you; saved or not)
      const b = el("button", { text: "\u21ba", title: "Put this shipped keyword back", disabled: !admin() || D.dead });
      b.onclick = () => { D.kw.push(k); renderKeywords(); };
      ul.append(el("li", { class: "gone", title: "Shipped with the program, removed from the list" }, el("span", { text: show(k) }), b));
    });
    const n = D.kw.join(",").length;
    const dirty = isDirty();
    $("kw-count").textContent = `${n} characters` + (dirty ? " (not saved)" : "");
    $("kw-count").className = "small" + (dirty ? " badq" : "");
    $("kw-save").disabled = !admin() || !D.sel || D.dead || !dirty;
    $("kw-revert").disabled = !dirty;
    const st = D.byId.get(D.sel), mine = st ? ((D.kind === "GID" ? st.own_gid : st.own_uid) || []).length + ((D.kind === "GID" ? st.del_gid : st.del_uid) || []).length : 0;
    $("kw-orig").disabled = !admin() || !D.sel || D.extra || (!(st && st.edits) && !dirty);
    $("kw-orig").title = D.extra ? "A station of your own has nothing to go back to: delete it instead" : "Give this station the keywords (GID and UID) and the state that the program ships it with; a deleted station comes back";
    $("kw-add").disabled = !admin() || !D.sel || D.dead;
    $("kw-new").disabled = !admin() || !D.sel || D.dead;
  }

  // Leaving a keyword list with changes that are not saved (another station, GID <-> UID) asks first: OK saves them, else they
  // can be discarded, or the operator stays where they are. Returns whether to go on.
  async function leaveOk() {
    if ((D.adding || formDirty()) && !confirm(D.adding ? "The new station is not added yet.\n\nOK drops it. Cancel stays here." : `The fields of ${kwTitle()} have changes that are not saved.\n\nOK discards them. Cancel stays here.`)) return false;
    D.adding = false;
    if (!isDirty()) return true;
    const what = `The ${D.kind} keywords of ${kwTitle()} have changes that are not saved.`;
    if (admin() && confirm(what + "\n\nOK saves them now. Cancel gives the choice to discard them.")) return await saveKeywords();
    return confirm(what + "\n\nOK discards the changes. Cancel stays here with them.");
  }
  window.addEventListener("beforeunload", (e) => { if (isDirty()) { e.preventDefault(); e.returnValue = ""; } });

  document.querySelector("#st-table tbody").addEventListener("click", async (e) => {
    const tr = e.target.closest("tr");
    if (!tr) return;
    const id = Number(tr.dataset.id);
    if (id !== D.sel && !(await leaveOk())) return;
    D.sel = id;
    $("kw-msg").textContent = ""; $("se-msg").textContent = "";
    renderStations(); loadKeywords(); fillForm();
  });
  document.querySelectorAll("input[name=kwtype]").forEach((r) => {
    r.onchange = async () => {
      if (!(await leaveOk())) { document.querySelector(`input[name=kwtype][value=${D.kind}]`).checked = true; return; }
      $("kw-msg").textContent = ""; loadKeywords();
    };
  });
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
  $("kw-orig").onclick = async () => {
    if (!(await leaveOk())) return;
    const st = D.byId.get(D.sel); if (!st) return;
    let more = "";
    if (st.deleted) {
      try { const j = await api("GET", `/api/stations/${D.sel}/restore`); if (j.own.length) more = `\n\nYour own station${j.own.length > 1 ? "s" : ""} with the same frequency, NAVAREA and B1 letter (${j.own.join(", ")}) will be deleted.`; } catch (e) { /* no plan: ask anyway */ }
    }
    if (!confirm(`Give ${kwTitle()} back as the program ships it?\n\nYour keyword changes (GID and UID) go${st.deleted ? ", and the station is in use again" : ""}.` + more)) return;
    try {
      await api(st.deleted ? "POST" : "PUT", st.deleted ? `/api/stations/${D.sel}/restore` : `/api/stations/${D.sel}`, st.deleted ? undefined : { revert: true });
      $("kw-msg").textContent = "Back as shipped."; D.orig = D.kw = []; await load(); loadKeywords();
    } catch (e) { $("kw-msg").textContent = "Not changed: " + e.message; }
  };
  async function saveKeywords() {
    try {
      await api("PUT", `/api/stations/${D.sel}/keywords`, { type: D.kind, keywords: D.kw });
      D.orig = D.kw.slice(); $("kw-msg").textContent = "Saved."; await load(); return true;
    } catch (e) { $("kw-msg").textContent = "Not saved: " + e.message; return false; }
  }
  $("kw-save").onclick = saveKeywords;


  // ---- the fields of a station (name, country, position, times; and the key fields of a new station) ----
  const F = {}, FIELDS = ["freq", "area", "b1", "call", "name", "cc", "country", "lat", "lon", "t0", "t1", "t2", "t3", "t4", "t5"];
  FIELDS.forEach((k) => { F[k] = $("se-" + k); });
  const KEYF = ["freq", "area", "b1", "call"];
  const formVals = () => ({ name: F.name.value.trim(), cc: F.cc.value.trim(), country: F.country.value.trim(), lat: F.lat.value.trim(), lon: F.lon.value.trim(), times: [0, 1, 2, 3, 4, 5].map((i) => F["t" + i].value.trim()) });
  const stVals = (s) => ({ name: s.name, cc: s.cc, country: s.country, lat: String(s.lat), lon: String(s.lon), times: s.times.map((t) => t || "") });
  const formDirty = () => { const s = D.byId.get(D.sel); return !!s && !D.adding && JSON.stringify(formVals()) !== JSON.stringify(stVals(s)); };
  function fillForm() {
    const s = D.adding ? null : D.byId.get(D.sel);
    $("se-title").textContent = D.adding ? "new" : s ? `${s.freq} ${s.b1} ${s.call}` : "";
    if (s) {
      F.freq.value = String(s.freq); F.area.value = s.area; F.b1.value = s.b1; F.call.value = s.call; F.name.value = s.name; F.cc.value = s.cc; F.country.value = s.country;
      F.lat.value = s.lat; F.lon.value = s.lon; s.times.forEach((t, i) => { F["t" + i].value = t || ""; });
    } else if (!D.adding) FIELDS.forEach((k) => { F[k].value = ""; });
    formState();
  }
  function formState() {
    const s = D.byId.get(D.sel), a = admin();
    KEYF.forEach((k) => { F[k].disabled = !(a && D.adding); });
    // the fields of a station shipped with the program are the program's: only the fields of a station of your own can be changed
    ["name", "cc", "country", "lat", "lon", "t0", "t1", "t2", "t3", "t4", "t5"].forEach((k) => { F[k].disabled = !a || !(D.adding || (s && s.own)); });
    $("se-save").textContent = D.adding ? "Add station" : "Save station";
    $("se-save").disabled = !a || !(D.adding || (s && s.own && formDirty()));
    $("se-remove").disabled = !a || D.adding || !s || s.deleted;
    $("se-new").disabled = !a || D.adding;
    $("se-cancel").hidden = !D.adding;
  }
  const body = () => {
    const v = formVals(), b = { name: v.name, cc: v.cc, country: v.country, lat: Number(v.lat), lon: Number(v.lon), times: v.times };
    if (v.lat === "" || v.lon === "" || !Number.isFinite(b.lat) || !Number.isFinite(b.lon)) throw new Error("the latitude and longitude are numbers (degrees)");
    if (D.adding) Object.assign(b, { freq: Number(F.freq.value), area: Number(F.area.value), b1: F.b1.value.trim().toUpperCase(), call: F.call.value.trim().toUpperCase() });
    return b;
  };
  FIELDS.forEach((k) => { F[k].addEventListener("input", formState); });
  $("se-save").onclick = async () => {
    try {
      const b = body();
      if (D.adding) {
        const j = await api("POST", "/api/stations", b);
        D.adding = false; D.sel = j.id; $("se-msg").textContent = "Added."; await load();
        $("kw-msg").textContent = ""; loadKeywords();
      } else { await api("PUT", `/api/stations/${D.sel}`, b); $("se-msg").textContent = "Saved."; await load(); }
    } catch (e) { $("se-msg").textContent = "Not saved: " + e.message; }
  };
  $("se-remove").onclick = async () => {
    const s = D.byId.get(D.sel); if (!s) return;
    if (!confirm(s.own ? `Delete your station ${kwTitle()}?\n\nIt is removed from the table. Its messages keep their label.`
      : `Delete ${kwTitle()}?\n\nIt is shipped with the program, so it stays in the list, struck through, and is not used to identify messages. Restore original brings it back.`)) return;
    try { await api("DELETE", `/api/stations/${D.sel}`); if (s.own) { D.sel = null; D.kw = D.orig = []; } $("se-msg").textContent = "Deleted."; await load(); loadKeywords(); } catch (e) { $("se-msg").textContent = "Not deleted: " + e.message; }
  };
  $("se-new").onclick = async () => {
    if (!(await leaveOk())) return;
    D.sel = null; D.kw = D.orig = []; renderKeywords(); renderStations();
    D.adding = true; FIELDS.forEach((k) => { F[k].value = ""; }); F.freq.value = "518"; $("se-msg").textContent = "Fill in the fields; the keywords come after the station is added.";
    fillForm();
  };
  $("se-cancel").onclick = () => { D.adding = false; $("se-msg").textContent = ""; fillForm(); };
  // the whole table back as shipped
  function tableState() {
    const c = D.changes || { own: 0, deleted: 0, edited: 0 };
    $("st-revert").disabled = !admin() || c.own + c.deleted + c.edited === 0;
    $("st-changes").textContent = c.own + c.deleted + c.edited ? `Your changes: ${c.edited} stations with other keywords, ${c.deleted} deleted, ${c.own} of your own.` : "The table is as shipped.";
  }
  $("st-revert").onclick = async () => {
    if (!(await leaveOk())) return;
    const c = D.changes;
    if (!confirm(`Give the whole station table back as the program ships it?\n\n${c.own} stations of your own are removed, ${c.deleted} deleted stations are restored, and the keyword changes of ${c.edited} stations go.\n\nThe table as it is now is saved first in the backups folder. OK or Cancel?`)) return;
    try { const j = await api("POST", "/api/stations/revert"); D.sel = null; D.kw = D.orig = []; $("st-msg").textContent = "The table is as shipped." + (j.backup ? ` The previous table is saved as backups/${j.backup}.` : ""); await load(); renderKeywords(); }
    catch (e) { $("st-msg").textContent = "Not changed: " + e.message; }
  };

  // ---- station table ----
  $("st-export").onclick = () => { const a = document.createElement("a"); a.href = "/api/stations/export"; a.download = "navtexd_stations.txt"; a.click(); };
  $("st-import").onclick = () => $("st-file").click(); // the button opens the file chooser; choosing a file continues below
  $("st-file").onchange = async () => {
    const f = $("st-file").files[0]; $("st-file").value = "";
    if (!f) return;
    const mode = $("st-mode").value;
    const ask = {
      "merge": `Merge ${f.name} into the station table?\n\nKeywords of the file that you lack are added to the stations you use (yours stay), and stations that the table lacks are added as your own. Nothing is deleted. Other fields of the shipped stations in the file are ignored.`,
      "replace": `Make the station table equal to ${f.name}?\n\nShipped stations that the file lacks are deleted (they stay in the list, struck through), the keyword lists of the file replace yours, and stations that are not shipped become your own. Fields of the shipped stations other than the keywords are ignored.`,
    }[mode] + "\n\nThe current table is saved first in the backups folder, with the date and time it was last saved in the file name.";
    if (!confirm(ask)) return;
    try {
      const text = await f.text();
      const r = await fetch("/api/stations/import?mode=" + encodeURIComponent(mode), { method: "POST", headers: { "X-Navtex": "1", "Content-Type": "text/plain" }, body: text, credentials: "same-origin" });
      const j = await r.json();
      if (!r.ok) throw new Error(j.error);
      if (j.merged) {
        $("st-msg").textContent = !j.added && !j.changed ? "Nothing to merge: the table already has everything in the file."
          : `${j.added} stations added, ${j.changed} changed (${j.keywords} keywords added), ${j.stations} stations in use now.` + (j.backup ? ` The previous table is saved as backups/${j.backup}.` : "") + (j.notes && j.notes.length ? " Left out: " + j.notes.join("; ") + "." : "");
      } else {
        $("st-msg").textContent = `${j.stations} stations in use now.` + (j.backup ? ` The previous table is saved as backups/${j.backup}.` : "") + (j.notes && j.notes.length ? " Left out: " + j.notes.join("; ") + "." : "");
      }
      D.sel = null; await load();
    } catch (e) { $("st-msg").textContent = "Not imported: " + e.message; }
  };

  function roles() {
    const a = admin();
    document.querySelectorAll("#tableio input, #tableio select, #tableio button:not(#st-export)").forEach((x) => { x.disabled = !a; });
    renderKeywords(); tableState();
  }
  NX.listen("role", roles);
  NX.listen("stations", () => { if (D.stations.length) load(); });
  NX.listen("role", () => { if (D.stations.length) load(); });
  NX.tabs.database = () => { D.loaded = true; load().then(roles).catch((e) => { $("s-count").textContent = e.message; }); };
})();
