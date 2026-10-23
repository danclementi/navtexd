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
        # numbering follows time, in both orders; left = earlier, right = later
        for order in ("oldest", "newest"):
            pg.select_option("#n-order", order); pg.wait_for_timeout(500)
            rows = pg.locator("#logscroll tbody tr"); open_row(pg, rows.count() - 1 if order == "oldest" else 0)
            check(order + ": newest is 6 of 6", num(pg) == (6, 6), title(pg))
            pg.click("#ld-prev"); pg.wait_for_timeout(600); check(order + ": left goes to 5 of 6", num(pg) == (5, 6), title(pg))
            pg.click("#ld-next"); pg.wait_for_timeout(600); check(order + ": right goes back to 6 of 6", num(pg) == (6, 6), title(pg))
            pg.keyboard.press("Escape"); pg.wait_for_timeout(300)
        # a filtered list: an edit that removes the record must not skip its neighbour
        pg.select_option("#n-order", "oldest"); pg.wait_for_timeout(400)
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
        check("delete leaves 5", pg.inner_text("#n-total") == "5", pg.inner_text("#n-total"))
        pg.click("#ld-undo"); pg.wait_for_timeout(1200)
        check("undo of the deletion: same record, same place", hexof(pg) == h3 and num(pg) == (3, 6), title(pg))
        # the station area: fixed fields, no distance or bearing, at most two candidates plus the list
        info = pg.inner_text("#rec-info"); pg.screenshot(path="/tmp/rec.png")
        check("pop-up has Set by, no distance/bearing", "Set by" in info and "Distance" not in info and "bearing" not in info.lower() and " km" not in info, info)
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
        check("Ctrl-A selects the whole list", "6 selected" in pg.inner_text("#n-selinfo") or "5 selected" in pg.inner_text("#n-selinfo"), pg.inner_text("#n-selinfo"))
        # a click on a small spectrum opens Monitor
        pg.click("#spec-mini"); pg.wait_for_timeout(600)
        check("click on the small spectrum opens Monitor", pg.evaluate("document.querySelector('#tabs button.on').dataset.tab") == "status")
        b.close()
finally:
    srv.terminate()
print("\n%d failed" % len(fails) if fails else "\nall passed")
sys.exit(1 if fails else 0)
