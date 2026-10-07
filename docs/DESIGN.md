# Design note: streaming Go implementation

Status: design notes, partly superseded by `docs/SERVER_DESIGN.md`. Deviations from the Python reference are in `docs/DEVIATIONS.md`.

> **History note.** This document comes from the development of the decoder. It mentions a Python prototype ("the reference") that
> was used to develop and check the Go code. The prototype and its test-vector generator are not part of this repository, so the
> tests that need those vectors (`testdata/`) are skipped here.

## 0. Revision 2026-09-29: single channel, real audio first (overrides sections 1, 2, 4 where they conflict)
- **Primary input:** one real-valued baseband audio stream (mono), NAVTEX carrier at roughly 500-2000 Hz in the audio,
  e.g. a USB receiver channel or SDRconnect audio through a VAC. **One source, one pipeline, one channel** (490 or 518,
  chosen by flag; default 518). Configuration gives `lo_hz` (the RF frequency of 0 Hz audio) and the sideband.
- Wideband I/Q and dual-channel decoding remain possible through the same `Source` interface (`Kind` = `IQ`) but are
  secondary; do not let them complicate the single-channel path. The fan-out in section 4 is therefore optional.
- **Portability:** core packages are pure Go (no cgo) and build for Windows and Linux (`GOOS=windows go build ./...` is
  part of the check). Sources, in order: WAV file, raw samples on stdin, network stream, then platform audio capture
  (miniaudio via cgo, behind a build tag).
- **Events, not printing:** the decoder emits an event stream so YaND-like features (message store, filter by station/subject,
  de-duplication, logging, alerts, GUI) are subscribers added later.
- The DDC treats real audio as I/Q with Q = 0 (a later optimisation may skip the imaginary multiplies). Image rejection
  of the decimator is covered by `TestRealAudioImageRejection`.

## 1. Goals and constraints
- Decode NAVTEX from a live stereo I/Q stream at any sample rate that covers the channel(s) (62.5 kHz upward), for one or
  both of 490/518 kHz at once, with a latency of a few seconds.
- Same code path for files and live input; only the source differs.
- Real-time budget is generous: 64 kHz complex input is trivial for one core; 1 Msps still comfortable. Correctness and
  clean state handling matter far more than speed. Do not optimise before profiling.

## 2. Package layout (proposed)
```
cmd/navtex/          main: flags, wiring, output
internal/iq/         Source interface, WAV file source, (later) audio/VAC source, ring buffer
internal/ddc/        NCO mixer, FIR decimator stages, streaming FIR
internal/fsk/        blanker, presence detector, discriminator, timing loop, integrate-and-dump, carrier tracking
internal/sitor/      CCIR 476 tables, framing state machine, DX/RX combiner, text decoder
internal/navtex/     message parser (ZCZC/NNNN, B1..B4), message events
internal/testvec/    loaders for testdata/ (WAV, JSON, raw binary)
tools/               Python helpers: make_audio.py (I/Q recording to receiver-style audio), pace_pcm.py (real-time pacing of a WAV)
testdata/            generated vectors (not checked in if large)
```

## 3. Interfaces
```go
// Source delivers I/Q in blocks; sample index, not wall-clock, is time.
type Source interface {
    SampleRate() float64
    CentreHz() float64
    // Read fills dst with up to len(dst) samples; io.EOF at end of a file. Live sources block or return what is ready.
    Read(dst []complex64) (int, error)
}

// Each DSP stage is a small struct with state and a Process method that accepts any block length.
type Stage interface { Process(in []complex128) []complex128 } // exact shapes per stage may differ
```
Use `complex128` internally for DSP (matches numpy/scipy float64 results closely), `complex64` at the source boundary.

## 4. Runtime structure
```
Source --> reader goroutine --> fan-out --> per-channel pipeline goroutine (490) --> events
                                        \-> per-channel pipeline goroutine (518) --> events
                                                                                       |
                                                                                 printer / log
```
- Reader copies into buffered channels of fixed-size blocks (e.g. 16384 samples). If a consumer falls behind, drop the block,
  bump an overrun counter, and log it. For files, apply backpressure instead of dropping.
