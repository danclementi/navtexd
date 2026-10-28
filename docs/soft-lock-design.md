# Design brief: soft repetition lock for the NAVTEX decoder

Status: proposal. Written from a read of `bgm60/navtex-rs` (`navtex-core`) and an
experiment running our older Python reference on that project's test recordings.
Nothing from navtex-rs was built or run; all numbers below come from reading the
source and from the experiment in section 2.

## 1. Goal

Replace the current framing stage (hard 4-of-7 word detection: 10 valid words to
lock, extend until 8 invalid) with a **soft, repetition-based alignment lock** that:

- finds the character alignment from the 35-bit DX/RX repetition, using soft
  symbol values, so it locks in noise where hard framing cannot;
- survives and repairs a one-bit slip in the bit stream instead of losing sync;
- handles several messages / stations in one recording (lock, release, relock);
- reports characters it could not trust (`~`, `!`) instead of silently guessing.

Everything upstream (DDC, blanker, carrier search/AFC, arctan discriminator,
timing, integrate-and-dump, polarity and bit-order detection) stays as it is.

## 2. Evidence that this is worth doing

I ran our older reference (the attached `navtex_demod.py`, no presence gating, no
mono WavSource) on navtex-rs's recordings. Their 48 kHz mono audio was fed in as
I/Q with Q=0 and LO = 518000 - 1700 Hz, via `ArraySource`. Distances are edit
distances against their `decoded_full.txt`, which contains `~`/`!` markers, so
they overstate real error. No ground truth exists for the real clips. Texts were
not inspected side by side.

| Set | Ours | Theirs (full soft lock) |
|---|---|---|
| real_clip1 (Cherbourg AVURNAV) | 1 message, 250 chars; identical to their *non-lock* decoder | 278 chars, readable |
| real_clip2 | no message, 62 chars; identical to their non-lock decoder | 315 chars, still garbled |
| scen_weak | 0 chars | 180 chars |
| scen_slip_drop / scen_slip_dup | ~195 chars, no parsed message | ~225 chars |
| scen_two_messages | 1 of 2 messages | both |
| synth_clean_seed2 | within 7 edits | reference |

Reading: demodulation is not the gap. Framing and lock are. On clean signals we
match; on weak, slipped, or multi-message input we fall behind.

Cross-check that came out of it: their charset equals ours when codes are read
**LSB first = first bit received** (34 of 35 codes agree; only `<SIB>` is absent
from theirs; their figure-case F/G/D/H are unassigned where we use `!` `&`
`<ENQ>` `£`). That independently confirms our bit order and table.

## 3. Licence

navtex-rs is GPL-3.0-or-later. Our application is GPL-3. A direct port is
licence-compatible provided we keep their copyright and licence notices on
derived files and credit the project in the README. Confirm our repo's declared
version (GPL-3.0-only or -or-later); both can incorporate GPL-3.0-or-later code.

## 4. The algorithm

All of this works on **one soft value per bit decision**, in reception order,
positive = mark = 1. Their soft value is the sum of 8 per-frame `diff` values
(range about ±8). Ours will be the integrate-and-dump output; see section 6 for
calibration.

### 4.1 Hypotheses and comparisons

- 14 hypotheses: 7 bit phases x 2 parities. `h = 2*phase + parity`.
- A character is sent twice; the copy is 5 slots later, i.e. **35 bit decisions**.
- Comparison `b` covers 14 bit decisions and needs `14*b + 55` decisions
  available. For each `h`:
  - `first_group = 2*b + (1 if parity == 0 else 0)`
  - `x_start = phase + 7*first_group`; `x = soft[x_start .. +7]`,
    `y = soft[x_start+35 .. +7]`.

### 4.2 Pair score (scale-invariant)

```
cosine      = (x . y) / (|x| |y| + eps)
both        = x + y
correlation = max over the 32 traffic codes c of (sum_k both[k]*sign(c,k))
              / (sqrt(7) * |both| + eps)
score = (cosine - cos_mu)/cos_sd + (correlation - corr_mu)/corr_sd
```

`sign(c,k)` is +1 if bit `(6-k)` of the code is set, else -1 (first received bit
is the MSB of the stored code). The `_mu/_sd` pairs are the mean and standard
deviation of each measure on pure noise, which puts the two terms on a common
scale. Their values: cosine (0.0026, 0.3793), correlation (0.715, 0.1284).
Because both measures are normalised, the score does not depend on signal level.

### 4.3 Detection

- Keep a sliding window of the last `window` (45) score rows (14 columns each).
- `z[h] = sum_window(score[h]) / (score_sd * sqrt(window))`, with `score_sd`
  1.4168 (the standard deviation of one noise score).
