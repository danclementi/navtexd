import websocket, json, time, sys
ws = websocket.create_connection("ws://127.0.0.1:5454")
ws.settimeout(0.3)
def drain(t):
    end = time.time() + t
    out = {}
    while time.time() < end:
        try:
            m = json.loads(ws.recv())
            out[m.get("property")] = m.get("value")
        except Exception:
            pass
    return out
def put(p, v):
    ws.send(json.dumps({"event_type": "set_property", "property": p, "device": "primary", "value": str(v)}))
def get(p):
    ws.send(json.dumps({"event_type": "get_property", "property": p, "device": "primary", "value": "N/A"}))
rate = sys.argv[1]
put("device_sample_rate", rate); drain(1.5)
for v in sys.argv[2:]:
    put("device_vfo_frequency", 6000000); drain(1.2)   # leave the window
    put("device_vfo_frequency", v); drain(1.2)
    get("device_center_frequency"); r = drain(0.6)
    c = int(r.get("device_center_frequency", 0))
    print("rate", rate, "vfo", v, "-> centre", c, " centre-vfo", c - int(v))
put("device_sample_rate", "2000000"); drain(1.0)
ws.close()