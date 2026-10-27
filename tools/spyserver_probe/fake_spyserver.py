#!/usr/bin/env python3
"""Minimal stand-in for a SpyServer (HF+ Discovery) to test probe.py: python fake_spyserver.py [PORT]"""
import socket, struct, sys, threading, time, math

def msg(c, typ, body, seq=0, stream=0):
    c.sendall(struct.pack("<5I", 0x53505953, typ, stream, seq, len(body)) + body)

def serve(c):
    st = {"f": 0, "stage": 0, "stream": 0, "gain": 5}
    def rd(n):
        b = b""
        while len(b) < n:
            x = c.recv(n - len(b))
            if not x: raise EOFError
            b += x
        return b
    try:
        t, n = struct.unpack("<II", rd(8)); rd(n)
        msg(c, 0, struct.pack("<12I", 2, 1234, 768000, 660000, 8, 0, 8, 9000, 260000000, 1, 0, 2))
        msg(c, 1, struct.pack("<9I", 1, 0, 7000000, 7000000, 7000000, 9000, 31000000, 9000, 31000000))
        while True:
            t, n = struct.unpack("<II", rd(8)); b = rd(n)
            if t == 2:
                sid, v = struct.unpack("<II", b[:8])
                if sid == 101: st["f"] = v; msg(c, 1, struct.pack("<9I", 1, 0, v, v, v, 9000, 31000000, 9000, 31000000))
                if sid == 102: st["stage"] = v
                if sid == 2: st["gain"] = v
                if sid == 1: st["stream"] = v; threading.Thread(target=stream, args=(c, st), daemon=True).start() if v else None
    except Exception:
        pass

def stream(c, st):
    rate = 768000 / 2 ** st["stage"]; seq = 0; k = 0; nblk = 4096
    t0 = time.time()
    while st["stream"]:
        out = bytearray()
        amp = 4000 * 10 ** ((st["gain"] - 5) * 6 / 20)
        for i in range(nblk):
            ph = 2 * math.pi * 12000 * (k + i) / rate
            out += struct.pack("<hh", int(amp * math.cos(ph)), int(amp * math.sin(ph)))
        k += nblk
        try: msg(c, 101, bytes(out), seq, 1)
        except Exception: return
        seq += 1
        time.sleep(max(0, t0 + k / rate - time.time()))

s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", int(sys.argv[1]) if len(sys.argv) > 1 else 5555)); s.listen(5)
while True:
    c, _ = s.accept(); threading.Thread(target=serve, args=(c,), daemon=True).start()
