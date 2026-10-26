#!/usr/bin/env python3
"""Probe SDRconnect's WebSocket API: what does it do with the centre frequency (LO), the tuned frequency (VFO) and the rate?

  python probe.py [--addr HOST:PORT] show
  python probe.py [--addr HOST:PORT] set  property=value [property=value ...]
  python probe.py [--addr HOST:PORT] sweep RATE VFO [VFO ...]

show   prints centre, VFO, started and sample rate.
set    sends each set_property in turn and prints the events and the read-back that follow (2 s + 1 s per step).
sweep  sets the sample rate, then for each VFO: tunes 6 MHz away (so that SDRconnect has to re-centre), tunes to the VFO and
       prints the centre it chose and centre-minus-VFO. The sample rate is put back to 2000000 at the end, so pass the rate
       you normally use as the last step with 'set device_sample_rate=250000'.
Needs: pip install websocket-client. The radio must be running (SDRconnect GUI or headless, WebSocket server on).
Do not run it while navtexd is using the same SDRconnect: it moves the receiver."""
import json, sys, time
import websocket

WATCH = ("device_center_frequency", "device_vfo_frequency", "started", "device_sample_rate")
args = sys.argv[1:]
addr = "127.0.0.1:5454"
if args[:1] == ["--addr"]:
    addr, args = args[1], args[2:]
if not args:
    sys.exit(__doc__)
ws = websocket.create_connection("ws://" + addr)
ws.settimeout(0.3)


def drain(t, show=False):
    end, out = time.time() + t, {}
    while time.time() < end:
        try:
            m = json.loads(ws.recv())
        except Exception:
            continue
        out[m.get("property")] = m.get("value")
        if show and m.get("property") in WATCH:
            print("  %5.1f  %-22s %-24s %s" % (time.time() % 100, m.get("event_type"), m.get("property"), m.get("value")))
    return out


def put(p, v):
    ws.send(json.dumps({"event_type": "set_property", "property": p, "device": "primary", "value": str(v)}))


def get(p):
    ws.send(json.dumps({"event_type": "get_property", "property": p, "device": "primary", "value": "N/A"}))


def state(show=True):
    for p in WATCH:
        get(p)
    return drain(1.0, show)


cmd = args[0]
if cmd == "show":
    state()
elif cmd == "set":
    state()
    for a in args[1:]:
        p, v = a.split("=", 1)
        print("SET", p, v)
        put(p, v)
        drain(2, True)
        get("device_center_frequency"); get("device_vfo_frequency")
        drain(1, True)
elif cmd == "sweep" and len(args) >= 3:
    rate = args[1]
    put("device_sample_rate", rate); drain(1.5)
    for v in args[2:]:
        put("device_vfo_frequency", 6000000); drain(1.2)
        put("device_vfo_frequency", v); drain(1.2)
        get("device_center_frequency")
        c = int(drain(0.6).get("device_center_frequency") or 0)
        print("rate", rate, "vfo", v, "-> centre", c, " centre-vfo", c - int(v))
    put("device_sample_rate", "2000000"); drain(1.0)
else:
    sys.exit(__doc__)
ws.close()
