"use strict";
// Report tab: the records moved here from the Log tab, in time order. (The NavtexDX report format comes later.)
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el, api = NX.api;
  const R = { rows: [], sel: new Set(), edited: null }; // edited: { text, key } while the operator's edited report text is in use
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
    $("rp-save").disabled = $("rp-copy").disabled = $("rp-edit").disabled = !R.rows.length;
    $("rp-edited").hidden = !R.edited;
    $("rp-info").hidden = !admin(); // personal information is only known to administrators
  }
  async function load() {
    try { R.rows = (await api("GET", "/api/report")).records; } catch (e) { $("rp-count").textContent = e.message; return; }
    const have = new Set(R.rows.map((r) => r.id));
    R.sel = new Set([...R.sel].filter((id) => have.has(id)));
    if (R.edited && R.edited.key !== key()) {
      // the list changed (records added in the Log tab) since the text was edited
      if (confirm("The report list has changed since you edited the report text.\n\nOK = discard your edits and use the new list.\nCancel = keep your edited text (it will not include the new list).")) R.edited = null;
      else R.edited.key = key();
    }
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
  const dropEdits = () => { if (!R.edited) return true; if (!confirm("Your edits to the report text will be discarded. Continue?")) return false; R.edited = null; return true; };
  $("rp-remove").onclick = async () => { if (!dropEdits()) return; try { R.rows = (await api("POST", "/api/report", { remove: [...R.sel] })).records; R.sel = new Set(); render(); } catch (e) { alert(e.message); } };
  $("rp-clear").onclick = async () => {
    if (!confirm("Remove all records from the report list? (The records stay in the log.)")) return;
    if (!dropEdits()) return;
    try { R.rows = (await api("POST", "/api/report", { remove: R.rows.map((r) => r.id) })).records; R.sel = new Set(); render(); } catch (e) { alert(e.message); }
  };
  // ---- the report as text: save (with the browser's save dialog where it has one) or copy ----
  const infoMode = () => (admin() ? (document.querySelector("input[name=rp-info]:checked") || {}).value || "omit" : "omit");
  try { const m = localStorage.getItem("nx-rp-info"); const r = m && document.querySelector(`input[name=rp-info][value=${m}]`); if (r) r.checked = true; } catch (e) { /* no storage */ }
  let lastInfo = infoMode();
  document.querySelectorAll("input[name=rp-info]").forEach((r) => {
    r.onchange = () => {
      if (!dropEdits()) { document.querySelector(`input[name=rp-info][value=${lastInfo}]`).checked = true; return; } // keep the edited text and the old choice
      lastInfo = r.value; render();
      try { localStorage.setItem("nx-rp-info", r.value); } catch (e) { /* no storage */ }
    };
  });
  const key = () => R.rows.map((r) => r.id).join(",") + "|" + infoMode();
  const note = (t) => { $("rp-msg").textContent = t; clearTimeout(note.t); note.t = setTimeout(() => { $("rp-msg").textContent = ""; }, 4000); };
  const crlf = (t) => t.replace(/\r?\n/g, "\r\n");
  const generated = async () => { const r = await fetch("/api/report.txt?info=" + infoMode(), { credentials: "same-origin", headers: { "X-Navtex": "1" } }); if (!r.ok) throw new Error(r.statusText); return r.text(); };
  const reportText = async () => (R.edited ? crlf(R.edited.text) : generated());
  // saves text (with the browser's save dialog where it has one); true if it was saved
  async function saveText(text) {
    const d = new Date(), z = (n) => String(n).padStart(2, "0");
    const name = `NDX${d.getFullYear()}${z(d.getMonth() + 1)}${z(d.getDate())}_${z(d.getHours())}${z(d.getMinutes())}${z(d.getSeconds())}.TXT`;
    try {
      if (window.showSaveFilePicker) {
        const h = await window.showSaveFilePicker({ suggestedName: name, types: [{ description: "Text file", accept: { "text/plain": [".txt", ".TXT"] } }] });
        const w = await h.createWritable(); await w.write(text); await w.close();
        note("Saved " + h.name);
      } else {
        // no save dialog in this browser: an ordinary download (the browser may ask where to put it)
        const url = URL.createObjectURL(new Blob([text], { type: "text/plain;charset=utf-8" }));
        const a = el("a", { href: url, download: name }); document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 5000);
        note("Downloaded " + name);
      }
      return true;
    } catch (e) { if (e.name !== "AbortError") note("Not saved: " + e.message); return false; }
  }
  async function copyText(t) {
    try {
      if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(t);
      else { const ta = el("textarea", { style: "position:fixed;opacity:0" }); ta.value = t; document.body.append(ta); ta.select(); const ok = document.execCommand("copy"); ta.remove(); if (!ok) throw new Error("the browser refused"); }
      note("Copied to the clipboard.");
      return true;
    } catch (e) { note("Not copied: " + e.message); return false; }
  }
  $("rp-save").onclick = async () => { try { await saveText(await reportText()); } catch (e) { note("Not saved: " + e.message); } };
  $("rp-copy").onclick = async () => { try { await copyText(await reportText()); } catch (e) { note("Not copied: " + e.message); } };
  $("rp-unedit").onclick = () => { R.edited = null; render(); note("Edits discarded."); };

  // ---- Edit report: the text can be changed by hand. The edit lives in this browser only, never in the log records. ----
  const dlg = $("rp-dlg"), ta = $("re-text");
  let initial = "", gen = "";
  const counts = () => { const t = ta.value; $("re-count").textContent = `${t ? t.split("\n").length : 0} lines, ${t.length} characters${ta.value !== initial ? " \u2014 changed" : ""}`; };
  const rmsg = (t) => { $("re-msg").textContent = t; clearTimeout(rmsg.t); rmsg.t = setTimeout(() => { $("re-msg").textContent = ""; }, 4000); };
  ta.addEventListener("input", counts);
  $("rp-edit").onclick = async () => {
    try {
      gen = (await generated()).replace(/\r\n/g, "\n");
      ta.value = initial = R.edited ? R.edited.text : gen;
      $("re-msg").textContent = "";
      counts(); dlg.showModal(); ta.focus({ preventScroll: true }); ta.setSelectionRange(0, 0); ta.scrollTop = 0;
    } catch (e) { note("Cannot edit: " + e.message); }
  };
  const close = (force) => {
    if (!force && ta.value !== initial && !confirm("Discard your changes to the report text?")) return;
    dlg.close();
  };
  const accept = () => { // keep the text for Save Report / Copy, unless it is the generated report again
    R.edited = ta.value === gen ? null : { text: ta.value, key: key() };
    render(); dlg.close();
  };
  $("re-accept").onclick = accept;
  $("re-cancel").onclick = $("re-close").onclick = () => close(false);
  dlg.addEventListener("cancel", (e) => { e.preventDefault(); close(false); });
  $("re-revert").onclick = async () => {
    if (ta.value !== gen && !confirm("Replace the text with the report as generated from the list?")) return;
    try { gen = (await generated()).replace(/\r\n/g, "\n"); ta.value = gen; counts(); rmsg("Reverted."); } catch (e) { rmsg("Cannot revert: " + e.message); }
  };
  $("re-copy").onclick = async () => { if (await copyText(crlf(ta.value))) rmsg("Copied."); };
  $("re-save").onclick = async () => { if (await saveText(crlf(ta.value))) accept(); };

  NX.listen("role", render);
  NX.tabs.report = load;
})();
