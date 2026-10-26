# SDRconnect probe

`probe.py` talks to SDRconnect's WebSocket API directly and prints what comes back. It was written to find out how SDRconnect
(1.0.10, API 1.0.3) treats its centre frequency (LO), and is worth running again after an SDRconnect update. See the header of
`probe.py` for the commands. It needs `pip install websocket-client`, and it moves the receiver, so do not run it while navtexd
uses the same SDRconnect.

## What was measured (SDRconnect 1.0.10, RSPduo, GUI and headless)

- `set_property device_center_frequency` is often ignored: no error, no event, with or without `"device":"primary"`. (It was
  accepted right after a sample-rate change and after starting from a far-away tuning.)
- The centre moves by itself only when the tuned frequency (VFO) leaves the received span; moving the VFO inside the span leaves it
  where it is. After an excursion it stays put, so tuning back does not restore the old centre.
- When it re-centres, the new centre is the VFO plus an offset of SDRconnect's own, except at 250 kS/s, where the centre equals the
  VFO exactly (`sweep 250000 100000 150000 200000 517000`: centre - VFO = 0 every time).
- At 500 kS/s and 2 MS/s the offset was not 0 (+100 to +150 kHz and +283 to +700 kHz), and the centre never goes below 0.4 x the
  sample rate (200 kHz and 800 kHz), so 517 kHz cannot be the centre at 2 MS/s.
- Changing the sample rate to 250000 re-centres on the VFO.

navtexd therefore uses 250 kS/s, asks for the wanted centre, and if that is ignored tunes the VFO away and then onto the wanted
centre (`recentre` in `internal/sdrconnect`). If someone moves SDRconnect's centre while a channel runs, the channel stops with
an explanation. Details: `docs/SDRCONNECT_DESIGN.md`.
