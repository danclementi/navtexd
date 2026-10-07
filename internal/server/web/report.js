"use strict";
// Report tab: the records moved here from the Log tab, in time order. (The NavtexDX report format comes later.)
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el, api = NX.api;
  const R = { rows: [], sel: new Set() };
  const admin = () => NX.state.role === "admin";

  function render() {
    const tb = document.querySelector("#rp-table tbody");
    tb.replaceChildren(...R.rows.map((r) => {
      const tr = el("tr", { "data-id": r.id, class: R.sel.has(r.id) ? "sel" : "" });
      tr.append(el("td", { text: NX.stamp(r.rx) }), el("td", { text: r.ch }), el("td", { text: r.q.toFixed(0) }), el("td", { text: (r.b1 || "?") + r.b2 + (r.ser || "") }),
        el("td", { text: r.st + (r.cmt ? ` [${r.cmt}]` : "") }), el("td", { class: "km", text: r.km ? r.km.toFixed(0) : "" }), el("td", { text: r.kind }));
      return tr;
    }));
    $("rp-count").textContent = `${R.rows.length} record${R.rows.length === 1 ? "" : "s"}`;
    $("rp-remove").disabled = !admin() || !R.sel.size;
    $("rp-clear").disabled = !admin() || !R.rows.length;
    $("rp-save").disabled = $("rp-copy").disabled = !R.rows.length;
    $("rp-info").hidden = !admin(); // personal information is only known to administrators
  }
  async function load() {
    try { R.rows = (await api("GET", "/api/report")).records; } catch (e) { $("rp-count").textContent = e.message; return; }
    const have = new Set(R.rows.map((r) => r.id));
    R.sel = new Set([...R.sel].filter((id) => have.has(id)));
    render();
  }
  async function show(id) {
    try {
      const r = await api("GET", "/api/messages/" + id);
      $("rp-title").textContent = `#${r.id}  ${r.st}`;
      $("rp-text").textContent = r.lines.map((l) => l.x).join("\n");
    } catch (e) { $("rp-text").textContent = e.message; }
  }
  document.querySelector("#rp-table tbody").addEventListener("click", (e) => {
    const tr = e.target.closest("tr[data-id]");
    if (!tr) return;
    const id = Number(tr.dataset.id);
    if (e.ctrlKey || e.metaKey) { if (R.sel.has(id)) R.sel.delete(id); else R.sel.add(id); } else R.sel = new Set([id]);
    render(); show(id);
  });
  $("rp-remove").onclick = async () => { try { R.rows = (await api("POST", "/api/report", { remove: [...R.sel] })).records; R.sel = new Set(); render(); } catch (e) { alert(e.message); } };
  $("rp-clear").onclick = async () => {
    if (!confirm("Remove all records from the report list? (The records stay in the log.)")) return;
    try { R.rows = (await api("POST", "/api/report", { remove: R.rows.map((r) => r.id) })).records; R.sel = new Set(); render(); } catch (e) { alert(e.message); }
  };
  // ---- the report as text: save (with the browser's save dialog where it has one) or copy ----
  const infoMode = () => (admin() ? (document.querySelector("input[name=rp-info]:checked") || {}).value || "omit" : "omit");
  try { const m = localStorage.getItem("nx-rp-info"); const r = m && document.querySelector(`input[name=rp-info][value=${m}]`); if (r) r.checked = true; } catch (e) { /* no storage */ }
  document.querySelectorAll("input[name=rp-info]").forEach((r) => { r.onchange = () => { try { localStorage.setItem("nx-rp-info", r.value); } catch (e) { /* no storage */ } }; });
  const note = (t) => { $("rp-msg").textContent = t; clearTimeout(note.t); note.t = setTimeout(() => { $("rp-msg").textContent = ""; }, 4000); };
  const reportText = async () => { const r = await fetch("/api/report.txt?info=" + infoMode(), { credentials: "same-origin", headers: { "X-Navtex": "1" } }); if (!r.ok) throw new Error(r.statusText); return r.text(); };
  $("rp-save").onclick = async () => {
    const d = new Date(), z = (n) => String(n).padStart(2, "0");
    const name = `NDX${d.getFullYear()}${z(d.getMonth() + 1)}${z(d.getDate())}_${z(d.getHours())}${z(d.getMinutes())}${z(d.getSeconds())}.TXT`;
    try {
      if (window.showSaveFilePicker) {
        const h = await window.showSaveFilePicker({ suggestedName: name, types: [{ description: "Text file", accept: { "text/plain": [".txt", ".TXT"] } }] });
        const w = await h.createWritable(); await w.write(await reportText()); await w.close();
        note("Saved " + h.name);
      } else {
        // no save dialog in this browser: an ordinary download (the browser may ask where to put it)
        const url = URL.createObjectURL(new Blob([await reportText()], { type: "text/plain;charset=utf-8" }));
        const a = el("a", { href: url, download: name }); document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 5000);
        note("Downloaded " + name);
      }
    } catch (e) { if (e.name !== "AbortError") note("Not saved: " + e.message); }
  };
  $("rp-copy").onclick = async () => {
    try {
      const t = await reportText();
      if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(t);
      else { const ta = el("textarea", { style: "position:fixed;opacity:0" }); ta.value = t; document.body.append(ta); ta.select(); const ok = document.execCommand("copy"); ta.remove(); if (!ok) throw new Error("the browser refused"); }
      note("Copied to the clipboard.");
    } catch (e) { note("Not copied: " + e.message); }
  };

  NX.listen("role", render);
  NX.tabs.report = load;
})();
