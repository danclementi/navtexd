package server

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"math"
)

// ---- compact spectrum frames ----
// Viewers (who are usually far away) get the spectrum as event "spectrum2" instead of "spectrum": 1 dB steps in 8 bits,
// only the filtered and channel traces, each frame as the change from the previous one (a full frame every KeyEvery
// frames and whenever the client missed one), deflated and sent as base64. That is about a tenth of the JSON frame.
// The browser asks for it with ?z=1 when it has DecompressionStream.

const (
	keyEvery = 20
	noData   = int8(-128) // "no data" (the JSON frame says -3000 tenths of a dB)
	qMax     = 126
)

type specState struct {
	prev []int8 // the last frame sent (n filtered + m channel values)
	n    int    // frames since the last full frame
}

type compactMsg struct {
	Rcv     int     `json:"rcv,omitempty"`
	Start   float64 `json:"start"`
	Step    float64 `json:"step"`
	Carrier float64 `json:"carrier"`
	FS      float64 `json:"fs"`
	N       int     `json:"n"`           // filtered values
	M       int     `json:"m,omitempty"` // channel values
	Key     int     `json:"k,omitempty"` // 1 = a full frame, else the change from the previous one
	D       string  `json:"d"`
}

func quant(tenths int16) int8 {
	if tenths <= -3000 {
		return noData
	}
	q := int(math.Round(float64(tenths) / 10))
	return int8(max(-qMax, min(qMax, q)))
}

// encode makes the compact event for one client and remembers what it sent.
func (st *specState) encode(sm spectrumMsg) (event, bool) {
	q := make([]int8, 0, len(sm.Filt)+len(sm.Chan))
	for _, v := range sm.Filt {
		q = append(q, quant(v))
	}
	for _, v := range sm.Chan {
		q = append(q, quant(v))
	}
	key := st.prev == nil || len(st.prev) != len(q) || st.n >= keyEvery-1
	raw := make([]byte, len(q))
	for i, v := range q {
		if key {
			raw[i] = byte(v)
		} else {
			raw[i] = byte(v - st.prev[i]) // wraps; the browser adds it to an Int8Array, which wraps the same way
		}
	}
	if key {
		st.n = 0
	} else {
		st.n++
	}
	st.prev = q
	var buf bytes.Buffer
	zw, _ := flate.NewWriter(&buf, flate.BestCompression)
	zw.Write(raw)
	zw.Close()
	m := compactMsg{Rcv: sm.Rcv, Start: sm.Start, Step: sm.Step, Carrier: sm.Carrier, FS: sm.FS, N: len(sm.Filt), M: len(sm.Chan),
		D: base64.StdEncoding.EncodeToString(buf.Bytes())}
	if key {
		m.Key = 1
	}
	b, err := json.Marshal(m)
	if err != nil {
		return event{}, false
	}
	return event{"spectrum2", b}, true
}
