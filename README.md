# navtexd — NAVTEX decoder with a browser interface

`navtexd` decodes NAVTEX maritime safety broadcasts (518 kHz English, 490 kHz local language; 100 baud FSK, 170 Hz
shift, CCIR 476 / SITOR-B) from an audio stream and presents the messages in a web interface that several people can
use at the same time. It is modelled on the Windows program YaND and keeps YaND's station-file format.

It is a single self-contained program written in pure Go (standard library only, no cgo, no installer).

> **Not for navigation.** NAVTEX reception here is experimental and error-prone. Never rely on this software for
> navigation, weather routing, collision avoidance, or any other safety-of-life decision. Use certified equipment and
> official sources.
>
> **Use at your own risk.** This software is provided "as is", without warranty of any kind, express or implied. The
> authors and contributors are not liable for any damage, loss, or consequence arising from its use. See the
> license (GNU GPL v3, sections 15 and 16) for the full terms.

## Features

- Streaming decoder: carrier search and tracking, symbol timing, SITOR-B framing, diversity combining of the two
  transmitted copies of every character, optional spelling correction of unreadable characters (the raw received text
  is always kept unchanged).
- Input from a sound card or virtual audio cable, a WAV file (real audio or I/Q), raw samples on stdin, or I/Q straight from an OpenHPSDR (Metis / Hermes) radio over the network (Protocol 1: Hermes, Hermes Lite, ANAN, Pavel Demin's Red Pitaya and QMtech images). Receive only. An SDRplay RSP can also be used through SDRconnect's WebSocket API (SDRconnect, also its headless version, does the receiver; this program asks it for the I/Q stream, tunes it and decodes) — see `docs/SDRCONNECT_DESIGN.md`. An Airspy HF+ Discovery (or other Airspy) can be used through Airspy's SpyServer program, on this or another computer — see `docs/SPYSERVER_DESIGN.md`.
- The **Spectrum** tab (and the small spectrum in the header) draws the input spectrum together with a second,
  orange trace: the signal after the receiver's narrow channel filter, which is what the demodulator actually sees. If
  the carrier frequency or the filtering is wrong (easy to do with a broadband I/Q stream) the orange trace is flat or
  misses the signal. Switch it off with `meters.spectrum_show_channel`.
- A live input that stops delivering samples, or delivers the same value over and over, is reported in the System Log
  after `input.watchdog_s` seconds (default 10, 0 = off).
- Multi-client web interface: **Monitor**, **Spectrum**, **Log**, **Schedule**, **Signal**, **Report**,
  **Statistics**, **Ephemeris**, **Options**, **Database** and **System Log** tabs.
- The big spectrum can be zoomed (mouse wheel) and moved (drag); double-click shows the whole slice again. Administrators can drag
  beyond the received slice and the server then sends a slice centered there (`channel.view_offset_hz`, applied at once; double-click
  resets it). This changes only what is displayed: the receiver's window (carrier search, tracking) is always centered on the
  channel frequency minus the LO (`lo_below_hz`), and dragging the carrier marker (Auto off) moves only the carrier. The receiver
  decodes a carrier up to about 1800 Hz from the center of its window, so a manual carrier farther away is refused.
- Message log with filters (station, subject, text, distance), a reader pop-up, and de-duplication.
- A message whose `ZCZC` was damaged (`ZCZ~`, `CZC`, `~BR~ZCZ~` ...) is still recognized as a message start by its position and the
  identification group that follows; the repaired start is shown as `[ZCZC]` like other autocorrected words. In the reader pop-up the
  operator can give any record (also a fragment) a station from the list of stations scheduled at that time, or any station of the
  frequency; the identification is then marked `forced ID` when the message text does not support it.
- Station identification from a YaND-format station table (`YAND_STATIONS.txt`), with distance and bearing from the
  operator position.
- Two access roles: **admin** (direct connections from the local machine or private network) and **view-only**
  (anything arriving through a proxy or tunnel, such as a Cloudflare tunnel). The operator position is stored only in
  the server settings and is hidden from viewers unless you allow it.
- Messages are stored as plain JSON lines in the data folder; all times are UTC.

## Quick start (Windows)

1. Download the latest zip from the **Releases** page and unpack it anywhere.
2. Run `navtexd.exe` (double-click it, or start it from a command prompt). It listens to the system's default audio
   input, opens its web page in your browser (turn this off with the `server.open_browser` option), and prints the
   address, normally <http://127.0.0.1:8080/>. If that port is already in use it
   takes the next free one and says so.
3. Open the page, go to the **Options** tab, and choose the audio input and your position. Everything can be changed
   there; settings are saved in the `navtex-data` folder next to the program and used again at the next start.

The program writes its settings, message log and station table to the data folder (`navtex-data` next to the program
by default), so unpack it somewhere you can write to, such as your Documents or a folder on your desktop, and not into
`C:\Program Files`. It prints the folder it uses (`data folder: ...`) when it starts; use `-data DIR` to choose another.

Windows SmartScreen or your antivirus may warn about an unsigned program the first time. Check the SHA-256 checksum
published with the release, or build the program yourself from source.

To list audio devices and start with one of them:

```
navtexd -list-devices
navtexd -device "CABLE Output" -centre 517000
```

`-centre` is the receiver dial frequency in Hz, so that the decoder knows where the carrier is in the audio.
The default expects the carrier near 1000 Hz of audio. It is stored as `input.<source>.lo_below_hz`, the distance of the LO
below the channel frequency (1000 Hz; 3000 Hz for a radio), so the LO follows when the channel frequency is changed; the
Options page shows the resulting LO next to it. (A WAV file keeps an absolute `centre_hz`, because a recording has a fixed centre.)

**One input for several channels.** If your receiver delivers a single wide I/Q stream that contains more than one NAVTEX
frequency (for example 490 and 518 kHz), set `input.shared_from` of the second channel to the number of the first (Options,
Channel 2, Input: *same input as channel 1*). The first channel opens the input; the other decodes its own carrier from the same
samples. The stream's sample rate and centre must cover both channels, otherwise the channel reports an error.

## Common options

| Option | Meaning |
|---|---|
| `-listen ADDR` | Web server address. Default `127.0.0.1:8080` (the next free port is used if it is busy); `:8080` serves the LAN; `off` disables it. An address given here is never changed. |
| `-tunnel ADDR` | Extra view-only listener, e.g. `127.0.0.1:8081` for a tunnel. |
| `-data DIR` | Data folder (settings, messages, station table). Default: `navtex-data` next to the program. |
| `-config FILE` | Use this JSON settings file instead of `settings.json` in the data folder (`-dump-config` prints the effective settings). |
| `-realtime` | Play a WAV file back at its real speed instead of as fast as possible (for trying the web interface). |
| `-in FILE` | Decode a WAV file instead of live audio (the default input is the system's default audio device). Mono is real audio; stereo is I/Q. |
| `-device NAME` | Live audio device (part of the name). `-list-devices` lists them. |
| `-radio ADDRESS` | Take I/Q from an OpenHPSDR (Metis) radio at this address, or `auto` to search the local network. The radio is tuned for you (receiver LO = channel − 3 kHz unless `-centre` is given); `-rate` picks 48000, 96000, 192000 or 384000 Hz. The address is remembered for the next start (change it on the Options page). The last four radios that worked are kept (most recent first) in `radios.json` in the data folder and offered in the Options dropdown and in the header **Input** selector, which switches between audio and radio without visiting Options. With several radios on the network, a previously used one is preferred, otherwise the only idle one; if it is ambiguous, the log lists them and you pick one. Only one program can use a radio address at a time, so close Thetis / SDRConnect first. Channels of this program that use the same address share the radio: it is asked for one receiver (down-converter) per channel, each tuned to its own channel, so 518, 490 and 424 kHz can all come from one radio and one antenna. All of them need the same sample rate; a radio offers up to 8 receivers. |
| `-stdin` | Read raw samples from stdin. |
| `-channel 490\|518` | Which NAVTEX channel (default 518). |
| `-rate HZ`, `-centre HZ`, `-carrier HZ` | Sample rate, receiver (dial) frequency, and a fixed carrier position (turns autotune off) for the selected input. Every kind of input (audio device, radio, raw stream, WAV file) has its own group of options (`input.device.*`, `input.hpsdr.*`, `input.stdin.*`, `input.file.*`) with its own rate, centre, autotune and carrier, shown for the selected source on the Options page (Input tab). |
| `-start "yyyy-mm-dd hh:mm:ss"` | UTC start time for file or stdin input. |
| `-set path=value` | Override any option, e.g. `-set decoder.unreadable=star`. |
| `-options`, `-dump-config` | List all options / print the current configuration. |
| `-list`, `-show ID` | Print the stored messages / one message and exit. |
| `-v` | Verbose output. |

Run `navtexd -h` for the complete list.

### Fewer junk fragments in the Log
Text received outside any ZCZC...NNNN message is kept as a "fragment". Much of it is noise. On the Options page (the Channel tab of each channel,
Message segmentation; every channel has its own settings) two settings thin it out: `min_fragment_chars` (fewer visible characters than this: dropped) and the word
test, `min_fragment_words` words of at least `min_fragment_word_letters` letters (0 = no word test, the default). With
`fragment_words_after_autocorrect` on, words repaired from the vocabulary count too. `fragment_words_vocabulary` decides what counts as a word: `on` = only words found in the
vocabulary (it holds words of 5 letters or more), `off` = any run of letters, `auto` (default) = `on` at 518 kHz, `off` at 490 and 424 kHz. Dropped fragments are not stored; the System
Log notes each one ("fragment not stored (N words ...)") so that you can tune the numbers. Messages are never affected.

### Running two copies

Each copy needs its own data folder, and a copy refuses to start on a folder that another copy is using. The simplest
way is to unpack the zip into a second folder (for example `C:\navtexd2`) and start it from there. It keeps its settings
and messages next to its own program, and takes the next free web port (8081, ...). Set a different audio input and
frequency in each copy, for example 518 kHz in one and 490 kHz in the other.

### More channels

One copy can also decode up to four channels at once, each from its own input (sound cards or virtual cables, radios, a
file for testing; a sound card that other programs can share, such as a virtual cable, may feed several channels), with its own frequency, filter, blanker, timing and decoder settings. Switch it on with `enabled`
on the Options page (Channels tab), or with the "+ Add channel" chip under the header, then give it an input and frequency. `channel.name` and `channel.comment` (and
`extra.0.channel.name` / `.comment`, and so on) label a channel; the comment is stored with each record received on it, and records carry
the channel that received them. While it is on, system-log lines are tagged with the channel's name. One line per channel under the header (click its name)
selects the channel the page shows: the header controls, spectrum, meters, in-progress text, Save clip and the Signal tab
follow it, and the Log tab gets a Channel filter and a channel badge on each record. The header has one small spectrum per channel (click one to select that channel and open the Spectrum tab; LOG/LIN is set there, per channel), and the Auto (carrier auto-tune) checkbox and LOCK light are on the channel's line (LOCK is greyed out while Auto is off). To enter a carrier by hand, switch Auto off and drag the dashed line, or use Options. The Options page has a tab per channel (Channel 1 to
Channel 4) with that channel's settings. Settings from an older version's `second` group are carried over automatically.

## Security notes

- By default the web server listens on the loopback address only. Use `-listen :8080` only on a network you trust.
- Only direct connections from the local machine or a private network get admin rights. Requests that carry proxy
  headers (Cloudflare and similar) are always view-only, and write requests must carry the `X-Navtex` header.
- There are no user accounts or passwords. Do not expose the admin listener to the internet.
- Looking up the location of connecting browsers (`server.geo_lookup`) is off by default, because it sends client
  addresses to an external service.

## Building from source

Requires Go 1.24 or newer.

```
go build ./cmd/navtexd
go test ./...
```

For Windows from another system: `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/navtexd`.

More detail is in `docs/` (`SERVER_DESIGN.md` for the server and client, `DESIGN.md` for the decoder).

## License

Copyright (C) 2026 Dan Clementi

This program is free software: you can redistribute it and/or modify it under the terms of the GNU General Public
License, version 3, as published by the Free Software Foundation. It is distributed WITHOUT ANY WARRANTY. See
[LICENSE](LICENSE).

The station list and word list shipped with the program are compiled from public sources for convenience. If you
believe any of it should not be included, please open an issue.

NAVTEX is a service of the IMO/IHO; this project is not affiliated with or endorsed by either, nor by the authors of YaND.
