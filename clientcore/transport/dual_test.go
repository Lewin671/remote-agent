package transport

import "testing"

// Guards the dual-send merge: first trusted result wins without waiting for
// the other leg, late arrivals are ignored, and a double failure reports the
// relay leg.
func TestDual(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []struct {
			leg     Leg
			trusted bool
		}
		want DualStep // the step that settled
	}{
		{"direct first", steps(LegDirect, true, LegRelay, true), DualStep{Done: true, Winner: LegDirect, Trusted: true, Cancel: true}},
		{"relay first", steps(LegRelay, true, LegDirect, true), DualStep{Done: true, Winner: LegRelay, Trusted: true, Cancel: true}},
		{"direct fails, relay wins", steps(LegDirect, false, LegRelay, true), DualStep{Done: true, Winner: LegRelay, Trusted: true, Cancel: true}},
		{"relay fails, direct wins", steps(LegRelay, false, LegDirect, true), DualStep{Done: true, Winner: LegDirect, Trusted: true, Cancel: true}},
		{"both fail", steps(LegRelay, false, LegDirect, false), DualStep{Done: true, Winner: LegRelay}},
	} {
		var d Dual
		var settled DualStep
		for _, s := range tc.steps {
			if got := d.End(s.leg, s.trusted); got.Done {
				if settled.Done {
					t.Errorf("%s: settled twice", tc.name)
				}
				settled = got
			}
		}
		if settled != tc.want {
			t.Errorf("%s: %+v", tc.name, settled)
		}
	}
}

func steps(a Leg, at bool, b Leg, bt bool) []struct {
	leg     Leg
	trusted bool
} {
	return []struct {
		leg     Leg
		trusted bool
	}{{a, at}, {b, bt}}
}
