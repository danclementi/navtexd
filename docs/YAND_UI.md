# YaND feature notes (from the developer's screenshots and descriptions)

Working document: one section per screen. Status: S = server (decoder side), C = client (browser) only.

## Fixed header (always visible)
| YaND element | Meaning | Our mapping |
|---|---|---|
| INP.LEVEL gauge | audio input level, % | S: peak/RMS of the input audio, streamed ~5/s |
| CRF gauge | signal-quality indicator, believed to come from crest factor | S: to define (candidates: mark/space crest factor, soft-symbol margin, valid-word ratio); discuss later |
| CPU LOAD gauge | CPU use | S: this process's CPU share (portable: Go runtime + OS counters) |
| UTC clock + sunrise / sunset | clock, local sunrise and sunset times | C: clock; sun times need the operator's location (setting) |
| Operational comment | free text, e.g. antenna in use | S: stored setting, shared by all clients; probably written into log / reports |
| AUDIO INPUT button | dialog to pick the audio source | S: `input.source`, `input.device`, `-list-devices` -> device list API |
| AUTOTUNING ON + LOCK LED | automatic carrier search/tracking; LED = locked | S: receiver carrier search/tracking on/off (off = fixed centre frequency); LOCK = carrier found |
| UoS checkbox | "Unshift on Space" | S: NEW decoder option (after a space the receiver returns to the letters shift) |
| FREQUENCY 518 / 490 | which NAVTEX channel is decoded; also changes what the tabs show | S: `channel.frequency_hz`; tags messages; selects schedule/language data |
| SIGNAL POLARITY normal (USB) / inverted (LSB) | | S: today both polarities are tracked automatically; add a forced setting |
| Hammer button | force resync | S: command: reset framing (and optionally re-run carrier search) |
| SYNC LED / DATA LED | framing locked / valid data arriving | S: from decoder lock events and per-word validity |
| Latest-text ticker | scrolling most recent decoded text | C: last N characters of the text event stream |
| Bottom status bar | current schedule: `SCHEDULE 518KHz 07:20UTC 3U:Mondolfo,ITA/55°  4U:Chebogue (Fundy),CAN/60° ...` | S: needs NAVTEX station schedule database + operator location (bearing); C: display |

## Tabs (YaND order)
Spectrum, Log, NAVTEX Schedule, Signal/Calibration, Report, Reception Statistics, Ephemeris, Options, Database, System Log.

### Spectrum (seen)
- Audio spectrum 0..2700 Hz, shaded decoder passband, yellow lines at mark and space, red line at the centre with a Hz readout (top orange 1008 Hz, bottom cyan 1008 Hz), FIL and LIN toggles (filtered / linear-log display presumably - to confirm).
- Text pane below: lines stamped `yyyy-mm-dd hh:mm:ss NN> text` (what is `NN`? - to confirm).
- Server needs: spectrum frames (FFT of the input audio, ~5/s), mark/space/centre frequencies.

### Log (seen) - the main view of the message database
Filter bar (all combine; a FILTER ON LED shows when active; buttons apply / clear-all):
- FREQUENCY: both / 518 / 490.  NAVAREA: ALL or one area.  ID-LETTER (B1 station letter): ALL or one letter.
- Raw text search in the message BODY.
- FIGURES/LETTERS SHIFT REVERSER: a text box + REV button. Presumed purpose: repair text decoded in the wrong shift
  (figures where letters were meant and vice versa) - to confirm.
