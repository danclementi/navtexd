# SDRconnect input

`input.source = sdrconnect` takes I/Q from an SDRplay RSP through **SDRconnect's WebSocket API** (SDRconnect version 1.0.9 or later; the sample rate can be set from 1.0.9). SDRconnect, with its window or headless, owns the receiver; this program is a client. Receive only.

## Setting up SDRconnect
- Preferences → WebSocket Server: switch it on (port 5454). Only one SDRconnect may run, and no other client may hold control of the receiver.
- On Windows the firewall allows the port on a *Private* network and blocks it on a *Public* one (or add an inbound TCP 5454 rule).
- SDRconnect can run on another computer: set `input.sdrconnect.address` to `host:port`.

## What this program does (`internal/sdrconnect`, `internal/ws`)
1. Opens a WebSocket (own small RFC 6455 client in `internal/ws`, standard library only).
2. Reads `api_version`, `can_control` (refuses if false), `valid_devices` and `active_device`; selects the receiver whose name contains `input.sdrconnect.device` (the API's device index base is not documented, so index and index+1 are tried and `active_device` decides).
3. Quiets spectrum and audio (`spectrum_enable`, `audio_stream_enable` off), sets the antenna and `lna_state` if configured (checked against `valid_antennas`, `lna_state_min/max`), then `device_sample_rate`, `device_center_frequency` (the LO: channel − `lo_below_hz`, default 3 kHz) and `device_vfo_frequency` (the channel frequency, which keeps the VFO inside the received span). Each is read back until SDRconnect reports the value (a new rate takes about half a second).
4. If the receiver is not streaming, `device_stream_enable` = true (and false again on close); finally `iq_stream_enable` = true.
5. Binary frames start with a 2-byte little-endian type; type 2 is 16-bit interleaved I/Q of the primary receiver (other types are ignored). Samples are scaled by 1/32768, decimated by an FIR cascade (`internal/ddc`) to 250 kHz, and then go through the normal chain. Rates offered: 250, 500, 1000, 2000 kHz (62.5 and 125 kHz arrive about 3% short; 6 MHz and up cost processing for nothing).

## Things to know
- Frames carry no counter or time stamp, so lost data cannot be detected. If this program cannot keep up, blocks are dropped and a warning is logged; SDRconnect itself keeps about 2.5 s for a slow reader.
- Another client that changes the sample rate ends the stream with an error; a changed centre frequency is logged as a warning (the carrier moves).
- RSPduo: the primary tuner is used; choose the input with `antenna`. SDRconnect offers `Tuner 1 50 ohm`, `Tuner 2 50 ohm` and `HiZ Port`; case, spaces and hyphens do not matter and a part that fits only one name is enough (`hiz`, `Tuner 1`). If the name does not fit, the log lists the names.
- If SDRconnect will not move its centre frequency (it keeps its own, e.g. while its window controls the tuning), the program uses the centre it has, as long as the channel lies inside the received span, and logs a warning.
- SDRconnect is proprietary software of SDRplay and is not distributed with this program.

## Testing
- `internal/sdrconnect` tests use a fake server in the test file (tone, device selection with both index bases, errors, the server closing and coming back, a foreign rate change, a moved centre, radio stop/start).
- `tools/fakesdrconnect/fake_sdrconnect.py WAV PORT RATE [SPEED]` is a stand-in for SDRconnect that streams a real-audio test clip as I/Q (needs numpy and scipy); point `input.sdrconnect.address` at it to test the whole chain without a receiver.

## Finding out what SDRconnect does
With `output.verbose` on (Options, Command line), every message sent to and received from SDRconnect (apart from the signal readings) is written to the system log. This shows, for example, whether SDRconnect answers a centre-frequency request and then puts it back.

## Several channels from one receiver
The sample rate is fixed at 250 kS/s (`config.SDRconnectRateHz`; there is no setting), so the received span is +-125 kHz around the centre: 518 and 490 kHz are inside it, 424 kHz is at its very edge. Give the first channel the SDRconnect input and set the other channels' `shared_from` to that channel ("same input as Channel 1"): they are decoded from the same stream, each with its own down-converter, and only one connection to SDRconnect is made. (Tested with the stand-in server: a second channel at 490 kHz decodes from the stream of the first.)

## Changes made by others while running

- Radio stopped/started in SDRconnect (`started`): the connection and the I/Q subscription survive; samples resume by themselves. Logged as a warning and an info line.
- Connection lost (headless SDRconnect stopped, GUI closed): the source retries every 5 s, then repeats the whole set-up. If the centre frequency differs from the one the channel started with, the channel stops with an explanation.
- Sample rate or centre frequency changed by someone else (the GUI moves its centre when the tuned frequency leaves the received span, and does not move it back): the channel stops with an explanation rather than compensating, because the carrier is no longer where the decoder looks.

## Centre frequency (SDRconnect 1.0.10)

Measured with API probes: `device_center_frequency` writes are silently ignored. SDRconnect re-centres only when the tuned frequency (VFO) leaves the received span; the new centre is then VFO plus an offset of its own, except at 250 kS/s where the centre equals the VFO. The centre is never below 0.4 x the sample rate. Hence the set-up tunes the VFO far outside the span and then to the wanted centre (`recentre`), and the rate is therefore fixed at 250 kS/s (the old `sample_rate_hz` setting is hidden and ignored; older files that contain it still load). The result is checked, and SDRconnect's own centre is used with a warning if it is still off.
