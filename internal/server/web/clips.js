// Audio clips: the "Save clip" button and the list in the Database tab.
(() => {
  const { $, el, api, state } = NX;
  const admin = () => state.role === "admin";
  const size = (b) => (b >= 1048576 ? (b / 1048576).toFixed(1) + " MB" : Math.round(b / 1024) + " kB");
  let busy = false;

  async function load() {
    const tb = document.querySelector("#clip-table tbody");
    if (!admin()) { $("clipbox").hidden = true; return; }
    $("clipbox").hidden = false;
    let j;
    try { j = await api("GET", "/api/clips"); } catch (e) { $("clip-msg").textContent = e.message; return; }
    tb.replaceChildren();
    j.clips.forEach((c) => {
      const del = el("button", { type: "button", class: "linkbtn", text: "delete", title: "Delete this clip" });
      del.onclick = async () => {
        if (!confirm("Delete " + c.name + "?")) return;
        try { await api("DELETE", "/api/clips/" + encodeURIComponent(c.name)); } catch (e) { $("clip-msg").textContent = e.message; }
        load();
      };
      const a = el("a", { href: "/api/clips/" + encodeURIComponent(c.name), download: c.name, text: c.start ? NX.stamp(c.start).slice(0, 19) : c.name, title: c.name });
      tb.append(el("tr", {}, [el("td", {}, [a]), el("td", { text: c.s ? Math.round(c.s) + " s" : "" }), el("td", { text: size(c.bytes) }), el("td", {}, [del])]));
    });
    $("clip-msg").textContent = j.clips.length ? "Folder: " + j.dir : "No clips yet. Folder: " + j.dir;
  }

  $("b-clip").onclick = async () => {
    if (busy) return;
    const b = $("b-clip"), old = b.textContent;
    let wait;
    try { wait = (await api("POST", "/api/clips")).wait_s; } catch (e) { b.textContent = "Not saved"; b.title = e.message; setTimeout(() => { b.textContent = old; }, 3000); return; }
    busy = true; b.disabled = true;
    const end = Date.now() + wait * 1000;
    const tick = () => {
      const left = Math.ceil((end - Date.now()) / 1000);
      if (left > 0) { b.textContent = "Recording " + left + " s"; setTimeout(tick, 250); return; }
      b.textContent = "Saved";
      setTimeout(() => { b.textContent = old; b.disabled = !admin(); busy = false; load(); }, 2000);
    };
    tick();
  };
  document.querySelector("button[data-tab=database]").addEventListener("click", load);
})();
