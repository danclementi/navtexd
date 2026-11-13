// Package ident identifies the transmitting NAVTEX station of a received message.
//
// The B1 letter and the frequency do not name a station: (frequency, B1) is reused in every NAVAREA, and US stations
// often transmit outside their slot. So the candidates (stations on the frequency, with the B1 letter if it was read)
// are scored on what the message says and where the station is:
//
//   - unique keywords (UID: the station's own name or call sign) are strong evidence;
//   - geographical keywords (GID: places named in the station's warnings) count more the fewer stations share them;
//   - the time slot only matters when the B1 letter is unknown or unreliable (every station with the same letter shares
//     the slot), and US stations are allowed to be late or early;
//   - nearer stations are slightly preferred (ground wave), and stations already received here slightly more.
//
// A station is named only when the evidence is clear; otherwise the record stays unidentified (UNID) and the best
// candidates with their scores are kept so that the operator can decide.
package ident

import (
	"math"
	"sort"
	"strings"
	"time"

	"navtex/internal/geo"
	"navtex/internal/stations"
)

// Scoring constants.
const (
	uidScore      = 10.0 // first UID hit
	uidExtra      = 2.0  // each further UID hit
	uidCap        = 14.0
	gidUnit       = 4.0 // a GID hit is worth gidUnit / (number of stations sharing it)
	gidCap        = 8.0
	slotMax       = 2.0
	distMax       = 0.6
	histMax       = 0.5
	minKeyword    = 2.0 // keyword evidence needed to name a station
	minMargin     = 0.75
	maxCandidates = 6
	maxOffLetter  = 2 // stations of other letters offered as candidates when B1 may have been misread
)

// Input is what is known about a received message.
type Input struct {
	FreqKHz float64
	B1      string    // station letter; "" or "?" when not read
	NavArea int       // > 0 when set by the operator (manual override)
	When    time.Time // start of reception, UTC
	Text    string    // the message text without the ZCZC line and NNNN
	HaveOp  bool      // operator position known
	OpLat   float64
	OpLon   float64
}

// Term is one piece of evidence in a station's score: what was found and how many points it gave.
type Term struct {
	Kind    string  `json:"k"`            // uid, gid, slot, dist, hist
	Keyword string  `json:"kw,omitempty"` // uid, gid: the keyword of the station table
	Shared  int     `json:"n,omitempty"`  // gid: stations of the frequency that use the keyword; hist: messages received from the station before
	Points  float64 `json:"p"`
	Loose   bool    `json:"loose,omitempty"` // uid, gid: found only inside a longer word
	Dup     bool    `json:"dup,omitempty"`   // uid, gid: the station's list holds the keyword more than once, so it counted again
	Capped  bool    `json:"cap,omitempty"`   // uid, gid: held back by the cap on keyword points
	Used    bool    `json:"used"`            // slot: false when B1 was read, so the time of day is not counted
	Km      float64 `json:"km,omitempty"`    // dist: the distance
}

// Candidate is a possible station with its score.
type Candidate struct {
	Index   int // index into the station table
	Score   float64
	Keyword float64 // the part of the score that comes from keywords
	Terms   []Term
}

// Result is the outcome.
type Result struct {
	Index      int // station index, -1 = unidentified
	Score      float64
	Method     string // UID, GID, B1 (the only station with that letter), manual; "" when unidentified
	Ambiguous  bool   // several stations fit about equally
	Candidates []Candidate
	Margin     float64 // best score minus the second best (0 when only one station was left)
	Rivals     bool    // more than one station was scored
	// LetterDiffers is set when a station of another B1 letter than the one read is among the candidates: it is due to transmit at
	// the time of reception and its keywords are in the text, so the letter may have been misread. It is never named by itself.
	LetterDiffers bool
}

// Identifier scores messages against a station table.
type Identifier struct {
	tab    *stations.Table
	byFreq map[float64][]int
	df     map[float64]map[string]int   // per frequency: keyword -> number of stations using it
	users  map[float64]map[string][]int // per frequency: keyword -> the stations using it
	// History returns how many messages from station index i were received before (nil = no history).
	History func(i int) int
}

