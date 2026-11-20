package transport

// Leg is one leg of a dual-sent interactive command (docs/transport.md
// 「升级与热备」 3): the same sealed bytes go both ways.
type Leg uint8

const (
	LegDirect Leg = iota
	LegRelay
)

// Dual merges the two legs: the first trusted result wins at once and the
// other leg is cancelled; a late arrival changes nothing. When neither leg is
// trusted, the relay leg's failure is the one reported.
type Dual struct {
	settled bool
	failed  [2]bool
}

// DualStep is what one leg's end decided.
type DualStep struct {
	Done   bool // the command has its result
	Winner Leg  // the trusted leg, when Trusted
	// Trusted: a leg returned a trusted result; otherwise both failed and the
	// relay leg's failure stands.
	Trusted bool
	Cancel  bool // cancel the other leg now
}

// End records that leg finished, with a trusted result or not.
func (d *Dual) End(leg Leg, trusted bool) DualStep {
	if d.settled {
		return DualStep{}
	}
	if trusted {
		d.settled = true
		return DualStep{Done: true, Winner: leg, Trusted: true, Cancel: true}
	}
	d.failed[leg] = true
	if d.failed[LegDirect] && d.failed[LegRelay] {
		d.settled = true
		return DualStep{Done: true, Winner: LegRelay}
	}
	return DualStep{}
}
