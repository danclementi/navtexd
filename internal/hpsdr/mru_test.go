package hpsdr

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func radio(ip string, busy bool) Radio {
	return Radio{Addr: &net.UDPAddr{IP: net.ParseIP(ip), Port: 1024}, MAC: net.HardwareAddr{1, 2, 3, 4, 5, 6}, Board: 1, Busy: busy}
}

func TestMRUOrderCapAndPersistence(t *testing.T) {
	p := filepath.Join(t.TempDir(), "radios.json")
	m := OpenMRU(p)
	if len(m.List()) != 0 {
		t.Fatal("new list should be empty")
	}
	for _, a := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5"} {
		if err := m.Touch(a, "R_"+a); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.Join(m.Addrs(), " ")
	if got != "10.0.0.5 10.0.0.4 10.0.0.3 10.0.0.2" {
		t.Fatalf("cap of %d, newest first: %s", MaxKnown, got)
	}
	m.Touch("10.0.0.3", "R_10.0.0.3") // used again: back to the top, nothing dropped
	if got := strings.Join(m.Addrs(), " "); got != "10.0.0.3 10.0.0.5 10.0.0.4 10.0.0.2" {
		t.Fatalf("reuse moves to the top: %s", got)
	}
	m2 := OpenMRU(p) // survives a restart
	if got := strings.Join(m2.Addrs(), " "); got != "10.0.0.3 10.0.0.5 10.0.0.4 10.0.0.2" {
		t.Fatalf("after reload: %s", got)
	}
	if l := m2.List(); l[0].Label() != "10.0.0.3  R_10.0.0.3" || l[0].Used.IsZero() {
		t.Fatalf("entry: %+v", l[0])
	}
	if (*MRU)(nil).Touch("1.2.3.4", "") != nil || len((*MRU)(nil).List()) != 0 {
		t.Fatal("a nil list must be harmless")
	}
	if OpenMRU(filepath.Join(t.TempDir(), "nope", "x.json")).Touch("", "x") != nil {
		t.Fatal("an empty address is ignored")
	}
}

func TestChooseAmongSeveral(t *testing.T) {
	a, b, c := radio("10.0.0.1", false), radio("10.0.0.2", false), radio("10.0.0.3", true)
	if r, why, err := Choose([]Radio{a}, nil); err != nil || r.Addr.IP.String() != "10.0.0.1" || why != "" {
		t.Fatalf("single: %v %q %v", r, why, err)
	}
	// a known one wins; the most recently used of the known ones
	if r, why, err := Choose([]Radio{a, b}, []string{"10.0.0.9", "10.0.0.2", "10.0.0.1"}); err != nil || r.Addr.IP.String() != "10.0.0.2" || why == "" {
		t.Fatalf("known: %v %q %v", r, why, err)
	}
	// a known one that is busy is passed over
	if r, _, err := Choose([]Radio{c, b}, []string{"10.0.0.3"}); err != nil || r.Addr.IP.String() != "10.0.0.2" {
		t.Fatalf("known but busy: %v %v", r, err)
	}
	// none known: the only free one
	if r, why, err := Choose([]Radio{c, a}, nil); err != nil || r.Addr.IP.String() != "10.0.0.1" || !strings.Contains(why, "only one not in use") {
		t.Fatalf("only free: %v %q %v", r, why, err)
	}
	// none known, two free: no guess; the error names them all
	_, _, err := Choose([]Radio{a, b, c}, nil)
	if err == nil || !strings.Contains(err.Error(), "10.0.0.1") || !strings.Contains(err.Error(), "10.0.0.2") || !strings.Contains(err.Error(), "10.0.0.3") {
		t.Fatalf("ambiguous: %v", err)
	}
}
