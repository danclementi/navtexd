package navtex

import (
	"reflect"
	"testing"
)

func feedAll(chunks ...string) []Message {
	var p Parser
	var out []Message
	for _, c := range chunks {
		out = append(out, p.Feed(c)...)
	}
	return append(out, p.Flush()...)
}

func TestParser(t *testing.T) {
	text := "junk\nZCZC EE73\nFIRST BODY\nLINE 2\nNNNN\nBRK\nZCZC EA78\nSHORT ONE\nBRK\nZCZC FE80\nSECOND\nNNNN\nBRK\nZCZC FA74\nCUT OFF"
	want := []Message{
		{"E", "E", "73", "FIRST BODY\nLINE 2", true},
		{"E", "A", "78", "SHORT ONE", false},
		{"F", "E", "80", "SECOND", true},
		{"F", "A", "74", "CUT OFF", false},
	}
	if got := feedAll(text); !reflect.DeepEqual(got, want) {
		t.Errorf("whole text:\n got %+v\nwant %+v", got, want)
	}
	// identical result for any chunking, including splits inside ZCZC and NNNN
	for n := 1; n <= 7; n++ {
		var chunks []string
		for i := 0; i < len(text); i += n {
			chunks = append(chunks, text[i:min(i+n, len(text))])
		}
		if got := feedAll(chunks...); !reflect.DeepEqual(got, want) {
			t.Fatalf("chunk size %d differs:\n got %+v", n, got)
		}
	}
	if got := feedAll("ZCZC F*97\nBODY\nNNNN"); len(got) != 1 || got[0].ID() != "F*97" || !got[0].Complete {
		t.Errorf("garbled header: %+v", got)
	}
	if got := feedAll("no message here NNNN"); len(got) != 0 {
		t.Errorf("unexpected %+v", got)
	}
	if got := feedAll("ZCZC AB12\n"); len(got) != 1 || got[0].ID() != "AB12" || got[0].Body != "" || got[0].Complete {
		t.Errorf("header-only message at end of stream: %+v", got)
	}
}
