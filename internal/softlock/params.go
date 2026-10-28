package softlock

// Params are the settings of the lock. They were calibrated together (window length, noise statistics, thresholds):
// change none of them in isolation.
type Params struct {
	ZStar           float64    // lock threshold on the window z-score
	Window          int        // comparisons in the detection window
	Gap             float64    // how far the best hypothesis must lead the runner-up, in z
	ScoreSD         float64    // standard deviation of one comparison score on noise alone
	CosineNull      [2]float64 // (mean, sd) of the cosine term on noise alone
	CorrelationNull [2]float64 // (mean, sd) of the correlation term on noise alone
	ReleaseWindow   int
	ReleaseZ        float64
	ReleaseHold     int
	OutputDelay     int
	ReplayMargin    int
	Follow          bool // move the lock to a clearly better alignment
	SwitchWindow    int
	SlipZ           float64
	SlipMargin      float64
	SwitchZ         float64
	SwitchMargin    float64
	SwitchHold      int
	OwnReleaseHold  int
	MaxLetterRun    int // drop runs of this many identical letters (0 = keep them)
	GhostHold       int // idle look-alike pairs in a row before they are dropped (0 = off)
	// Polarity: which reading of the soft values is searched. +1 and -1 force one (the soft value is negated for -1), 0
	// searches both at once (28 alignment hypotheses; the first 14 are normal polarity).
	Polarity int
}

// DefaultParams are the reference values (the full variant: following and both junk filters on, normal polarity).
func DefaultParams() Params {
	return Params{
		ZStar: 5.0, Window: 45, Gap: 1.0, ScoreSD: 1.4168,
		CosineNull: [2]float64{0.0026, 0.3793}, CorrelationNull: [2]float64{0.715, 0.1284},
		ReleaseWindow: 30, ReleaseZ: 1.5, ReleaseHold: 5, OutputDelay: 15, ReplayMargin: 10,
		Follow: true, SwitchWindow: 12, SlipZ: 4.0, SlipMargin: 2.0, SwitchZ: 5.0, SwitchMargin: 3.0, SwitchHold: 3,
		OwnReleaseHold: 25, MaxLetterRun: 8, GhostHold: 3, Polarity: 1,
	}
}

// CoreParams is the "core" variant of the reference: no following, no filters.
func CoreParams() Params {
	p := DefaultParams()
	p.Follow, p.MaxLetterRun, p.GhostHold = false, 0, 0
	return p
}

// FollowParams is the "follow" variant: following, no filters.
func FollowParams() Params {
	p := CoreParams()
	p.Follow = true
	return p
}