Navigation: RECORDS: n (3845 in the screenshot), first / previous / next / last, and a "-" button (delete record, presumably).
Grid, one row per received transmission, newest last: UTCRECEIVED, FREQ, SQ (0-100 quality), B1 (station letter), B2 (subject
letter), MSGNUMBER (serial; `?`/`???`/`0` when not decoded), NAVAREA (0 = unknown), STATION (identified name + operational
comment, e.g. `GNI Niton,ENG [LZ1AQ 1.2m dia.]`; `UNID` = unidentified), DIST (km from the operator's position; 0 if unidentified).
Shift+click on UTCRECEIVED copies the record to the Reception Report (Report tab).
Text pane below the grid: the selected record's raw text, one line per decoded line, each `yyyy-mm-dd hh:mm:ss NN> text`
(NN varies from line to line: 71, 73, 75, 16 - presumably the signal quality at that line, which would also explain the
`01>` in the live view). Unreadable characters show as `~`; `{}` also appears (meaning to confirm).

Observations for our design:
- Every transmission burst is a record, including garbage / partial ones (`B~K`, header `?`). Our message parser only
  keeps ZCZC..; we will need "unstructured" records too (text received while locked outside any ZCZC message).
- STATION is identified from B1 + frequency + the time slot in the NAVTEX schedule (B1 letters are reused worldwide), which is
  why the schedule database and the operator location are needed. NAVAREA and DIST derive from the station record.
- Record = utc_received, freq, sq, b1, b2, serial, navarea, station, dist, comment (antenna at time of reception), text lines
  with per-line time and quality.
- Storage plan: SQLite through a pure-Go driver (no cgo), server-side; filters and body search become SQL (FTS5 for text).

### Database (seen)
- Keyword List Editor: STATION LIST (rows like `518 1E GNI Niton,ENG` = freq, NAVAREA+B1, callsign, name, country code);
  choose keyword type Geographical (GID) or Unique (UID); the selected station's keywords are listed and can be added
  (NEW KEYWORD + ADD) and saved (CONFIRM KEYWORD LIST).
- Log Database Maintenance: delete records older than a selected date; whose distance is less than a set distance (km);
  both; or delete all UNID (unidentified) records (the other options keep UNIDs). Option "compact database afterwards".
- Export / Import Station Table to / from file.
- Station file: `data/YAND_STATIONS.txt` (from the developer; the community updates it, so we keep exactly this format:
  `VERSION5`, CRLF, `#`-separated, 17-18 fields, see `internal/stations`). Fields: navarea, freq, B1, callsign (`___` =
  unknown), name (may carry `{TRIAL}`), country code, country, lat, lon, six start times (4 h apart), GID keywords,
  UID keywords. Keywords may carry significant spaces (` KARA ` = whole-word match). 234 stations.
- (freq, B1, time slot) is NOT unique worldwide (e.g. 518 O 02:20 is used by 9 stations in different NAVAREAs), so station
  identification must combine slot match, keyword hits in the message body and distance from the operator.
- Our plan: `internal/stations` parses / writes this file (done, round-trips); the station table lives in the database with
  import / export in the same format; identification scores candidates (slot, GID/UID hits, distance).

### Decisions by the developer
- Shift reverser: apply to a chosen portion of a message, not the whole; how to select the portion is open.
- Delete record: with confirmation.
- SQ = the CRF quality measure averaged over the message; YaND's exact method is unknown (author deceased, no source), so
  we define our own; it need not match numerically.

### Options (seen)
- Confirmation popup on record deletion (checkbox).
- "SNR on log line" (checkbox): probably switches the `NN>` per-line number in the text pane / live view on and off, so NN is
  a per-line SNR-type figure (0-100). To confirm.
- Your latitude / longitude (decimal degrees; west negative). Used for sun times, bearings and distances. (Not recorded in
  this repository: it is an operator setting, stored in the server's own settings file.)
- SELECTIVE LOGGING: the station list (two columns of `freq NAVAREA+B1 callsign name,country`) with a checkbox each;
  unchecked stations are not logged. CHECK ALL / UNCHECK ALL. "Do not log message fragments containing less than N
  characters" (checkbox + N, 16 shown).
- "Personal & Equipment Info": free text (goes into the reception report? to confirm). CONFIRM & SAVE ALL OPTIONS.
- Our mapping: server-side settings (config tree gains `station` / `logging` sections), shared by all clients, changed only
  from local-network clients.

### NAVTEX Schedule (seen)
- "On Air Now" button (jump to / highlight the transmissions on air now).
- NAVAREA preferences: checkboxes 1..21 + APPLY = which areas' columns are shown. "Show Bearings" checkbox (bearing from the
  operator to each station).
- Grid: rows = 10-minute time slots (TSLOT, FREQ 518 / 490); a following `-->` row holds a second station sharing the same
  slot; one column per NAVAREA; a cell shows `<NAVAREA><B1> Name,COUNTRY` (e.g. `1E Niton,ENG`). The row for the current
  slot is selected. It is a view of the station table (times repeat every 4 hours in the file, so 6 slots a day are listed).
  This is also what the status bar under the tabs summarises (`SCHEDULE 518KHz 07:20UTC 3U:...`).

### Ephemeris (seen)
- "SUN EPHEMERIS": a 0-24 h UTC bar chart titled `Ephemeris comparison for <callsign>, <location> (<distance> Km) on <date>`.
  Yellow = local daytime (at the operator), orange = the selected DX station's daytime, cyan lines = that station's
  transmission slots, red line = now.
- Station list (NAVAREA, IDLETTER, FREQ, CALLSIGN, LOCATION, COUNTRYCODE): selecting a row selects the DX station; DATE picker;
  the DX station's latitude / longitude are shown (editable?).
- Purpose: MF ground-wave / sky-wave reception depends on daylight at both ends; this shows when a slot falls in
  darkness at both ends. Needs only a sunrise / sunset algorithm (NOAA formulas, no data files).

### Report (seen)
- Buttons: SAVE REPORT, COPY > CLIPBRD, NEW REPORT (clears). Personal Info (from Options): do not include / include as
  header / include as footer. Report Format: NavtexDX or DX-A. The FIGURES/LETTERS SHIFT REVERSER (REV) appears here too.
- Upper pane = the report being built (editable text); lower pane = the record(s) added by Shift+click on UTCRECEIVED in the
  Log tab, each block starting with a `---` line.
- NavtexDX line format, one per reception:
  `20260930 0658 518 $01E GNI Niton,ENG                5632 [LZ1AQ 1.2m dia.]`
  = date yyyymmdd, time hhmm (UTC), freq, `$` + NAVAREA (2 digits) + B1, station name,country, distance km, [operational comment].
  The block underneath is the received text, each line `yyyy-mm-dd hh:mm:ss NN> text`.
- NN is confirmed as a per-line SNR-type number (the Options checkbox is "SNR on log line"): values 23..65 within one message.
- Observation: this example was identified as `GNI Niton` (5632 km away) although the body talks about NOAA and Nantucket,
  i.e. a US station - identification from B1 + frequency alone is unreliable; our keyword / slot / distance scoring should do
  better. Need a sample of the DX-A format and any spec of NavtexDX reporting.

- DX-A format (sample from the developer; the text box next to the DX-A button is appended at the end):
  `518;"1010";"20260126";"NMN";"USA    [LZ1AQ 1.2m dia.]";"Portsmouth";"MET Warning";4;"DXA num?"`
  = freq; "hhmm"; "yyyymmdd"; "callsign"; "country + [comment]"; "location"; "message category"; navarea; "free text".
  The category is the ITU B2 subject indicator spelled out (A nav warnings, B met warnings, C ice, D SAR/piracy,
  E met forecasts, F pilot, G AIS, H LORAN, J SATNAV, K other navaids, L nav warnings additional, V-Y special, Z none) -
  to check against more samples. Strings quoted, numbers not.
- Ephemeris tab: the DX station's latitude / longitude are read-only (display them as labels, not text boxes).

### Signal/Calibration (seen)
- Two sub-tabs: Scope (not seen yet) and CRF History (plot of CRF 0-100 against time, history 1 / 2 / 4 / 8 h).
- Audio sample rate calibration (enable checkbox): AUTOCAL checkbox, PHASE ERROR needle meter, correction factor (0.99387 in
  the screenshot, i.e. 0.6 % off), manual slider, SAVE FACTOR; sound card rate choice 11025 / 12000 Hz (YaND runs at these low
  rates). Instructions: use a strong stable signal with no clipping, tuned on the peaks, move the slider until the needle
  is steady and centred.
- Why it matters to YaND (our reading): its decoder is built around a nominal sample rate (bit clock = fs/100 samples, filters
  and FFT bins scaled with fs). A card whose real rate differs from nominal shifts every tone frequency and the bit clock by
  the same ratio; the phase-error meter is presumably the bit-clock phase error, steady when the factor is right.
- For us: mark/space are measured from the signal and the symbol timing is estimated from the data, so no manual
  calibration is needed. A small "sample clock error (ppm)" estimate from the timing estimator can be shown as a diagnostic,
  and a frequency-correction factor kept for displayed audio frequencies.

### Reception Statistics (seen; DX-Atlas map report skipped for now)
- Start month/day, end month/day, NAVAREA, FREQUENCY, station selector (`A:NMA Miami,USA`), PLOT.
- Plot per day / per month; count for a single station / whole NAVAREA.
- Bar chart "Minutes RX time per MONTH/DAY" per station over the date range, titled `NMA Miami,USA-518KHz 1709km,198deg.`
  (freq, distance, bearing). Data = summed reception duration of logged messages.

### System Log (seen)
Time-stamped (UTC, hh:mm:ss) event lines, newest last, e.g.:
- `Decoder: resync, too many errors: "~~C ~ ~"` (with the last characters received)
- `End of emission signal...resync.`
- `DATABASE: message FE80/UNID stored, station could not be identified. (GID)`  (GID = identification by geographical keywords)
- `DATABASE: no valid ID-char for ???/0, message stored. (GID)`
- `Unshift On Space: ON` / `OFF` (option toggles are logged)
- `WATE~S corrected to WATERS` - a dictionary-based correction of unreadable characters (a feature to consider).
- `EXCEPTION trapped ...` (program errors).

### Notes from the developer
- US NAVTEX transmitters often transmit outside their scheduled slot, and keywords alone do not always identify a station:
  identification must be probabilistic and correctable by the operator.

### Facts from the YaND 6.x manual (April 2013; not distributed here)
Corrections and additions to the notes above:
- **Frequencies:** 518 kHz English, 490 kHz local language, 424 kHz Japan only. The frequency setting only affects the schedule and
  station identification (the decoder cannot know the receiver frequency). Our station parser now accepts 424 too.
- **Tuning:** USB, tone pair anywhere above ~400-500 Hz. Autotuning tracks +-250 Hz (the blue region in the spectrum); strong
  beacons on 518.000 (ARD, JBR in Europe) can pull it off, then switch autotuning off and tune by hand; the last frequency is
  remembered.
- **UoS:** "Unshift on Space" forces letters shift after each space; may help weak signals.
- **SYNC LED:** steady green = full sync; flickering green = sync signal being received (a special phasing signal is sent for about
  30 s before data); yellow = pre-sync, thinks it saw a signal (also on noise); red = out of sync. **DATA LED:** data arriving.
- **"SNR" / CRF is a signal "usability" figure, not a true SNR:** a strong signal is nearly a sinusoid, whose peak-to-peak / standard
  deviation ratio is 2*sqrt(2) = 2.83; a noisy signal approaches ~7; the rest is scaling, meaningful for receiver bandwidths of
  400-1000 Hz (2.2 kHz gives too low values). Displayed as 0-100 and also as a 60-minute history. The per-line `NN>` value and the
  record's SQ are this figure (per line, and averaged over the message). We can implement the same idea on our narrow-filtered
  signal and calibrate the 0-100 scaling ourselves.
- **Scope sub-tab:** oscilloscope of RAW audio, FIR (mark/space filter outputs), ENV (envelope), BIT (recovered bit stream after the
  bit-clock PLL), time base x1/x2/x4, one-shot mode, 10 Hz updates, auto-scaled. We can offer the same from our stages
  (input audio, discriminator/soft symbols, recovered bits).
- **Calibration:** exists because its bit clock is derived from the sound-card rate (errors of 3 % seen on some laptops). Ignored
  for us (our timing estimator tracks the data).
- **Message window rules:** decoded characters go to the ticker continuously; complete lines go to the message window; text with no
  line breaks is forced onto a line after 200 characters; NNNN stores the message and clears the window (some stations send no
  CR/LF after NNNN: the message stays until the next ZCZC).
- **Log tab details:** the ID-letter filter needs a letter or `*`; the text search cannot be combined with the other filters (we can
  combine them); a blinking yellow LED means a filter is active. **Manual override:** double-click B1 (or Enter), type the letter
  in capitals, arrow up/down to leave; same for NAVAREA; the station name and country then fill in (stays UNID if the pair is not
  assigned).
- **Reports:** saved as text files with a unique time-stamped name (e.g. `NDX20130330 204022.TXT`) in a fixed LOG folder. Shift reverser
  example: `-`54-(#-,J4-$89` becomes `ASTRAKHAN RADIO`. The DX-A format is a Belgian DX club (DX-Antwerp) report format, so
  the name is not "DX-Atlas". Ignore for now (the developer's decision).
- **Reception statistics:** plot average signal quality, message count or both, per day or month. Message counts are a debatable DX
  indicator (weather, one-subject-per-message stations, etc.).
- **Options:** the fragment threshold suggested is 35-50 characters (the developer uses 16). Selective logging is mainly to exclude local
  high-power stations. "Personal & Equipment Info" is included in all reports if selected in the Report tab. Database
  maintenance: deleting does not shrink the file, hence the compact option (SQLite: VACUUM). Station table updates are
  distributed by the NAVTEXDX community group as `YAND_STATIONS.TXT`; the keyword lists are a work in progress
  (Europe good, USCG reasonable, Asia poorest); max 300 characters of keywords per station; a keyword is added once only; delete =
  double-click + confirm; the editor saves only the list currently shown (GID or UID).
- **Station identification algorithm (the manual's own description):**
  1. Try the globally unique identifiers (UID keywords: station name, ITU call sign, e.g. `IZMIR TURK RADIO`, `ISR AUTH`, `VCO SYDNEY`).
     If one matches, stop, whatever else the message says (this can misidentify when fragments of two stations are interleaved).
  2. Otherwise need a correct header `ZCZC <space><letter>` and take the ID letter plus the first matching geographical keyword (GID)
     among stations with that letter; first match wins.
  3. No hints at all -> UNID (e.g. lists of "messages in force").
  Our plan improves on this by scoring all candidates (UID hit, GID hits, slot, distance, the operator's history) and keeping the
  ranking, but the same keyword data drive it.
- **Word autocorrection:** for a word with one or more `~`, look it up in a built-in maritime vocabulary (about 1800 unique words,
  compiled from logs); candidates 5-16 characters long, at most 50 % `~`; the reconstructed word is shown in [square brackets],
  several candidates as `[A|B|C]`; done on the stored message, not the live window. We would need to build our own vocabulary from
  collected messages; a first version could be seeded from the keyword lists and our decoded messages.
- **Multiple instances:** separate install folders. For us: one server able to run several channels (e.g. 518 and 490
  simultaneously, from two audio inputs or an I/Q input), which our DDC design already allows.

### Still to see: nothing required.

### Others: not seen yet.
