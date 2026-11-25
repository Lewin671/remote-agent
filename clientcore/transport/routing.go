// Package transport is the client's direct-path engine and route choice
// (docs/transport.md 「升级与热备」, §4, §6, §7), sans-IO: the caller supplies
// the time in milliseconds with every input, randomness arrives as unit floats,
// and every action leaves as an Effect. Nothing here reads a clock, draws
// randomness, starts a goroutine or logs. The session layer (offer/answer,
// signatures, peer frames) is reached only through Round* effects and events.
package transport

import (
	"math"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
)

// Route is the path a request takes.
type Route uint8

const (
	Relay Route = iota
	Direct
)

// Use is a direct path's purpose, numbered as the session's peer connections:
// 1 loads and controls, 2 carries files. UseBoth addresses both at once.
type Use uint8

const (
	UseBoth Use = 0
	UseRead Use = 1
	UseBulk Use = 2
)

// Upload is the kind of an upload that needs the capability snapshot.
type Upload uint8

const (
	UploadNone Upload = iota
	UploadImage
	UploadFile
)

// Hint describes a request for route choice: a read, or a bulk transfer of one
// file (an upload also names its kind and chunk size).
type Hint struct {
	Use    Use
	File   string
	Upload Upload
	Bytes  int64
}

// The direct-first rule's numbers (docs/transport.md 「升级与热备」 4 and 6).
const (
	ReadWindowMs     = 30_000 // reads compared over the last 30 s
	ReadDirectMin    = 8      // direct samples needed to fall back
	ReadRelayMin     = 4      // relay samples needed to fall back
	ReadMedianRatio  = 1.5    // fall back when direct > 1.5 × relay + 20 ms
	ReadSlackMs      = 20
	BulkRatio        = 0.7     // fall back when direct < 70% of relay throughput
	BulkDirectMin    = 1 << 20 // per file segment
	BulkRelayMin     = 512 << 10
	FallbackFirstMs  = 60_000  // wait before direct is preferred again
	FallbackMaxMs    = 600_000 // doubling up to 10 min
	KeepSamples      = 512
	KeepMs           = 60_000 // display window
	DisplayMin       = 3      // samples before a number is shown
	ControlEvery     = 8      // one relay control sample in eight
	fileRateBytes    = 4 << 20
	fileSegmentsKept = 32
)

type sample struct {
	at int64
	ms float64
}

type window struct {
	bytes int64
	ms    float64
}

func (w window) rate() float64 {
	if w.ms <= 0 {
		return 0
	}
	return float64(w.bytes) / w.ms
}

type segment [2]window // by Route

type lane struct {
	stableAfter int64
	fallbacks   int
	slow        bool
	counter     uint64
}

func (l *lane) fail(now int64) {
	l.fallbacks++
	wait := int64(FallbackFirstMs) << min(l.fallbacks-1, 10)
	l.stableAfter = now + min(wait, FallbackMaxMs)
	l.slow = true
}

// choose applies the backoff and the one-in-eight relay control sample.
func (l *lane) choose(now int64) Route {
	if now < l.stableAfter {
		return Relay
	}
	l.counter++
	if l.counter%ControlEvery == 0 {
		return Relay
	}
	return Direct
}

// Routing is one computer's direct-first route rule. Ready DirectPaths are
// preferred at once; observed completions demote a path that is clearly worse,
// and an exponential wait keeps it from flapping.
type Routing struct {
	read  lane
	bulk  lane
	reads [2][]sample
	// shown is the display latency per route: the last 60 s median with at
	// least three samples; kept while samples run out, cleared on fallback.
	shown    [2]float64
	hasShown [2]bool

	files    map[string]*segment
	order    []string
	tally    [2]window
	fileRate [2]float64
	hasRate  [2]bool
}

// Measured is what has been measured. A route without data is absent, never zero.
type Measured struct {
	ReadMs       [2]float64
	HasReadMs    [2]bool
	FileMiBps    [2]float64
	HasFileMiBps [2]bool
}

func (r *Routing) lane(u Use) *lane {
	if u == UseBulk {
		return &r.bulk
	}
	return &r.read
}

func (r *Routing) live(p Route, span, now int64) []sample {
	var out []sample
	for _, s := range r.reads[p] {
		if now-s.at <= span {
			out = append(out, s)
		}
	}
	return out
}

func median(s []sample) float64 {
	v := make([]float64, len(s))
	for i, x := range s {
		v[i] = x.ms
	}
	sortutil.Float64s(v)
	return v[len(v)/2]
}

// Choose picks the route for one request. Without a ready DirectPath it is
// always the relay.
func (r *Routing) Choose(h Hint, ready bool, now int64) Route {
	if !ready {
		return Relay
	}
	return r.lane(h.Use).choose(now)
}

// ReadRoute is the route reads take now when the read path is ready.
func (r *Routing) ReadRoute(now int64) Route { return r.preferred(&r.read, now) }

// FileRoute is the route files take now when the bulk path is ready.
func (r *Routing) FileRoute(now int64) Route { return r.preferred(&r.bulk, now) }

