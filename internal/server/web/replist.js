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
  // guard (set by report.js) asks before a change of the list that would throw away the edits of the report text; it says whether to go on
  const ok = () => !NX.replist.guard || NX.replist.guard();
  NX.replist = {
    ids: () => ids.slice(),
    count: () => ids.length,
    // returns how many were added, 0 if they were all there, -1 if the change was refused (the report text has edits)
    add(list) { const have = new Set(ids), fresh = []; for (const id of list) if (!have.has(id)) { have.add(id); fresh.push(id); } if (!fresh.length) return 0; if (!ok()) return -1; ids.push(...fresh); changed(); return fresh.length; },
    // remove and clear return false if the change was refused
    remove(list) { const gone = new Set(list); if (!ids.some((id) => gone.has(id))) return true; if (!ok()) return false; ids = ids.filter((id) => !gone.has(id)); changed(); return true; },
    clear() { if (!ids.length) return true; if (!ok()) return false; ids = []; changed(); return true; },
    // drop numbers the server no longer knows (records deleted from the log)
    keepOnly(list) { const known = new Set(list); const n = ids.length; ids = ids.filter((id) => known.has(id)); if (ids.length !== n) changed(); },
  };
})();
