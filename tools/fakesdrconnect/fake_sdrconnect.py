#!/usr/bin/env python3
"""A stand-in for SDRconnect's WebSocket server that streams the NAVTEX test clip as I/Q (for end-to-end tests)."""
import base64, hashlib, json, socket, struct, sys, threading, time
import numpy as np
from scipy.signal import hilbert, resample_poly

WAV, PORT, RATE = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
SPEED = float(sys.argv[4]) if len(sys.argv) > 4 else 2.0
LO_IN_AUDIO = 1002.0          # the carrier in the real-audio clip (Hz); the clip's LO is 517000
raw = open(WAV, 'rb').read()
i = raw.index(b'data') + 8
x = np.frombuffer(raw[i:], dtype='<f4').astype(np.float64)
fs = 48000
a = hilbert(x)                                   # upper-sideband audio -> analytic signal
def make(lo):                                     # I/Q for a receiver centre 'lo' (carrier RF = 518002)
    shift = (float(__import__("os").environ.get("CARRIER_RF", "518002")) - lo) - LO_IN_AUDIO
    t = np.arange(len(a)) / fs
    b = a * np.exp(2j * np.pi * shift * t)
    y = resample_poly(b, RATE // 2000, fs // 2000)
    y = y / (np.abs(y).max() * 1.3) * 0.9
    out = np.empty(len(y) * 2, dtype='<i2')
    out[0::2] = (y.real * 32767).astype('<i2'); out[1::2] = (y.imag * 32767).astype('<i2')
    return out

props = {"api_version": "1.0.3", "can_control": "true", "valid_devices": "RSPduo 1234", "active_device": "RSPduo 1234", "started": "false",
         "device_sample_rate": "2000000", "device_center_frequency": "100000000", "device_vfo_frequency": "100000000",
         "valid_antennas": "Tuner 1 50 ohm,Tuner 1 Hi-Z", "active_antenna": "Tuner 1 50 ohm", "lna_state": "0", "lna_state_min": "0", "lna_state_max": "9"}

def frame(op, p):
    h = bytes([0x80 | op])
    n = len(p)
    h += bytes([n]) if n < 126 else (bytes([126]) + struct.pack('>H', n) if n < 65536 else bytes([127]) + struct.pack('>Q', n))
    return h + p

def serve(c):
    f = c.makefile('rb')
    key = None
    while True:
        l = f.readline()
        if l.lower().startswith(b'sec-websocket-key'): key = l.split(b':')[1].strip()
        if l in (b'\r\n', b''): break
    acc = base64.b64encode(hashlib.sha1(key + b'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest())
    c.sendall(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + acc + b'\r\n\r\n')
    lock = threading.Lock()
    def send(op, p):
        with lock: c.sendall(frame(op, p))
    def js(ev, prop, val): send(1, json.dumps({"event_type": ev, "property": prop, "value": val}).encode())
    streaming = [False]
    def stream():
        iq = make(float(props["device_center_frequency"]))
        n = int(RATE / 50)
        k = 0
        t0 = time.time()
        while streaming[0] and k * 2 < len(iq):
            blk = iq[k * 2:(k + n) * 2]
            send(2, struct.pack('<H', 2) + blk.tobytes())
            k += n
            wait = t0 + k / RATE / SPEED - time.time()
            if wait > 0: time.sleep(wait)
        print("fake: stream finished", flush=True)
    while True:
        h = f.read(2)
        if len(h) < 2: return
        op = h[0] & 15; n = h[1] & 127
        if n == 126: n = struct.unpack('>H', f.read(2))[0]
        elif n == 127: n = struct.unpack('>Q', f.read(8))[0]
        m = f.read(4); p = bytearray(f.read(n))
        for j in range(n): p[j] ^= m[j & 3]
        if op == 8: return
        if op != 1: continue
        d = json.loads(p); ev, prop, val = d["event_type"], d["property"], d["value"]
        print("fake <-", ev, prop, val, flush=True)
        if ev == "get_property": js("get_property_response", prop, props.get(prop, ""))
        elif ev == "set_property":
            def late(prop=prop, val=val):
                time.sleep(0.3 if prop == "device_sample_rate" else 0.02)
                props[prop] = val
                if prop == "device_stream_enable": props["started"] = val
                js("property_changed", prop, val)
            threading.Thread(target=late).start()
        elif ev == "iq_stream_enable":
            if val == "true" and not streaming[0]:
                streaming[0] = True; threading.Thread(target=stream, daemon=True).start()
            elif val == "false": streaming[0] = False
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("127.0.0.1", PORT)); s.listen(5)
print("fake listening", flush=True)
while True:
    c, _ = s.accept()
    threading.Thread(target=serve, args=(c,), daemon=True).start()