- One goroutine per channel; no shared mutable DSP state between them.
- Output is an event stream: `LockAcquired`, `LockLost`, `Char`, `Message{B1,B2,B3B4,Text}`, `Stats`.

## 5. Replacing the batch parts of the reference
| Reference (batch) | Streaming replacement |
|---|---|
| Whole-record Welch + comb for carrier offset | Sliding-window comb (e.g. 4 s, 50% overlap) over the decimated stream while unlocked; once locked, a slow frequency loop driven by the soft-symbol cluster centres (`(median hi + median lo)/2` over the last N symbols, or a leaky integrator). Re-search after lock is lost. |
| Multi-pass carrier refinement | The slow loop above; it converges over seconds, so expect the first few characters of a burst to be less clean. Phasing lasts >= 10 s, which is the intended acquisition time. |
| Blanker uses per-0.5 s block median | Running median or a robust running scale estimate (e.g. exponentially-smoothed |x|, or median-of-blocks of the last 1-2 s). Must be causal and must not blank a strong steady signal. |
| Framing extends regions backwards | Keep, for each of the 7 bit alignments, a history ring of the last ~40 words with validity. On lock (10 valid words at one alignment), you can look back to recover words before the lock. Forward: LOCKED state until 8 invalid in a row; trailing-word trimming becomes "hold output until 5 valid words have followed" or accept a few junk characters at the end. |
| Polarity and bit order from whole record | Run both polarities (and both bit orders) as parallel hypotheses during acquisition; commit when one shows a valid run of 10+ words (polarity) and `ZCZC` appears (bit order). After commit, discard the loser. Log the decision. |
| DX/RX parity from whole region | Per lock, accumulate DX(s)==RX(s+5) agreement per slot parity over a window of ~20 slots and commit when one parity clearly wins. Keep a 5-slot soft-value history so combining is possible as soon as parity is known. |
| ~1 s timing blocks | Acceptable at first. Later: shorter blocks with the same tracker, or a Gardner-type loop; validate against golden vectors before/after. |

## 6. Latency budget (rough)
Timing block 1 s + lock acquisition (10 valid words = 0.7 s) + diversity delay 0.35 s: a character appears about 2 s after
it is received, once locked. Acquisition of a new burst takes on the order of 1-3 s.

## 7. Failure and robustness policy
- Sample loss: count gaps; a gap of a few samples is absorbed by the timing loop, larger gaps should reset timing (and log).
- Silence / no signal: presence detector stays low, nothing is decoded, no output beyond periodic stats.
- Adjacent-channel and impulsive interference: blanker first; if it is not enough, consider robust discriminator weighting.
- Every threshold is a named, documented config field with the reference value as its default.

## 8. Test plan
Golden vectors came from a generator that is not distributed (deterministic seeds). For each case `<name>` it writes:
- `<name>.wav`: int16 stereo I/Q, 64 kHz, centre 504 kHz, with 490 and 518 kHz stations,
- `<name>.json`: parameters, true messages, and per channel the reference results (carrier estimate, polarity, bit order,
  decoded text, parsed messages, region list, counts),
- `<name>_<ch>_soft.f32`: reference soft symbols (little-endian float32) and `<name>_<ch>_lock.u8`: lock mask,
- `filters.json`: FIR tap arrays (DDC stages and the narrow low-pass) so Go can start with the reference filters and later
  verify its own Kaiser design against them.

Test ladder (each level must pass before the next is built):
1. WAV reader: sample count, first samples, RIFF quirks (JUNK chunk, data size larger than file, 24-bit, float32).
2. Streaming invariance: any block partition gives identical output (per stage).
3. DDC output vs. reference within numerical tolerance (relative error ~1e-6 in float64).
4. Discriminator, presence, timing, integrate-and-dump: soft symbols vs. reference (correlation ~1, max abs error small; exact
   equality is not required once you stream, but lock decisions and decoded text must match).
5. Framing and combiner: bit-exact codewords vs. reference decode on the same soft symbols.
6. End to end: decoded text and parsed messages match the reference on clean and nominal cases; weak/impulsive cases must
   not be worse than the reference by more than a few characters (document the numbers).
