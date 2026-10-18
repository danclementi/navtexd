# HPSDR / Metis input — design notes

**Status:** Protocol 1, receive-only is implemented, with several receivers per radio shared by the channels of the program (section 6) (`internal/hpsdr`, `input.source = hpsdr`, `-radio`). Tested against a fake radio modelled on Pavel Demin's server, including a full real NAVTEX message decoded through it. **Not yet tried on real hardware.** Radio list: up to four successfully used radios are kept in `radios.json` (MRU); header Input selector; multi-radio search prefers a known radio, else the only non-busy one, else errors listing all. Options are per source (`input.device.*`, `input.hpsdr.*`, `input.stdin.*`, `input.file.*`), including each source's own rate, centre, autotune and manual carrier. Not done: Hermes-specific controls (attenuator, preamp, dither, random), ADC-overload indicator, Protocol 2.

Goal: a new `input.source = "hpsdr"` that takes I/Q straight from a Hermes-class radio over UDP
(OpenHPSDR **Protocol 1**, "Metis"), receive-only, with the radio doing the down-conversion.

Status of the facts below:
- **[code]** = read directly from Pavel Demin's servers (`red-pitaya-notes`,
  `projects/sdr_receiver_hpsdr/server/sdr-receiver-hpsdr.c`, and the QMtech copy
  `qmtech-xc7z020-notes/projects/sdr_receiver_hpsdr_77_76/server/`).
- **[memory]** = my recollection of the OpenHPSDR spec and the Hermes board; to be checked against the
  published Protocol 1 document and the Hermes gateware before relying on it.

## 1. Wire protocol (Protocol 1, all UDP, radio port 1024)

**Discovery** — host sends 60 bytes `EF FE 02 00 ...` (broadcast, or unicast if the address is configured). [code]
Reply (60 bytes): `EF FE 02` idle or `EF FE 03` already streaming to someone [code];
bytes 3..8 MAC; byte 9 gateware version (25 on the RP receiver, 32 on the RP transceiver) [code];
byte 10 board ID (1 = Hermes; both Pavel images report 1) [code]. Pavel appends `"R_PITAYA"` and a
receiver count (8) after byte 10 — a private extension, not part of the standard [code].
Other board IDs (Metis 0, Hermes Lite 6, Orion 5 …) [memory].

**Start/stop** — `EF FE 04 01` start I/Q (02 = wideband, 03 = both), `EF FE 04 00` stop. [code]
The radio streams to the **source address and port of the start packet** [code], so one UDP socket must
send the start and receive the data. No keep-alive is needed by Pavel's servers; they stream until
told to stop [code]. (Hermes/HL2 gateware may differ — check.)

**Commands (host → radio)** — 1032-byte packet `EF FE 01 02`, 4-byte sequence number, then two
512-byte frames. Each frame: `7F 7F 7F`, C0, C1..C4, then 504 bytes of TX data (ignored; send zeros). [code]
- C0 bit 0 is MOX/PTT. **We always send it 0** (receive-only, see §4). C0 bits 7:1 are the address. [code/memory]
- Address 0 (C0 = 0): C1 bits 1:0 = sample rate (0 = 48k, 1 = 96k, 2 = 192k, 3 = 384k where the
  image supports it — the 125 MHz RP receiver stops at 192k, QMtech goes to 384k); C4 bits 5:3 =
  number of receivers − 1. [code]
- RX1 frequency: C0 = 0x04, C1..C4 = frequency in **Hz, big-endian 32-bit**. RX2 = 0x06, RX3 = 0x08,
  RX4 = 0x0A, RX5 = 0x0C, RX6 = 0x0E, RX7 = 0x10, RX8 = 0x24. [code] The radio converts Hz to its own
  NCO word (125 MHz or 77.76 MHz clock) — the host never sees the clock rate. [code]
- Attenuator, preamp, dither, random, Alex filters: other C0 addresses (RP *transceiver* decodes
  attenuator in C4 & 0x1F) [code]; Hermes meanings [memory]. The receiver-only images ignore them. [code]
- Frames should be sent at least when something changes; repeating the sequence is harmless.

