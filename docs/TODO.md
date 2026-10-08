# To do

A running list. Move finished items to the bottom or delete them.

## Next
- **Input watchdog ("no new audio"):** warn (log, banner, status lamp) when the input delivers no new samples or a constant
  value for about 10 s, and optionally reopen it. A frozen USB / virtual audio path looks like a quiet band otherwise.
- **README:** say that the program needs a folder it can write to (not Program Files).
- **Release:** try the zip on a second PC; then clear the "pre-release" flag, or issue v0.1.1 with any fixes.
- **Repos:** this folder (`navtexd`) is the working repo. The older `navtex` folder is an archive and can be renamed.

## Later
- Multi-channel decoding (490 and 518 kHz in one program).
- Packaging: Windows service, systemd unit, installer; code signing against SmartScreen warnings.
- Linux build and test.
- Decoder quality: indicator for how well the DX and RX copies of each character agree.
- Optional golden-vector tests (small committed files) so the decoder keeps a regression test.
- Importing a YaND log (left out of the first release on purpose).

## Decided against
- A vocabulary editor, and moving the log to SQLite (the current word lists and JSON-lines store stay).
- Per-record edit before the report (Edit report works on the whole text).
