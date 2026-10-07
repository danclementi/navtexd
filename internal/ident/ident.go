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
)

// Input is what is known about a received message.
type Input struct {
	FreqKHz int
	B1      string    // station letter; "" or "?" when not read
	NavArea int       // > 0 when set by the operator (manual override)
	When    time.Time // start of reception, UTC
	Text    string    // the message text without the ZCZC line and NNNN
	HaveOp  bool      // operator position known
	OpLat   float64
	OpLon   float64
}

// Candidate is a possible station with its score.
type Candidate struct {
	Index int // index into the station table
	Score float64
}

// Result is the outcome.
type Result struct {
	Index      int // station index, -1 = unidentified
	Score      float64
	Method     string // UID, GID, B1 (the only station with that letter), manual; "" when unidentified
	Ambiguous  bool   // several stations fit about equally
	Candidates []Candidate
}

// Identifier scores messages against a station table.
type Identifier struct {
	tab    *stations.Table
	byFreq map[int][]int
	df     map[int]map[string]int // per frequency: keyword -> number of stations using it
	// History returns how many messages from station index i were received before (nil = no history).
	History func(i int) int
}

// New prepares an identifier for a station table.
func New(t *stations.Table) *Identifier {
	id := &Identifier{tab: t, byFreq: map[int][]int{}, df: map[int]map[string]int{}}
	for i, s := range t.Stations {
		id.byFreq[s.FreqKHz] = append(id.byFreq[s.FreqKHz], i)
		m := id.df[s.FreqKHz]
		if m == nil {
			m = map[string]int{}
			id.df[s.FreqKHz] = m
		}
		seen := map[string]bool{}
		for _, k := range append(append([]string(nil), s.GID...), s.UID...) {
			if k = strings.ToUpper(k); k != "" && !seen[k] {
				seen[k] = true
				m[k]++
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

// slotTerm says how well a time fits the station's transmission slots: full inside the slot (allowing a minute early),
// half within 30 minutes after the start, else none.
func slotTerm(s stations.Station, when time.Time) float64 {
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
	w := slotMax
	if s.CountryCode == "USA" {
		w /= 2 // US stations are often off schedule
	}
	return w * best
}

type score struct {
	total, kw float64
	uid, gid  int
}

func (id *Identifier) score(i int, in Input, text string) score {
	s := id.tab.Stations[i]
	df := id.df[s.FreqKHz]
	var sc score
	uids := 0
	for _, k := range s.UID {
		if hit(text, strings.ToUpper(k)) {
			uids++
		}
	}
	if uids > 0 {
		sc.kw += math.Min(uidCap, uidScore+uidExtra*float64(uids-1))
		sc.uid = uids
	}
	g := 0.0
	for _, k := range s.GID {
		k = strings.ToUpper(k)
		if hit(text, k) {
			n := df[k]
			if n < 1 {
				n = 1
			}
			g += gidUnit / float64(n)
			sc.gid++
		}
	}
	sc.kw += math.Min(gidCap, g)
	sc.total = sc.kw
	if in.B1 == "" || in.B1 == "?" { // the slot only tells something when B1 does not
		sc.total += slotTerm(s, in.When)
	}
	if in.HaveOp {
		d := geo.DistanceKm(in.OpLat, in.OpLon, s.Lat, s.Lon)
		sc.total += distMax * math.Exp(-d/2000)
	}
	if id.History != nil {
		sc.total += math.Min(histMax, 0.1*math.Log1p(float64(id.History(i))))
	}
	return sc
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
	if len(cands) == 0 {
		return res
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
	for n := 0; n < len(all) && n < maxCandidates; n++ {
		if all[n].total > 0 || n == 0 {
			res.Candidates = append(res.Candidates, Candidate{all[n].i, round2(all[n].total)})
		}
	}
	best := all[0]
	margin := math.Inf(1)
	if len(all) > 1 {
		margin = best.total - all[1].total
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
