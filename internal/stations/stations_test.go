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

// 4209.5 kHz is a channel of the table, and a station may have fewer than six transmissions a day.
func TestDecimalFrequencyAndFewTimes(t *testing.T) {
	in := "VERSION5\r\n" +
		"9#4209.5#X#SUZ#Ismailia#EGY#Egypt#30.467#32.367#07:50#11:50#####KEY#UNIQUE#\r\n" +
		"11#486#B#XSQ#Guangzhou#CHN#China#23.159#113.513#00:10#04:10#08:10#12:10#16:10#20:10###\r\n"
	tab, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	s := tab.Stations[0]
	if s.FreqKHz != 4209.5 || s.Hz() != 4209500 || s.Times[0] != "07:50" || s.Times[2] != "" || len(s.GID) != 0 && s.GID[0] != "KEY" {
		t.Fatalf("station: %+v", s)
	}
	if tab.Stations[1].Hz() != 486000 || FreqString(486) != "486" || FreqString(4209.5) != "4209.5" {
		t.Fatalf("frequencies: %d %s %s", tab.Stations[1].Hz(), FreqString(486), FreqString(4209.5))
	}
	var out bytes.Buffer
	if err := tab.Write(&out); err != nil {
		t.Fatal(err)
	}
	if out.String() != in {
		t.Fatalf("written differently:\n%q\n%q", out.String(), in)
	}
	for _, bad := range []string{
		"9#4209#X#SUZ#I#EGY#Egypt#30.467#32.367#07:50#####\r\n",   // not a channel
		"9#4209.5#X#SUZ#I#EGY#Egypt#30.467#32.367#######\r\n",     // no time at all
		"9#4209.5#X#SUZ#I#EGY#Egypt#30.467#32.367#7:50######\r\n", // not hh:mm
	} {
		if _, err := Parse(strings.NewReader("VERSION5\r\n" + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