func (r *Routing) preferred(l *lane, now int64) Route {
	if now >= l.stableAfter {
		return Direct
	}
	return Relay
}

// ReadSlow reports a read fallback under rule 4 that is still waiting.
func (r *Routing) ReadSlow(now int64) bool { return r.read.slow && now < r.read.stableAfter }

// FileSlow reports a file fallback under rule 4 that is still waiting.
func (r *Routing) FileSlow(now int64) bool { return r.bulk.slow && now < r.bulk.stableAfter }

// Measured returns the display numbers, refreshing each route's latency from
// the samples of the last minute when there are at least three.
func (r *Routing) Measured(now int64) Measured {
	var m Measured
	for p := Relay; p <= Direct; p++ {
		if r.hasRate[p] {
			m.FileMiBps[p], m.HasFileMiBps[p] = r.fileRate[p]*1000/(1<<20), true
		}
		r.refreshShown(p, now)
		m.ReadMs[p], m.HasReadMs[p] = r.shown[p], r.hasShown[p]
	}
	return m
}

func (r *Routing) refreshShown(p Route, now int64) {
	if live := r.live(p, KeepMs, now); len(live) >= DisplayMin {
		r.shown[p], r.hasShown[p] = median(live), true
	}
}

// Observe records one verified completion on route p. Reads compare medians;
// files compare per-file segments of throughput.
func (r *Routing) Observe(h Hint, p Route, bytes int64, ms float64, now int64) {
	if !(ms > 0) || math.IsInf(ms, 1) {
		return
	}
	if h.Use != UseBulk {
		r.reads[p] = append(r.reads[p], sample{now, ms})
		for i := range r.reads {
			q := r.reads[i]
			for len(q) > KeepSamples || len(q) > 0 && now-q[0].at > KeepMs {
				q = q[1:]
			}
			r.reads[i] = q
		}
		r.refreshShown(p, now)
		r.decideReads(now)
		return
	}
	if bytes <= 0 {
		return
	}
	t := &r.tally[p]
	t.bytes += bytes
	t.ms += ms
	if t.bytes >= fileRateBytes {
		r.fileRate[p], r.hasRate[p] = t.rate(), true
		*t = window{}
	}
	seg := r.file(h.File)
	seg[p].bytes += bytes
	seg[p].ms += ms
	if now >= r.bulk.stableAfter && seg[Direct].bytes >= BulkDirectMin && seg[Relay].bytes >= BulkRelayMin {
		if seg[Direct].rate() < seg[Relay].rate()*BulkRatio {
			r.Fail(UseBulk, now)
		} else {
			*seg = segment{}
			r.bulk.fallbacks = 0
			r.bulk.slow = false
		}
	}
}

func (r *Routing) decideReads(now int64) {
	if now < r.read.stableAfter {
		return
	}
	relay, direct := r.live(Relay, ReadWindowMs, now), r.live(Direct, ReadWindowMs, now)
	if len(direct) >= ReadDirectMin && len(relay) >= ReadRelayMin {
		if median(direct) > ReadMedianRatio*median(relay)+ReadSlackMs {
			r.Fail(UseRead, now)
		} else {
			r.read.fallbacks = 0
			r.read.slow = false
		}
	}
}

// Fail demotes direct for the use (both for UseBoth) and starts the wait:
// 60 s, doubling up to 10 min. The samples go with it.
func (r *Routing) Fail(u Use, now int64) {
	if u != UseBulk {
		r.read.fail(now)
		r.clearReads()
	}
	if u != UseRead {
		r.bulk.fail(now)
		r.clearFiles()
	}
}

// HardFail clears the samples after a path closed, without the slow-fallback
// note or any wait.
func (r *Routing) HardFail(u Use) {
	if u != UseBulk {
		r.read.slow = false
		r.clearReads()
	}
	if u != UseRead {
		r.bulk.slow = false
		r.clearFiles()
	}
}

// Reset forgets the backoff and samples: a new network generation or a
// rebuilt direct path starts over.
func (r *Routing) Reset(u Use) {
	if u != UseBulk {
		r.read = lane{}
		r.clearReads()
	}
	if u != UseRead {
		r.bulk = lane{}
		r.clearFiles()
	}
}

func (r *Routing) clearReads() {
	r.reads = [2][]sample{}
	r.shown, r.hasShown = [2]float64{}, [2]bool{}
}

func (r *Routing) clearFiles() {
	r.files, r.order = nil, nil
	r.tally = [2]window{}
	r.fileRate, r.hasRate = [2]float64{}, [2]bool{}
}

func (r *Routing) file(key string) *segment {
	if s := r.files[key]; s != nil {
		return s
	}
	if r.files == nil {
		r.files = map[string]*segment{}
	}
	if len(r.order) >= fileSegmentsKept {
		delete(r.files, r.order[0])
		r.order = r.order[1:]
	}
	s := &segment{}
	r.files[key] = s
	r.order = append(r.order, key)
	return s
}
