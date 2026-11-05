# navtexd code review — findings and ranked summary

Nov 5, 2026 · @Dan

17 findings are confirmed by a failing test and 8 more are suspected; the most serious are a restart race in the runner, a viewer leak of the operator position, and admin access through a header-less local proxy.

## Overview

The review followed the nine prompts of `docs/REVIEW_PACKAGE.md` section 5, one pass at a time. No tracked file was changed: `git status` shows only the new `*_review_test.go` files.

- Tests ran on a Linux copy of the tracked files plus the vendored modules, with Go 1.24.7 and the `go` line lowered in the copy only.
- The existing suite still passes, including a full 32-bit run (`GOARCH=386`, standing in for armv7's 32-bit `int`).
- `sdrconnect` under `-race`, listed in the package as not yet run, is clean.
- Nothing ran on Windows or on real ARM hardware.

## Pass 1: yand parsers (untrusted files)

**Y1 — confirmed: import work grows with the square of the file size.** `internal/yand/abs.go:372-373`.

- For every record, `ReadABS` reads every candidate text of its page in full, with no caching. A blob header can claim up to 2^28 bytes (`:118`), so one fake "chained" text spans the whole file.
- In the test a 400 KB file made the reader read 1.39 GB (3,335× the file); a 20 MB file would need about 1 TB.
- Test: `TestReview_ABSQuadraticReread`.
- Fix: decode each blob once and cache it; cap a blob's length at what is left of the file (`(pages − pg) × dataLen`); stop searching candidates after the first time match.
- Import is admin-only, so the realistic trigger is an operator importing a crafted file.

**Y2 — suspected: memory from a large report.** `report.go:56` keeps every line in memory, so a 1 GiB report likely needs several GB of heap. Fix: cap record and line counts.

Fuzzing both readers (`FuzzReview_ReadABS`, `FuzzReview_ReadReport`) found no panics or infinite loops. Page chains are never followed, so a cyclic chain is a non-issue.

## Pass 2: ws and sdrconnect

**S1 — confirmed: a silent server is never detected.** `sdrconnect.go:141`.

- No read deadline is ever set. If SDRconnect stops sending but the TCP connection stays open (hung program, sleeping PC, Wi-Fi drop), `Read` blocks forever: no error and no reconnect. The input watchdog only warns.
- Test: `TestReview_SilentServerNeverDetected` (fails after 26 s of silence).
- Fix: set a read deadline (for example 5–10 s, refreshed on each frame) and send a ping when idle.
- SpyServer has the same pattern (`spyserver.go:207` clears its deadline after setup); that part is suspected.

**W1–W4 — confirmed: `ws.go` does not enforce RFC 6455.**

| ID | Line | Defect |
| --- | --- | --- |
| W1 | `ws.go:177` | A continuation frame with nothing started is returned as message type 0 |
| W2 | `ws.go:174` | A new data frame inside a fragmented message silently drops the earlier fragments |
| W3 | `ws.go:169` | Control frames are not limited to 125 bytes: a 1 MiB ping gets a 1 MiB pong |
| W4 | `ws.go:151` | Masked frames from the server are accepted |

Tests: `ws_review_test.go`. Fix: fail the connection in each case, and check that RSV bits are zero.

## Pass 3: server security model

**A1 — confirmed: viewers can work out the operator's position.** `server/records.go:156` sends `cand` and `is` (identification scores) to viewers unchanged.

- The scores include `0.6·exp(−d/2000)` for distance (`ident/ident.go:208`).
- In the test, the same message seen by a viewer scores Charleston 0.45 with the receiver at Norfolk and 0.02 with it at London. 0.45 converts directly to about 575 km.
- This breaks CLAUDE.md rule 6. Test: `TestReview_ViewerScoresDependOnPosition`.
- Fix: when `!withDist`, remove `Candidates` scores and `IdentScore`, or recompute them without the distance term.

**A2 — confirmed as behaviour: a local reverse proxy that adds no forwarding headers makes every client an admin.** `access.go:93`.

- nginx's default `proxy_pass` is an example. A script sending `X-Navtex: 1` with no `Origin` also passes `CheckWrite`.
- Test: `TestReview_HeaderlessLocalProxyIsAdmin`.
- Fix: either require a token whenever `server.listen` is not loopback-only, or treat loopback as admin only when the Host header names `localhost` or the actual listen address.

**A3 — confirmed: token guessing is not throttled.** `/api/login` (`server.go:953`) answered 300 wrong tokens in 65 ms from the tunnel listener. Test: `TestReview_LoginNoRateLimit`. Fix: rate-limit per peer.

**Suspected, low:**

- The cookie's `Secure` flag is false behind a TLS proxy.
- `conn.go:136` puts a spoofable `X-Real-Ip` into the ipwho.is URL, with an unbounded cache (only when `geo_lookup` is on).
- The hub has no limit on the number of SSE clients.

No other path leaks the operator position. DNS rebinding and IPv4-mapped IPv6 are handled correctly.

## Pass 4: races and goroutine leaks

**R1 — confirmed, highest-impact bug in the review: concurrent restarts corrupt channel state.** `app/runner.go:269`.

- `stopChain` clears `c.stop/c.done` and then waits without holding the lock. A second `Restart` running at the same time sees nothing running, skips the wait, and starts a new run while the old one is still finishing.
- The old run's deferred cleanup (`:490`, `c.tap.Store(nil)`, and the matching ring and scope cleanup) then wipes the new run's tap, ring and state.
- In the test, both channels showed as stopped while running, and the shared tap was gone, in round 0.
- Real trigger: two HTTP handlers calling `Restart` at once (`server.go:634/667/698/899/913/930`).
- Test: `TestReview_ConcurrentRestartLeavesWrongState`.
- Fix: serialize Stop/Start/Restart with one mutex; use `CompareAndSwap(tap, nil)` in the defers.

**Suspected:**

- When the input owner panics, it restarts after 3 s, but channels sharing its input get EOF and stay stopped.
- `Tap.Close` running at the same time as `Publish` would panic (send on a closed channel). It isn't reachable today but is fragile.

A `-race` stress test of restarts, settings changes and engine commands (`TestReview_RunnerRestartStress`) passes, with no goroutine leaks after `Stop`.

## Pass 5: store

**D1 — confirmed: a replace import loses records stored while it runs.** `store/dbio.go:214`.

- The backup (`VACUUM INTO`) is taken before the write transaction that deletes everything. Records stored in between are in neither the backup nor the new log.
- The test lost 52–58 of about 85 such records.
- Test: `TestReview_ReplaceLosesConcurrentAdds`.
- Fix: take the backup inside the write lock, or check the highest stored ID after the transaction and copy newer rows across.

**Suspected, low:** replace reuses old record IDs, so a stale client's PATCH can pass the version check against a different record.

The SQL is all parameterized, LIKE is escaped correctly, IDs use AUTOINCREMENT, and no way was found for undo or trash to lose or duplicate records.

## Pass 6: 32-bit ARM

**I1 — confirmed: WAV crash and restart loop.** `iq/wav.go:154`. A `block_align` smaller than channels × bits / 8 makes `Read` index past its buffer and panic. The runner then restarts the channel every 3 s forever, and `cmd/navtex` crashes. Fix: reject the file if `align < channels*bits/8`.

**I2 — confirmed: huge allocation from a 44-byte WAV.** `wav.go:110/125`. The `fmt` and `ds64` chunk sizes are allocated as the file states them, so a 44-byte file allocated 2 GB. On armv7, sizes above 2^31 make `make()` panic. Fix: cap the size at about 64 bytes, or skip the chunk by seeking.

Tests: `wav_review_test.go`. Both fail on amd64 and 386.

**Suspected: running out of memory on a 1 GB board.** `clip.go:64` caps memory at 256 MiB per ring, not overall. At 384 kHz the default 70 s of clip buffer is about 215 MB per channel, and `Take` copies the whole ring when a clip is saved. Fix: a global budget, or set `GOMEMLIMIT` on ARM.

No `int` overflow at 2^31 was found (sample counters are `int64`), and 64-bit alignment is fine.

## Pass 7: hpsdr and spyserver

**H1 — confirmed: the HPSDR input stalls silently if the radio's packet counter starts again from 0.** `hpsdr/session.go:403`.

- Every packet with `Seq < next` is dropped as late. `lastData` is refreshed before that check (`:399`), so the 10 s silence check never fires.
- In the test, 1000 of 1000 packets were dropped: no samples and no error.
- Test: `TestReview_SequenceRestartStallsForever`.
- Fix: treat a backward jump of more than about 64 packets as a restart; resync and log it.

Transmit safety is fine: C0 bit 0 (MOX) is never set and no command ever selects the transmit frequency. The SpyServer length cap (1 MiB) and `u32s` bounds checks are sound.

## Pass 8: XSS in web/\*.js

Nothing found. The only `innerHTML` uses are static (an SVG and `data-label` values from `index.html`). All other data goes through `textContent`, attributes are set with `setAttribute`, clip links are URL-encoded, and the CSP blocks inline script.

## Pass 9: sitor and segment property and fuzz tests

The new tests `TestReview_FramerBlockSizeIndependence`, `TestReview_FramerBoundedMemory`, `FuzzReview_Framer` (1.8 M runs, with NaN and Inf in the input), `FuzzReview_Segmenter` and `TestReview_SegmenterChunkIndependence` all pass.

**G1 — confirmed, low: records have no size limit.** `segment.go:79`. A record is closed only by ZCZC, NNNN or 60 s without a character, so text that never pauses grows one record without limit. In the test, 175 minutes of text became a single record of 375 lines. Test: `TestReview_SegmenterUnboundedRecord`. Fix: a maximum record length in characters or time.

## Ranked summary

| # | Finding | Severity | Status |
| --- | --- | --- | --- |
| 1 | R1: concurrent `Restart` corrupts channel state and the shared tap | High | confirmed |
| 2 | A1: viewers can infer the operator position from identification scores | High | confirmed |
| 3 | A2: local proxy without forwarding headers gives every client admin | High (depends on setup) | confirmed |
| 4 | H1: HPSDR counter restart stalls the input silently | Medium | confirmed |
| 5 | S1: SDRconnect has no read deadline, so a silent server hangs it | Medium | confirmed (SpyServer suspected) |
| 6 | D1: replace import loses records stored during it | Medium | confirmed |
| 7 | Y1: ABS import work grows with the square of the file size | Medium | confirmed |
| 8 | I1, I2: crafted WAV causes panic and restart loop, or 2 GB allocation | Medium–Low | confirmed |
| 9 | Clip ring memory on a 1 GB ARM board | Medium | suspected |
| 10 | A3: no throttling of token guesses | Low–Medium | confirmed |
| 11 | Channels sharing an input stay stopped after the owner panics | Low–Medium | suspected |
| 12 | W1–W4: ws protocol strictness | Low | confirmed |
| 13 | G1: records have no size limit | Low | confirmed |
| 14 | Report memory, hub client cap, geo-lookup input, cookie `Secure`, ID reuse on replace, `Tap.Close` fragility | Low | suspected |

## New test files

These are the only changes in the repo. To reproduce: `go test -run Review ./internal/...`; the failing tests are exactly the confirmed findings above.

- `internal/app/runner_review_test.go`
- `internal/hpsdr/session_review_test.go`
- `internal/iq/wav_review_test.go`
- `internal/iq/mem_review_test.go`
- `internal/sdrconnect/sdrconnect_review_test.go`
- `internal/segment/segment_review_test.go`
- `internal/server/server_review_test.go`
- `internal/sitor/sitor_review_test.go`
- `internal/sitor/mem_review_test.go`
- `internal/store/dbio_review_test.go`
- `internal/ws/ws_review_test.go`
- `internal/yand/abs_review_test.go`

## Notes for a follow-up session

- **The review tests are meant to fail.** 17 of them fail until their bugs are fixed, so `go test ./...` fails for now. Treat each failing `TestReview_*` as the check that its fix worked; the other review tests and fuzz targets pass and should keep passing.
- **Each test file explains its finding.** The comment above every `TestReview_*` describes the defect and how it is triggered; read them alongside this document.
- **How the review was run.** Go 1.24.7 on Linux, vendored modules, the `go` line lowered in a copy only. On the owner's Windows PC with Go 1.26 the tests should run as they are. `TestReview_SilentServerNeverDetected` takes about 28 s; `TestReview_SequenceRestartStallsForever` about 9 s.
- **Minor observations not listed above:**
    - `Runner.Do` (`app/runner.go:319`) can report success for a command that never runs if the channel stops in between.
    - `navtex.SplitHeader` (`navtex/message.go`) slices bytes, so a non-ASCII header could put invalid UTF-8 into B1.
    - SpyServer's lost-packet counter (`spyserver.go`, `s.lost += seq - last - 1`) wraps to a huge number when the sequence goes backwards.
