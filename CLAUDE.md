# navtexd — project context

## What this is
A pure-Go (stdlib only, no cgo) NAVTEX decoder (518/490/424 kHz, 100 baud FSK, CCIR 476 / SITOR-B) with an HTTP + SSE
server and a multi-client browser UI, comparable to YaND. Public GPL v3 project. The user-facing description is in
`README.md`; the design is in `docs/DESIGN.md` (decoder) and `docs/SERVER_DESIGN.md` (server/UI), `docs/HPSDR_DESIGN.md` (radio input) and `docs/TODO.md` (what is next).

## Ground rules
1. Standard library only, no cgo. Ask before adding any dependency or changing the package layout.
2. Streaming everywhere: every stage is causal, keeps state between calls, and gives identical output regardless of
   block size. Time is the sample index; never wall-clock inside the DSP.
3. Never block the audio callback; copy to a ring buffer and drain in a worker.
4. Raw received text is never altered. Corrections are stored separately.
5. All displayed and stored times are UTC.
6. The operator position exists only in server settings. Viewers never see distance, bearing or sun data unless
   `operator.public_location_info` is set.
7. Keep the YaND station-file format (`YAND_STATIONS.txt`) exactly.
8. Never commit executables, user data (`data/`, `navtex-data/`, settings, messages) or recordings.
9. Deviations from the standard algorithms go in `docs/DEVIATIONS.md` with the reason.
10. The radio input is receive-only: command frames never set the MOX bit and carry zero transmit data (a test asserts it).
    Never add anything that could key a transmitter.
11. Per-source options: each input source has its own group (`input.device.*`, `input.hpsdr.*`, `input.stdin.*`,
    `input.file.*`) with its own rate, centre, `autotune` and `manual_carrier_hz`. Use `Config.Tuning()` /
    `SetTuning()` / `SampleRate()` for the selected source; do not read another source's group. `Config` and `Input` must
    stay comparable (`==`), so lists such as the remembered radios live in their own file, not in the config.

12. Two decoding channels (`config.Channels`): channel 0 uses the top-level `input`, `channel`, `ddc`, `receiver`, `decoder`
    and `segment` groups, channel 1 the same groups inside `second` (plus `second.enabled`); everything else is shared. Code
    that decodes works on `cfg.ForChannel(i)`, a single-channel `Config`; change one channel with `cfg.EditChannel(i, ...)`.
    Anything sent to clients or stored carries its channel as `rcv` (absent = 0). Never use `cfg.Channel` etc. for "the"
    channel in code that serves both. Design: `docs/SERVER_DESIGN.md` section 33.

## Layout
- `cmd/navtexd` server/daemon (main); `cmd/navtex` console decoder; `cmd/navtexvocab` word-list tool.
- DSP chain: `internal/iq` (sources) → `ddc` → `fsk` → `sitor` → `navtex` (ZCZC..NNNN parser); `pipeline` wires it;
  `capture` is live audio (winmm on Windows, `parec`/`arecord` elsewhere); `hpsdr` is the OpenHPSDR Protocol 1 (Metis)
  radio input over UDP (discovery, start/stop, tuning, I/Q packets, `radios.json` list of the last four working radios);
  `source` opens whichever source the config selects.
- `internal/engine` meters/sync/controls; `segment` message/incomplete/fragment records; `app` Runner;
  `store` JSON-lines message store and filters; `server` HTTP+SSE and the embedded web client (`server/web`, plain JS
  IIFE modules on `window.NX`); `config` the single option tree (`Fields()` drives the Options tab and `-options`).
- Station identification: `ident`, `catalog` (station table, selective logging), `stations`, `geo`, `astro`.
- Text quality: `autocorr`, `vocab`, `textout`.
- `testvec`, `clip`, `sysinfo` — test helpers, clip export, system info.

## Commands
- `go vet ./... && go test ./...` (the `internal/hpsdr` tests take about 30 s)
- Windows build: `go build -trimpath -ldflags "-s -w" -o navtexd.exe ./cmd/navtexd` (executables are git-ignored)
- Command-line options apply to the selected source: `-in FILE`, `-device NAME`, `-radio ADDRESS|auto`, `-stdin`, with `-rate`,
  `-centre`, `-carrier` (turns autotune off) and `-start`; anything else via `-set path=value` (`navtexd -options` lists them).
  A radio or device given on the command line is remembered in `settings.json`.
