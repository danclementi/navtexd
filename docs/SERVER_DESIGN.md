# NAVTEX server and web client: design

Status: proposal for review. Companion documents: `docs/YAND_UI.md` (what YaND does, from screenshots and the manual),
`docs/DESIGN.md` (decoder), `docs/DEVIATIONS.md`.

## 1. Goals and non-goals
Goal: everything YaND does that the developer uses, as a server (decoder + database) with a browser client, usable from any device on the
network, with several clients at once. Settings can be changed only from the local network.

Not now: DX-A report format, DX-Atlas interface, sound-card sample-rate calibration (our timing is data-driven), a native/local
GUI, I/Q input in the UI (the decoder still supports it from files).

## 2. Architecture
One new executable, `navtexd` (the existing `navtex` command stays as the batch/console tool). Pure Go, no cgo, Windows and Linux.

```
audio source (VAC/sound card/stdin/file)
        |
   channel engine  ---- meters, spectrum, scope, LEDs ---->  event bus  ---->  WebSocket clients
   (DDC, FSK receiver, SITOR, segmenter)                        |
        | text lines, messages                                  +--> store writer (file store)
        v                                                       +--> system log
   identification + autocorrection ---> store <--- REST API <--- browser client (static files embedded in the binary)
```
Packages (new unless noted): `internal/engine` (wraps `pipeline`; live meters and control), `internal/store` (stdlib file store: append-only JSON lines replayed into memory; the Go module proxy was unreachable so no SQLite driver
), `internal/stations` (done: YaND station file), `internal/ident` (station identification),
`internal/autocorr` (word reconstruction), `internal/ephem` (sun position), `internal/report` (NavtexDX text),
`internal/api` (HTTP, WebSocket, roles), `internal/settings` (config tree, persisted as JSON; extends `internal/config`),
`web/` (the client; embedded with `go:embed`, no build step: ES modules, canvas charts).

A server can run several *channels* (e.g. 518 and 490 from two audio inputs); each channel has its own source, frequency and
decoder; all records carry the channel frequency. The first milestone runs one channel; the schema and API are keyed by channel.

## 3. Roles and security
- Two roles chosen from the connecting socket's **direct** peer address: `admin` for loopback and private networks
  (10/8, 172.16/12, 192.168/16, 169.254/16, fc00::/7, ::1; extendable with `server.admin_networks`, e.g. a VPN range),
  `viewer` for everything else. Forwarding headers are ignored.
- Viewers can watch: live text, status, meters, spectrum, messages, schedule, statistics. They cannot change anything, and
  they never receive the operator's coordinates (distance and bearing are shown, position is not).
- State-changing requests also require a matching `Origin`/`Host` (defends against a web page attacking a LAN server) and,
  optionally, a token (`server.token`) for setups behind a proxy where every client looks local.
- Listens on `127.0.0.1` by default; `server.listen` opens it to the LAN. No TLS in the server; use a VPN or reverse proxy for
  remote viewing.
- Concurrent edits: every editable record carries a version; a stale update is refused and the client reloads (last-writer
  conflicts are rare with one operator).

## 4. Engine additions (what the decoder must provide)
Existing: text events with send time, lock/unlock, bit order, carrier found/track, messages. New, all streamed:
| Item | Source |
|---|---|
| Input level, clipping flag | peak/RMS of input blocks, ~10/s |
| Spectrum frame | FFT of the audio, ~10/s, smoothed (FIL) and raw (RAW), linear/log is a client choice; mark/space/centre Hz |
| Quality (CRF) | crest-factor figure: peak-to-peak / std of the narrow-filtered signal, mapped 2.83 -> 100, ~7 -> 0 (calibrate on recordings); per line and averaged per message; history at 1/s |
| Sync state | red (out of sync) / yellow (pre-sync) / flicker (phasing) / green (full sync); DATA = valid words arriving |
| Controls | resync, autotune on/off with manual centre, polarity forced normal/inverted/auto, channel frequency, unshift-on-space (new decoder option) |
| Scope taps | on demand, 10 Hz: raw audio, filtered mark/space, envelope/soft symbols, recovered bits |
| Process CPU | this process's CPU share |
| Segmenter | splits the character stream into records: ZCZC..NNNN messages, incomplete messages, and unstructured fragments (text outside any message); applies the minimum-fragment-length option and the per-station logging exclusions |

## 5. Data model (file store, see internal/store)
- `messages`: id, channel_khz, received_utc, kind (message / incomplete / fragment), b1, b2, serial, navarea, station_id (nullable),
  station_label, distance_km, bearing_deg, quality_avg, comment (operator comment at reception), ident_method, ident_score,
  ident_candidates (JSON: top few with scores), manual (bool: B1/NAVAREA set by the operator, never re-identified), body_raw,
  body_corrected, version.
- `lines`: message_id, seq, utc, quality, text (raw, unreadable = `~`, control/shift markers preserved) so per-line stamps and
  quality survive.
- FTS5 index over `body_raw` + `body_corrected` for text search (combinable with all other filters).
- `stations`: as in the YaND file, plus `source` (imported / user), `enabled_for_logging`; `keywords(station_id, type GID/UID, word)`
  with the 300-character-per-station limit enforced in the editor. Import/export use the exact YaND file format.
- `settings` (key/value JSON), `syslog` (utc, level, text; trimmed at 10 000 lines), `quality_history` (utc, value; 1/s, thinned
  after a day) for the 1-8 h plot and per-day statistics.
- Deleting rows plus a Compact command (`VACUUM`). Maintenance deletes: older than a date, closer than N km, both, all UNID.
Statistics (minutes received per station per day/month, message counts, average quality) are SQL views over `messages`.

## 6. Station identification (own method, same keyword data)
Inputs: header letter B1 (if a valid `ZCZC <letter>` header), frequency, message time, body text, operator position.
1. Candidate set: stations on this frequency (with letter B1 if known).
2. Score = UID hit (station name/callsign present; strong, and partial fragments count) + GID hits weighted by how few stations
   share the keyword + a soft time-slot term (US stations often transmit outside their slot, so this is a small weight, wider for
   them) + a mild distance prior + history (stations already received here).
3. Best score above a threshold -> identified (method recorded: UID / GID / slot); below -> UNID; near-ties are flagged as ambiguous
   and the candidates stay visible in the record.
4. Operator override (B1 and/or NAVAREA edited in the Log): station, name, distance are recomputed, `manual = true`.
   Re-identification after a station-table update skips manual records.
B2 letters are shown with their ITU meaning (A navigational warnings, B meteorological warnings, C ice, D SAR/piracy,
E meteorological forecasts, F pilot service, G AIS, H LORAN, J SATNAV, K other navaids, L additional navigational warnings, V-Y special
services, Z no messages).

