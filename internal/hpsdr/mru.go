package hpsdr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// MaxKnown is how many radios the list of successfully used radios keeps.
const MaxKnown = 4

// Known is a radio that has delivered data to this program.
type Known struct {
	Addr string    `json:"addr"`
	Name string    `json:"name,omitempty"` // what the radio called itself, e.g. R_PITAYA
	Used time.Time `json:"used"`
}

// Label is the text shown for the radio in the interface.
func (k Known) Label() string {
	if k.Name == "" {
		return k.Addr
	}
	return k.Addr + "  " + k.Name
}

// MRU is the list of radios used successfully, most recently used first, kept in a small file. A radio is added when it
// has delivered data, not when its address is merely typed in.
type MRU struct {
	mu   sync.Mutex
	path string
	list []Known
}

// OpenMRU reads the list from path (a missing or unreadable file is an empty list).
func OpenMRU(path string) *MRU {
	m := &MRU{path: path}
	if b, err := os.ReadFile(path); err == nil {
		var l []Known
		if json.Unmarshal(b, &l) == nil {
			for _, k := range l {
				if k.Addr != "" && len(m.list) < MaxKnown {
					m.list = append(m.list, k)
				}
			}
		}
	}
	return m
}

// List returns a copy, most recently used first.
func (m *MRU) List() []Known {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Known(nil), m.list...)
}

// Addrs returns the addresses, most recently used first.
func (m *MRU) Addrs() []string {
	var out []string
	for _, k := range m.List() {
		out = append(out, k.Addr)
	}
	return out
}

// Touch records a successful use: the radio moves to the top, the oldest entries beyond MaxKnown fall off.
func (m *MRU) Touch(addr, name string) error {
	if m == nil || addr == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := []Known{{Addr: addr, Name: name, Used: time.Now().UTC().Truncate(time.Second)}}
	for _, k := range m.list {
		if k.Addr != addr && len(n) < MaxKnown {
			n = append(n, k)
		}
	}
	if len(m.list) > 0 && m.list[0].Addr == addr && m.list[0].Name == name && len(n) == len(m.list) {
		// already first: only the time changes; avoid a write on every restart within the same second
		if m.list[0].Used.Equal(n[0].Used) {
			return nil
		}
	}
	m.list = n
	b, err := json.MarshalIndent(m.list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.path+".tmp", b, 0o644); err != nil {
		return err
	}
	return replaceFile(m.path+".tmp", m.path)
}

// Choose picks the radio to use among those that answered a search. The rules: a radio used before wins (the most
// recently used of them); otherwise the only radio that is not busy; otherwise there is no choice and the error lists
// the candidates. why says in words what decided, for the log.
func Choose(found []Radio, known []string) (r Radio, why string, err error) {
	if len(found) == 0 {
		return Radio{}, "", errors.New("no radio found")
	}
	if len(found) == 1 {
		return found[0], "", nil
	}
	for _, a := range known {
		for _, f := range found {
			if f.Addr != nil && f.Addr.IP.String() == a && !f.Busy {
				return f, fmt.Sprintf("used before; %d radios answered", len(found)), nil
			}
		}
	}
	var free []Radio
	for _, f := range found {
		if !f.Busy {
			free = append(free, f)
		}
	}
	if len(free) == 1 {
		return free[0], fmt.Sprintf("the only one not in use by another program; %d radios answered", len(found)), nil
	}
	var names []string
	for _, f := range found {
		names = append(names, strings.TrimSpace(f.String()))
	}
	return Radio{}, "", fmt.Errorf("found %d radios: %s. Set the radio address (input.radio) to the one you want", len(found), strings.Join(names, "; "))
}

// replaceFile moves tmp over dst. On Windows a rename can fail for a moment while a virus scanner or the search indexer
// has the new file open, so it is retried; as a last resort dst is written directly.
func replaceFile(tmp, dst string) error {
	var err error
	for i := 0; i < 8; i++ {
		if err = os.Rename(tmp, dst); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 15 * time.Millisecond)
	}
	b, rerr := os.ReadFile(tmp)
	if rerr != nil {
		return err
	}
	defer os.Remove(tmp)
	return os.WriteFile(dst, b, 0o644)
}
