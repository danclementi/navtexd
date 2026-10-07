package stations

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestShippedTable(t *testing.T) {
	b, err := os.ReadFile("../../data/YAND_STATIONS.txt")
	if err != nil {
		t.Skip("station file not present:", err)
	}
	tab, err := Parse(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if tab.Version != "VERSION5" || len(tab.Stations) < 200 {
		t.Fatalf("version %q, %d stations", tab.Version, len(tab.Stations))
	}
	// write and parse again: identical content
	var out bytes.Buffer
	if err := tab.Write(&out); err != nil {
		t.Fatal(err)
	}
	tab2, err := Parse(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var out2 bytes.Buffer
	tab2.Write(&out2)
	if !bytes.Equal(out.Bytes(), out2.Bytes()) {
		t.Error("write is not stable")
	}
	// byte-identical to the original apart from the line that lacks its final field
	orig := strings.Split(string(b), "\r\n")
	got := strings.Split(out.String(), "\r\n")
	diff := 0
	for i := range orig {
		if i < len(got) && orig[i] != got[i] {
			diff++
		}
	}
	if diff > 1 {
		t.Errorf("%d lines differ from the original", diff)
	}
	first := tab.Stations[0]
	if first.NavArea != 1 || first.FreqKHz != 490 || first.ID != "A" || first.Callsign != "EJM" || first.Lat != 55.363 ||
		first.Times[1] != "04:00" || len(first.GID) != 4 {
		t.Errorf("first station %+v", first)
	}
	// keyword spaces are significant
	found := false
	for _, s := range tab.Stations {
		for _, k := range s.GID {
			if k == " KARA " {
				found = true
			}
		}
	}
	if !found {
		t.Error("word-bounded keyword \" KARA \" lost its spaces")
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "x\r\n", "VERSION5\r\n1#519#A#X#N#C#C#1#1#00:00#04:00#08:00#12:00#16:00#20:00###\r\n",
		"VERSION5\r\n1#518#A#X#N#C#C#95#1#00:00#04:00#08:00#12:00#16:00#20:00###\r\n",
		"VERSION5\r\n1#518#A#X#N#C#C#1#1#25:00#04:00#08:00#12:00#16:00#20:00###\r\n"} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}
