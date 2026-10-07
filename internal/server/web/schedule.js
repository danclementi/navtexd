"use strict";
// Schedule tab and the "on air now" bar under the tabs, built from the station table.
(() => {
  const NX = window.NX, $ = NX.$, el = NX.el;
  const S = { freq: 518, areas: null, loaded: false };
  const pref = {
    get(k, d) { try { const v = localStorage.getItem("navtex." + k); return v === null ? d : JSON.parse(v); } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem("navtex." + k, JSON.stringify(v)); } catch (e) { /* no storage */ } },
  };
  const now = () => new Date(Date.now() + NX.state.offset);
  const minutes = (hhmm) => { const [h, m] = hhmm.split(":").map(Number); return h * 60 + m; };
  const hhmm = (m) => `${NX.pad(Math.floor(m / 60) % 24)}:${NX.pad(m % 60)}`;
  const list = () => NX.stationList();
  const areasWith = () => new Set(list().map((s) => s.area));
  const areasAll = () => [...new Set([...Array(21).keys()].map((i) => i + 1).concat([...areasWith()]))].sort((a, b) => a - b);
  const areaOn = (a) => !S.areas || S.areas.has(a);
  const label = (s, brg, km) => `${s.area}${s.b1} ${s.name},${s.cc}` + (km && s.km ? ` ${s.km}km` : "") + (brg && s.brg ? ` ${s.brg}°` : "");
  const onAir = (s, m) => s.times.some((t) => ((m - minutes(t)) % 1440 + 1440) % 1440 < 10);

  function bar() {
    const b = $("sched-text");
    if (!list().length) { b.textContent = ""; return; }
    const f = NX.state.controls ? NX.state.controls.channel : 518;
    const d = now(), m = d.getUTCHours() * 60 + d.getUTCMinutes();
    const on = list().filter((s) => s.freq === f && areaOn(s.area) && onAir(s, m));
    b.textContent = `SCHEDULE ${f}kHz ${hhmm(m)}UTC  ` + (on.length ? on.map((s) => label(s, false, false)).join("   ") : "no station on air");
  }

  function renderAreas() {
    const box = $("sc-areas");
    box.replaceChildren();
    for (const a of areasAll()) {
      const has = areasWith().has(a);
      const cb = el("input", { type: "checkbox", checked: areaOn(a), disabled: !has });
      cb.onchange = () => {
        const cur = S.areas || new Set(areasAll());
        if (cb.checked) cur.add(a); else cur.delete(a);
        S.areas = cur; pref.set("sched.areas", [...cur]); table(); bar();
      };
      box.append(el("label", { class: "check" + (has ? "" : " dim"), title: has ? "NAVAREA " + a : `NAVAREA ${a}: no stations in the station table` }, cb, String(a)));
    }
  }

  function table() {
    const tb = $("sc-table");
    tb.replaceChildren();
    const st = list().filter((s) => s.freq === S.freq);
    const areas = [...new Set(st.map((s) => s.area))].filter(areaOn).sort((a, b) => a - b);
    const brg = $("sc-brg").checked, km = $("sc-km").checked;
    const head = el("tr", {}, el("th", { class: "slot", text: "Slot (UTC, every 4 h)" }));
    for (const a of areas) head.append(el("th", { text: "NAVAREA " + a }));
    tb.append(el("thead", {}, head));
    const body = el("tbody");
    for (let off = 0; off < 240; off += 10) {
      const tr = el("tr", { "data-off": off });
      const times = [0, 1, 2, 3, 4, 5].map((i) => hhmm(off + 240 * i));
      tr.append(el("td", { class: "slot", text: times.join(" ") }));
      for (const a of areas) {
        const td = el("td");
        for (const s of st) {
          if (s.area === a && s.times.some((t) => Math.floor((minutes(t) % 240) / 10) * 10 === off)) td.append(el("span", { class: "st", text: label(s, brg, km), title: `${label(s, true, true)}\n${s.call}  ${s.times.join(" ")}` }));
        }
        tr.append(td);
      }
      body.append(tr);
    }
    tb.append(body);
    mark(false);
  }

  function mark(scroll) {
    const d = now(), cur = Math.floor(((d.getUTCHours() * 60 + d.getUTCMinutes()) % 240) / 10) * 10;
    $("sc-utc").textContent = `now ${hhmm(d.getUTCHours() * 60 + d.getUTCMinutes())} UTC`;
    document.querySelectorAll("#sc-table tbody tr").forEach((tr) => {
      const isNow = Number(tr.dataset.off) === cur;
      tr.classList.toggle("now", isNow);
      if (isNow && scroll) tr.scrollIntoView({ block: "center", behavior: "smooth" });
    });
  }

  async function start() {
    if (!list().length) await NX.loadStations();
    S.areas = pref.get("sched.areas", null) ? new Set(pref.get("sched.areas", [])) : null;
    $("sc-brg").checked = pref.get("sched.brg", false);
    $("sc-km").checked = pref.get("sched.km", true);
    $("sc-brg-wrap").hidden = !list().some((s) => s.brg);
    $("sc-km-wrap").hidden = !list().some((s) => s.km);
    renderAreas(); table(); bar();
  }

  $("sc-freq").onchange = () => { S.freq = Number($("sc-freq").value); table(); };
  $("sc-brg").onchange = () => { pref.set("sched.brg", $("sc-brg").checked); table(); };
  $("sc-km").onchange = () => { pref.set("sched.km", $("sc-km").checked); table(); };
  const setAll = (on) => { S.areas = on ? null : new Set(); pref.set("sched.areas", on ? null : []); renderAreas(); table(); bar(); };
  $("sc-all").onclick = () => setAll(true); $("sc-none").onclick = () => setAll(false);
  $("sc-now").onclick = () => mark(true);
  NX.tabs.schedule = () => { $("sc-freq").value = String(NX.state.controls ? NX.state.controls.channel : 518); S.freq = Number($("sc-freq").value); start().then(() => mark(true)); };
  NX.listen("hello", () => { start().catch(() => {}); });
  NX.listen("controls", () => bar());
  NX.hooks.stationsLoaded = (NX.hooks.stationsLoaded || []).concat([() => { $("sc-brg-wrap").hidden = !list().some((s) => s.brg); $("sc-km-wrap").hidden = !list().some((s) => s.km); if (!$("tab-schedule").hidden) { renderAreas(); table(); } bar(); }]);
  setInterval(() => { bar(); if (!$("tab-schedule").hidden) mark(false); }, 10000);
})();
