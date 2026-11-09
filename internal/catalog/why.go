package catalog

import (
	"fmt"
	"math"

	"navtex/internal/ident"
	"navtex/internal/store"
)

// Chip is a short remark on the identification of a record, shown as a small label; Kind is ok, info or warn.
type Chip struct {
	Kind string `json:"k"`
	Text string `json:"t"`
	Tip  string `json:"tip,omitempty"`
}

// WhyCand is one station that was scored for a record, with the evidence that gave its score.
type WhyCand struct {
	ID      int          `json:"id"` // station number (1-based)
	Label   string       `json:"label"`
	Score   float64      `json:"score"`
	Keyword float64      `json:"kw"`
	Terms   []ident.Term `json:"terms"`
	Chosen  bool         `json:"chosen,omitempty"` // the station of the record
}

// Why explains the identification of a record: it is worked out again from the message text with the current station table.
type Why struct {
	Method     string    `json:"method"`
	Reason     string    `json:"reason,omitempty"`
	Ambiguous  bool      `json:"amb,omitempty"`
	Rivals     bool      `json:"rivals,omitempty"`
	Margin     float64   `json:"margin"`
	NeedKW     float64   `json:"need_kw"`
	NeedMargin float64   `json:"need_margin"`
	B1         string    `json:"b1"`
	B1Known    bool      `json:"b1_known"`
	Differs    string    `json:"differs,omitempty"` // the station that working it out again gives, when that is not the record's
	Cands      []WhyCand `json:"cands"`
	Chips      []Chip    `json:"chips"`
	Keywords   []string  `json:"words,omitempty"` // the keywords of the chosen (else best) station that were found in the text
}

