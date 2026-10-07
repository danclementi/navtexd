"use strict";
// Status tab: double-click a recent record to read it in a dialog; the arrows step through the log.
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el, api = NX.api;
  const dlg = $("rec-dlg");
  const D = { i: 0, total: 0, busy: false };

  async function show(i) {
    if (D.busy || i < 0) return;
    D.busy = true;
    try {
      const j = await api("GET", `/api/messages?limit=1&offset=${i}&order=newest`);
      D.total = j.total;
      if (!j.records.length) return;
      const r = await api("GET", "/api/messages/" + j.records[0].id);
      D.i = i;
      const dl = $("dlg-info");
      dl.replaceChildren();
      const add = (k, v) => dl.append(el("dt", { text: k }), el("dd", { text: v }));
      add("Received", NX.stamp(Date.parse(r.rx)) + " UTC");
      add("Frequency", r.ch + " kHz");
      add("Header", (r.b1 || "?") + (r.b2 || "?") + (r.ser || "??"));
      add("Station", r.st);
      add("Quality", r.q.toFixed(1));
      if (r.km) add("Distance", `${r.km.toFixed(0)} km, bearing ${r.brg.toFixed(0)}°`);
      if (r.cmt) add("Comment", r.cmt);
      if (r.rev && r.rev.length) add("Revised", `${r.rev.length} stretch${r.rev.length > 1 ? "es" : ""} replaced by the operator (in <angle brackets>)`);
      $("dlg-title").textContent = `#${r.id}  ${r.kind}   (${i + 1} of ${D.total})`;
      NX.fillRecordText($("dlg-text"), r, { cor: true });
      $("dlg-text").scrollTop = 0;
      $("dlg-newer").disabled = i === 0;
      $("dlg-older").disabled = i + 1 >= D.total;
    } catch (e) {
      $("dlg-text").textContent = e.message;
    } finally { D.busy = false; }
  }

  document.querySelector("#recent tbody").addEventListener("dblclick", (e) => {
    const tr = e.target.closest("tr[data-i]");
    if (!tr) return;
    window.getSelection().removeAllRanges();
    if (!dlg.open) dlg.showModal();
    show(Number(tr.dataset.i));
  });
  $("dlg-older").onclick = () => show(D.i + 1);
  $("dlg-newer").onclick = () => show(D.i - 1);
  $("dlg-close").onclick = () => dlg.close();
  dlg.addEventListener("keydown", (e) => {
    if (e.key === "ArrowLeft") { e.preventDefault(); show(D.i + 1); }
    else if (e.key === "ArrowRight") { e.preventDefault(); show(D.i - 1); }
  });
  dlg.addEventListener("click", (e) => { if (e.target === dlg) dlg.close(); }); // a click on the backdrop
})();