- Run on a file: `go run ./cmd/navtexd -in file.wav -centre 516500 -data /tmp/nx -listen off -v`
  (mono = real audio, `-centre` = receiver LO in Hz; stereo I/Q takes the centre from a `..._504000HZ.wav` file name).
  Never test with a real `settings.json`.
- Client dev: `-realtime` and `NAVTEX_WEB_DIR` serve client files from disk.
- Radio tests use a fake radio in `internal/hpsdr/hpsdr_test.go` modelled on Pavel Demin's server (answers discovery,
  streams a tone, can drop packets or report busy). Real radios used by the owner: QMtech Zynq-7020 and Red Pitaya
  (both Pavel Demin images), Apache Labs Hermes later. The radios may be on a different subnet from the PC: use
  `-radio IP` or the Options page; broadcast discovery only finds radios on the local subnet.
- Some tests use golden vectors in `testdata/` and skip when absent.
- `tools/make_audio.py`, `tools/pace_pcm.py` make and pace test audio (need numpy, scipy).

## Signal facts (ITU-R M.476 / M.625)
- 518 kHz English (international), 490 kHz local language. 100 baud, shift 170 Hz (carrier ±85 Hz).
- CCIR 476: 7-bit characters with exactly four 1-bits (35 valid words). SITOR-B: each character is sent twice; the
  repeat (RX) of slot s is in slot s+5, 350 ms later. Phasing alternates alpha (0x0F) and RPT (0x66) ≥ 10 s.
- Message: `ZCZC B1B2B3B4 CR LF text NNNN` (B1 station, B2 subject, B3B4 serial). 0x5A = LTRS, 0x36 = FIGS.
- Bit order, mark/space polarity and DX/RX parity are detected from the data, not assumed.

## Radio notes
- Protocol 1 over UDP port 1024; the radio streams to the source address/port of the start packet. One host per radio:
  navtexd replaces Thetis / SDRConnect while it runs. Rates 48/96/192/384 kHz; the radio is tuned to the LO (default
  channel − 3000 Hz) and navtexd's own mixer takes the carrier from there. Details and open questions: `docs/HPSDR_DESIGN.md`.
- The QMtech image delivers a mirrored spectrum with the default I/Q order: the owner turns on `input.hpsdr.swap_iq`.
  Automatic detection was considered and postponed (a later 490 + 518 kHz decode from one stream may have the two
  carriers on opposite sides of the centre).
- Lost packets are zero-filled (≤ 1 s) and counted; the receive socket asks for a 4 MB buffer.
- Radio clock errors (a QMtech radio is about 10 ppm off) are absorbed by autotune; no frequency correction is planned.

## Server and data notes
- Options live in `<data>/settings.json`; `-config` bypasses it. A settings file from before the per-source split is
  migrated when loaded (`migrateInput` in `internal/config`). Live-changeable vs restart-needed options are
  tracked by `liveFields`/`IsLive`/`RestartNeeded` in `internal/config`.
- Roles are decided by peer address and proxy headers; write requests need `X-Navtex: 1`.
- Stored times are rounded to ms, qualities to two decimals (`store.F2`).
- Station table: `<data>/YAND_STATIONS.txt`, seeded from `internal/catalog/default_stations.txt`.
  Extra vocabulary: `autocorrect.extra_words_file` (default `vocabulary.txt` in the data dir).
- Log tab is a virtual list with a frozen view when a record is selected (see SERVER_DESIGN §28–29).
- Connection messages and the server-closed banner: SERVER_DESIGN §30.

## Out of scope for now
Several channels from one I/Q stream or radio (two channels with separate inputs exist, see rule 12), combining the
decoded text of two channels, installers/services,
importing YaND logs, Linux/Raspberry Pi release builds (see `docs/TODO.md`).

## Working here
- The owner pushes to GitHub himself: commit only when asked, never push, and never commit executables or data.
- Windows is the main platform (Go is installed on the owner's PC); radio and audio hardware can only be tried there.