## 7. Word autocorrection
Same idea as YaND: words with `~` of 5-16 characters (at most half unreadable) are matched against a maritime vocabulary; the
result is shown in [brackets], several candidates as [A|B]; stored separately (`body_corrected`), raw text is never lost.
The vocabulary is built by `cmd/navtexvocab` from collected text and shipped as a plain text file that users can extend
(`internal/autocorr/maritime_en.txt`: WORD, tab, count; `#` comments). Sources: an existing vocabulary file (`-vocab`; the shipped list was seeded from the word list of another NAVTEX decoder), our own store (`-store messages.jsonl`) or plain text (`-text`). A corpus word needs 5-16 letters and must occur in at
least `-min-lines` (3) different error-free lines (no `~ * ? {}`; repeats of a broadcast count once); idle/phasing noise
like `WUXQPZ` is dropped. 490 kHz needs per-language lists, built the same way from their own text. Shift-reverser: the client selects a portion of a line and reverses
figures/letters; later, suggest portions where a run of figures/symbols sits in the middle of otherwise readable text.

## 8. API sketch
REST (JSON), viewers may GET, admin may write:
`GET /api/state` · `GET|PUT /api/settings` · `GET /api/audio-devices` · `POST /api/control/{resync|autotune|polarity|frequency}` ·
`GET /api/messages?freq&navarea&b1&text&from&to&limit&cursor` · `GET|PATCH|DELETE /api/messages/{id}` ·
`POST /api/maintenance/{delete|compact}` · `GET|PUT /api/stations` (+ `/import`, `/export`, `/{id}/keywords`) ·
`GET /api/schedule?freq&areas` · `GET /api/ephemeris?station&date` · `GET /api/stats?...` · `POST /api/report/list` and `POST /api/report/text` (record ids -> rows / NavtexDX text) ·
`GET /api/syslog`.
WebSocket `/ws` (server -> client): `status` (2/s: clock, level, quality, LEDs, offset, CPU), `spectrum` (10/s), `text` (each decoded
character group with its time and quality), `message` (stored/updated/deleted), `syslog`, `scope` (only after a subscribe message).
Settings changes apply live where possible; changing the audio device or channel restarts that channel.

## 9. Client
Tabs as in YaND minus Calibration: Spectrum (spectrum, current message, ticker), Log (filters, grid, text pane, shift reverser,
override), Schedule (with On Air Now, area selection, bearings), Signal (quality history 1/2/4/8 h, scope), Report (NavtexDX,
personal info header/footer, save/copy), Statistics, Ephemeris, Options, Database (keyword editor, maintenance, station import/export),
System Log. Fixed header: level, quality and CPU gauges, UTC clock with sun times, operational comment, audio input, autotune/lock,
UoS, frequency, polarity, resync, sync/data LEDs, latest-text ticker, and a schedule status bar. Viewers get the same screens with
edit controls disabled. Plain JavaScript modules and canvas drawing so there is nothing to build or install.

## 10. Milestones (each finishes with tests on the recording and, where relevant, live audio)
1. **Engine + store, headless**: live meters, sync states, quality, controls, segmenter; SQLite store; `navtexd` logs to the database
   from a WAV or audio device. Tests: recording -> 5 records (3 complete, 2 incomplete) + fragments as designed.
2. **Server core + first page**: HTTP/WebSocket, roles, settings persistence, header, Spectrum tab, live text.
3. **Log + Database + identification**: message grid with all filters, override, delete, text search; station table import/export,
   keyword editor, maintenance; identification v1.
4. **Schedule, Ephemeris, Options, System Log.**
5. **Signal (quality history, scope), Statistics, Report (NavtexDX).**
6. **Autocorrection and shift-reverser aids, multi-channel, packaging** (Windows service option, Linux systemd unit, single binary).

## 11. Open questions / risks
- Quality (CRF) definition and 0-100 scaling: tune on recordings; YaND's exact numbers are not reproducible.
- Vocabulary for autocorrection: needs a large sample (in progress); 490 kHz languages later.
- The station table and keywords come from the NAVTEX DX community; check redistribution terms before publishing the repository.
- Slot timing is unreliable for some stations (US): keep it a soft feature; consider learning actual slot usage per station from the log.
- SQLite in pure Go is slower than the C library but ample for this data rate; revisit only if statistics queries are slow.
- Exposure of coordinates: never sent to viewers; admin clients on the LAN see them in Options.

## 12. Decisions since first draft
- Store is the stdlib file store (`internal/store`), not SQLite; the SQLite risk note above no longer applies.
- Signal/Calibration tab and DX-A / DX-Atlas map are dropped (NavtexDX report stays). B1 and NAVAREA are manually editable overrides; overridden records are never re-identified.
- Quality is an inverse mapping of crest factor (`Meters.QualityNoiseCF`/`QualityGoodCF`); matching YaND is not a goal.
- Cloudflare tunnel: a tunnel makes every request appear to come from loopback, so peer address alone is not enough. Use a dedicated tunnel-only listener that is always viewer; additionally treat any request carrying Cloudflare/proxy headers (CF-Connecting-IP, X-Forwarded-For, ...) as remote/viewer, and check Origin/Host against an allowlist that includes the public hostname on writes. Cloudflare Access in front is recommended.
- Operator lat/lon lives only in server settings, never in the repo, never sent to viewers.
- Live data goes to the browser as **server-sent events** (`GET /api/events`) instead of a WebSocket: the Go standard library has no WebSocket and no third-party module can be fetched here. Traffic is server -> client only, so SSE is enough, needs no upgrade handshake and passes through Cloudflare tunnels; commands are ordinary POST/PUT requests.
- Roles (`internal/server/access.go`): admin needs a private/loopback peer address, no proxy headers (Cf-*, X-Forwarded-*, Forwarded, Via ...), a known Host name (defence against DNS rebinding), and the token if one is set. The tunnel listener (`server.tunnel_listen`) is always viewer. Writes also need `X-Navtex: 1`, a same-origin `Origin` and `Sec-Fetch-Site`.

## 13. Milestone 2 status (done)
`internal/app` (Runner: source + engine + store, live controls, settings saved in `<data>/settings.json`), `internal/server` (API, events, roles, embedded client in `web/`), `internal/astro` (sun times for the clock).
Client: header (level, quality, CPU gauges; UTC clock with sun times; comment; input; frequency; polarity; UoS; autotune + LOCK; carrier; Resync; SYNC/DATA LEDs; ticker), Spectrum tab (spectrum, current text, recent records), Options (generated from the option list), System Log.
API so far: `GET /api/state|events|messages|messages/{id}`, admin: `GET|PUT /api/settings`, `GET /api/audio-devices|syslog`, `POST /api/control/{resync|polarity|uos|autotune|carrier|comment|frequency|device}`, `POST /api/login`.
Testing aid: `navtexd -realtime -in x.wav` plays a recording at real speed; `NAVTEX_WEB_DIR=dir` serves the client files from disk instead of the embedded copy.

