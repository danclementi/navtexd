package vocab

import (
	"bytes"
	"reflect"
	"testing"
)

func TestCleanLine(t *testing.T) {
	for s, want := range map[string]bool{
		"WINDS 10 TO 15 KT.": true, "WIND~ 10": false, "A{}B": false, "SEAS *3 FT": false, "caps": false, "5-10 FT/ SW": true,
	} {
		if CleanLine(s) != want {
			t.Errorf("%q: want %v", s, want)
		}
	}
}

func TestWords(t *testing.T) {
	got := Words("SCATTERED SHOWERS AT 0900K, NOV23 AND ABC FIFTEENKNOTSWINDSX WUXQPZ", DefaultOptions())
	if want := []string{"SCATTERED", "SHOWERS"}; !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

func TestCounterDistinctLines(t *testing.T) {
	c := NewCounter(Options{MinLen: 5, MaxLen: 16, MinLines: 2})
	for i := 0; i < 5; i++ {
		c.AddLine("REPEATED BROADCAST LINE") // five identical lines count once
	}
	c.AddLine("SHOWERS LIKELY")
	c.AddLine("SHOWERS ENDING")
	c.AddLine("GARBLED SH~WERS")
	v := c.Result()
	if _, ok := v["SHOWERS"]; !ok || v["SHOWERS"] != 2 {
		t.Errorf("%v", v)
	}
	if _, ok := v["BROADCAST"]; ok {
		t.Errorf("a word from one distinct line must not pass: %v", v)
	}
	if c.Lines != 8 || c.Clean != 7 || c.Distinct != 3 {
		t.Errorf("%d %d %d", c.Lines, c.Clean, c.Distinct)
	}
}

func TestWriteRead(t *testing.T) {
	v := Vocabulary{"ZETA": 2, "ALPHA": 5}
	var b bytes.Buffer
	if err := v.Write(&b, "test"); err != nil {
		t.Fatal(err)
	}
	r, err := Read(&b)
	if err != nil || !reflect.DeepEqual(r, v) {
		t.Errorf("%v %v", r, err)
	}
	if r2, _ := Read(bytes.NewBufferString("# c\nword\nOTHER 3\n")); r2["WORD"] != 0 || r2["OTHER"] != 3 || len(r2) != 2 {
		t.Errorf("%v", r2)
	}
}
