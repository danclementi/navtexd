// The Scheduler page of the Options tab: a list of "set this setting to this value at this time of day, every day".
// The server keeps and runs the list (so it works with no browser open); this page only edits it.
(() => {
  const NX = window.NX, $ = (id) => document.getElementById(id);
  const mk = (tag, props, ...kids) => NX.el(tag, props, ...kids);
  const tmOk = (v) => /^([01]\d|2[0-3]):[0-5]\d$/.test(v);
  const spanWord = (m) => (m === 0 ? "" : (m < 0 ? "-" : "+") + (Math.abs(m) % 60 === 0 ? Math.abs(m) / 60 + "h" : Math.abs(m) + "m"));
  const spanText = (m) => (m === 0 ? "" : (m < 0 ? " − " : " + ") + Math.floor(Math.abs(m) / 60) + ":" + String(Math.abs(m) % 60).padStart(2, "0"));
  // "sunset-1h" -> "sunset − 1:00"; a clock time stays as it is
  const atText = (at) => {
    const m = /^(sunrise|sunset|midnight)(?:([+-])(\d+)([hm]))?$/.exec(at);
    if (!m) return at;
    const n = m[2] ? (m[2] === "-" ? -1 : 1) * Number(m[3]) * (m[4] === "h" ? 60 : 1) : 0;
    return m[1] + spanText(n);
  };
  const S = { entries: [], targets: [], sun: false, zone: "", abbr: "", max: 200, maxValue: 300 };
  let root = null;

  async function put(entries) {
    const j = await NX.api("PUT", "/api/schedule", { entries });
    S.entries = j.entries; S.targets = j.targets; S.sun = j.sun; S.max = j.max; S.maxValue = j.max_value;
  }
  const say = (t, bad) => { const el = $("sdl-msg"); if (el) { el.textContent = t || ""; el.classList.toggle("bad", !!bad); } };
  const plain = (e) => ({ id: e.id, param: e.param, zone: e.zone, at: e.at, value: e.value, off: e.off || undefined });

  function build(page) {
    root = page;
    page.replaceChildren();
    page.append(
      mk("p", { class: "small", text: "Each line sets a setting to a value at a time of day, every day. A time is a clock time in UTC or in the receiver's local time; local times may also be relative to sunrise, sunset or local midnight (the sun times need the position under Options, Station). Every setting takes the value of its latest line when navtexd starts. Changing the list does not change a setting by itself; the next time of a line does. Settings changed by hand stay as they are until then." })
    );
    // adding a line: two rows of a grid, so the controls line up
    const tgt = mk("select", { id: "sdl-param", title: "The setting to change" });
    for (const t of S.targets) tgt.add(new Option(t.label, t.path));
    const val = mk("input", { id: "sdl-value", type: "text", maxlength: String(S.maxValue), placeholder: "the new value (may be empty)", title: "What the setting becomes at that time" });
    const zone = mk("select", { id: "sdl-zone", title: "UTC, or the receiver's local time (Options, Station: time zone)" }, mk("option", { value: "utc", text: "UTC" }), mk("option", { value: "local", text: "Local" }));
    const tm = mk("input", { id: "sdl-time", class: "tm", type: "text", maxlength: "5", placeholder: "hh:mm", title: "Time of day, 24 hours" });
    const rel = mk("select", { id: "sdl-rel", class: "rel", title: "Relative to the local sunrise, sunset or midnight. Fixed = the time typed in the box" });
    rel.add(new Option("fixed", ""));
    const grp = (label, name, from, to, step) => {
      const g = mk("optgroup", { label });
      for (let m = from; m <= to; m += step) g.append(new Option(name + spanText(m), name + spanWord(m)));
      rel.append(g); return g;
    };
    grp("Sunrise", "sunrise", -180, 180, 30); grp("Sunset", "sunset", -180, 180, 30); grp("Local midnight", "midnight", -360, 360, 30);
    const add = mk("button", { id: "sdl-add", type: "button", text: "Add", title: "Add the line to the schedule" });
    const info = mk("span", { id: "sdl-zinfo", class: "small" });
    page.append(mk("div", { class: "scgrid" },
      mk("span", { class: "fl", text: "Set" }), mk("span", { class: "scrow" }, tgt, mk("span", { text: "to" }), val),
      mk("span", { class: "fl", text: "At" }), mk("span", { class: "scrow" }, zone, tm, rel, add, info),
      mk("span", { class: "fl" }), mk("span", { id: "sdl-msg", class: "small scmsg" })));
    page.append(mk("div", { id: "sdl-table" }));
    // 24-hour time: impossible digits are refused as they are typed, the colon is added
    tm.addEventListener("input", () => {
      if (rel.value) rel.value = "";
      const raw = tm.value.replace(/\D/g, "").slice(0, 4); let d = "";
      for (const ch of raw) { const n = d.length, x = +ch; if (n === 0 ? x > 2 : n === 1 ? (d[0] === "2" && x > 3) : n === 2 ? x > 5 : false) break; d += ch; }
      tm.value = d.length > 2 ? d.slice(0, 2) + ":" + d.slice(2) : d; tm.classList.remove("bad");
    });
    const decorate = () => {
      const loc = zone.value === "local";
      rel.disabled = !loc;
      if (!loc) rel.value = "";
      for (const g of rel.querySelectorAll("optgroup")) { const sun = g.label !== "Local midnight"; for (const o of g.children) o.disabled = sun && !S.sun; }
      rel.title = loc ? (S.sun ? "Relative to the local sunrise, sunset or midnight. Fixed = the time typed in the box" : "Sunrise and sunset need the receiving position (Options, Station); midnight does not") : "Only local times can be relative";
      zone.options[1].textContent = "Local" + (S.abbr ? " (" + S.abbr + ")" : "");
      info.textContent = loc && S.zone ? S.zone : "";
    };
    // a relative time shows what it is today
    const refresh = async () => {
      decorate();
      if (!rel.value) return;
      try { tm.value = (await NX.api("GET", "/api/when?v=" + encodeURIComponent("local:today@" + rel.value))).local_time; tm.classList.remove("bad"); tm.title = ""; }
      catch (e) { tm.value = ""; tm.title = e.message; tm.classList.add("bad"); }
    };
    zone.onchange = refresh; rel.onchange = refresh; decorate();
    add.onclick = async () => {
      const at = rel.value || tm.value.trim();
      if (!tgt.value) return say("Choose the setting.", true);
      if (!rel.value && !tmOk(at)) { tm.classList.add("bad"); return say("Give the time as hh:mm, or choose a sunrise, sunset or midnight time.", true); }
      try { await put([...S.entries.map(plain), { param: tgt.value, zone: zone.value, at, value: val.value }]); say("Added."); paint(); }
      catch (e) { say(e.message, true); }
    };
    val.addEventListener("keydown", (e) => { if (e.key === "Enter") add.click(); });
  }

  function paint() {
    const box = $("sdl-table"); if (!box) return;
    const label = (p) => (S.targets.find((t) => t.path === p) || { label: p }).label;
    const rows = [...S.entries].sort((a, b) => (a.off ? 1 : 0) - (b.off ? 1 : 0) || (a.next_utc || "").localeCompare(b.next_utc || ""));
    if (!rows.length) { box.replaceChildren(mk("p", { class: "small", text: "The schedule is empty." })); return; }
    const head = ["On", "Setting", "New value", "At", "Next", ""].map((t) => mk("span", { class: "sch", text: t }));
    const cells = [...head];
    for (const e of rows) {
      const on = mk("input", { type: "checkbox", title: "Switch the line off or on without deleting it" }); on.checked = !e.off;
      on.onchange = async () => { try { await put(S.entries.map((x) => ({ ...plain(x), off: x.id === e.id ? !on.checked || undefined : x.off || undefined }))); paint(); say(""); } catch (er) { say(er.message, true); on.checked = !on.checked; } };
      const val = mk("input", { type: "text", class: "scval", value: e.value, maxlength: String(S.maxValue), title: "Edit the value; it is saved when you leave the box" });
      val.onchange = async () => { try { await put(S.entries.map((x) => ({ ...plain(x), value: x.id === e.id ? val.value : x.value }))); paint(); say("Saved."); } catch (er) { say(er.message, true); } };
      const del = mk("button", { type: "button", class: "scdel", text: "Delete", title: "Remove this line" });
      del.onclick = async () => { try { await put(S.entries.filter((x) => x.id !== e.id).map(plain)); paint(); say(""); } catch (er) { say(er.message, true); } };
      const nxt = e.off ? "off" : e.next_local ? e.next_local.slice(0, 10) + " " + e.next_local.slice(11) + " local · " + e.next_utc.slice(11) + " UTC" : "–";
      cells.push(mk("span", { class: "sc" + (e.off ? " dim" : "") }, on), mk("span", { class: "sc" + (e.off ? " dim" : ""), text: label(e.param) }), mk("span", { class: "sc" }, val),
        mk("span", { class: "sc" + (e.off ? " dim" : ""), text: atText(e.at) + " " + (e.zone === "utc" ? "UTC" : "local") }), mk("span", { class: "sc small", text: nxt }), mk("span", { class: "sc" }, del));
    }
    box.replaceChildren(mk("div", { class: "sctable" }, ...cells));
  }

  async function load() {
    const [j, w] = await Promise.all([NX.api("GET", "/api/schedule"), NX.api("GET", "/api/when").catch(() => ({}))]);
    S.entries = j.entries; S.targets = j.targets; S.sun = j.sun; S.max = j.max; S.maxValue = j.max_value; S.zone = w.zone || ""; S.abbr = w.abbr || "";
  }

  NX.scheduler = {
    mount: async (page) => {
      try { await load(); build(page); paint(); }
      catch (e) { page.replaceChildren(mk("p", { class: "small bad", text: "The schedule could not be loaded: " + e.message })); }
    },
  };
})();
