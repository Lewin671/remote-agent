package transport

import "testing"

var read = Hint{Use: UseRead}

func bulk(file string) Hint { return Hint{Use: UseBulk, File: file} }

// mib is x MiB in whole bytes, rounded up.
func mib(x float64) int64 { return int64(x*(1<<20) + 0.999) }

type clock struct{ now int64 }

func (c *clock) advance(ms int64) { c.now += ms }

// observeReads records direct samples, then relay samples, step ms apart.
func observeReads(r *Routing, c *clock, direct, relay int, directMs, relayMs float64, step int64) {
	for range direct {
		r.Observe(read, Direct, 0, directMs, c.now)
		c.advance(step)
	}
	for range relay {
		r.Observe(read, Relay, 0, relayMs, c.now)
		c.advance(step)
	}
}

// Guards rule 2 (就绪即优先) and the one-in-eight relay control sample.
func TestDirectPreferredOnReadyWithRelayControlSample(t *testing.T) {
	var r Routing
	now := int64(10_000)
	if r.ReadRoute(now) != Direct || r.FileRoute(now) != Direct {
		t.Fatal("a ready path is preferred without warm-up")
	}
	for _, h := range []Hint{read, bulk("f")} {
		var n [2]int
		for range 64 {
			n[r.Choose(h, true, now)]++
		}
		if n != [2]int{8, 56} {
			t.Errorf("use %d: relay/direct = %v, want [8 56]", h.Use, n)
		}
		if r.Choose(h, false, now) != Relay {
			t.Errorf("use %d: no ready path must take the relay", h.Use)
		}
	}
}

// Guards rule 4's sample counts and the strict 1.5× + 20 ms margin.
func TestReadFallbackRule(t *testing.T) {
	for _, tc := range []struct {
		name              string
		direct, relay     int
		directMs, relayMs float64
		want              Route
	}{
		{"insufficient direct samples (7/4)", 7, 4, 200, 40, Direct},
		{"insufficient relay samples (8/3)", 8, 3, 200, 40, Direct},
		{"within the margin (80 vs 40)", 8, 4, 80, 40, Direct},
		{"strictly slower (81 vs 40)", 8, 4, 81, 40, Relay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r Routing
			c := &clock{10_000}
			observeReads(&r, c, tc.direct, tc.relay, tc.directMs, tc.relayMs, 500)
			if got := r.ReadRoute(c.now); got != tc.want {
				t.Fatalf("read route %d, want %d", got, tc.want)
			}
			if r.ReadSlow(c.now) != (tc.want == Relay) {
				t.Fatal("slow flag must follow the fallback")
			}
			if r.FileRoute(c.now) != Direct {
				t.Fatal("a read fallback leaves files alone")
			}
		})
	}
}

// Guards the 30 s comparison window.
func TestReadFallbackIgnoresOldSamples(t *testing.T) {
	var r Routing
	c := &clock{10_000}
	observeReads(&r, c, 4, 4, 500, 20, 500)
	c.advance(ReadWindowMs + 1)
	observeReads(&r, c, 4, 4, 500, 20, 500)
	if r.ReadRoute(c.now) != Direct {
		t.Fatal("samples older than 30 s must not count")
	}
	observeReads(&r, c, 4, 0, 500, 20, 500)
	if r.ReadRoute(c.now) != Relay {
		t.Fatal("eight fresh slow direct samples fall back")
	}
}

// Guards the per-file 70% throughput rule.
func TestBulkSegmentFallbackRule(t *testing.T) {
	for _, tc := range []struct {
		direct, relay float64 // MiB/s
		want          Route
	}{{2.8, 4.0, Direct}, {2.7, 4.0, Relay}} {
		var r Routing
		h := bulk("archive.tar")
		r.Observe(h, Relay, mib(tc.relay), 1000, 0)
		r.Observe(h, Direct, mib(tc.direct), 1000, 0)
		if r.FileRoute(0) != tc.want || r.FileSlow(0) != (tc.want == Relay) || r.ReadRoute(0) != Direct {
			t.Errorf("direct %.1f vs relay %.1f MiB/s: file route %d, want %d", tc.direct, tc.relay, r.FileRoute(0), tc.want)
		}
	}
}

