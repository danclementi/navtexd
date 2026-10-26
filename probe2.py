import websocket, json, time, sys
ws = websocket.create_connection("ws://127.0.0.1:5454")
ws.settimeout(0.3)
want = ("device_center_frequency", "device_vfo_frequency", "started", "device_sample_rate")
def show(t):
    end = time.time() + t
    while time.time() < end:
        try:
            m = json.loads(ws.recv())
            if m.get("property") in want:
                print(round(time.time() % 100, 1), m["event_type"], m["property"], m["value"])
        except Exception:
            pass
def get(p): ws.send(json.dumps({"event_type": "get_property", "property": p, "device": "primary", "value": "N/A"}))
for p in want: get(p)
show(1)
for a in sys.argv[1:]:
    p, v = a.split("=")
    print("SET", p, v)
    ws.send(json.dumps({"event_type": "set_property", "property": p, "device": "primary", "value": v}))
    show(2)
    get("device_center_frequency"); get("device_vfo_frequency"); show(1)
ws.close()