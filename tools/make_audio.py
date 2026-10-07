#!/usr/bin/env python3
"""Turn an I/Q recording into single-channel real audio, as a receiver in USB mode would deliver it.

The NAVTEX channel (default 518 kHz) is moved to CARRIER Hz of audio, everything outside about +-2.5 kHz is removed,
and the result is written as a mono 16-bit WAV at 48 kHz. The file name carries the equivalent receiver dial (LO)
frequency, e.g. ..._517000HZ_audio48k.wav, so `navtex -in` picks it up; play it through a virtual audio cable to test
live input.

usage: make_audio.py in_iq.wav out.wav [--channel 518000] [--centre Hz] [--carrier 1000] [--seconds N]
"""
import argparse, re, struct
import numpy as np
from scipy import signal


def find_chunks(f):
    f.seek(12)
    while True:
        h = f.read(8)
        if len(h) < 8:
            raise SystemExit("no data chunk")
        cid, size = h[:4], struct.unpack("<I", h[4:])[0]
        if cid == b"fmt ":
            fmt = struct.unpack("<HHIIHH", f.read(16))
            f.seek(size - 16, 1)
        elif cid == b"data":
            return fmt, f.tell(), size
        else:
            f.seek(size + (size & 1), 1)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("src"); ap.add_argument("dst")
    ap.add_argument("--channel", type=float, default=518000)
    ap.add_argument("--centre", type=float, default=None)
    ap.add_argument("--carrier", type=float, default=1000.0)
    ap.add_argument("--seconds", type=float, default=0)
    ap.add_argument("--swap", action="store_true")
    a = ap.parse_args()
    centre = a.centre
    if centre is None:
        m = re.search(r"(\d{4,10})\s*Hz", a.src, re.I)
        if not m:
            raise SystemExit("cannot find the centre frequency in the file name; use --centre")
        centre = float(m.group(1))
    with open(a.src, "rb") as f:
        (tag, ch, fs, _, _, bits), off, size = find_chunks(f)
        if ch != 2 or bits != 16 or tag not in (1, 0xFFFE):
            raise SystemExit("expected 16-bit stereo I/Q PCM")
        fs = float(fs)
        nfr = size // 4
        if a.seconds:
            nfr = min(nfr, int(a.seconds * fs))
        f_mix = a.carrier - (a.channel - centre)      # moves the channel to +carrier Hz
        D = 5
        sos = signal.butter(8, 2500, fs=fs, output="sos")
        zi = np.zeros((sos.shape[0], 2), complex)
        chunk = int(fs) * 10 // D * D
        n0, out = 0, []
        f.seek(off)
        while n0 < nfr:
            raw = np.frombuffer(f.read(min(chunk, nfr - n0) * 4), "<i2")
            raw = raw[: len(raw) // 2 * 2].reshape(-1, 2).astype(np.float64)
            n = len(raw)
            if n == 0:
                break
            x = (raw[:, 1] + 1j * raw[:, 0]) if a.swap else (raw[:, 0] + 1j * raw[:, 1])
            x = x * np.exp(2j * np.pi * f_mix * np.arange(n0, n0 + n) / fs)
            y, zi = signal.sosfilt(sos, x, zi=zi)
            out.append(y[::D].real.astype(np.float32))   # a real audio signal only needs the real part
            n0 += n
    r = np.concatenate(out)
    down = int(round(fs / D))
    g = np.gcd(48000, down)
    r = signal.resample_poly(r, 48000 // g, down // g).astype(np.float32)
    s = np.percentile(np.abs(r), 99)                   # robust scale: impulse noise clips instead of squashing the signal
    r = np.clip(r / (s * 3.0), -1, 1) * 0.9
    pcm = (r * 32767).astype("<i2")
    with open(a.dst, "wb") as w:
        w.write(b"RIFF" + struct.pack("<I", 36 + pcm.nbytes) + b"WAVEfmt " + struct.pack("<IHHIIHH", 16, 1, 1, 48000, 96000, 2, 16))
        w.write(b"data" + struct.pack("<I", pcm.nbytes))
        w.write(pcm.tobytes())
    print(f"{a.dst}: {len(pcm)/48000:.0f} s, 48000 Hz mono, channel {a.channel/1e3:.1f} kHz at {a.carrier:.0f} Hz "
          f"(dial/LO {int(round(a.channel - a.carrier))} Hz)")


main()