// Explain works out the identification of r again and describes it. With loc false the operator's position is not used, so
// that nothing shows distances.
func (c *Catalog) Explain(r store.Record, loc bool) *Why {
	w := &Why{Method: r.IdentMethod, B1: r.B1, Cands: []WhyCand{}, Chips: []Chip{}}
	w.NeedKW, w.NeedMargin = ident.Thresholds()
	if w.Method == "" {
		w.Method = "UNID"
	}
	b1 := r.B1
	w.B1Known = len(b1) == 1 && b1[0] >= 'A' && b1[0] <= 'Z' || len(b1) == 1 && b1[0] >= 'a' && b1[0] <= 'z'
	if r.Kind == "fragment" {
		w.Chips = append(w.Chips, Chip{"info", "fragment", "A fragment has no start, so no station is worked out for it; the operator can give it one."})
		if r.StationID > 0 {
			w.Chips = append(w.Chips, Chip{"warn", "forced ID", "The station was chosen by the operator; the message text does not support it."})
		}
		return w
	}
	counts := map[int]int(nil)
	if c.st != nil {
		counts = c.st.CountByStation()
	}
	c.mu.Lock() // the identifier's History hook is shared
	defer c.mu.Unlock()
	in := ident.Input{FreqKHz: float64(r.Channel) / 1000, B1: r.B1, When: r.Received, Text: bodyText(&r)}
	if r.Manual {
		in.NavArea = r.NavArea
	}
	if loc && c.Position != nil {
		in.OpLat, in.OpLon, in.HaveOp = c.Position()
	}
	c.id.History = func(i int) int { return counts[i+1] }
	res := c.id.Identify(in)
	w.Rivals, w.Ambiguous = res.Rivals, res.Ambiguous
	if !math.IsInf(res.Margin, 0) && !math.IsNaN(res.Margin) {
		w.Margin = math.Round(res.Margin*100) / 100
	}
	add := func(cd ident.Candidate) {
		w.Cands = append(w.Cands, WhyCand{ID: cd.Index + 1, Label: Label(c.tab.Stations[cd.Index]), Score: cd.Score, Keyword: math.Round(cd.Keyword*100) / 100, Terms: cd.Terms, Chosen: cd.Index+1 == r.StationID})
	}
	have := false
	for _, cd := range res.Candidates {
		add(cd)
		have = have || cd.Index+1 == r.StationID
	}
	if r.StationID > 0 && !have && r.StationID <= len(c.tab.Stations) { // a station the operator chose that was not scored in the list
		add(c.id.Explain(in, r.StationID-1))
	}
	if r.Picked == 0 && res.Index+1 != r.StationID && r.Kind != "fragment" {
		if res.Index >= 0 {
			w.Differs = Label(c.tab.Stations[res.Index])
		} else {
			w.Differs = "UNID"
		}
	}
	// the evidence of the station of the record, else of the best candidate
	var main *WhyCand
	for i := range w.Cands {
		if w.Cands[i].Chosen {
			main = &w.Cands[i]
		}
	}
	if main == nil && len(w.Cands) > 0 {
		main = &w.Cands[0]
	}
	if r.StationID == 0 {
		switch {
		case len(w.Cands) == 0:
			w.Reason = fmt.Sprintf("No station of this frequency fits B1 %q.", r.B1)
		case main.Keyword < w.NeedKW:
			w.Reason = fmt.Sprintf("The best station has %.1f points from keywords; %.1f are needed to name one.", main.Keyword, w.NeedKW)
		case w.Rivals && w.Margin < w.NeedMargin:
			w.Reason = fmt.Sprintf("The best station leads by %.2f; %.2f are needed to name one.", w.Margin, w.NeedMargin)
		}
	}
	chip := func(k, t, tip string) { w.Chips = append(w.Chips, Chip{k, t, tip}) }
	switch w.Method {
	case "UNID":
		chip("warn", "UNID", "No station could be named. "+w.Reason)
	case "UID":
		chip("ok", "UID", "Named by a keyword that only this station uses (its name, call sign...).")
	case "GID":
		chip("ok", "GID", "Named by keywords of the area that this station reads out.")
	case "B1":
		chip("info", "B1", "The only station of this frequency with that B1 letter.")
	case "manual":
		chip("info", "manual", "B1 and NAVAREA, or the station, were set by the operator.")
	case "forced ID":
		chip("warn", "forced ID", "The station was chosen by the operator; the message text does not support it.")
	default:
		chip("info", w.Method, "")
	}
	if main != nil {
		uid, gid, shared, loose, dup := 0, 0, 0, false, false
		seen := map[string]bool{}
		for _, t := range main.Terms {
			switch t.Kind {
			case "uid":
				uid++
			case "gid":
				gid++
				if t.Shared > 1 {
					shared++
				}
			case "slot":
				if t.Used && t.Points > 0 {
					chip("ok", "slot ✓", "The message started inside the station's transmission time slot (B1 was not read, so the time counts).")
				}
			}
			if (t.Kind == "uid" || t.Kind == "gid") && t.Points >= 0 {
				if t.Keyword != "" && !seen[t.Keyword] {
					seen[t.Keyword] = true
					w.Keywords = append(w.Keywords, t.Keyword)
				}
				loose = loose || t.Loose
				dup = dup || t.Dup
			}
		}
		if uid > 0 {
			chip("ok", fmt.Sprintf("UID ×%d", uid), "Keywords of this station found in the text, each only used by it.")
		}
		if gid > 0 {
			chip("ok", fmt.Sprintf("GID ×%d", gid), "Area keywords found in the text.")
		}
		if uid == 0 && gid > 0 && shared == gid {
			chip("warn", "shared keywords", "All keyword evidence is also used by other stations of this frequency, so each hit counts for only a share.")
		}
		if loose {
			chip("warn", "inside a word", "A keyword was found only as part of a longer word.")
		}
		if dup {
			chip("warn", "repeated keyword", "The station's keyword list holds a keyword twice, so it was counted twice.")
		}
	}
	if w.B1Known {
		chip("info", "B1 "+b1, "The station letter as received. If it was misread, the stations are narrowed down wrongly.")
	} else {
		chip("warn", "B1 not read", "The station letter was not received; the time slot is used instead.")
	}
	if w.Ambiguous && r.StationID == 0 {
		chip("warn", "ambiguous", "Several stations fit about equally well.")
	}
	if main != nil && main.Chosen && w.Rivals && w.Margin < 1.5 && len(w.Cands) > 1 && w.Cands[0].ID == main.ID {
		chip("warn", fmt.Sprintf("close call %.1f", w.Margin), "The next station is only this many points behind.")
	}
	if w.Differs != "" {
		chip("warn", "differs now", "Worked out again with the current station table, this message gives: "+w.Differs)
	}
	return w
}