## 14. Milestone 3 status (done)
- **Identification** (`internal/ident`, `internal/catalog`): candidates = stations on the frequency (with the B1 letter if read, and the NAVAREA if the operator set it). Score = UID hits (10, +2 each further, cap 14) + GID hits (4 / number of stations on that frequency sharing the keyword, cap 8) + slot term (max 2, half for US stations; only used when B1 is unread, because every station with the same letter shares the slot) + distance prior (max 0.6) + history (max 0.5). A station is named when keyword evidence is at least 2 and leads the runner-up by 0.75, or when it is the only station with the operator-set area and letter. Otherwise the record stays UNID and the best candidates with scores are stored (shown in the Log, with a "Use" button that sets B1 and NAVAREA). Keywords: short ones (3 characters or fewer) must be whole words; a leading/trailing space makes any keyword whole-word; others match as substrings.
- **Station table**: `<data>/YAND_STATIONS.txt` in the exact YaND format (created from the copy built into the program if missing). Station ID = 1-based line number; after an import, "re-identify" refreshes the stored records. Keyword lists are limited to 300 characters each, duplicates dropped, stored in upper case.
- **Manual override**: editing B1 and/or NAVAREA (double-click in the Log grid, or the API) marks the record `manual`; the station is then looked up for that pair and re-identification keeps the pair.
- **Privacy change**: distances and bearings to known stations reveal where the receiver is (two or three of them pin it down), and so do sunrise/sunset times. Viewers therefore get neither, unless `operator.public_location_info` is turned on. (The first design had said viewers see distance and bearing; that was wrong.)
- **API added**: `GET /api/messages` with all filters (freq, navarea, b1, text, from, to, kind, unid, maxkm (admin), limit, offset, order), `GET|PATCH|DELETE /api/messages/{id}`, `POST /api/maintenance/{delete|compact|reidentify}`, `GET /api/stations`, `GET /api/stations/export`, `PUT /api/stations/{id}/keywords`, `POST /api/stations/import`, `POST /api/reverse`.
- **Shift reverser**: `sitor.Reverse` swaps letters and figures by code (D becomes `$` and back, J is not reversed because the decoder drops BEL/ENQ). The Log tab has a box where text (or the selected text) is reversed; nothing is stored.
- Not yet: autocorrection (milestone 6), automatic choice of which portion to reverse.

