# SpyServer input (Airspy HF+ Discovery)

`input.source = spyserver` takes I/Q from an Airspy receiver (HF+ Discovery, Airspy One, RTL-SDR) through **Airspy's SpyServer**, a separate program that owns the hardware and streams over TCP (default port 5555). It runs on Windows, Linux and a Raspberry Pi, so the radio can sit on another computer on the LAN: put its `host:port` in `input.spyserver.address`. Receive only. Package `internal/spyserver`; test it by hand with `tools/spyserver_probe/probe.py`.

## Protocol used

Little endian, from Airspy's `spyserver_protocol.h` (version 2.0.1700). Command = u32 type, u32 body size, body; message = five u32 (id, type, stream, sequence, body size) and body.

1. `HELLO` (version, client name). The server answers with device information (type, serial, maximum sample rate, decimation stages, maximum gain index, ...) and a client sync (can control, gain, device and I/Q centre, the tunable range).
2. Settings, each `SET_SETTING` (id, value): I/Q format (int16; the format the server forces, if any), I/Q decimation stage, I/Q frequency, gain index (only if configured), streaming mode I/Q only, streaming enabled.
3. The I/Q messages carry a sequence number; a gap is logged ("N blocks lost").

## Choices

- **Sample rate:** not a setting. The decimation stage whose rate is nearest to 250 kS/s is used (the HF+ Discovery: 912 kS/s / 4 = 228 kS/s; ±114 kHz around the centre holds 518 and 490 kHz). The engine takes the rate as it is.
- **Centre:** the I/Q centre is set to the channel frequency minus `lo_below_hz` (default 1 kHz) and the server's client sync must report exactly that, otherwise the channel fails to start with the server's answer. The I/Q centre of SpyServer is a digital channel inside the device's span; unlike SDRconnect it follows the request exactly (measured: 518000 gave 518000, `resolution` 16 notwithstanding). The server's tunable minimum (390 kHz for the HF+) is checked first.
- **Changes by others:** a client sync that moves the I/Q centre without us asking stops the channel with an explanation.
- **Connection lost:** retried every 5 s with the whole set-up repeated; one reconnect loop at a time.
- **Gain:** index 0 up to the device's maximum; -1 leaves it as the server has it. What each step does depends on the device.
- **Channels sharing:** `shared_from` works as with the other radios; 490 kHz is at -27 kHz from a 517 kHz centre.
- **Edge of the band:** how clean the server's decimation filter is near ±114 kHz is not documented; keep the channels within about ±100 kHz of the centre.
