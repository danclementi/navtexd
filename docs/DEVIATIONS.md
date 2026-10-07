# Deviations from the Python reference

> **History note.** This document comes from the development of the decoder. It mentions a Python prototype ("the reference") that
> was used to develop and check the Go code. The prototype and its test-vector generator are not part of this repository, so the
> tests that need those vectors (`testdata/`) are skipped here.

## D1. Carrier offset: streaming search + tracking (milestone 3b)
Reference: Welch PSD comb over the whole record, then up to 4 batch refinement passes.
Go (`internal/fsk/carrier.go`, `receiver.go`):
- While no carrier is known, every 1 s the comb is evaluated on the last 4 s of blanked baseband (2048-point Hann Welch,
  50 % overlap, same 20 Hz smoothing and +/-400 Hz grid). A result is accepted when the comb peak is >= 2.5x the median
  score ("contrast"; noise gives ~1.2, signals >= 3.3 on the golden cases).
- Once accepted, a slow loop nudges f0 every >= 100 new locked symbols using the mark/space cluster medians of the last 300
  locked symbols (needs >= 200 symbols, >= 60 of each sign), gain 0.7 (reference used 1.5 in batch passes), step <= 20 Hz.
  Symbols produced before the last retune are ignored.
- The carrier is forgotten after 10 s without timing lock and searched again.
- Decisions happen on hop boundaries (1 s of samples) so results are bit-identical for any input block size.
Results (golden vectors, 13 case/channel runs): lock within 2-5 s, locked-symbol counts equal the reference (weak 490 kHz
also equal), tracked f0 within ~1-5 Hz of the reference refined value. Final f0 can wander a few Hz in the noise after a burst.

## D2. Blanker
Same block-median rule, applied per 0.5 s block of the stream; output is delayed by up to one block plus one sample
(needed for the median and the +/-1 sample dilation). Output equals the reference exactly. `Flush` handles the final partial block.

## D3. Integrate-and-dump / symbol release
Symbols of a 100-symbol timing block are released after the block is analysed and the samples of their windows exist
(latency about 1-2 s, as planned). Values equal the reference to ~1e-5 Hz. Symbols whose window would extend beyond the end
of the stream are reported unlocked with soft = 0, as in the reference.

## D4. NCO
64-bit phase accumulator instead of `exp(-j(phi + w n))`; identical to ~1e-12 and drift-free.

## D5. SITOR framing (milestone 4)
Reference: whole-record polarity score, region search with backward extension and ragged-end trimming, per-region DX/RX parity.
Go (`internal/sitor/decoder.go`): both polarities are tracked in parallel; lock after 10 consecutive valid words at one
alignment (region starts at the first of those 10 words, no backward extension - only phasing precedes it); unlock after
8 consecutive invalid words. DX/RX parity is voted from agreeing (s, s+5) pairs (>= 10 agreeing and >= 3x the other parity),
buffered words are then decoded retroactively; a re-lock at the same alignment within 3000 symbols reuses the parity.
Trailing junk (up to ~4 characters after `NNNN`) is not trimmed. Bit order: both readings run until `ZCZC` appears in one;
that order is then kept for the rest of the run (`-order` fixes it). Until then the LSB reading is shown live; if the MSB
reading finds ZCZC first, its buffered text is released and LSB output stops. Soft-decision combining and the "two different valid
words = phasing, keep DX" rule are as in the reference. On the reference's own soft symbols the decoded messages match
(identical wrong-character counts on every golden case).

## D6. End-to-end statistics
On 16 random-seed runs per SNR (bench generator not distributed, both channels) total wrong characters: nominal 219 (Go) vs 218 (reference),
weak 1013 vs 947. Individual runs flip either way by chance (carrier trajectory differs); tracker gain/window changes did not
move the totals, so the defaults stay.

## D7 Output shaping and incomplete messages
Line breaks around ZCZC/BRK, the unreadable-character policy and incomplete-message reporting are additions
to the Python reference (which only parsed complete ZCZC..NNNN messages).
