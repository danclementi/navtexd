# To do

A running list. Move finished items to the bottom or delete them.

## Next
- **Input watchdog ("no new audio"):** warn (log, banner, status lamp) when the input delivers no new samples or a constant
  value for about 10 s, and optionally reopen it. A frozen USB / virtual audio path looks like a quiet band otherwise.
- **Startup message:** print the data folder in use (for example "data folder: C:\...\navtex-data") so it is obvious where
  settings and messages are read from and written to. Found when an old log in another folder seemed to be missing.
- **README:** say that the program needs a folder it can write to (not Program Files).
- **Release:** try the zip on a second PC; then clear the "pre-release" flag, or issue v0.1.1 with any fixes.
- **Repos:** this folder (`navtexd`) is the working repo. The older `navtex` folder is an archive and can be renamed.

## Next (radio input)
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
- Optional golden-vector tests (small committed files) so the decoder keeps a regression test.
- Importing a YaND log (left out of the first release on purpose).

## Decided against
- A vocabulary editor, and moving the log to SQLite (the current word lists and JSON-lines store stay).
- Per-record edit before the report (Edit report works on the whole text).
