#!/usr/bin/env python3
"""Probe an Airspy SpyServer (HF+ Discovery, Airspy One, RTL-SDR behind it): connect, read what the server reports, tune, and
look at the I/Q stream. Python standard library only; numpy (optional) adds a spectrum peak report.

  python probe.py [--addr HOST:PORT] info
  python probe.py [--addr HOST:PORT] gains FREQ_HZ [--stage N] [--near HZ]
  python probe.py [--addr HOST:PORT] run FREQ_HZ [--stage N] [--format uint8|int16|float] [--gain G] [--secs S] [--ask-twice] [--near HZ]

info  prints the device information and the settings the server reports, and the sample rate of each decimation stage.
gains steps through every gain index (0 to the server's maximum) at FREQ_HZ and prints, for each, the level of the whole stream
      (dB relative to full scale, RMS and peak), the share of clipped samples, and (with numpy and --near) the level within
      +-NEAR Hz of the centre. The change from the previous index shows what a step does (6 dB per step = an attenuator).
      The gain is put back to its original value at the end.
run   tunes the I/Q stream to FREQ_HZ at decimation stage N (rate = maximum sample rate / 2^N), starts it for S seconds and
      prints, once per second, blocks, samples/s, sequence gaps and mean level; at the end the centre frequency the server
      reports back (to see whether it moved), and (with numpy) the strongest spectral lines as offsets from the centre.
      --ask-twice sends the frequency a second time after the stream has started (to see whether a late request is honoured).

Wire format (little endian), from Airspy's published spyserver_protocol.h as used by third-party clients:
  command  = u32 type, u32 body size, body        (HELLO=0, GET_SETTING=1, SET_SETTING=2, PING=3)
  hello    = u32 protocol version (2<<24 | 1700), client name (UTF-8)
  setting  = u32 setting id, u32 value            (streaming mode 0, streaming enabled 1, gain 2, IQ format 100, IQ frequency 101,
                                                   IQ decimation 102, IQ digital gain 103)
  message  = u32 protocol id, u32 type, u32 stream type, u32 sequence, u32 body size, body
             (device info 0, client sync 1, pong 2, setting 3, uint8 IQ 100, int16 IQ 101, int24 IQ 102, float IQ 103)
Not tested against every server version: if the hello is not answered, use --addr to check the port and try --hello-len."""
import socket, struct, sys, time, argparse

VERSION = (2 << 24) | 1700
FMT = {"uint8": 1, "int16": 2, "float": 4}
DEV = {0: "invalid", 1: "Airspy One", 2: "Airspy HF+", 3: "RTL-SDR"}


def recv_n(s, n):
    b = bytearray()
    while len(b) < n:
        c = s.recv(n - len(b))
        if not c:
            raise ConnectionError("the server closed the connection")
        b += c
    return bytes(b)


def message(s):
    h = struct.unpack("<5I", recv_n(s, 20))
    pid, typ, stream, seq, size = h
    return typ & 0xFFFF, stream, seq, recv_n(s, size) if size else b""


def command(s, typ, body=b""):
    s.sendall(struct.pack("<II", typ, len(body)) + body)


def setting(s, sid, val):
    command(s, 2, struct.pack("<II", sid, val))


def hello(s, with_len):
    name = b"navtexd-probe"
    body = struct.pack("<I", VERSION) + (struct.pack("<I", len(name)) if with_len else b"") + name
    command(s, 0, body)


def read_info(s):
    info = sync = None
    t0 = time.time()
    while (info is None or sync is None) and time.time() - t0 < 5:
        typ, stream, seq, body = message(s)
        if typ == 0:
            info = struct.unpack("<12I", body[:48])
        elif typ == 1:
            sync = struct.unpack("<9I", body[:36])
    return info, sync