## 15. Milestone 4 status (done)
- **Line events**: the segmenter calls `OnLine` per finished line; the runner publishes `line` events (`{t,q,x}`) and the hub keeps the last 300 for `hello`, so the Spectrum text pane is line based and shows per-line quality when `logging.line_quality` is on (default).
- **Selective logging**: `<data>/selective_logging.json` holds the keys (`freq#area#b1#call`) of stations that are *not* stored. `Runner.Keep` is checked before the store; skipped messages are identified, logged in the System Log ("not stored (selective logging)") and dropped. Edited in Options (frequency filter, check/uncheck all). API: `GET /api/logging` (viewer), `PUT /api/logging` (admin).
- **Schedule tab**: 24 ten-minute slots by NAVAREA from the station table, per-area checkboxes (kept in the browser's localStorage), "On air now", optional bearings; a status bar under the tabs shows the stations on air now (same hint as YaND).
- **Ephemeris tab**: day/night bars for receiver and chosen station plus the station's slots and a now marker. `GET /api/ephemeris?station=ID&date=` (admin, or viewers only with `operator.public_location_info`). Latitude/longitude are shown read-only; they are set in Options.
- **Options**: grouped into collapsible sections with friendly titles; `operator.info` (name/equipment for reports) added; System Log has level filter, text search, follow and download (`GET /api/syslog.txt`, admin).
- Not yet: quality history and Scope, Statistics, NavtexDX Report (milestone 5); autocorrection, multi-channel, packaging (milestone 6).

## 16. Spectrum and Scope (milestone 5, first part)
- **Spectrum**: the carrier marker follows the selected input's `manual_carrier_hz` (`input.<source>.manual_carrier_hz`) when autotune is turned off (the runner now moves the carrier there) and can be dragged with the left mouse button (admin only); on release the value is stored (`POST /api/control/carrier`). LOG / LIN buttons (remembered per browser); LIN shows amplitude as a fraction of a full-scale tone. A subtle dotted line marks the level of a full-scale single tone (amplitude 1.0; `fs` field of the spectrum event, computed from the FFT window), with the margin of the signal peak below it; if the line is above the plot a small note says so. Spectrum values travel in tenths of a dB.
- **Scope** (`internal/engine/scope.go`, `GET /api/scope`, tab "Scope"): the demodulator offers its blocks (`Demod.OnBlock`) and symbols (`Symbol.Center`, sample index of the symbol centre). The Scope keeps 24 s of: audio (real baseband input), channel-filter output and envelope, one-symbol matched filters at the high and low tone, discriminator frequency, soft symbols and decided bits. Every lane is stored on the sample axis of the filtered stream and shifted for its own delay (audio by the channel filter's group delay, matched filters by half a symbol), so a decided bit lies under the signal that produced it. Sweeps are requested by the browser (`span`, `trig=rise|fall|none`, `pre`, `after`): the first trigger (bit edge, high tone = 1) whose window ends before the newest decided symbol and starts after `after` (end of the previous sweep; 0 = newest). One-shot asks for the head time and then waits for the next sweep; Free-run keeps requesting (it is unchecked by One-shot). Sweeps are about two seconds old because bits are decided with that latency. Any role may view the scope.

## 17. Header and layout in the style of YaND
- Header: three group boxes on one row. Left: logo (`cand_logo.png`, "CaND"; tooltip "Claude's attempted NAVTEX Decoder"), RESYNC text pushbutton, SYNC/DATA LEDs, three round dial gauges, NET (bytes per second sent to all clients) and USERS (`net` event every 2 s, also in `hello`). Centre (untitled): date, time + "UTC", Receive Location with "Sunrise: hh:mm / Sunset: hh:mm" (admin only), Operational Comment box. Right SETTINGS: autotuning ON + LOCK, carrier, UoS, audio input, frequency, polarity. The connection/role pills sit on a thin row below.
- One font family throughout (`--font` in app.css; canvases use it too).
- Quality dial/meter is smoothed with a first-order low-pass; `meters.quality_display_tau_s` (Options, default 1 s, 0 = off). The input level gauge has the same, `meters.level_display_tau_s` (default 0.5 s, applied in dB, clip flag unsmoothed). `meters.spectrum_show_unfiltered` switches the raw spectrum trace on/off (server then stops sending it); `spectrum_smooth` is limited to 0..0.99 (filtered = a*old + (1-a)*new on power). `meters.spectrum_show_channel` (default on) adds the `chan` array to the spectrum event: the spectrum of the narrow channel filter's output (what the demodulator sees), mapped onto the input grid and calibrated on tones so a tone in the passband has the same height in both traces; bins outside the filtered stream's bandwidth are -300 dB ("no data", the client lifts the pen). `input.watchdog_s` (default 10, 0 = off) logs a warning when a live input delivers no samples, or constant samples, for that long.
- Ephemeris defaults to the NAVTEX station nearest the receive location (admin only; needs operator position).
- The "on the air now" status bar is a fixed footer, visible in every tab. Tab order as in YaND: Spectrum, Log, Schedule, Signal (our scope), Report, Statistics, Ephemeris, Options, Database, System Log (Report and Statistics are placeholders until milestone 5).
- Settings fields of named-integer types (bit_order, polarity, unreadable) are reported as `enum` with the current name, so the Options drop-downs show the current value and save by name.
- While autotuning is on and no carrier is detected, the carrier marker is dimmed and labelled "searching…".

## 18. Live options
- `config.RestartNeeded(a, b)` decides whether a change restarts the channel; `config.IsLive(path)` is the same rule per option (a test cross-checks them for every option). Live: operator, logging, meters (display), segmentation, decoder decision/format options, carrier search/tracking, blanker threshold (not 0↔non-0), presence/timing thresholds, autotune and manual carrier. Restart: input, channel frequency, DDC, baud, tone spacing, channel cutoff, timing block length, blanker on/off, `output.*`. `server.*` needs navtexd restarted.
- `Runner.Modify` saves, and for live changes queues `Engine.Reconfigure(latest config)` on the decoding goroutine (components: `Decoder.SetConfig`, `Segmenter.SetConfig`, `Receiver.SetConfig`/`Demod.SetLive`/`Blanker.SetK`, spectrum rebuild). Header controls are just `Modify` now. A forced `bit_order` change drops the framing lock (a moment of resync).
- Options tab: live options apply and save when the edit is committed (green/red outline, no Save button); options with a "restarts channel" badge are batched under Save changes.

## 19. Autocorrection and shift-reverser aids (milestone 6, first part)

- **Package** `internal/autocorr` embeds `maritime_en.txt` (plus an optional `autocorrect.extra_words_file`, default `vocabulary.txt` in the data directory, reloaded on change).
- **Rule (as YaND):** a word of 5-16 characters with unreadable marks (`~`/`*`, at most `max_unreadable_percent`, default 50) is matched against the vocabulary; up to `max_candidates` (4) matches are shown as `[A|B|C]`. The raw text is never changed; the corrected body is stored in `Record.Corrected` (`cor`, lines joined by newline).
- **Wrong-shift suggestions:** runs of space-separated tokens with no letters whose `sitor.Reverse` yields a vocabulary word are returned in the message detail as `rv`.
- **Config:** `autocorrect.{enabled,min_word_letters,max_word_letters,max_unreadable_percent,max_candidates,extra_words_file}`, all live (no channel restart).
- **Decoder default:** `decoder.unreadable` is now `tilde` (a `~` per unreadable character), which is what the corrector needs.
- **API:** `GET /api/messages/{id}` includes `rv`; `POST /api/maintenance/recorrect` (admin) redoes the correction on the whole store. The Database tab has a "Redo autocorrection" button; the Log tab shows corrections in brackets, hover shows the received text, and an option toggles the display (localStorage `nx-cor`).

## 20. Carrier tracker safeguards

Field report (518 kHz, very weak signal): the tracker walked the carrier offset from about -5 Hz to -92 Hz (the lower tone) over 14 minutes. Demodulator timing stayed "locked" on noise, so the carrier was never forgotten, and the only search ran while no carrier was known.

- `receiver.track_max_excursion_hz` (default 30): the tracker cannot move the carrier further than this from where the search placed it.
- `receiver.search_verify_s` (default 8, 0 = off): while locked, the comb search is repeated on the recent signal at this interval. If its best position is at least 25 Hz away, has contrast >= `search_min_contrast` and scores at least 1.5x the current position, the carrier is retuned and the System Log shows "carrier re-acquired".
- Both are live options (prefixes `receiver.track_` and `receiver.search_`). Tests: `internal/fsk/verify_test.go`.

## 21. Header regrouped, continuous Log, report list (UI round after the first trials)

- **Header:** left box = logo, three dials, and under them date, UTC clock and sunrise/sunset; centre box = autotuning, LOCK, carrier and a small live spectrum (same drawing code as the Spectrum tab, own smoothing state, carrier can be dragged in either when autotuning is off); right box (no title) = audio input, frequency, polarity, UoS, RESYNC with SYNC/DATA LEDs, operational comment. The run-state text ("listening to ...") is now the tooltip of the three boxes.
- **Status bar (footer):** NET and USERS first, then the stations on air (no distance/bearing), then the live/role pills.
- **Message text** (ticker, current text, log text, reverser) is monospace, light green on a dark ground; the colour is the swatch at the right of the ticker (per browser, stored in localStorage `nx-msgcolor`, double-click resets).
- **Log tab:** one continuous list. Only the rows in view are in the table; the rest is two spacer rows, so 100 000 records cost the same as 100. Rows are fetched in chunks of 100 as they come into view. Up/Down move one line, PageUp/PageDown a page minus one, Home/End the ends; "Go to date" binary-searches the time-ordered list; "Most recent" jumps to the newest end. A click on B1 or Area opens the cell; Up/Down (or Enter, Tab) accepts a valid value and opens the same cell of the next record. The list is not reloaded while a cell is open, so stepping through UNIDs is not disturbed when an edit makes a record drop out of the filter.
- **Selection:** click, Ctrl-click (several), Shift-click (range, on any column). "Add to report" (toolbar, and in the record window) moves the selected records, in time order. Delete acts on all selected.
- **Report list:** every user, administrator or viewer, has their own list of record IDs, kept in their browser (`replist.js`, localStorage; nothing is stored on the server). "Add to report" in the Log tab (toolbar and record window) adds to it. `POST /api/report/list {ids}` returns the rows and `POST /api/report/text {ids, info}` the report text, for anyone; the server drops IDs that no longer exist and applies the usual viewer rules (no distances, no personal information).

## 22. Second UI round: Status tab, public location option, header layout, crash fix

- **Status tab** (leftmost, shown first) holds Current text and Recent records; the Spectrum tab keeps the large spectrum only.
- **`operator.public_location_info`** (Station page, live) replaces `server.viewer_position_hints`: when on, view-only users see sunrise/sunset, distances and bearings (Log, Schedule with its checkboxes, Report) and the Ephemeris tab. When off those items are not just empty but hidden (`hello.loc` false -> `body.noloc`). Toggling it sends a `location` event; connected viewers reconnect and get the new state at once. An old settings file with the obsolete key is carried over on load (the key is hidden from the option list via the `hidden:"true"` tag).
- **Header:** left box = logo, dials, date and UTC time in the same size, sunrise/sunset, comment; centre box = spectrum over the full box height with a narrow column (Auto, LOCK, "Carrier (Hz)", carrier value, spacer); right box = audio input, frequency, polarity, "Unshift on Space", RESYNC with SYNC/DATA.
- **"Failed to fetch" bug:** `autocorr.reversible` indexed a string by byte where the figure-shift text contains multi-byte characters (e.g. the pound sign); the handler panicked and net/http dropped the connection, which the browser reports as "Failed to fetch". Fixed (rune based, test added). Every request is now also wrapped so a handler bug gives a 500 with the reason and an `error` line in the System Log instead of a dropped connection.

## 23. Fixed-height page

The page is as high as the browser window (`100dvh`, but not less than 520 px, below which the page itself scrolls). Header, tab bar and status bar keep their height; the tab panel takes the rest, and anything taller scrolls inside the panel. Individual tabs still have some fixed-height elements (Log list, text panes, canvases); they are to be made to fill the panel tab by tab.

Filled so far: Status (in-progress message and recent records fill the panel; the list now holds the 60 newest records), Spectrum (graph fills the panel), Schedule and System Log (the inner table / text area fills the panel and is the only scrollbar). `operator.info` is a multi-line text box in Options. Log, Signal, Ephemeris and Database are still to do.

## 24. Record dialog, operator revisions, independent spectrum scales, report export

- **Status tab:** double-click a Recent-records row opens a read-only `<dialog>` (info plus scrollable text). The ◀/▶ buttons (or Left/Right keys) go to the next older/newer record; ×, Esc or a click on the backdrop closes it. The record is fetched by position (`/api/messages?limit=1&offset=i&order=newest`), so it works while new records arrive.
- **Revisions:** the received text (`lines`) is never changed. `Record.Revs` (`rev`, items `{l,s,e,o,n}` = line, start, end, old, new) holds operator replacements. `Effective(i)`/`EffectiveLines()` substitute each stretch as `<New>`; the `<…>` marks show in the UI (class `.revd`, tooltip "received: …"), in the dialog, in the report text and are what autocorrection and the reverser work on. `PATCH /api/messages/{id}` takes `rev:{op:"add"|"del"|"clear",…}`: `add` needs line `l`, text `t` (1–400 printable ASCII, no `<`/`>`) and either `s`/`e` or `find`+`occ`; overlaps are refused. Admin only. The detail response adds `eff`.
- **Log tab flow:** select text in the message (or click a wrong-shift suggestion) → "Use selection" → Reverse → edit the result → "Replace in message". The list of revisions has per-item "undo" and "undo all".
- **Spectrum scale:** the mini display and the Spectrum tab each keep their own LOG/LIN (`navtex.mini.scale`, `navtex.spec.scale`).
- **Report tab:** Personal info radios (Omit / As header / As footer, remembered per browser; admin only, since it is `operator.info`), "Save Report" (browser save dialog through `showSaveFilePicker`, or an ordinary download if the browser lacks it) and "Copy to Clipboard". Both use `POST /api/report/text` with `info` omit, header or footer: records in time order, a header line per record (UTC, kHz, station, B1B2, Q, km if allowed, comment) and the effective text.

**Report layout (NavtexDX):** `report.txt` follows the NavtexDX sample: CR LF line ends; a title row and 64 dashes; one summary row per message (`YYYYMMDD HHMM kHz $<area><B1> CALL NAME,CC` with the distance right-aligned at column 59 and the comment in `[ ]`); a blank line, `RAW LOG`; then, per message separated by `---`, the summary row again and every line as `YYYY-MM-DD HH:MM:SS qq> text` (autocorrections as `[WORD]`/`[A|B]`, operator revisions as `<…>`), and a final `"  "` row. Personal info goes before the title row or after the last message. The suggested file name is `NDXyyyymmdd_hhmmss.TXT` (local time).

## 25. Audio clips

- **Purpose:** keep a stretch of the input for later analysis or replay with other settings, e.g. a weak signal that the decoder missed.
- **Ring buffer:** `internal/clip.Ring` holds the last `clips.before_s + clips.after_s` seconds (default 60 + 10) of the source samples as read, before any processing (float32; mono for real audio, I/Q pairs for I/Q). The memory is capped at 256 MB (the clip is then shorter; a warning is logged). Changing the clip options restarts the channel.
- **Request:** the header button "Save clip" (admin only, `POST /api/clips`) marks the moment; the clip is completed `after_s` seconds later (if the input ends first, what there is is saved). A second press while one is pending is refused.
- **Files:** `<data>/clips/clip_<start UTC>_<centre>Hz.wav` (32-bit float WAV) and a `.json` with start time (ms), sample rate, kind, centre/LO frequency, channel, carrier position and autotune state at the press, and the decoding-relevant options in force (`settings`; server, operator, display and clip options are left out). The `Database` tab lists, downloads and deletes clips (`GET/DELETE /api/clips[/name]`, admin only).
- **Replay:** `navtexd -in <clip>.wav ...` finds the `.json` beside it and takes the start time and centre frequency from it (unless `-set input.file.start_utc` / `input.file.centre_hz` are given), so the records get the original times.

## 26. Blended signal quality

- **Why:** the crest-factor figure (peak/rms of the narrow-filter output) reaches 0 at about 6 dB SNR in the 340 Hz around the carrier, while decoding still works down to about 4 dB and degrades gradually below that. A quality of 0 on a cleanly decoded message is not confidence-inspiring.
- **Eye measure (new):** the soft values of the last 200 locked symbols (2 s) are about +-A plus noise; the M2M4 estimator gives the decision signal power S = sqrt((3*M2^2 - M4)/2) and noise power M2 - S. This depends only on the decisions, not on any filter or reference band (the presence test compares the carrier band with bands 350 Hz away, which an external 800-1200 Hz filter attenuates, so that is not used for a figure). `10*log10(S/N)` is mapped linearly from `meters.quality_eye_low_db` (3 dB, quality 0) to `quality_eye_high_db` (15 dB, quality 100). It is not available while symbols are unlocked (nothing in the last 3 s, or fewer than 100 locked symbols).
- **Blend:** `Q = w*eye + (1-w)*crest` with `meters.quality_eye_weight` w = 0.8 while the eye measure is available, otherwise the crest figure alone. All three options are live.
- **Calibration** (100 s recording plus white noise, SNR in 340 Hz): clean-text recovery 99% down to 6 dB, 89% at 4 dB, 44% at 2 dB, none at 0 dB or below; old Q 66/47/23/10/2/0.2 at 20/14/10/8/6/4 dB; blended Q 93/89/85/82/75/58 at the same points and 39/21/6 at 2/0/-2 dB.
- Stored qualities of existing records and the quality history file are not recomputed.

**Status tab split:** the divider between the in-progress message and the recent records can be dragged (or moved with the arrow keys when focused); the width is remembered per browser and a double-click resets it to the default 30%. The recent-records table uses fixed narrow columns and shortens the Station text with an ellipsis.

## 27. Weak-signal framing: quick re-lock

- **Case (2026-10-03, clip `clip_20261003T081712Z`):** a fade of about 40 s. The decoder printed garbage, ended its word-framing lock after `unlock_bad_words` invalid words in a row, and then needed `lock_words` (10) valid words in a row to lock again. At a poor signal-to-noise ratio only a minority of words are valid 4-of-7 words, so that run did not occur until the signal was good again, and the text sent meanwhile was lost; YaND, which keeps its framing, printed it with `~` marks.
- **Reasoning:** the word framing (alignment, polarity, DX/RX parity) depends only on the bit clock, and the bit clock goes on through a fade. After a lock ends on invalid words, the same polarity and alignment are therefore the likely ones for a while.
- **Change:** `decoder.relock_words` (4) and `decoder.relock_window_s` (120 s): for that long after a lock ended, the framing locks again at the *same* polarity and alignment after only `relock_words` valid words in a row, and the DX/RX parity of the lost lock is reused (so text appears at once). Other alignments still need `lock_words`. A manual Resync forgets the remembered framing. On the clip the text "... ESTABLISHED AN AREA TO BE AVOIDED AROUND THE BERMUDA ISLANDS" now comes out (with errors) as it does in YaND.
- **Cost:** in pure noise, with the receiver's timing still locked (the user's sensitive timing settings keep it locked), a chance run of `relock_words` valid words at the remembered alignment occurs now and then: 60 s of noise gave 57 characters with 4 (3: 169, 0: 42), almost all `~` with the tilde policy. Alignment agreement of DX/RX repeats was tried as a lock criterion and rejected: at these signal levels neighbouring alignments agree about as often as the true one.
- **`decoder.unreadable`:** the options page offered only `nearest` and `star`, although the default is `tilde` (`~` as YaND prints it); the page could therefore not show or restore `tilde`, and `nearest` prints plausible-looking wrong letters for unreadable characters. `tilde` is now in the list.

## 28. Log tab: live updates keep the view

When a record is logged (SSE `record`, debounced 400 ms) the Log tab's `refresh()` no longer rebuilds the list from scratch.
It fetches only the new total and, in newest-first order, shifts the cached rows, the open edit cell (`L.edit.i`), the range
anchor and `scrollTop` by the change in the total, so the same rows stay on screen. With any row selected or an edit open the view is frozen: the visible rows never move, a new record
appears neither at the top nor at the bottom of the displayed area, and the view does not follow the newest rows, even when
scrolled to the top (newest first) or the end (oldest first); the scroll bar may adjust. Only with nothing selected does a view
at the top (newest first) or at the end (oldest first) follow the new rows. Selected rows are held by record id; the cache is refilled in place (no empty
flash); the record text pane is rebuilt only if the selected record's data actually changed; an open edit is applied
immediately (it is no longer deferred) and keeps its value, focus and caret. `render()` also restores `scrollTop` if the browser
clamped it while the focused cell was replaced, and Escape no longer scrolls the list (`focus({preventScroll:true})`).

## 29. Log tab: pop-up reader, distance window, filter indicator

- **Layout.** The Log tab no longer has a text pane under the list. The record grid fills the panel (flex), so the outer scrollbar is gone; only the grid's own virtual scrolling remains.
- **Record pop-up** (`<dialog id="log-dlg">`, `log.js`): opened by double-click on a row (detected as `click` with `detail == 2`, because the rows are rebuilt on the first click and the browser then sends no `dblclick`), Enter on the selected row, or the "Open record" button. Left/right arrow keys and the ◀ ▶ buttons step to the previous/next row *of the list as shown* (filtered, in the chosen order); the title shows the position ("12 of 83, filtered"). It holds the record info at the top (including the operator's B1 / NAVAREA fields and the comment), the text with the autocorrect switch and the revision list in the middle, and the shift reverser at the bottom, plus Add to report and Delete (after a delete the next record is shown). The Status tab's dialog is unchanged and has no reverser.
- **B1 / NAVAREA** are edited only in the pop-up (the click-to-edit cells in the grid are gone). Enter or leaving the field saves it (`PATCH` with `b1` / `area`).
- **Frozen view** (§28) still holds: an open pop-up is a selection, so new records never move the grid; the position shown in the title follows the shift.
- **Distance window.** `GET /api/messages` takes `minkm` as well as `maxkm` (`store.Filter.MinDistance`); either bound excludes unidentified records. The inputs are hidden when no operator position is set; an empty maximum means no limit.
- **Filter indicator.** The indicator shows the filter that is *applied* (the query used for the last Apply), not what is typed: pulsing amber LED (3 s period, static under `prefers-reduced-motion`), "FILTER ON · showing n of N", an amber bar on the filter panel and list border, and a dot on the Log tab. The Apply button is highlighted while the boxes differ from the applied filter. "+k new hidden" counts records that arrived since the filter was applied and that it hides (growth of the hidden count since Apply); it is kept current while another tab is showing (`liveCounts`, 1 s after a `record` event).

## 30. Connection messages and "server closed" notice

- **Console lines** (`internal/server/conn.go`): every event-stream connection prints `browser connected: <ip> (<place>) <browser> on <system> [role, via tunnel]   users N` and, when it ends, `browser disconnected: ... after <time>, users N` (`Server.Notify` can redirect the lines; default standard output). The address is the peer address, or behind a tunnel / proxy the first of `Cf-Connecting-Ip`, `True-Client-Ip`, `X-Real-Ip`, `X-Forwarded-For` (only logged, never used for roles; see §access). The browser comes from the User-Agent (`uaSummary`: Chrome, Edge, Firefox, Safari, Opera, Samsung Internet, curl; Windows, macOS, Linux, Android, iPhone, iPad, ChromeOS).
- **Place:** private, loopback, link-local and 100.64.0.0/10 addresses are "local network". Cloudflare's `Cf-Ipcountry` is used when present, plus `Cf-Ipcity` / `Cf-Region` if "Add visitor location headers" is switched on for the tunnel (Cloudflare dashboard, Rules > Transform Rules > Managed Transforms). Otherwise, for a public address with no city, the place is looked up at https://ipwho.is (3 s timeout, cached per address) only if `server.geo_lookup` is on (default off). That lookup sends the visitor's address to ipwho.is; it never delays the page, only the console line. A browser reconnecting (network glitch, or after the operator changed location visibility) shows as a disconnect and a connect.
- **Server closing:** on Ctrl+C / SIGTERM (also console close and Windows shutdown) the server sends a `bye` event to every browser, waits 0.4 s and closes; `main` waits for that (max 2 s). The page shows a red banner: "The server has been shut down. You have been disconnected. When the server is running again this page reconnects by itself; if it does not, reload it with Ctrl+F5." The browser's event stream keeps retrying, and the banner disappears by itself when the server's `hello` arrives.
- **Lost connection:** if the server dies without a goodbye (crash, kill, power, network or tunnel down) the page cannot be told; after 12 s of failed reconnects it shows "The connection to the server has been lost ... reload the page with Ctrl+F5". Same self-clearing.

## 31. Start-up behaviour

- Default input is the system's default audio device (`input.source` = device); `-in FILE` selects a file.
- The data folder defaults to `navtex-data` next to the executable (the current folder when run with `go run`). A copy of the
  program unpacked into its own folder therefore has its own settings and messages.
- `internal/dirlock` holds an operating-system lock on `<data>/navtexd.lock` for the life of the process (Windows: a file
  opened with no sharing; elsewhere: `flock`). A second copy on the same folder refuses to start, whatever the options.
  The lock vanishes if the program is killed.
- If the default web address is busy (no `-listen` given) the next nine ports are tried, then any free port; an explicit
  `-listen` is never changed.
- `server.open_browser` (default true) opens the interface in the default browser once the web server is listening.

## 32. Edit report

- The Report tab has an **Edit report** button (left of Save Report) for everyone, viewers included. It opens a modal text box
  holding the report as generated (header or footer included), monospace, no wrapping, with a line and character count.
- **Accept** keeps the text as the report text: Save Report and Copy to Clipboard then use it, and an "edited text / revert"
  note appears. **Revert** (in the pane) replaces the text with the generated report; **Save** saves the pane's text as a file
  and accepts it; **Copy** copies it; **Cancel**, the close button and Esc ask before throwing away changes.
- The edit lives in the browser only (`R.edited` in `report.js`): it never touches the log records or the server. It is
  discarded, after a confirmation, when the list or the Personal info choice changes; if records were added in the Log tab, the
  Report tab asks when it is next opened (OK = discard the edit, Cancel = keep the edited text).
- Saved and copied edited text uses CRLF line endings like the generated report.

## 33. Decoding channels

- There are four decoding channels (`config.Channels`), each with its own input, frequency, filter, blanker, timing, framing and decoder
  settings, and its own engine, scope, clip buffer, carrier and state. Channel 0 always runs; channels 1..3 are optional (`extra.N.enabled`).
- Configuration: channel 0 is the top-level `input`, `channel`, `ddc`, `receiver`, `decoder`, `segment` groups; channels 1..3 are the
  same groups inside `extra[0..2]` (JSON `extra`, settings paths `extra.<n>.*`; `Set` pads the array with nulls so other entries stay; `Load` migrates an old `second` group into `extra[0]`). `Config.ForChannel(i)` returns a single-channel configuration (the groups of channel i plus
  the shared options), which is what the source, the engine and the runner work on; `EditChannel` / `SetChannel` write it
  back. `channel.name` and `channel.comment` describe a channel: the name is shown in the system log tag and the channel
  selector, the comment is stored with every record received on it (after the operator comment, separated by "; ").
- Restarts are per channel (`RestartNeededFor`): a change that needs a restart restarts only the channel it concerns;
  switching `extra.N.enabled` starts or stops that channel. `Runner.Modify` marks the channels, `Runner.Restart`
  restarts the marked ones. While a channel is off, its settings are not validated and do not matter.
- API: the per-channel requests take the query `?ch=0..3` (default 0): `POST /api/control/{name}`, `GET /api/scope`,
  `POST /api/clips`, `GET /api/input`. `/api/state` (hello) has `chans`, an array of `config.Channels` objects (`controls`, `run`, `meters`,
  `text`, `lines`); the old top-level `run`, `controls`, `meters`, `text` and `lines` are channel 0 and stay for now.
- Events: `meters`, `spectrum`, `text`, `line`, `run` (state) and `controls` carry `rcv` (omitted = 0); a `record` event and
  stored records carry it too (`rcv` in the JSON-lines store; absent = 0). `GET /api/messages` takes `rcv=1..4` to filter.
- The page (`app.js`) keeps each channel's controls, meters, run state, spectrum, text and lines in `state.chans[i]`; `state.*`
  is the selected channel's (`state.sel`), so the existing display code is unchanged. One line per enabled channel under the header selects
  the channel (plus a "+ Add channel" chip for administrators that opens that channel's Options tab). The header has one small spectrum per enabled
  channel (LOG/LIN is a per-channel setting made on the Spectrum tab; a click on a small spectrum selects its channel and opens that tab; the small ones have no carrier readout or drag); each channel's line carries its Auto (auto-tune carrier) checkbox and LOCK LED
  (greyed out while Auto is off). Each spectrum's tooltip describes its own channel.
  The header controls, Save clip, the Signal tab and the Schedule/Ephemeris frequency follow the selected channel; the choice
  is remembered per browser (`nx-chan`). The Log tab has a Channel filter and a channel badge on the frequency, and the
  Status tab's recent records carry the channel, all only while more than one channel is on. The header Comment is shared.
- The system log tags the lines of a channel with its name or `ch1`..`ch4` (for example `[ch2] lock pol=+1 align=3`) as soon
  as more than one channel is on; with one channel nothing changes. The quality history (Statistics) is channel 0's.
- Each channel opens its own input. Two channels can open the same sound device when the driver shares it (virtual audio cables
  usually do); a radio serves one stream per address, so several channels from one radio need the shared-input feature (TODO). A WAV file can be used for either channel, which makes the feature testable without radios.

## 34. Damaged message starts and forced ID
- `segment.damagedStart` looks at each finished line of a fragment: if it ends with an identification group (two letters, two
  figures, each may be an unreadable mark) after a space, and the 3 or 4 characters before the space are ZCZC with marks for
  some characters or one character missing (at least two characters equal), the text before the token stays a fragment and a
  message starts at the token. `Record.StartFix` (store `sfix`) holds the token as received; the raw first line is kept.
  `Body()`, the header parser and the catalog skip the first line of such a record. `Runner.Correct` shows the token as
  `[ZCZC]` in the corrected text. Damaged starts inside a message (no NNNN yet) are not looked for.
- The reader pop-up lists the stations of the record's frequency that are on air at the minute it was received (a slot is 10
  minutes from a start time) and a drop-down of the other stations of that frequency. Use sends `PATCH {station}` for any
  record. `catalog.Enrich` marks a station that the text does not support (not among the candidates, or any station given to a
  fragment) with identification method `forced ID`.

## 35. Spectrum view center
- `channel.view_offset_hz` (per channel, live) moves only the center of the spectrum slice sent to the displays (kept inside
  the band). The receiver window, carrier search and DDC are always centered on `shift` = channel frequency minus the source
  center. Settings saved before this change (no `view_v2` marker) have the offset reset to 0 on load, since it used to move both.
- Controls (administrators): `view` (axis Hz; offset = hz - window center, limited to the carrier range), `viewreset`. `carrier`
  never moves the view; it is refused when more than `carrierReachHz` (1800 Hz) from the window center (`Runner.WindowCenter`).
- Measured on a recorded real-audio clip: the receiver decoded a message with the carrier 1800 Hz off the window center and
  failed at 2200 Hz (decimated rate about 4 kHz).
- Client: `CH.vr` is the range shown by the big spectrum (zoom and pan inside the slice, everyone); beyond the slice only
  administrators can pan, which sends `view` when the mouse is released. Dragging the carrier marker within 28 px of an edge
  scrolls the axis. The area outside the received slice is shaded.


## 36. Relative LO and shared inputs

- The LO of a live input (`device`, `hpsdr`, `stdin`) is stored as `lo_below_hz`: LO = channel frequency - `lo_below_hz`. The old
  absolute `centre_hz` of those sources is converted when the settings load (`migrateLO`; 0 meant "default" and is dropped).
  A WAV file keeps `centre_hz`, since the recording's centre is fixed. `-centre` on the command line is converted the same way.
- `input.shared_from` (0 = own input, 1..4 = channel number) makes a channel decode from another channel's stream. The owner must
  be on and have an input of its own (no chains). In the runner the owner creates a `source.Tap` after opening its input and
  publishes each block it reads; a sharer waits (up to 15 s) for the tap and subscribes, then runs its own engine with the same
  rate and centre (`shift` = its channel - the centre). Recordings are published blocking (no samples lost, the sharer is not
  paced separately); live inputs drop a block a slow subscriber has no room for. Restarting a channel also restarts the channels
  that share its input. The header shows "same input as <channel>".

## 37. Deleting with undo; record numbers in hex

- Every deletion (Log tab, or the maintenance delete) first writes the records to `trash.jsonl` in the data folder, one line per
  deletion ("batch"); if that write fails nothing is deleted. `POST /api/messages/delete {ids}` deletes a selection as one batch;
  `GET /api/messages/undo` says whether there is one (`available`, `count`, `at`); `POST /api/messages/undo` restores the last
  batch (original IDs, version + 1; an ID taken since gets a new one) and records the restore in the file. Undo can be repeated,
  newest deletion first, and survives restarts. When the store opens, batches older than 30 days are dropped, and the oldest
  ones beyond 64 MB. New IDs are always above every ID in the trash.
- Record numbers are still integers in the files and the API; the UI and the logs show them as `0x%08X` (`NX.hex`, `hexID`).

## 38. Log database dialog

- The Database tab's "Log database maintenance" section and `POST /api/maintenance/delete` are gone: deleting is done in the Log
  tab (with Undo). The Log tab has a **Database...** button (administrators only) that opens a dialog:
  - **Compact** (`POST /api/maintenance/compact`) rewrites `messages.jsonl` with one line per record, after copying it to
    `backups/messages-before-compact-<time>.jsonl` (the last three copies are kept).
  - **Export** (`GET /api/database/export`, optionally with the Log tab's filter parameters) downloads records in the file's own
    format (`{"op":"put","r":{...}}` per line).
  - **Import** (`POST /api/database/import?mode=merge|replace`, the file is the body). Merge adds records whose time, channel,
    kind and text are not in the log yet, with new numbers; replace makes the log exactly the file's records (numbers kept)
    after saving the old file as `backups/messages-before-import-<time>.jsonl` (all of those are kept). A file without
    records is refused and nothing changes.
  - `GET /api/database` gives the record count, file size and backups for the dialog. All of these are administrator-only.
- The filter of the Log tab is read by one function (`logFilter`) used by the list and the export.

## 39. Station table import / export

- The Database tab's station table block has only **Export table** and **Import table**. Import replaces the whole table; the file is
  parsed and checked first (a file without stations is refused), then the current table is copied to
  `backups/YAND_STATIONS-<time it was last saved>.TXT` (a `-2`, `-3` suffix if that name exists), and the confirmation says so.
  The result line names the backup.
- Removed for now: "re-identify records" on import, "Re-identify all records now" and "Redo autocorrection of all records"
  (`/api/maintenance/reidentify` and `/recorrect`). `Catalog.Reidentify` stays as a library function.

## 40. Monitor tab and compact header
- The Status tab is now **Monitor**: the channel rows (chip, frequency, input, comment, Auto, LOCK/SYNC/DATA LEDs, RESYNC, UoS, polarity, "+ Add channel") are its top part and the in-progress message fills the rest. Recent records, its splitter and the record pop-up (`recdlg.js`) are gone (the Log tab has the filters). The CaND logo is gone; the browser tab title is `navtexd`.
- Each small spectrum in the header has a row under it: chip (frequency; selects the channel) and the words LOCKED/MANUAL, SYNC, DATA. LOCKED/MANUAL is an admin pushbutton for autotune (LOCKED lit while the carrier is found; MANUAL is amber); SYNC is an admin pushbutton for RESYNC; DATA is an indicator. Viewers see the same words, not clickable. Narrow cells (container query) drop "kHz" and shrink the words so four channels fit.
- A click on a small spectrum selects the channel (and nothing else); a double-click selects it and opens Spectrum. Pressing LOCKED (Auto → Manual, admin) selects that channel and opens Spectrum so the carrier can be adjusted; Manual → Auto changes nothing else.

## 41. One undo stack; the log pop-up cursor; UI smoke test
- **UNDO acts on the last action, whatever it was.** `trash.jsonl` is a single stack of batches. Deletions push the removed records; edits (manual B1/NAVAREA, station "Use", comment, accepting reversed text or a revision) push the record as it was before (`Kind` "edit", `Desc` e.g. "B1 change", `RecID`, `Rx`). `Store.Undo` pops the newest batch: a deletion puts the records back, an edit restores the old version. Records are sorted by (Received, ID), so a restored record returns to its old place and keeps its number. Edits go through `Store.UpdateUndoable`; an edit made stale by a later one is refused rather than overwritten.
- `GET /api/undo` / `POST /api/undo` return `kind`, `desc`, `id`, `hex`, `rx` (the time of the *message*, not of the action); the response of an undo carries `ids`. The UNDO tooltip reads "Undo B1 change on message 0x… (date UTC)" or "Bring back deleted message … in its old place".
- `GET /api/messages/locate?id=&filters&order=` returns the position of a record in the filtered list, so the client can show it after an undo.
- **Cursor in log.js**: one helper (`pos()`) answers "where is the displayed record in the list"; stepping (left = chronologically earlier, right = later), `canStep()`, the "no longer in the filtered list" case and the counter all use it. Numbering rank = newestFirst ? n−i : i+1.
- **UI smoke test**: `tools/uitest/smoke.py --exe ./navtexd --wav clip.wav [--chromium path]` (Python + Playwright) seeds six records and checks numbering, stepping in both sort orders and after an edit removes a record, DELETE/UNDO, edit/UNDO, "Use selection", Ctrl-A and the Monitor tab. Run it after any change to log.js.

## 42. Advanced options hidden by default
- The Receiver, Decoder and Down-converter groups (of every channel) are not shown in Options, to keep casual users from being overwhelmed; the list is `ADVANCED` in `app.js`. Polarity and UoS stay available on the Monitor tab. Nothing changes in the configuration file or the settings API; this is display only.
- An administrator clicks the "admin" badge (footer) and types `sudo` (shows them) or `exit` (hides them) into the one unlabelled field, any case, then Enter. The Options tab is redrawn when the box is closed (unsaved edits are kept and the new view appears at the next visit). The choice is kept in the browser (`localStorage` `nx-advanced`), so it survives restarts of the browser and of navtexd but is per browser. While it is on, each channel page starts with a note saying how to hide the options again.
