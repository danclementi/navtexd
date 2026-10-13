# To do

A running list. Move finished items to the bottom or delete them.

## Next
- **Tests that run everywhere:** the decoder regression tests (block invariance, end to end, receiver, decoder; 17 tests)
  skip when `testdata/` recordings are absent, which is the case in a fresh clone. Write a small Go test helper that
  synthesises SITOR-B / FSK audio (known text, adjustable SNR, carrier offset, drift, impulse noise) so they always run.
- **Coverage:** `app` (runner) 13 %, `catalog` 30 %, `server` 55 %; add tests with a fake source.
- **Input watchdog: reopen.** The watchdog now only warns (`input.watchdog_s`). Optionally reopen the input by itself when
  it stalls (needs a way to unblock a read that never returns).
- **Retest** the packet-loss burst reporting and the `swap_iq` note on the other radios when they are available.

## Next (radio input) - ON HOLD
Wait until we have an approach for several receivers (or several audio inputs) in the GUI.

- **HPSDR / Metis input:** the QMtech image works over the air (needs `input.hpsdr.swap_iq` on; Thetis compensates by itself).
  Still to try: Red Pitaya (does it need `swap_iq` too? if so, note it in the log or default it for Pavel's images, which
  report the name "R_PITAYA") and the Hermes board. Watch packet loss on the LAN. Automatic I/Q detection was postponed:
  with 490 and 518 kHz decoded together the two carriers may lie on opposite sides of the centre.
- **Hermes controls:** attenuator, preamp, dither, random; ADC-overload indicator in the header; read the Hermes gateware
  (see `docs/HPSDR_DESIGN.md`, section 5, open questions).
- **Several receivers from one radio** (518, 490 and 424 kHz together); Protocol 2 radios (Orion, Saturn).

## Later
- One-line Linux installer script (download the right release binary for the machine, check its SHA-256, create a systemd
  unit; no Docker): needs Linux arm64 / armv7 release builds first. Raspberry Pi 3 / 4 section in the README; try
  `parec` against a virtual sink, and fall back to `arecord` if `parec` fails.
- Multi-channel decoding (490 and 518 kHz in one program).
- Packaging: Windows service, systemd unit, installer; code signing against SmartScreen warnings.
- Linux build and test.
- Decoder quality: indicator for how well the DX and RX copies of each character agree.
- Importing a YaND log (left out of the first release on purpose).

## Decided against
- A vocabulary editor, and moving the log to SQLite (the current word lists and JSON-lines store stay).
- Per-record edit before the report (Edit report works on the whole text).
