#!/usr/bin/env python3
"""Browser smoke test of the web interface (Log tab, record pop-up, undo, selection).

Needs: python3 with playwright and a Chromium, a built navtexd, and a short WAV recording that decodes to a message.
  python3 tools/uitest/smoke.py --exe ./navtexd --wav clip.wav [--chromium /opt/pw-browsers/chromium]
It stores the recording six times in a scratch data folder, starts the server on a spare port and checks what the
operator does most: stepping through a filtered list after an edit takes a record out of it, the numbering, DELETE and
UNDO (deletions and edits), "Use selection", Ctrl-A, and what can be selected. Exit status 0 = all passed.
"""
import argparse, os, subprocess, sys, tempfile, time
from playwright.sync_api import sync_playwright

ap = argparse.ArgumentParser()
ap.add_argument("--exe", required=True)
ap.add_argument("--wav", required=True)
ap.add_argument("--chromium", default=None)
ap.add_argument("--port", default="8197")
a = ap.parse_args()

data = tempfile.mkdtemp(prefix="navtexd-ui-")
for _ in range(6):  # six records with the same time: they are ordered by number
    s = subprocess.Popen([a.exe, "-data", data, "-listen", "off", "-in", a.wav], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(6)  # the program keeps running after the recording ends
    s.terminate(); s.wait()
srv = subprocess.Popen([a.exe, "-data", data, "-listen", "127.0.0.1:" + a.port], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
time.sleep(2)
URL = "http://127.0.0.1:" + a.port + "/"
fails = []
def check(name, ok, info=""):
    print(("PASS " if ok else "FAIL ") + name + ((" - " + str(info)) if info and not ok else ""))
    if not ok: fails.append(name)

def title(pg): return pg.inner_text("#rec-title")
def num(pg):  # "incomplete 4 of 6 (0x...)" -> (4, 6)
    t = title(pg).split(" of ")
    return int(t[0].split()[-1]), int(t[1].split()[0])
def open_row(pg, i):
    rows = pg.locator("#logscroll tbody tr"); rows.nth(i).locator("td").nth(1).click(); pg.wait_for_timeout(300)
    pg.keyboard.press("Enter"); pg.wait_for_timeout(700)
def start(b):
    pg = b.new_page(viewport={"width": 1400, "height": 900})
    pg.on("pageerror", lambda e: check("no page error", False, e))
    pg.on("dialog", lambda d: (check("no unexpected confirm: " + d.message[:40], False), d.dismiss()))
    pg.goto(URL); pg.wait_for_timeout(1200)
    pg.click("button[data-tab=log]"); pg.wait_for_timeout(600)
    return pg
def hexof(pg): return title(pg)[title(pg).index("(0x"):]

try:
    with sync_playwright() as p:
        b = p.chromium.launch(executable_path=a.chromium) if a.chromium else p.chromium.launch()
        pg = start(b)
        tabs = pg.inner_text("#tabs")
        check("tab names", "Monitor" in tabs and "Station List" in tabs)
        # the toolbar stays put: no control moves or changes size when the filter, the selection or the order change
        ids = ["f-btn", "f-edit", "n-chrono", "n-reverse", "n-latest", "n-date", "n-undo", "n-open", "n-report", "n-delete", "n-db"]
        def boxes(): return pg.evaluate("ids => Object.fromEntries(ids.map(i => { const r = document.getElementById(i).getBoundingClientRect(); return [i, [Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height)]]; }))", ids)
        def same(name):
            now = boxes(); moved = [i for i in ids if now[i] != base[i]]
            check("toolbar stays put: " + name, not moved, [(i, base[i], now[i]) for i in moved])
        base = boxes()
        if os.environ.get("SHOT"): pg.screenshot(path=os.environ["SHOT"] + "-1.png", clip=dict(zip(("x", "y", "width", "height"), (0, pg.evaluate("document.querySelector('.lognav').getBoundingClientRect().y - 6"), 1400, 140))))
        def visible(name):  # both toolbar rows keep their height and lie above the list, however short the window is
            g = pg.evaluate("(() => { const r = i => document.getElementById(i).getBoundingClientRect(); return [r('filters').bottom, r('n-open').height, r('n-open').bottom, r('logscroll').top]; })()")
            check("toolbar not covered: " + name, g[1] >= 40 and g[2] <= g[3] + 1 and g[0] <= g[3], g)
        h2 = pg.evaluate("(() => { const a = document.querySelector('.lognav').getBoundingClientRect(), b = document.getElementById('filters').getBoundingClientRect(); return [a.bottom, b.top, a.height, b.height]; })()")
        check("button row on top, filter row below and lower", h2[0] <= h2[1] + 1 and h2[3] < h2[2], h2)
        pg.evaluate("document.querySelector('#log-table').style.height = '6000px'")  # a long list, as in real use: it is taller than the window
        visible("tall window")
        for h in (700, 520):
            pg.set_viewport_size({"width": 1400, "height": h}); pg.wait_for_timeout(400); visible("%d px high" % h)
        pg.set_viewport_size({"width": 1400, "height": 900}); pg.wait_for_timeout(400)
        pg.evaluate("document.querySelector('#log-table').style.height = ''")
        pg.locator("#logscroll tbody tr").nth(1).locator("td").nth(1).click(); pg.wait_for_timeout(300); same("one record selected")
        pg.click("#logscroll"); pg.keyboard.press("Control+a"); pg.wait_for_timeout(300); same("all records selected")
        pg.click("#n-reverse"); pg.wait_for_timeout(500); same("reverse order")
        check("reverse button is pressed", pg.get_attribute("#n-reverse", "aria-pressed") == "true" and pg.get_attribute("#n-chrono", "aria-pressed") == "false")
        pg.click("#n-chrono"); pg.wait_for_timeout(400)
        pg.click("#f-edit"); pg.evaluate("const e=document.getElementById('f-b1'); e.value='ZZ'; e.dispatchEvent(new Event('input',{bubbles:true}))"); pg.click("#f-apply"); pg.wait_for_timeout(800)
        same("filter on"); check("filter button is pressed", pg.get_attribute("#f-btn", "aria-pressed") == "true")
        if os.environ.get("SHOT"): pg.screenshot(path=os.environ["SHOT"] + "-2.png", clip=dict(zip(("x", "y", "width", "height"), (0, pg.evaluate("document.querySelector('.lognav').getBoundingClientRect().y - 6"), 1400, 140))))
        pg.click("#f-btn"); pg.wait_for_timeout(600); same("filter switched off"); check("filter button is released", pg.get_attribute("#f-btn", "aria-pressed") == "false")
        pg.click("#f-edit"); pg.click("#f-clear"); pg.click("#f-apply"); pg.wait_for_timeout(600)
        pg.click("#f-btn") if pg.get_attribute("#f-btn", "aria-pressed") == "true" else None
        pg.keyboard.press("Escape")
        # numbering follows time, in both orders; left = earlier, right = later
        for order in ("oldest", "newest"):
            pg.click("#n-chrono" if order == "oldest" else "#n-reverse"); pg.wait_for_timeout(500)
            rows = pg.locator("#logscroll tbody tr"); open_row(pg, rows.count() - 1 if order == "oldest" else 0)
            check(order + ": newest is 6 of 6", num(pg) == (6, 6), title(pg))
            pg.click("#ld-prev"); pg.wait_for_timeout(600); check(order + ": left goes to 5 of 6", num(pg) == (5, 6), title(pg))
            pg.click("#ld-next"); pg.wait_for_timeout(600); check(order + ": right goes back to 6 of 6", num(pg) == (6, 6), title(pg))
            pg.keyboard.press("Escape"); pg.wait_for_timeout(300)
        # a filtered list: an edit that removes the record must not skip its neighbour
        pg.click("#n-chrono"); pg.wait_for_timeout(400)
        pg.click("#f-edit"); pg.evaluate("const e=document.getElementById('f-b1'); e.value='E'; e.dispatchEvent(new Event('input',{bubbles:true}))"); pg.click("#f-apply"); pg.wait_for_timeout(800)
        open_row(pg, 2)
        h3 = hexof(pg)
        pg.fill("#rec-info input.mini >> nth=0", "F"); pg.keyboard.press("Enter"); pg.wait_for_timeout(1200)
        check("removed record stays shown, marked", "no longer in the filtered list" in title(pg), title(pg))
        tip = pg.get_attribute("#ld-undo", "title") or ""
        check("undo tooltip names the edit and the message", "B1 change" in tip and h3.strip("()") in tip, tip)
        pg.click("#ld-next"); pg.wait_for_timeout(800)
        check("right after the removal goes to the next original record", num(pg) == (3, 5) and hexof(pg) != h3, title(pg))
        # UNDO takes the edit back and shows that record again
        pg.click("#ld-undo"); pg.wait_for_timeout(1200)
        check("undo of the edit shows the record again", hexof(pg) == h3 and num(pg) == (3, 6), title(pg))
        # DELETE without a question, then UNDO
        pg.click("#ld-delete"); pg.wait_for_timeout(1000)
        total = pg.evaluate("fetch('/api/messages?limit=1').then(r => r.json()).then(j => j.total)")
        check("delete leaves 5", total == 5, total)
        pg.click("#ld-undo"); pg.wait_for_timeout(1200)
        check("undo of the deletion: same record, same place", hexof(pg) == h3 and num(pg) == (3, 6), title(pg))
        # From / To keep date and time (UTC) between visits
        pg.keyboard.press("Escape"); pg.wait_for_timeout(300)
        pg.click("#f-edit"); pg.fill("#f-fromt", ""); pg.type("#f-fromt", "3299")
        check("impossible time digits are refused", pg.input_value("#f-fromt") == "2", pg.input_value("#f-fromt"))
        pg.fill("#f-from", "2026-09-29"); pg.fill("#f-fromt", "00:10"); pg.fill("#f-to", "2026-09-29")
        for i in ("#f-from", "#f-fromt", "#f-to"): pg.dispatch_event(i, "change")
        pg.reload(); pg.wait_for_timeout(1200); pg.click("button[data-tab=log]"); pg.wait_for_timeout(500); pg.click("#f-edit")
        check("From and To are remembered", pg.input_value("#f-from") == "2026-09-29" and pg.input_value("#f-fromt") == "00:10" and pg.input_value("#f-to") == "2026-09-29", pg.input_value("#f-fromt"))
        pg.click("#f-clear"); pg.dispatch_event("#f-from", "change"); pg.dispatch_event("#f-fromt", "change"); pg.dispatch_event("#f-to", "change"); pg.click("#f-apply"); pg.wait_for_timeout(600)
        open_row(pg, 2)
        # the station area: fixed fields, no distance or bearing, at most two candidates plus the list
        info = pg.inner_text("#rec-info"); pg.screenshot(path="/tmp/rec.png")
        check("pop-up has Set by, no distance/bearing", "Set by" in info and "Msg ID#" in info and "Distance" not in info and "bearing" not in info.lower() and " km" not in info, info)
        check("at most two candidate buttons plus one for the list", pg.locator("#rec-info .cands button").count() <= 3, pg.locator("#rec-info .cands button").count())
        check("time slot text", "time slot" in info, info)
        # Use selection
        box = pg.locator("#rec-text .tx").first.bounding_box()
        pg.mouse.move(box["x"] + 5, box["y"] + 8); pg.mouse.down(); pg.mouse.move(box["x"] + 120, box["y"] + 8, steps=8); pg.mouse.up()
        pg.click("#rev-sel"); pg.wait_for_timeout(300)
        check("Use selection fills the reverser", pg.input_value("#rev-in").strip() != "", pg.input_value("#rev-in"))
        check("the pop-up title is not selectable", pg.evaluate("getComputedStyle(document.getElementById('rec-title')).userSelect") == "none")
        pg.keyboard.press("Escape"); pg.wait_for_timeout(300)
        # Ctrl-A in the table
        pg.locator("#logscroll tbody tr").first.locator("td").nth(1).click(); pg.keyboard.press("Control+a"); pg.wait_for_timeout(800)
        check("Ctrl-A selects the whole list", "6 records selected" in pg.inner_text("#n-selinfo") or "5 records selected" in pg.inner_text("#n-selinfo"), pg.inner_text("#n-selinfo"))
        # a click on a small spectrum only selects the channel
        pg.click("#spec-mini"); pg.wait_for_timeout(600)
        check("click on the small spectrum does not change the tab", pg.evaluate("document.querySelector('#tabs button.on').dataset.tab") == "log")
        # advanced options: hidden until "sudo" is typed in the admin box, and hidden again by "exit"
        def legends():
            pg.click("button[data-tab=options]"); pg.wait_for_timeout(800); pg.click("#opt-tabs button >> nth=1"); pg.wait_for_timeout(300)
            return pg.inner_text("#opt-form").lower()
        t = legends()
        check("Receiver/Decoder/Down-converter hidden by default", "receiver (fsk" not in t and "decoder" not in t and "down-converter" not in t and "channel: frequency" in t)
        pg.click("#role"); pg.type("#adm-in", "SUDO"); pg.keyboard.press("Enter"); pg.click("#adm-close"); pg.wait_for_timeout(800)
        pg.click("#opt-tabs button >> nth=1"); t = pg.inner_text("#opt-form").lower()
        check("sudo shows them, with a note", "receiver (fsk" in t and "decoder" in t and "down-converter" in t and "advanced options are shown" in t)
        pg.reload(); pg.wait_for_timeout(1200); t = legends()
        check("sudo survives a reload", "receiver (fsk" in t)
        pg.click("#role"); pg.type("#adm-in", "exit"); pg.keyboard.press("Enter"); pg.click("#adm-close"); pg.wait_for_timeout(800)
        pg.click("#opt-tabs button >> nth=1"); t = pg.inner_text("#opt-form").lower()
        check("exit hides them again", "receiver (fsk" not in t and "advanced options are shown" not in t)
        pg.click("button[data-tab=log]"); pg.wait_for_timeout(300)
        pg.click(".specgrp[data-ch='0'] .m-lock"); pg.wait_for_timeout(800)
        check("LOCKED -> MANUAL opens Spectrum", pg.evaluate("document.querySelector('#tabs button.on').dataset.tab") == "spectrum")
        pg.click("button[data-tab=log]"); pg.wait_for_timeout(300)
        pg.click(".specgrp[data-ch='0'] .m-lock"); pg.wait_for_timeout(800)
        check("MANUAL -> AUTO stays on the tab", pg.evaluate("document.querySelector('#tabs button.on').dataset.tab") == "log")
        b.close()
finally:
    srv.terminate()
print("\n%d failed" % len(fails) if fails else "\nall passed")
sys.exit(1 if fails else 0)