- Search state: when the window is full, lock to the top `h` if
  `z_top >= z_star` (5.0) **and** `z_top - z_second >= gap` (1.0).

### 4.4 Lock, replay, combine

- On lock, decode from `replay_margin` (10) comparisons before the detection
  window start (not before the end of the previous lock), so no text is lost
  to detection latency. The history of soft values is kept for this
  (they cap it at 6000 values and trim to 3000).
- Walk 7-bit groups at the locked phase. A group is a *repeat* when its group
  number parity matches the locked parity and at least 5 groups have been seen;
  combine it with the group 5 back.
- Output is held back `output_delay` (15) comparisons so later filters can still
  remove it.

### 4.5 Combining DX and RX (the `~` / `!` logic)

- Sum the two copies' soft values bit by bit and decide each bit.
- Weight 4 (a valid 4-of-7 word): decode directly.
- Weight 3 or 5: flip the single least-confident bit and decode.
- Otherwise fall back to separate hard decisions on each copy: if exactly one
  is a valid word use it; if neither, emit `~`; if both are valid but differ,
  emit `!`.
- Phasing pairs (alpha, rep) print nothing. Letter/figure shift state is part
  of the combiner and is reset on a lock or an unrelated switch.

### 4.6 Release

While locked, compute `z` over the last `release_window` (30) rows. If the
locked hypothesis **and** both of its one-bit slip neighbours stay below
`release_z` (1.5) for `release_hold` (5) comparisons, release: discard the
pending tail, clear the score window, and start a new "message number" so the
caller can start a new line. A last-resort rule releases if the locked
hypothesis alone is low for `own_release_hold` (25) comparisons.

### 4.7 Following slips and station changes

Every comparison, over the last `switch_window` (12) rows, find the best
hypothesis. If it is not the locked one:

- A **neighbour** (one-bit slip; phase +/-1, wrapping phase 6<->0 flips parity)
  needs `z >= slip_z` (4.0) and a lead of `slip_margin` (2.0).
- An **unrelated** hypothesis needs `switch_z` (5.0) and `switch_margin` (3.0),
  and resets the combiner state.
- The challenger must win `switch_hold` (3) comparisons in a row.
- On switch, find where the change happened: `best_split(old_col, new_col)`
  picks `k` maximising `sum(old[:k]) + sum(new[k:])`, so decoding of the new
  alignment restarts from that point, and held-back characters after it are
  dropped.

### 4.8 Junk filters (idle-state artefacts)

- **Letter run:** 8 or more identical letters in a row (`max_letter_run`) are
  dropped, including the earlier ones still held back.
- **Idle ghost:** a pair whose two groups are each within 1 bit of the two
  phasing codes rotated by the same 1..6 places is a mis-aligned idle signal.
  The third such pair in a row (`ghost_hold`) removes the earlier two still
  pending and suppresses the run.

## 5. How it fits our pipeline

Input to the new stage: `(soft_value)` per bit decision, from our existing
integrate-and-dump, after polarity and bit-order are resolved. Output: a stream of
`(char, bit_index, message_number)`. Our `ZCZC...NNNN` parser then consumes it
with these changes:

- tolerate `~` / `!` inside the `ZCZC` header and `NNNN` trailer (fuzzy match, e.g.
  allow 1 bad character in `ZCZC`/`NNNN`), because the new stage makes them visible;
- use the message number to split messages instead of relying only on `NNNN`;
- the polarity decision stays upstream: the code-correlation term is sign
  sensitive, so an inverted stream will not lock. Do not try to make the lock
  polarity-agnostic.

Keep the old framing path selectable for A/B comparison until the new one wins
on every vector.

## 6. Risks and calibration (the part that needs real work)

1. **Soft-value scale and distribution differ from theirs.** The measures are
   normalised, but `cos_mu/sd`, `corr_mu/sd` and `score_sd` are *empirical noise
   statistics of their soft values*. Our integrate-and-dump symbols will have a
   different distribution. Recalibrate: feed noise-only I/Q through our chain, take
   the soft values, and compute the mean and sd of each measure and of the summed
   score. If they are wrong, `z_star = 5` will be too lax or too strict and false
   locks (or missed locks) follow.
2. **Hand-tuned thresholds.** Their loop gain, `z_star`, `gap`, release and switch
   parameters were tuned on a handful of clips. Treat them as starting points and
   sweep on our own recordings (the 504 kHz I/Q captures, not only their audio).
