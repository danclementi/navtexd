# navtexd — project context

## What this is
A pure-Go (stdlib only, no cgo) NAVTEX decoder (518/490 kHz, 100 baud FSK, CCIR 476 / SITOR-B) with an HTTP + SSE
server and a multi-client browser UI, comparable to YaND. Public GPL v3 project. The user-facing description is in
`README.md`; the design is in `docs/DESIGN.md` (decoder) and `docs/SERVER_DESIGN.md` (server/UI).

## Ground rules
1. Standard library only, no cgo. Ask before adding any dependency or changing the package layout.
2. Streaming everywhere: every stage is causal, keeps state between calls, and gives identical output regardless of
   block size. Time is the sample index; never wall-clock inside the DSP.
3. Never block the audio callback; copy to a ring buffer and drain in a worker.
4. Raw received text is never altered. Corrections are stored separately.
5. All displayed and stored times are UTC.
6. The operator position exists only in server settings. Viewers never see distance, bearing or sun data unless
   `server.viewer_position_hints` is set.
7. Keep the YaND station-file format (`YAND_STATIONS.txt`) exactly.
8. Never commit executables, user data (`data/`, `navtex-data/`, settings, messages) or recordings.
9. Deviations from the standard algorithms go in `docs/DEVIATIONS.md` with the reason.

## Layout
- `cmd/navtexd` server/daemon (main); `cmd/navtex` console decoder; `cmd/navtexvocab` word-list tool.
- DSP chain: `internal/iq` (sources) → `ddc` → `fsk` → `sitor` → `navtex` (ZCZC..NNNN parser); `pipeline` wires it;
  `capture` is live audio (winmm on Windows, `parec`/`arecord` elsewhere).
- `internal/engine` meters/sync/controls; `segment` message/incomplete/fragment records; `app` Runner;
  `store` JSON-lines message store and filters; `server` HTTP+SSE and the embedded web client (`server/web`, plain JS
  IIFE modules on `window.NX`); `config` the single option tree (`Fields()` drives the Options tab and `-options`).
- Station identification: `ident`, `catalog` (station table, selective logging), `stations`, `geo`, `astro`.
- Text quality: `autocorr`, `vocab`, `textout`.
- `testvec`, `clip`, `sysinfo` — test helpers, clip export, system info.

## Commands
- `go vet ./... && go test ./...`
- Windows build: `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o navtexd.exe ./cmd/navtexd`
- Run on a file: `go run ./cmd/navtexd -in file.wav -centre 516500 -data /tmp/nx -listen off -v`
  (mono = real audio, `-centre` = receiver LO in Hz; stereo I/Q takes the centre from a `..._504000HZ.wav` file name).
  Never test with a real `settings.json`.
- Client dev: `-realtime` and `NAVTEX_WEB_DIR` serve client files from disk.
- Some tests use golden vectors in `testdata/` and skip when absent.
- `tools/make_audio.py`, `tools/pace_pcm.py` make and pace test audio (need numpy, scipy).

## Signal facts (ITU-R M.476 / M.625)
- 518 kHz English (international), 490 kHz local language. 100 baud, shift 170 Hz (carrier ±85 Hz).
- CCIR 476: 7-bit characters with exactly four 1-bits (35 valid words). SITOR-B: each character is sent twice; the
  repeat (RX) of slot s is in slot s+5, 350 ms later. Phasing alternates alpha (0x0F) and RPT (0x66) ≥ 10 s.
- Message: `ZCZC B1B2B3B4 CR LF text NNNN` (B1 station, B2 subject, B3B4 serial). 0x5A = LTRS, 0x36 = FIGS.
- Bit order, mark/space polarity and DX/RX parity are detected from the data, not assumed.

## Server and data notes
- Options live in `<data>/settings.json`; `-config` bypasses it. Live-changeable vs restart-needed options are
  tracked by `liveFields`/`IsLive`/`RestartNeeded` in `internal/config`.
- Roles are decided by peer address and proxy headers; write requests need `X-Navtex: 1`.
- Stored times are rounded to ms, qualities to two decimals (`store.F2`).
- Station table: `<data>/YAND_STATIONS.txt`, seeded from `internal/catalog/default_stations.txt`.
  Extra vocabulary: `autocorrect.extra_words_file` (default `vocabulary.txt` in the data dir).
- Log tab is a virtual list with a frozen view when a record is selected (see SERVER_DESIGN §28–29).
- Connection messages and the server-closed banner: SERVER_DESIGN §30.

## Out of scope for now
Multi-channel decoding, installers/services, importing YaND logs.
