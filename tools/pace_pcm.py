#!/usr/bin/env python3
"""Write the samples of a 16-bit WAV to stdout as raw PCM at real-time speed (a stand-in for a sound card).
usage: pace_pcm.py file.wav [seconds] [speed]     e.g.  pace_pcm.py x.wav 60 | navtex -stdin -rate 48000 -v"""
import sys, time, struct
f = open(sys.argv[1], "rb"); f.seek(12)
while True:
    cid, size = struct.unpack("<4sI", f.read(8))
    if cid == b"data": break
    f.seek(size + (size & 1), 1)
secs = float(sys.argv[2]) if len(sys.argv) > 2 else 1e9
speed = float(sys.argv[3]) if len(sys.argv) > 3 else 1.0
step = 960 * 2  # 20 ms of 48 kHz mono
t0 = time.time(); sent = 0
out = sys.stdout.buffer
while sent < secs * 96000:
    b = f.read(step)
    if not b: break
    out.write(b); out.flush(); sent += len(b)
    d = t0 + sent / 96000 / speed - time.time()
    if d > 0: time.sleep(d)