def show_info(info, sync):
    n = ("device", "serial", "max sample rate", "max bandwidth", "decimation stages", "gain stages", "max gain index",
         "min frequency", "max frequency", "resolution", "min IQ decimation", "forced IQ format")
    print("device info:")
    for k, v in zip(n, info):
        print("  %-18s %s" % (k, DEV.get(v, v) if k == "device" else v))
    n = ("can control", "gain", "device centre frequency", "IQ centre frequency", "FFT centre frequency", "min IQ centre", "max IQ centre",
         "min FFT centre", "max FFT centre")
    print("client sync:")
    for k, v in zip(n, sync):
        print("  %-24s %s" % (k, v))
    print("sample rate per decimation stage:")
    for st in range(info[10], info[4] + 1):
        print("  stage %d: %.0f S/s" % (st, info[2] / 2 ** st))


def collect(s, secs, iqtype, width, discard=0.0):
    """Read I/Q messages for secs seconds (the first `discard` seconds are thrown away); returns the bytes kept."""
    keep = bytearray()
    t0 = time.time()
    while time.time() - t0 < secs:
        typ, stream, seq, body = message(s)
        if typ == iqtype and time.time() - t0 >= discard:
            keep += body
    return bytes(keep[:len(keep) // width * width])


def gains(s, a, info, sync, stage, rate):
    import math
    try:
        import numpy as np
    except ImportError:
        np = None
    setting(s, 100, 2)
    setting(s, 102, stage)
    setting(s, 101, a.freq)
    setting(s, 0, 1)
    setting(s, 1, 1)
    print("\ngain sweep at %d Hz, stage %d (%.0f S/s), original gain %d, maximum %d" % (a.freq, stage, rate, sync[1], info[6]))
    print("  gain   rms dBFS   peak dBFS   clipped%" + ("   near dB" if np is not None and a.near > 0 else "") + "   change")
    prev = None
    for g in range(0, info[6] + 1):
        setting(s, 2, g)
        raw = collect(s, 3.0, 101, 4, discard=1.5)
        if np is None:
            x = [int.from_bytes(raw[i:i + 2], "little", signed=True) for i in range(0, len(raw), 2)]
            rms = math.sqrt(sum(v * v for v in x) / max(1, len(x)))
            peak = max((abs(v) for v in x), default=0)
            clip = 100.0 * sum(1 for v in x if abs(v) >= 32700) / max(1, len(x))
            near = None
        else:
            x = np.frombuffer(raw, dtype="<i2").astype(np.float64)
            rms = float(np.sqrt(np.mean(x * x))) if len(x) else 0.0
            peak = float(np.abs(x).max()) if len(x) else 0.0
            clip = 100.0 * float(np.mean(np.abs(x) >= 32700)) if len(x) else 0.0
            near = None
            if a.near > 0 and len(x) >= 8192:
                z = x[0::2] + 1j * x[1::2]
                n = 1 << int(np.log2(min(len(z), 1 << 17)))
                sp = np.abs(np.fft.fftshift(np.fft.fft(z[:n] * np.hanning(n)))) ** 2
                fr = np.fft.fftshift(np.fft.fftfreq(n, 1 / rate))
                near = 10 * math.log10(float(sp[np.abs(fr) <= a.near].sum()) / n / n + 1e-30)
        db = 20 * math.log10(max(rms, 1e-9) / 32768)
        pk = 20 * math.log10(max(peak, 1e-9) / 32768)
        line = "  %4d   %8.1f   %9.1f   %7.2f" % (g, db, pk, clip)
        if near is not None:
            line += "   %7.1f" % near
        line += "   %+6.1f" % (db - prev) if prev is not None else ""
        print(line)
        prev = db
    setting(s, 2, sync[1])
    setting(s, 1, 0)
    print("gain put back to %d" % sync[1])


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--addr", default="127.0.0.1:5555")
    ap.add_argument("--hello-len", action="store_true", help="put a name length field in the hello (older/newer servers)")
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("info")
    g = sub.add_parser("gains")
    g.add_argument("freq", type=int)
    g.add_argument("--stage", type=int, default=None)
    g.add_argument("--near", type=float, default=0)
    r = sub.add_parser("run")
    r.add_argument("freq", type=int)
    r.add_argument("--stage", type=int, default=None)
    r.add_argument("--format", default="int16", choices=list(FMT))
    r.add_argument("--gain", type=int, default=None)
    r.add_argument("--secs", type=float, default=10)
    r.add_argument("--ask-twice", action="store_true")
    r.add_argument("--near", type=float, default=0, help="also list the strongest lines within +-NEAR Hz of the centre (e.g. 3000 to look for the NAVTEX carrier)")
    a = ap.parse_args()
    host, port = a.addr.rsplit(":", 1)
    s = socket.create_connection((host, int(port)), timeout=10)
    hello(s, a.hello_len)
    info, sync = read_info(s)
    if info is None:
        sys.exit("no device information from the server (try --hello-len, or check the port)")
    show_info(info, sync)
    if a.cmd == "info":
        return
    stage = a.stage if a.stage is not None else info[4]
    rate = info[2] / 2 ** stage
    if a.cmd == "gains":
        gains(s, a, info, sync, stage, rate)
        return
    print("\nasking for %d Hz, stage %d (%.0f S/s), format %s" % (a.freq, stage, rate, a.format))
    setting(s, 100, FMT[a.format])
    setting(s, 102, stage)
    setting(s, 101, a.freq)
    if a.gain is not None:
        setting(s, 2, a.gain)
    setting(s, 0, 1)   # I/Q only
    setting(s, 1, 1)   # streaming on
    t0 = tn = time.time()
    blocks = samples = gaps = 0
    last = None
    level = 0.0
    keep = bytearray()
    twice = a.ask_twice
    iqtype = {1: 100, 2: 101, 4: 103}[FMT[a.format]]
    width = {100: 2, 101: 4, 103: 8}[iqtype]
    while time.time() - t0 < a.secs:
        typ, stream, seq, body = message(s)
        if typ == 1:
            sync = struct.unpack("<9I", body[:36])
            print("  client sync: IQ centre %d, device centre %d, can control %d" % (sync[3], sync[2], sync[0]))
            continue
        if typ != iqtype:
            continue
        blocks += 1
        samples += len(body) // width
        if last is not None and seq != (last + 1) & 0xFFFFFFFF:
            gaps += 1
        last = seq
        if len(keep) < 1 << 20:
            keep += body
        if twice and time.time() - t0 > 2:
            twice = False
            print("  asking for the frequency again")
            setting(s, 101, a.freq)
        if time.time() - tn >= 1:
            print("  %4.1fs  %d blocks  %d samples/s  sequence gaps %d" % (time.time() - t0, blocks, samples, gaps))
            blocks = samples = 0
            tn = time.time()
    setting(s, 1, 0)
    print("\nfinal client sync: IQ centre frequency %d, device centre frequency %d" % (sync[3], sync[2]))
    try:
        import numpy as np
        dt = {100: "u1", 101: "<i2", 103: "<f4"}[iqtype]
        x = np.frombuffer(bytes(keep[:len(keep) // width * width]), dtype=dt).astype(np.float64)
        if iqtype == 100:
            x = x - 127.5
        z = x[0::2] + 1j * x[1::2]
        n = 1 << int(np.log2(min(len(z), 1 << 16)))
        sp = np.abs(np.fft.fftshift(np.fft.fft(z[:n] * np.hanning(n)))) ** 2
        fr = np.fft.fftshift(np.fft.fftfreq(n, 1 / rate))
        top = np.argsort(sp)[::-1]
        print("strongest spectral lines (offset from the centre, relative dB):")
        shown = []
        for i in top:
            if all(abs(fr[i] - f) > 500 for f in shown):
                shown.append(fr[i])
                print("  %+9.0f Hz  %5.1f dB" % (fr[i], 10 * np.log10(sp[i] / sp[top[0]] + 1e-30)))
            if len(shown) == 5:
                break
        if a.near > 0:
            m = np.abs(fr) <= a.near
            idx = np.where(m)[0]
            print("strongest lines within +-%.0f Hz of the centre (%.2f Hz per bin):" % (a.near, rate / n))
            shown = []
            for i in idx[np.argsort(sp[idx])[::-1]]:
                if all(abs(fr[i] - f) > 20 for f in shown):
                    shown.append(fr[i])
                    print("  %+9.1f Hz  %5.1f dB (relative to the strongest line overall)" % (fr[i], 10 * np.log10(sp[i] / sp[top[0]] + 1e-30)))
                if len(shown) == 5:
                    break
    except ImportError:
        print("(install numpy for a spectrum report)")


main()
