import websocket, json, time, sys
f = sys.argv[1] if len(sys.argv) > 1 else "517000"
ws = websocket.create_connection("ws://127.0.0.1:5454")
ws.settimeout(0.5)
def show(t):
    end = time.time() + t
    while time.time() < end:
        try:
            m = json.loads(ws.recv())
            if m.get("property") in ("device_center_frequency", "device_vfo_frequency", "started", "device_sample_rate"):
                print(round(time.time() % 100, 1), m)
        except websocket.WebSocketTimeoutException:
            pass
        except Exception:
            pass
ws.send(json.dumps({"event_type": "get_property", "property": "device_center_frequency", "device": "primary", "value": "N/A"}))
show(1)
ws.send(json.dumps({"event_type": "set_property", "property": "device_center_frequency", "device": "primary", "value": f}))
show(3)
ws.send(json.dumps({"event_type": "get_property", "property": "device_center_frequency", "device": "primary", "value": "N/A"}))
show(1)
ws.close()