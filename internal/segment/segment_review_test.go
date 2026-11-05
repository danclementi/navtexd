package segment

// Review tests (not part of the project): property and fuzz tests proposed in pass 9 of the review.

import (
	"reflect"
	"strings"
	"testing"
)

func segRun(text string, chunk int, cfg Config) []Record {
	var out []Record
	s := New(cfg, func(a, b float64) float64 { return b - a }, func(r Record) { out = append(out, r) })
	rs := []rune(text)
	for i := 0; i < len(rs); i += chunk {
		j := min(i+chunk, len(rs))
		// every character has its own time: 0.07 s apart, whatever the chunking
		for k := i; k < j; k++ {
			s.Feed(string(rs[k]), float64(k)*0.07)
		}
		s.Tick(float64(j) * 0.07)
	}
	s.Flush(float64(len(rs)) * 0.07)
	return out
}

func segRunChunks(text string, chunk int, cfg Config) []Record {
	var out []Record
	s := New(cfg, func(a, b float64) float64 { return b - a }, func(r Record) { out = append(out, r) })
	rs := []rune(text)
	for i := 0; i < len(rs); i += chunk {
		j := min(i+chunk, len(rs))
		s.Feed(string(rs[i:j]), float64(i)*0.07) // one time for the whole chunk, as the decoder delivers
	}
	s.Flush(float64(len(rs)) * 0.07)
	return out
}

// Fuzz: arbitrary text must not panic, and records must be well formed: lines in time order, EndS >= StartS, a Message ends
// with NNNN.
func FuzzReview_Segmenter(f *testing.F) {
	f.Add("ZCZC BA12\nTEXT\nNNNN\n", uint8(3))
	f.Add("XXZCZCZCZC~ZCZ\nNN~N\nNNNNZCZC", uint8(1))
	f.Add("CZC FE01\r\nHELLO WORLD THIS IS A TEST\nNNNN", uint8(5))
	f.Fuzz(func(t *testing.T, text string, chunk uint8) {
		if chunk == 0 {
			chunk = 1
		}
		for _, r := range segRunChunks(text, int(chunk), DefaultConfig()) {
			if r.EndS < r.StartS {
				t.Fatalf("record ends before it starts: %+v", r)
			}
			for i := 1; i < len(r.Lines); i++ {
				if r.Lines[i].StreamS < r.Lines[i-1].StreamS {
					t.Fatalf("lines out of time order: %+v", r.Lines)
				}
			}
			if r.Kind == Message && (len(r.Lines) == 0 || r.Lines[len(r.Lines)-1].Text != "NNNN") {
				t.Fatalf("message without NNNN: %+v", r)
			}
		}
	})
}

// Property: the records do not depend on how the text is split into Feed calls (per-character times kept the same).
func TestReview_SegmenterChunkIndependence(t *testing.T) {
	text := strings.Repeat("JUNK TEXT BEFORE ZCZC GA01\nGALE WARNING NORTH SEA\nNNNN\nCZC FE02\nSTRAY LINE WITHOUT END\n", 20)
	ref := segRun(text, len(text), DefaultConfig())
	if len(ref) == 0 {
		t.Fatal("no records")
	}
	for _, c := range []int{1, 2, 3, 7, 64} {
		if got := segRun(text, c, DefaultConfig()); !reflect.DeepEqual(ref, got) {
			t.Errorf("chunk %d: %d records, whole text %d", c, len(got), len(ref))
		}
	}
}

// Finding G1 (suspected, low): a record is closed only by ZCZC, NNNN or IdleCloseS (60 s) without a character. Text that never
// pauses (a continuous non-NAVTEX SITOR/teleprinter signal, or a framer locked on interference) makes one record that grows
// without bound: here about 3 hours of text become a single fragment of 75 000 characters.
func TestReview_SegmenterUnboundedRecord(t *testing.T) {
	var got []Record
	s := New(DefaultConfig(), nil, func(r Record) { got = append(got, r) })
	tm := 0.0
	for i := 0; i < 15000; i++ { // 15000 x 5 chars, one every 0.14 s: about 3 hours
		s.Feed("ABCD ", tm)
		tm += 0.7
	}
	s.Flush(tm)
	if len(got) == 1 {
		t.Fatalf("one record for %.0f minutes of uninterrupted text (%d lines): records have no upper bound", tm/60, len(got[0].Lines))
	}
}