// New prepares an identifier for a station table.
func New(t *stations.Table) *Identifier {
	id := &Identifier{tab: t, byFreq: map[float64][]int{}, df: map[float64]map[string]int{}, users: map[float64]map[string][]int{}}
	for i, s := range t.Stations {
		id.byFreq[s.FreqKHz] = append(id.byFreq[s.FreqKHz], i)
		m := id.df[s.FreqKHz]
		if m == nil {
			m = map[string]int{}
			id.df[s.FreqKHz] = m
		}
		u := id.users[s.FreqKHz]
		if u == nil {
			u = map[string][]int{}
			id.users[s.FreqKHz] = u
		}
		seen := map[string]bool{}
		for _, k := range append(append([]string(nil), s.GID...), s.UID...) {
			if k = strings.ToUpper(k); k != "" && !seen[k] {
				seen[k] = true
				m[k]++
				u[k] = append(u[k], i)
			}
		}
	}
	return id
}

// Normalize prepares message text for keyword matching: upper case, line ends and runs of blanks as single spaces, one
// space at each end so that keywords with significant spaces match whole words.
func Normalize(s string) string {
	s = strings.ToUpper(s)
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	return " " + strings.Join(strings.Fields(s), " ") + " "
}

func isAlnum(b byte) bool { return b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' }

// hit reports whether keyword kw occurs in the normalised text. A keyword with a leading or trailing space matches as
// written; a short keyword (3 characters or fewer) must be a whole word; longer ones may be part of a word.
func hit(text, kw string) bool {
	if strings.TrimSpace(kw) == "" {
		return false
	}
	if kw != strings.TrimSpace(kw) || len(kw) > 3 {
		return strings.Contains(text, kw)
	}
	for i := 0; ; {
		j := strings.Index(text[i:], kw)
		if j < 0 {
			return false
		}
		a, b := i+j-1, i+j+len(kw)
		if (a < 0 || !isAlnum(text[a])) && (b >= len(text) || !isAlnum(text[b])) {
			return true
		}
		i += j + 1
	}
}

// wholeWord reports whether kw occurs in the text as a word of its own (not inside a longer one).
func wholeWord(text, kw string) bool {
	k := strings.TrimSpace(kw)
	for i := 0; k != ""; {
		j := strings.Index(text[i:], k)
		if j < 0 {
			return false
		}
		a, b := i+j-1, i+j+len(k)
		if (a < 0 || !isAlnum(text[a])) && (b >= len(text) || !isAlnum(text[b])) {
			return true
		}
		i += j + 1
	}
	return false
}

// slotTerm says how well a time fits the station's transmission slots: full inside the slot (allowing a minute early),
// half within 30 minutes after the start, else none.
func slotTerm(s stations.Station, when time.Time) float64 {
	w := slotMax
	if s.CountryCode == "USA" {
		w /= 2 // US stations are often off schedule
	}
	return w * slotFit(s, when)
}

// slotFit is 1 when the time is inside one of the station's slots, 0.5 when it is up to 30 minutes after the start, else 0.
func slotFit(s stations.Station, when time.Time) float64 {
	if when.IsZero() {
		return 0
	}
	u := when.UTC()
	now := float64(u.Hour()*60+u.Minute()) + float64(u.Second())/60
	best := 0.0
	for _, ts := range s.Times {
		m, ok := stations.ParseClock(ts)
		if !ok {
			continue
		}
		d := now - float64(m)
		if d < -720 {
			d += 1440
		} else if d > 720 {
			d -= 1440
		}
		switch {
		case d >= -1 && d <= 10:
			best = math.Max(best, 1)
		case d > 10 && d <= 30:
			best = math.Max(best, 0.5)
		}
	}
	return best
}

// lateWeight is how much a station whose slot began 10 to 30 minutes ago (running late) counts as a possible sender.
const lateWeight = 0.3

// sharing says how much a keyword of station i is worth, as the number to divide gidUnit by, and how many stations count as
// sharing it. Only stations that could have sent the message share it: with B1 read, those with that letter; with the time known,
// those whose slot fits (weight 1) or began up to 30 minutes ago (lateWeight). The station itself always counts as 1. Without a
// time, every station of the frequency that uses the keyword counts.
func (id *Identifier) sharing(i int, k string, in Input) (div float64, n int) {
	if in.When.IsZero() {
		n = id.df[id.tab.Stations[i].FreqKHz][k]
		return math.Max(1, float64(n)), n
	}
	b1 := strings.ToUpper(strings.TrimSpace(in.B1))
	known := len(b1) == 1 && b1[0] >= 'A' && b1[0] <= 'Z'
	div, n = 1, 1
	for _, j := range id.users[id.tab.Stations[i].FreqKHz][k] {
		if j == i {
			continue
		}
		o := id.tab.Stations[j]
		if known && !strings.EqualFold(o.ID, b1) {
			continue
		}
		w := 0.0
		switch slotFit(o, in.When) {
		case 1:
			w = 1
		case 0.5:
			w = lateWeight
		}
		if w > 0 {
			div += w
			n++
		}
	}
	return div, n
}

type score struct {
	total, kw float64
	uid, gid  int
	terms     []Term
}

func (id *Identifier) score(i int, in Input, text string) score {
	s := id.tab.Stations[i]
	var sc score
	uids := 0
	var uterms []Term
	seen := map[string]bool{}
	for _, k := range s.UID {
		k = strings.ToUpper(k)
		if hit(text, k) {
			uterms = append(uterms, Term{Kind: "uid", Keyword: strings.TrimSpace(k), Loose: !wholeWord(text, k), Dup: seen[k]})
			uids++
		}
		seen[k] = true
	}
	if uids > 0 {
		sc.kw += math.Min(uidCap, uidScore+uidExtra*float64(uids-1))
		sc.uid = uids
		left := math.Min(uidCap, uidScore+uidExtra*float64(uids-1)) // handed out hit by hit
		for n := range uterms {
			want := uidExtra
			if n == 0 {
				want = uidScore
			}
			uterms[n].Points = math.Min(want, left)
			uterms[n].Capped = uterms[n].Points < want
			left -= uterms[n].Points
		}
		sc.terms = append(sc.terms, uterms...)
	}
	g := 0.0
	var gterms []Term
	seen = map[string]bool{}
	for _, k := range s.GID {
		k = strings.ToUpper(k)
		if hit(text, k) {
			div, n := id.sharing(i, k, in)
			if n < 1 {
				n = 1
			}
			g += gidUnit / div
			sc.gid++
			gterms = append(gterms, Term{Kind: "gid", Keyword: strings.TrimSpace(k), Shared: n, Points: gidUnit / div, Loose: !wholeWord(text, k), Dup: seen[k]})
		}
		seen[k] = true
	}
	sc.kw += math.Min(gidCap, g)
	left := math.Min(gidCap, g)
	for n := range gterms {
		want := gterms[n].Points
		gterms[n].Points = math.Min(want, left)
		gterms[n].Capped = gterms[n].Points < want
		left -= gterms[n].Points
	}
	sc.terms = append(sc.terms, gterms...)
	sc.total = sc.kw
	slot := slotTerm(s, in.When)
	used := in.B1 == "" || in.B1 == "?" // the slot only tells something when B1 does not
	if used {
		sc.total += slot
	}
	if slot > 0 || used {
		sc.terms = append(sc.terms, Term{Kind: "slot", Points: slot, Used: used})
	}
	if in.HaveOp {
		d := geo.DistanceKm(in.OpLat, in.OpLon, s.Lat, s.Lon)
		p := distMax * math.Exp(-d/2000)
		sc.total += p
		sc.terms = append(sc.terms, Term{Kind: "dist", Points: p, Km: math.Round(d)})
	}
	if id.History != nil {
		n := id.History(i)
		p := math.Min(histMax, 0.1*math.Log1p(float64(n)))
		sc.total += p
		sc.terms = append(sc.terms, Term{Kind: "hist", Points: p, Shared: n})
	}
	return sc
}

// Explain scores one station of the table for a message, whether or not Identify would consider it (to show why a station the
// operator picked has the score it has).
func (id *Identifier) Explain(in Input, index int) Candidate {
	if index < 0 || index >= len(id.tab.Stations) {
		return Candidate{Index: index}
	}
	sc := id.score(index, in, Normalize(in.Text))
	return Candidate{Index: index, Score: round2(sc.total), Keyword: sc.kw, Terms: sc.terms}
}

// Identify names the station of a message, or reports that it cannot.
func (id *Identifier) Identify(in Input) Result {
	res := Result{Index: -1}
	b1 := strings.ToUpper(strings.TrimSpace(in.B1))
	known := len(b1) == 1 && b1[0] >= 'A' && b1[0] <= 'Z'
	var cands []int
	for _, i := range id.byFreq[in.FreqKHz] {
		s := id.tab.Stations[i]
		if known && !strings.EqualFold(s.ID, b1) {
			continue
		}
		if in.NavArea > 0 && s.NavArea != in.NavArea {
			continue
		}
		cands = append(cands, i)
	}
	text := Normalize(in.Text)
	type sc struct {
		i int
		score
	}
	all := make([]sc, len(cands))
	for n, i := range cands {
		all[n] = sc{i, id.score(i, in, text)}
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].total > all[b].total })
	// A read B1 may be wrong. Stations of other letters that are due to transmit at the time of reception (none when the
	// operator has set a NAVAREA) are scored as if B1 were unknown; the time of day does not count in their favour
	// (it is what makes them candidates), so they are judged on keywords, distance and history alone.
	var off []sc
	if known && in.NavArea == 0 && !in.When.IsZero() {
		un := in
		un.B1 = "?"
		for _, i := range id.byFreq[in.FreqKHz] {
			s := id.tab.Stations[i]
			if strings.EqualFold(s.ID, b1) || slotFit(s, in.When) < 1 {
				continue
			}
			o := sc{i, id.score(i, un, text)}
			o.total -= slotTerm(s, in.When)
			for k := range o.terms {
				if o.terms[k].Kind == "slot" {
					o.terms[k].Used = false // shown, but not counted
				}
			}
			off = append(off, o)
		}
		sort.SliceStable(off, func(a, b int) bool { return off[a].total > off[b].total })
	}
	if len(all) == 0 && len(off) == 0 {
		return res
	}
	// Off-letter stations with keyword evidence are only offered as candidates: a station may also transmit out of its slot, or
	// read out messages for other areas, so the operator decides.
	type cand struct {
		sc
		off bool
	}
	var list []cand
	for _, a := range all {
		list = append(list, cand{a, false})
	}
	for n := 0; n < len(off) && n < maxOffLetter; n++ {
		if off[n].kw >= minKeyword {
			list = append(list, cand{off[n], true})
			res.LetterDiffers = true
		}
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].total > list[b].total })
	for n, c := range list {
		if len(res.Candidates) >= maxCandidates {
			break
		}
		if c.total > 0 || n == 0 {
			res.Candidates = append(res.Candidates, Candidate{Index: c.i, Score: round2(c.total), Keyword: c.kw, Terms: c.terms})
		}
	}
	if len(all) == 0 {
		return res
	}
	best := all[0]
	margin := math.Inf(1)
	if len(all) > 1 {
		margin = best.total - all[1].total
		res.Margin, res.Rivals = margin, true
	}
	switch {
	case len(all) == 1 && (in.NavArea > 0 || known):
		res.Index, res.Score = best.i, round2(best.total)
		res.Method = "B1"
		if in.NavArea > 0 {
			res.Method = "manual"
		}
	case in.NavArea > 0 && best.kw < minKeyword && margin < minMargin:
		res.Ambiguous = len(all) > 1 // manual area and letter still leave several stations: no guess
	case best.kw >= minKeyword && margin >= minMargin:
		res.Index, res.Score = best.i, round2(best.total)
		res.Method = "GID"
		if best.uid > 0 {
			res.Method = "UID"
		}
	default:
		res.Ambiguous = best.kw >= minKeyword
	}
	return res
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Thresholds are the keyword score a station needs to be named, and the lead it needs over the next one.
func Thresholds() (keyword, margin float64) { return minKeyword, minMargin }