3. **Noise independence assumption.** `z` assumes the window's scores are independent
   under noise. Check that this holds for our soft symbols (our timing loop and
   filtering correlate adjacent decisions).
4. **False locks on interference.** Our presence gating exists for a reason.
   Keep it ahead of the lock and confirm the lock does not produce text from
   noise-only I/Q (their `silence_never_locks` test is a model; add a
   noise-only test too).
5. **Our timing block structure.** Our timing is estimated in 100-symbol blocks; a
   slip at a block boundary may look different from a continuous clock slip.
   Verify with deliberately dropped/duplicated bits at block edges.

## 7. Test plan

Do this in two stages so framing is validated independently of the demodulator.

**Stage A, exact port (lock only).** navtex-rs ships `bits.csv` (their soft
values) and Python-generated oracle traces for the lock: `lock_core_trace.csv`,
`lock_follow_trace.csv`, `lock_full_trace.csv`, with matching `lock_*_chars.csv`
and `decoded_*.txt`. Feed their `bits.csv` straight into the new stage with their
default parameters and require the same locked state, hypothesis, release and
switch counts at every comparison, and the same characters and text. Run it in
three variants (`core`: no follow/filters; `follow`; `full`), as their tests do.
This is a sound regression oracle that doesn't touch our demodulator.

**Stage B, integration.**
1. Chunk/block invariance: identical output for any input block size (our
   existing rule).
2. Our own vectors: confirm no regression on the clean synthetic signals.
3. Their `scen_weak`, `scen_slip_drop`, `scen_slip_dup`, `scen_two_messages`,
   `scen_two_stations`, `scen_idle_gap` and the two real clips, as 48 kHz audio
   through the real-audio input path. Record characters recovered, messages
   parsed, and count of `~`/`!`.
4. Our 504 kHz I/Q recordings, once they contain NAVTEX; compare old vs new
   framing on the same files.
5. Noise-only and interference-only inputs: no locks, no text.
6. A property test: take a clean message's soft values, delete or duplicate one
   decision mid-message, and require the lock to follow and the text to recover
   within a few characters.

Success criterion: new >= old on every vector, strictly better on weak/slip/multi,
and zero text from noise-only input.

## 8. Suggested order of work

1. Add the navtex-rs vectors (or a licence-appropriate subset) to our tests; record
   the current baseline for each.
2. Implement the lock stage (sections 4.1-4.4, 4.6) and pass Stage A `core`.
3. Add following and switching (4.7); pass `follow`.
4. Add the filters (4.8) and `~`/`!` combining (4.5); pass `full`.
5. Wire it in behind a switch; update the parser (section 5).
6. Recalibrate on our soft symbols (section 6), then sweep thresholds.
7. Compare old vs new on all vectors and real recordings; keep the better default.

## 9. Not in scope here

- Replacing the arctan discriminator or timing stage. The PLL showed no gain,
  and the soft lock helps regardless of demodulator.
- Their audio-only fixed-tone front end; we keep our I/Q front end.
- Live-audio extras (overrun reporting, quality meter), which are worth a
  separate look later.

Source: https://github.com/bgm60/navtex-rs (files read: `navtex-core/src/{params,window,tone,frontend,demod,bitsync,combine,charset,score,softlock,decoder}.rs`,
`navtex-cli/src/{pipeline,overrun}.rs`, `README.md`, `PROVENANCE.md`, `test-vectors/`).

## 10. Status

- **Step 1 done** (`internal/softlock`, `internal/sitor/soft.go`). The lock is a port of navtex-rs's `softlock`, `score`, `combine` and `charset`, including following and both junk filters (they are interleaved with the core in the original). It reproduces the reference at every comparison on all 12 vector sets in the core, follow and full variants (`internal/softlock/lock_test.go`; vectors in `testdata/vectors.tar.gz`).
- **Option:** `decoder.framing = hard | soft`, per channel (Options, Decoder group, visible in sudo mode); changing it restarts the channel. Soft reads the code LSB first, ignores `unshift_on_space`, and searches both polarities at once (28 hypotheses) unless a polarity is forced. Characters are timed by the first copy (bit index - 35).
- **Not done yet:** noise-statistics calibration on our soft symbols (the reference's `cos_mu`, `corr_mu`, `score_sd` are used unchanged), threshold sweeps on our own recordings, ZCZC/NNNN tolerance of `~`/`!` in the parser, message splitting by lock number, bit-order detection for the soft path.
- **Known differences from the hard path:** a few seconds of evidence before the first lock (text from before the lock is recovered by the replay); the last characters of a stream whose repeat copies never arrive are lost (the hard path prints them from one copy); `!` is printed where the two copies each look valid but differ.
