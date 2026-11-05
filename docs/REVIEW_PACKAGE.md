# navtexd: code review package

For an independent reviewer (human or model). Read this first, then `CLAUDE.md` (ground rules), `docs/DESIGN.md` (decoder),
`docs/SERVER_DESIGN.md` (server, UI; sections 3, 33, 36, 43, 45 matter most) and `docs/DEVIATIONS.md`.

## 1. What it is

A pure-Go NAVTEX decoder (100 baud FSK, CCIR 476 / SITOR-B; 518, 490, 486, 424 and 4209.5 kHz) with an HTTP + SSE server and
a multi-client browser UI, modelled on YaND. GPL v3. About 29,000 lines of Go outside `vendor/`, plus a plain-JS client embedded
with `go:embed`. The only third-party dependency is `modernc.org/sqlite` (pure Go, no cgo). Targets: Windows (tested by the owner),
Linux amd64, arm64 and armv7 (built, little tested).

## 2. Architecture in one page

```
source (sound device / WAV / stdin / HPSDR UDP / SpyServer TCP / SDRconnect WebSocket)
   -> iq (real or I/Q frames) -> ddc (mix, decimate) -> fsk (demod, carrier tracking, timing)
   -> sitor (framing, FEC pairing, polarity) -> navtex (ZCZC..NNNN parse) -> segment (message / incomplete / fragment)
   -> ident + catalog (station identification, scoring) -> store (SQLite)
   engine: meters, spectrum, scope, clip ring buffer; app.Runner owns the channels and restarts them
   server: REST + SSE (+ ws) to browsers; roles; hub fans out events; compress/compact shrink client traffic
```

- Up to four channels (`config.Channels`). Channel 0 uses the top-level config groups, channels 1..3 use `extra[0..2]`.
  `cfg.ForChannel(i)` gives a single-channel config; do not read `cfg.Channel` etc. directly in code serving several channels.
- Channels can share one input (`internal/source`, `tap.go`): one radio stream is opened once and mixed down separately per channel.
  This is how 490 and 518 kHz are decoded from one I/Q stream.
- Streaming, causal DSP: output must not depend on block size. Time inside the DSP is the sample index, never wall-clock.
- Raw received text is never altered; corrections are stored separately.
- The radio input is receive-only; a test asserts that command frames never set MOX or carry transmit data.
- Concurrency shape: one goroutine per source reader, one per channel engine, a hub goroutine per SSE/WebSocket client,
  the Runner restarting channels on config changes, and HTTP handlers on the standard library server.

## 3. Baseline (taken 2026-11-05 on the build copy, Go 1.24.7; the project targets 1.26)

| Check | Result |
|---|---|
| `go vet ./...` | clean |
| `GOOS=linux GOARCH=arm GOARM=7 go vet ./...` | clean |
| `go test ./...` | all pass (sdrconnect 56 s, hpsdr 9 s) |
| `go test -race` (all packages except sdrconnect) | all pass |
| 64-bit `sync/atomic` calls (armv7 alignment) | none; the typed atomics are used |
| `golangci-lint` (default set) | 64 findings, below |

Lint findings, none known to be a bug:
- 50 unchecked errors (`errcheck`), mostly `Close`, `io.WriteString`, `fmt.Fprintf` to a buffer, and best-effort socket
  options. Most in `spyserver.go` (9), `cmd/navtexd/main.go` (7), `server.go` (6), `compress.go` (6). Worth a look: is any of
  them a write whose failure should reach the user?
- Dead code: `Hub.add`, `Filter.match`, `wordCount` in `app/fragfilter.go`, fields `iq` (engine/spectrum.go) and `agr`, `agrL`
  (sitor/decoder.go).
- `ineffassign` in `internal/navtex/message.go:31` (`id := raw` is overwritten): check that it is not a logic slip.
- Style hints only (De Morgan, embedded field selectors).

Not yet run: `sdrconnect` tests under `-race`, `staticcheck` with all checks, fuzzing, `govulncheck`, and anything on real
Linux or ARM hardware.

## 4. Where to look hardest (ordered by what a bug would cost)

1. **Hostile or malformed input to parsers**
   - `internal/yand/abs.go`: reads an arbitrary user-supplied binary database (page chains, length fields, text pointers).
     Look for: out-of-range slicing, integer overflow in offsets, unbounded allocation from a length field, loops that do not
     terminate on a cyclic page chain, huge file memory use. Also `report.go` (regexp on arbitrary text).
   - `internal/server/records.go` `dbImport`: peeks 8192 bytes, then spools `.ABS` to a temp file, otherwise streams JSONL.
     Body limit is 1 GiB (`MaxBytesReader`). Check temp-file cleanup on error, and the replace path (backup first, then replace).
   - Network clients parsing replies from a radio or server: `internal/hpsdr`, `internal/spyserver` (binary framing, length
     fields), `internal/sdrconnect` (JSON over WebSocket via `internal/ws`, which is our own implementation: check frame
     length limits, fragmentation, masking, close handling, ping/pong).
