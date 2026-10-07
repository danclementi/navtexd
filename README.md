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
- Input from a sound card or virtual audio cable, a WAV file (real audio or I/Q), or raw samples on stdin.
- Multi-client web interface: **Status**, **Spectrum**, **Log**, **Schedule**, **Signal**, **Report**,
  **Statistics**, **Ephemeris**, **Options**, **Database** and **System Log** tabs.
- Message log with filters (station, subject, text, distance), a reader pop-up, and de-duplication.
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

Windows SmartScreen or your antivirus may warn about an unsigned program the first time. Check the SHA-256 checksum
published with the release, or build the program yourself from source.

To list audio devices and start with one of them:

```
navtexd -list-devices
navtexd -device "CABLE Output" -centre 517000
```

`-centre` is the receiver dial frequency in Hz, so that the decoder knows where the carrier is in the audio.
The default expects the carrier near 1000 Hz of audio.

## Common options

| Option | Meaning |
|---|---|
| `-listen ADDR` | Web server address. Default `127.0.0.1:8080` (the next free port is used if it is busy); `:8080` serves the LAN; `off` disables it. An address given here is never changed. |
| `-tunnel ADDR` | Extra view-only listener, e.g. `127.0.0.1:8081` for a tunnel. |
| `-data DIR` | Data folder (settings, messages, station table). Default: `navtex-data` next to the program. |
| `-in FILE` | Decode a WAV file instead of live audio (the default input is the system's default audio device). Mono is real audio; stereo is I/Q. |
| `-device NAME` | Live audio device (part of the name). `-list-devices` lists them. |
| `-stdin` | Read raw samples from stdin. |
| `-channel 490\|518` | Which NAVTEX channel (default 518). |
| `-rate HZ`, `-centre HZ` | Sample rate and receiver (dial) frequency. |
| `-start "yyyy-mm-dd hh:mm:ss"` | UTC start time for file input. |
| `-set path=value` | Override any option, e.g. `-set decoder.unreadable=star`. |
| `-options`, `-dump-config` | List all options / print the current configuration. |
| `-list`, `-show ID` | Print the stored messages / one message and exit. |
| `-v` | Verbose output. |

Run `navtexd -h` for the complete list.

### Running two copies

Each copy needs its own data folder, and a copy refuses to start on a folder that another copy is using. The simplest
way is to unpack the zip into a second folder (for example `C:\navtexd2`) and start it from there. It keeps its settings
and messages next to its own program, and takes the next free web port (8081, ...). Set a different audio input and
frequency in each copy, for example 518 kHz in one and 490 kHz in the other.

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