// Guards the 60 s → 10 min backoff, its independence per use, and the resets.
func TestFallbackBackoffAndReset(t *testing.T) {
	var r Routing
	c := &clock{10_000}
	slowRead := func() {
		observeReads(&r, c, 8, 3, 250, 30, 100)
		r.Observe(read, Relay, 0, 30, c.now)
	}
	slowBulk := func() {
		f := bulk("f-" + string(rune('a'+c.now%26)))
		r.Observe(f, Relay, 8<<20, 1000, c.now)
		r.Observe(f, Direct, 2<<20, 1000, c.now)
	}
	for _, wait := range []int64{60_000, 120_000, 240_000, 480_000, 600_000, 600_000} {
		slowRead()
		c.advance(wait - 1)
		if r.ReadRoute(c.now) != Relay {
			t.Fatalf("still waiting 1 ms before %d", wait)
		}
		c.advance(1)
		if r.ReadRoute(c.now) != Direct {
			t.Fatalf("direct again after %d", wait)
		}
	}
	if r.Choose(read, true, c.now) != Direct {
		t.Fatal("re-preferring needs no new proof")
	}
	r.Reset(UseRead)
	slowRead()
	c.advance(59_999)
	if r.ReadRoute(c.now) != Relay {
		t.Fatal("reset starts the wait from 60 s again")
	}
	c.advance(1)
	if r.ReadRoute(c.now) != Direct {
		t.Fatal("reset wait is 60 s")
	}

	slowBulk()
	c.advance(60_000)
	slowBulk()
	if r.FileRoute(c.now) != Relay {
		t.Fatal("second bulk fallback")
	}
	r.Reset(UseBoth)
	if r.FileRoute(c.now) != Direct {
		t.Fatal("a full reset clears the bulk wait")
	}
	slowBulk()
	c.advance(60_000)
	if r.FileRoute(c.now) != Direct {
		t.Fatal("after a full reset the first bulk wait is 60 s")
	}
}

// Guards the display rule: three samples in 60 s, kept when samples run out,
// cleared by a fallback or a hard failure, never reported as zero.
func TestMeasuredDisplay(t *testing.T) {
	var r Routing
	for range 2 {
		r.Observe(read, Direct, 0, 18, 0)
		r.Observe(read, Relay, 0, 150, 0)
	}
	if m := r.Measured(0); m.HasReadMs != [2]bool{} {
		t.Fatal("two samples are not enough to show")
	}
	r.Observe(read, Direct, 0, 18, 0)
	r.Observe(read, Relay, 0, 150, 0)
	m := r.Measured(0)
	if m.ReadMs != [2]float64{150, 18} || m.HasReadMs != [2]bool{true, true} {
		t.Fatalf("shown %v", m.ReadMs)
	}
	if m := r.Measured(KeepMs + 1); m.ReadMs[Direct] != 18 || !m.HasReadMs[Direct] {
		t.Fatal("the last number stays while no new samples arrive")
	}
	r.Fail(UseRead, 0)
	if !r.ReadSlow(0) || r.Measured(0).HasReadMs != [2]bool{} {
		t.Fatal("a fallback clears the numbers")
	}
	r.Observe(bulk("f"), Direct, 4<<20, 500, 0)
	r.Observe(bulk("f"), Relay, 4<<20, 1000, 0)
	if m := r.Measured(0); m.FileMiBps != [2]float64{4, 8} {
		t.Fatalf("file MiB/s %v", m.FileMiBps)
	}
	r.HardFail(UseBulk)
	if m := r.Measured(0); m.HasFileMiBps != [2]bool{} || r.FileSlow(0) {
		t.Fatal("a hard failure clears the numbers without the slow note")
	}
	r.Observe(bulk("f"), Relay, 4<<20, 0, 0)
	if r.Measured(0).HasFileMiBps[Relay] {
		t.Fatal("a zero duration is not a sample")
	}
}