**I/Q data (radio → host)** — 1032-byte packet `EF FE 01 06`, 4-byte big-endian sequence number,
two 512-byte frames. Each frame: `7F 7F 7F`, C0..C4 (status), then 504 bytes of sample groups. [code]
- A sample group is N × (I 24-bit, Q 24-bit, big-endian two's complement) followed by 2 bytes of
  microphone audio (ignored). N = number of receivers. [code]
- Groups per frame = 504 / (6N + 2) (N=1 → 63; N=3 → 25). [code] Packets/s at 48 kHz, N=1: ≈ 381.
- Status frames rotate C0 = 0, 8, 16, 24, 32 (address << 3). Address 0 carries the gateware version in C4
  (25 on the RP receiver — matches the discovery reply) [code]. ADC overload is C1 bit 0 of address 0
  [memory]; Pavel's receiver sends 0 there, i.e. **the Red Pitaya never reports overload**. [code]
- Sequence numbers let the host count lost/reordered packets and show it (a "packets lost" figure in
  System Log, and a gap-fill with zeros so timing stays continuous).

## 2. Mapping to navtexd

- New `internal/hpsdr` package: discovery, start/stop, command writer, packet parser; emits complex64
  blocks and the sample rate through the existing I/Q source interface (the stereo I/Q path already in
  `internal/iq`). Pure `net`, no cgo, identical on Windows and Linux.
- `input.hpsdr.lo_below_hz` (the LO below the channel frequency, formerly an absolute `centre_hz`); with the radio the DDC is tuned **to** the dial frequency plus a
  small offset (a few kHz) so the carrier is not at DC; navtexd's own mixer removes the offset.
  48 kHz is enough (NAVTEX occupies ~±400 Hz).
- Multiple receivers on one radio (518/490/424 at once) are natural in this protocol — leave room for
  it in the API now (list of frequencies), implement one receiver first.
- Options: `input.hpsdr.address` (blank = discover), `input.hpsdr.sample_rate_hz`, hardware options only when the
  board reports them (attenuator, preamp, dither, random). The Red Pitaya/QMtech images show none.
- Status: SYNC/DATA as now, plus radio name/firmware, packets lost, and ADC overload where reported.
- Exclusivity: one host per radio. Discovery reply `03` tells us it is busy; say so instead of failing
  silently. The README must say navtexd then replaces Thetis/SDRConnect on that radio.

## 3. Testing

- A fake radio in `internal/hpsdr` tests (UDP server that answers discovery and plays back a recorded
  I/Q clip as protocol packets, with optional drops/reordering) — runs anywhere.
- Real hardware: Red Pitaya, QMtech/Zynq 7020 SDR (both Pavel Demin images), Apache Labs Hermes.
  A Wireshark capture of Thetis or SDRConnect against each radio would settle any disagreement
  between the written spec and a given gateware.

## 4. Safety rule: receive-only

navtexd never keys a transmitter. Transmit data bytes are zero, C0 bit 0 (MOX) is 0, drive-level and
transmit-related addresses are never written, and a test asserts that no outgoing command frame has
MOX set. (Hermes can transmit; a stray bit must not be able to.)

## 5. Open questions

- Hermes board: does the gateware stop streaming without regular host packets? Which C0 addresses set
  attenuator/preamp/dither/random, and how does the 500 kHz response look with the board's input filters?
- Protocol 2 (Orion/Saturn): separate ports per stream, phase-word frequencies. Not needed for the three
  radios on hand; add later if wanted.
- Sample-rate map differs by gateware (RP-125 MHz: 48/96/192; QMtech: also 384). Use what the user
  selects; warn if the radio's data rate disagrees with the requested one.

## 6. Several receivers from one radio (shared session)
A Protocol 1 radio streams to one host (the one that sent the start packet); one stream carries up to 8 receivers that
share the sample rate and the antenna. The samples are interleaved: every group holds I and Q (24 bit each) of receiver 1,
2, ... N followed by 2 microphone bytes, so a frame (504 bytes) holds 504 / (6N + 2) groups. The command packet's first frame
sets the rate and, in C4 bits 5:3, N - 1; its second frame carries one receiver's frequency (C0 = 4 for receiver 1, 6 for 2, ...),
so the keep-alive repeats the frequencies in turn.
- `hpsdr.Open` joins an existing `session` for the radio when a channel of this program already streams from it (found by
  address; a radio that is busy with our own session is not "busy"), otherwise it creates one. Opens are serialized.
- A channel gets the lowest free receiver. When the count must grow the stream is stopped, reconfigured and started again
  (packets in flight have the old layout and are discarded for 0.5 s); leaving does not change the count. The last channel to
  leave stops the radio.
- A channel that asks for another sample rate than the running session is refused with a message. The whole radio shares
  one packet-loss counter; its notices go to the first channel that uses it.
- Tested against the fake radio with three receivers (each with its own tone); not yet against real hardware.

