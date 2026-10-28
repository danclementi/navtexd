"use strict";
// Extra parts of the Options tab: selective logging.
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el;

  NX.optionsExtra = async (box) => {
    box.replaceChildren();
    if (NX.state.role !== "admin") return;
    let stations, logged;
    try {
      await NX.loadStations();
      stations = NX.stationList();
      const off = new Set((await NX.api("GET", "/api/logging")).disabled);
      logged = new Set(stations.filter((s) => !off.has(s.id)).map((s) => s.id));
    } catch (e) { return; }
    const grid = el("div", { class: "sellog" });
    const freq = el("select", {}, new Option("all frequencies", ""), new Option("518", "518"), new Option("490", "490"), new Option("486", "486"), new Option("424", "424"));
    const msg = el("span", { class: "small" });
    const visible = () => stations.filter((s) => !freq.value || String(s.freq) === freq.value);
    const count = () => { msg.textContent = `${stations.length - logged.size} of ${stations.length} stations are not logged`; };
    function render() {
      grid.replaceChildren();
      for (const s of visible()) {
        const cb = el("input", { type: "checkbox", checked: logged.has(s.id) });
        cb.onchange = () => { if (cb.checked) logged.add(s.id); else logged.delete(s.id); count(); };
        grid.append(el("label", { title: `NAVAREA ${s.area}` }, cb, `${s.freq} ${s.area}${s.b1} ${s.call} ${s.name},${s.cc}`));
      }
      count();
    }
    const all = el("button", { type: "button", text: "Check all" }), none = el("button", { type: "button", text: "Uncheck all" }), save = el("button", { type: "button", text: "Save selection" });
    all.onclick = () => { visible().forEach((s) => logged.add(s.id)); render(); };
    none.onclick = () => { visible().forEach((s) => logged.delete(s.id)); render(); };
    freq.onchange = render;
    save.onclick = async () => {
      try { await NX.api("PUT", "/api/logging", { disabled: stations.filter((s) => !logged.has(s.id)).map((s) => s.id) }); msg.textContent = "Saved."; }
      catch (e) { msg.textContent = "Not saved: " + e.message; }
    };
    const d = el("details", { open: true }, el("summary", { text: "Selective logging" }),
      el("p", { class: "small", text: "Messages from unchecked stations are identified but not stored. Use this to leave out strong local stations." }),
      el("div", { class: "row" }, freq, all, none, save, msg), grid);
    box.append(d);
    render();
  };
})();