7. Property tests: random block sizes, gaps, DC offsets, +/-20 Hz carrier offsets, +/-50 ppm clock offsets.

## 9. Milestones
1. Repo skeleton, `go.mod`, WAV source with tests. *Done when* level-1 tests pass.
2. DDC (mixer + decimators) with reference filters. *Done when* level-2/3 pass on all cases.
3. FSK stages through soft symbols. *Done when* level-4 passes; write down where and why it deviates.
4. SITOR framing, combiner, text decoder, message parser on the reference's soft symbols first, then on your own. *Done when*
   level-5/6 pass on the clean case.
5. Replace batch parts (section 5) one at a time, re-running the ladder after each. *Done when* all cases decode.
6. Two-channel goroutine wiring, event stream, CLI, stats.
7. VAC source (cgo question decided here), ring buffer, overrun accounting; soak test for hours; real off-air recordings.
8. Retune thresholds on real signals; update `docs/DEVIATIONS.md` and this note.

## 10. Open questions for the owner
- Output format: console text only, or also a log file / JSON lines / serial-number de-duplication of repeated messages?
- Should the program pick the channels to decode from flags, or always decode both?
- Which sample rate will the VAC deliver in practice, and does SDRconnect allow selecting it?

## 11. Real-valued audio input (planned, optional)
A later requirement: run from a narrow real-valued audio channel (for example a USB-mode receiver tuned so 518 kHz lands
at about 1.5 kHz, LO 516.5 kHz, ~3 kHz bandwidth, 8-48 kHz sample rate) instead of wideband I/Q. **Design for it now; it is
cheap.** It was verified in the Python reference (`usb_real` vector, plus ad-hoc tests at 8, 11.025 and 48 kHz).

- **It works through the existing chain.** Treat real audio as complex I/Q with Q = 0 and set *centre = LO frequency*. The NCO
  mixes by `channel - centre` (= the tone's audio frequency); the real signal's mirror image lands at twice that offset on
  the other side and is removed by the decimation filters. No Hilbert transform needed. LSB works the same way (centre =
  LO, shift becomes negative); the tones are then swapped, which the polarity detection already handles.
- **Make the `Source` interface carry this.** Add a kind (`IQ` or `Real`) or simply let a mono source return `complex64` with
  zero imaginary part; add sideband/LO configuration. Do not hard-code "two channels in, complex out" in `internal/ddc`.
  The WAV source must accept mono files (the reference does: 1 channel = real audio).
- **Audio tone offset must stay away from 0 Hz and Nyquist.** Near 0 Hz the mirror image falls inside the narrow filter and
  hurts; keep the tone at roughly 0.5-2.5 kHz (1.5 kHz is a good choice). The decimator must reject the image *before* the
  final rate reduction; the existing cascade design does, but keep a test that catches a violation.
- **One channel per source.** 490 and 518 kHz are 28 kHz apart and cannot share a 3 kHz audio channel, so a real-audio
  deployment needs one audio source (receiver, VAC) per channel. Structure the wiring so each pipeline owns its source
  rather than assuming one shared reader with two consumers.
- **Sample rates.** Any rate from ~6 kHz up is workable (8, 11.025, 12, 16, 44.1, 48 kHz...). `pick_decim` chooses an integer
  decimation near fs/4000; the resulting rate need not be exactly 4000 Hz, and samples/symbol may be fractional.
- **Receiver-side effects the Python tests do not model** (test with real recordings): AGC pumping on impulses and
  strong neighbours; receiver noise blankers or DSP noise reduction (may damage FSK; prefer off); audio band-pass filter phase
  and skirts near the tones; LO/dial error of a few Hz to tens of Hz shifting both tones (keep the carrier search/tracking
  range at least +/-200 Hz); audio-chain clock differences (the symbol clock comes from the transmitter, not the
  soundcard, so the timing loop absorbs it).
- **Signal-presence and blanker thresholds** were tuned on wideband I/Q. Narrow audio changes the noise statistics
  (impulses are already smeared and AGC-shaped), so treat those constants as needing re-tuning for this mode.
