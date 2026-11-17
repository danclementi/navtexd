"use strict";
// Report tab: the records this user added from the Log tab (the list is kept in this browser, see replist.js), in time order.
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el, api = NX.api;
  const R = { rows: [], sel: new Set(), anchor: -1, edited: null }; // edited: { text, key } while the report text has been edited by hand (key: the list and choices the generated text was made for)
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
    $("rp-remove").disabled = !R.sel.size;
    $("rp-clear").disabled = !R.rows.length;
    $("rp-save").disabled = $("rp-copy").disabled = $("rp-edit").disabled = !R.rows.length;
    $("rp-edited").hidden = !R.edited;
    $("rp-info").hidden = !admin(); // personal information is only known to administrators
  }
  async function load() {
    try { R.rows = (await api("POST", "/api/report/list", { ids: NX.replist.ids() })).records; } catch (e) { $("rp-count").textContent = e.message; return; }
    const have = new Set(R.rows.map((r) => r.id));
    NX.replist.keepOnly(R.rows.map((r) => r.id)); // records deleted from the log drop out of the list
    R.sel = new Set([...R.sel].filter((id) => have.has(id))); R.anchor = -1;
    render();
  }
  async function show(id) {
    try {
      const r = await api("GET", "/api/messages/" + id);
      $("rp-title").textContent = `${r.st} (${NX.hex(r.id)})`;
      $("rp-text").textContent = r.lines.map((l) => l.x).join("\n");
    } catch (e) { $("rp-text").textContent = e.message; }
  }
  // selection works as in the Log tab: click selects one record, Ctrl-click adds or removes one, Shift-click selects everything between the
  // last clicked record (the anchor) and this one, Ctrl-A selects all
  const rowIdx = (id) => R.rows.findIndex((r) => r.id === id);
  document.querySelector("#rp-table tbody").addEventListener("click", (e) => {
    const tr = e.target.closest("tr[data-id]");
    if (!tr) return;
    const id = Number(tr.dataset.id), i = rowIdx(id);
    if (e.ctrlKey || e.metaKey) { if (R.sel.has(id)) R.sel.delete(id); else R.sel.add(id); R.anchor = i; }
    else if (e.shiftKey && R.anchor >= 0 && R.anchor < R.rows.length) {
      window.getSelection().removeAllRanges();
      const a = Math.min(R.anchor, i), b = Math.max(R.anchor, i);
      R.sel = new Set(R.rows.slice(a, b + 1).map((r) => r.id)); // (the anchor stays where it was)
    } else { R.sel = new Set([id]); R.anchor = i; }
    render(); show(id);
  });
  document.addEventListener("keydown", (e) => {
    if (!(e.ctrlKey || e.metaKey) || e.altKey || e.key.toLowerCase() !== "a" || NX.state.tab !== "report" || document.querySelector("dialog[open]")) return;
    if (/INPUT|SELECT|TEXTAREA/.test(document.activeElement.tagName)) return;
    e.preventDefault(); R.sel = new Set(R.rows.map((r) => r.id)); render();
  });
  // The report text follows the list until it is edited by hand. While it is edited, a change of the list (or of the choices that make the
  // text) asks first: the edit belongs to the text as it was generated for one list. NX.replist asks through guard.
  const clearEdits = () => { R.edited = null; genKey = null; }; // (genKey: the generated text is made again when it is next shown)
  const dropEdits = () => {
    if (!R.edited) return true;
    if (!confirm("The report text has your edits, and changing the report discards them.\n\nOK = discard the edits and go on.\nCancel = leave everything as it is.")) return false;
    clearEdits(); render(); return true;
  };
  NX.replist.guard = dropEdits;
  $("rp-remove").onclick = async () => { if (NX.replist.remove([...R.sel]) === false) return; R.sel = new Set(); R.anchor = -1; await load(); };
  $("rp-clear").onclick = async () => {
    if (!confirm("Remove all records from the report list? (The records stay in the log.)")) return;
    if (NX.replist.clear() === false) return;
    R.sel = new Set(); await load();
  };
  // ---- the report as text: save (with the browser's save dialog where it has one) or copy ----
  const infoMode = () => (admin() ? (document.querySelector("input[name=rp-info]:checked") || {}).value || "omit" : "omit");
  try { const m = localStorage.getItem("nx-rp-info"); const r = m && document.querySelector(`input[name=rp-info][value=${m}]`); if (r) r.checked = true; } catch (e) { /* no storage */ }
  let lastInfo = infoMode();
  document.querySelectorAll("input[name=rp-info]").forEach((r) => {
    r.onchange = () => {
      if (!dropEdits()) { document.querySelector(`input[name=rp-info][value=${lastInfo}]`).checked = true; return; } // keep the edited text and the old choice
      lastInfo = r.value; render(); textChanged();
      try { localStorage.setItem("nx-rp-info", r.value); } catch (e) { /* no storage */ }
    };
  });
  // "include comments": the comment of each record goes at the end of its line in the report (kept in this browser; on by default)
  const commentsOn = () => $("rp-comments").checked;
  try { if (localStorage.getItem("nx-rp-comments") === "0") $("rp-comments").checked = false; } catch (e) { /* no storage */ }
  $("rp-comments").onchange = () => {
    if (!dropEdits()) { $("rp-comments").checked = !$("rp-comments").checked; return; } // keep the edited text and the old choice
    render(); textChanged();
    try { localStorage.setItem("nx-rp-comments", commentsOn() ? "1" : "0"); } catch (e) { /* no storage */ }
  };
  const key = () => NX.replist.ids().join(",") + "|" + infoMode() + "|" + (commentsOn() ? "c" : "");
  // a short message, shown in the Report tab and in the Report text tab
  const note = (t) => { $("rp-msg").textContent = $("re-msg").textContent = t; clearTimeout(note.t); note.t = setTimeout(() => { $("rp-msg").textContent = $("re-msg").textContent = ""; }, 4000); };
  const crlf = (t) => t.replace(/\r?\n/g, "\r\n");
  const generated = async () => {
    const r = await fetch("/api/report/text", { method: "POST", credentials: "same-origin", headers: { "X-Navtex": "1", "Content-Type": "application/json" }, body: JSON.stringify({ ids: NX.replist.ids(), info: infoMode(), comments: commentsOn() }) });
    if (!r.ok) throw new Error(r.statusText);
    return r.text();
  };
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
  $("rp-unedit").onclick = () => { clearEdits(); render(); textChanged(); note("Edits discarded."); };

  // ---- the report text: in the Report text tab or, where that tab is not on screen, in a pop-up that holds the same editor ----
  // The text is made from the list as it is now, and follows it until you edit it. The edit lives in this browser only, never in the log
  // records. In the pop-up, cancel puts back what was there when it opened; accept keeps it.
  const ta = $("re-text"), body = $("re-body"), tabSec = $("tab-retext"), dlg = $("rp-dlg");
  let gen = "", genKey = null, gseq = 0, tt = 0, snap = null, snapText = "";
  ta.readOnly = true; // until the first text has been made
  const stale = () => !!R.edited && R.edited.key !== key(); // records were deleted from the log: the edited text does not follow the list
  const counts = () => {
    const t = ta.value;
    $("re-count").textContent = `${t ? t.split("\n").length : 0} lines, ${t.length} characters${R.edited ? " \u2014 edited" : ""}${stale() ? " \u2014 the list has changed, revert to follow it" : ""}`;
    $("re-revert").disabled = !R.edited;
    $("re-copy").disabled = $("re-save").disabled = !NX.replist.count();
  };
  const setText = (t) => { const y = ta.scrollTop, x = ta.scrollLeft; ta.value = t; ta.scrollTop = y; ta.scrollLeft = x; counts(); };
  async function refresh() {
    const my = ++gseq, k = key();
    let text = "";
    try { if (NX.replist.count()) text = (await generated()).replace(/\r\n/g, "\n"); } catch (e) { note("Cannot make the report: " + e.message); return; }
    if (my !== gseq || R.edited) return; // a newer request, or edits made while this one was running
    gen = text; genKey = k; ta.readOnly = false; setText(gen);
  }
  const showing = () => NX.shown("retext") || dlg.open;
  function textChanged() { // the list or a choice changed: an unedited text is made again, an edited one stays
    counts();
    if (!showing()) return;
    clearTimeout(tt); tt = setTimeout(() => { if (!R.edited) refresh(); else counts(); }, 80);
  }
  ta.addEventListener("input", () => {
    if (ta.value === gen) R.edited = null; else if (R.edited) R.edited.text = ta.value; else R.edited = { text: ta.value, key: genKey };
    counts(); render();
  });
  $("re-revert").onclick = async () => {
    if (R.edited && !confirm("Replace the text with the report as generated from the list?\n\nYour edits are lost.")) return;
    clearEdits(); render(); await refresh(); note("Reverted.");
  };
  $("re-save").onclick = () => $("rp-save").onclick();
  $("re-copy").onclick = () => $("rp-copy").onclick();
  async function openDialog() {
    if (!R.edited && genKey !== key()) await refresh();
    snap = R.edited ? { ...R.edited } : null; snapText = ta.value;
    $("re-host").append(body); counts();
    dlg.showModal(); ta.focus({ preventScroll: true }); ta.setSelectionRange(0, 0); ta.scrollTop = 0;
  }
  $("rp-edit").onclick = () => {
    if (NX.shown("retext")) { ta.focus(); return; } // the tab is on screen: the text is edited there
    openDialog().catch((e) => note("Cannot edit: " + e.message));
  };
  dlg.addEventListener("close", () => { tabSec.append(body); counts(); render(); });
  const cancelDialog = () => {
    if (ta.value !== snapText && !confirm("Discard your changes to the report text?")) return;
    R.edited = snap; if (!snap) genKey = null; ta.value = snapText; counts(); render(); dlg.close();
  };
  $("re-accept").onclick = () => dlg.close();
  $("re-cancel").onclick = $("re-close").onclick = cancelDialog;
  dlg.addEventListener("cancel", (e) => { e.preventDefault(); cancelDialog(); });
  NX.tabs.retext = () => { counts(); if (!R.edited && genKey !== key()) refresh(); };

  NX.listen("role", () => { render(); textChanged(); });
  // in a tiled layout the Report (and its text) can be on screen while records are added in the Log tile
  let rlt = 0;
  NX.listen("replist", () => { if (NX.shown("report")) { clearTimeout(rlt); rlt = setTimeout(load, 80); } textChanged(); });
  NX.tabs.report = load;
})();
