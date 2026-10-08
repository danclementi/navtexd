"use strict";
// The report list: the numbers of the records a user has collected for a reception report. Every user (administrator or
// viewer) has their own list, kept in this browser (localStorage; in memory only if the browser will not store it). The
// server turns the list into rows and text (/api/report/list and /api/report/text).
(() => {
  const NX = window.NX;
  const KEY = "nx-report-ids";
  let ids = [];
  try { const v = JSON.parse(localStorage.getItem(KEY) || "[]"); if (Array.isArray(v)) ids = v.filter((n) => Number.isInteger(n)); } catch (e) { /* no storage */ }
  const save = () => { try { localStorage.setItem(KEY, JSON.stringify(ids)); } catch (e) { /* no storage */ } };
  const changed = () => { save(); (NX.hooks.replist || []).forEach((f) => f(ids)); };
  NX.replist = {
    ids: () => ids.slice(),
    count: () => ids.length,
    add(list) { const have = new Set(ids); let n = 0; for (const id of list) if (!have.has(id)) { have.add(id); ids.push(id); n++; } if (n) changed(); return n; },
    remove(list) { const gone = new Set(list); ids = ids.filter((id) => !gone.has(id)); changed(); },
    clear() { ids = []; changed(); },
    // drop numbers the server no longer knows (records deleted from the log)
    keepOnly(list) { const ok = new Set(list); const n = ids.length; ids = ids.filter((id) => ok.has(id)); if (ids.length !== n) changed(); },
  };
})();
