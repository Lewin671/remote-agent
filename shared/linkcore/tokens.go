package linkcore

// Tokens is a frame and byte rate bucket. The byte bucket holds at most one
// second of its rate; a fresh bucket starts full.
type Tokens struct {
	At            int64 // ms of the last refill
	Frames, Bytes float64
	started       bool
}

// Take refills by the elapsed time and charges one frame of n bytes. The
// returned bucket carries the refill even when the charge is refused.
func (t Tokens) Take(now int64, n int, fps, bps, burst float64) (Tokens, bool) {
	if !t.started {
		t = Tokens{At: now, Frames: burst, Bytes: bps, started: true}
	}
	dt := float64(max(0, now-t.At)) / 1000
	t.At = now
	t.Frames = min(burst, t.Frames+dt*fps)
	t.Bytes = min(bps, t.Bytes+dt*bps)
	if t.Frames < 1 || t.Bytes < float64(n) {
		return t, false
	}
	t.Frames--
	t.Bytes -= float64(n)
	return t, true
}