2. **Security of the web server** (`internal/server/access.go`, `server.go`, `compress.go`)
   - Roles come from the direct peer address: admin for loopback and private networks, viewer otherwise; forwarding headers
     are ignored; `server.tunnel_listen` makes everything arriving there a viewer. Check for bypasses (IPv4-mapped IPv6,
     link-local, `admin_networks` parsing, a proxy on the same machine making every client look like loopback).
   - State-changing requests check `Origin`/`Host` and an optional token (cookie `navtex_token`). Check DNS rebinding,
     missing `Origin` handling, constant-time token comparison, cookie flags, token logged anywhere.
   - Viewers must never receive the operator position (distance and bearing are allowed). Check every JSON path: records,
     station table export, reports, clips, the hello message, the system log, error messages.
   - Request body limits (1 MiB, 64 KiB, 4 MiB, 1 GiB import), timeouts (only `ReadHeaderTimeout` and `IdleTimeout` are set; no
     read or write timeout, deliberately for SSE; check slow-client resource use), path handling in clip and file endpoints.
   - No TLS in the server by design; the documented advice is VPN or reverse proxy.
3. **Concurrency and lifecycle**
   - `app.Runner` restarts channels on config change (`Modify` / `Restart` / `RestartNeededFor`). Look for races between a
     restart and in-flight events, goroutine leaks after restart, double close of channels, and shutdown ordering.
   - Shared inputs (`source/tap.go`): subscribers added and removed while the reader runs; a slow subscriber must not stall
     the others or the radio read.
   - Hub / SSE (`server/hub.go`, `conn.go`): bounded queues per client, dropping policy, clients disappearing mid-write.
   - SDRconnect client: the `can_control` / re-select logic, reconnect loops, timeouts.
4. **Data integrity in the store** (`internal/store`)
   - SQLite via `modernc.org/sqlite`: schema and migration (`migrate.go`), undo trash (`trash.go`), compaction, import merge
     (`ImportRecords`: dedup key time|channel|kind|text, ID renumbering), replace-with-backup. Look for lost writes on
     crash, IDs reused after undo, filter SQL built by string concatenation (should be parameterized), LIKE escaping.
5. **Decoder correctness** (`fsk`, `sitor`, `navtex`, `segment`, `autocorr`)
   - Hard to find by reading; better as "which properties should always hold" tests: block-size independence, behaviour at
     start and end of stream, NaN or Inf propagation, polarity flips, loss of carrier, very long messages, B1/B2 and serial
     parsing, handling of a message that starts mid-way, and the fragment word test.
6. **32-bit and cross-platform**
   - armv7: `int` is 32 bits (sample counters, file offsets, record counts), alignment of 64-bit fields, memory use of the
     ring buffers and spectrum arrays on a small board.
   - Windows-only code paths (winmm capture, locking, `dirlock`, `softlock`) versus the Unix fallbacks.
   - Instance locking (`dirlock`, `softlock`): stale locks, two instances, a data directory on a network share.

## 5. Suggested prompts (use one per pass; ask for a concrete trigger or failing test for each finding)

1. "Read `internal/yand/abs.go` and `report.go`. They parse untrusted files. List every way a crafted file could cause a panic,
   an infinite loop, an allocation over 100 MB, or an out-of-bounds read. For each, give the exact bytes or a Go test."
2. "Audit `internal/ws` and `internal/sdrconnect`. `ws` is a hand-written WebSocket client. Compare it with RFC 6455 for frame
   parsing, length limits, masking, fragmentation, control frames and close. Then check the reconnect and timeout logic."
3. "Audit the security model in `docs/SERVER_DESIGN.md` section 3 against `internal/server/access.go` and `server.go`. Try to find a
   way for a remote viewer to change state, learn the operator position, or reach an admin endpoint. Consider DNS rebinding,
   IPv4-mapped IPv6 addresses, a reverse proxy on localhost, and cookie or token handling."
4. "Find data races and goroutine leaks. Start from `internal/app/runner.go` (Modify, Restart, shutdown), then `internal/source/tap.go`,
   then `internal/server/hub.go` and `conn.go`. For each, say which goroutines touch which state and how it is protected."
5. "Review `internal/store` for SQL injection, LIKE escaping, transaction boundaries, crash safety, and the import and replace
   paths in `dbio.go`. Check that undo (`trash.go`) cannot lose or duplicate records."
6. "Review the 32-bit ARM build: places where `int` overflows at 2^31, 64-bit alignment, and memory use on a 1 GB board."
7. "Read `internal/hpsdr` and `internal/spyserver` for protocol parsing bugs and for anything that could send data to a radio that
   is not a receive command. CLAUDE.md rule 10 forbids any transmit capability."
8. "Look at `internal/server/web/*.js` for XSS: message text, station names, comments and imported labels are untrusted and are
   inserted into the page. Check every `innerHTML`, attribute and URL built from data."
9. "Read `internal/sitor` and `internal/segment`, then propose property-based or fuzz tests for them (block-size independence,
   no panic on arbitrary bit streams, bounded memory on endless input), and write the first ones."

## 6. Things that look odd but are deliberate

- The server listens on `127.0.0.1` by default; `server.listen` opens it to the LAN.
- Times are UTC everywhere. Frequencies in records are Hz; carrier settings are in Hz too.
- `Config` and `Input` must stay comparable with `==`, so lists such as remembered radios live in their own file.
- The `sdrconnect` tests take about a minute because they exercise real timeouts against a fake server.
- Deviations from textbook algorithms are in `docs/DEVIATIONS.md` with reasons; a reviewer should challenge them.
- The client has no build step: plain JS modules on `window.NX`.

## 7. What a useful report looks like

Findings ranked by severity, each with: file and line, a one-sentence defect, a concrete trigger (input, sequence of
events, or a Go test that fails), and a suggested fix. Separate "confirmed by a failing test" from "suspected". Please do not
send style-only comments unless they hide a bug.
