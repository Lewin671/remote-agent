package machinestatus

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestLinuxFixtures(t *testing.T) {
	b, e := os.ReadFile("testdata/linux.json")
	if e != nil {
		t.Fatal(e)
	}
	var f map[string]string
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	r, e := ParseLinux(f["stat"], f["mem"], f["load"], f["uptime"])
	if e != nil {
		t.Fatal(e)
	}
	s := r.Status
	if r.Total != 1000 || r.Idle != 850 || s.Cores != 2 || s.MemoryUsedBytes != 850000*1024 || *s.SwapUsedBytes != 150000*1024 || s.MemoryPressure != pb.ResourcePressure_RESOURCE_PRESSURE_WARNING || *s.MemoryPressurePosition != .85 || *s.LoadOne != 1.25 || s.UptimeSeconds != 1234 {
		t.Fatalf("bad fixture: %v %v", r, s)
	}
	p, e := ParseLinuxProcess(f["process"], 4096)
	if e != nil {
		t.Fatal(e)
	}
	if p.PID != 42 || p.Parent != 7 || p.Name != "worker ) name" || p.CPUSeconds != 3 || p.Start != 9876 || p.RSS != 409600 {
		t.Fatalf("bad process: %+v", p)
	}
	for _, free := range []uint64{200000, 100000, 99999} {
		mem := "MemTotal: 1000000 kB\nMemAvailable: " + stringNumber(free) + " kB"
		r, e := ParseLinux(f["stat"], mem, "", "")
		if e != nil {
			t.Fatal(e)
		}
		want := pb.ResourcePressure_RESOURCE_PRESSURE_NORMAL
		if free < 200000 {
			want = pb.ResourcePressure_RESOURCE_PRESSURE_WARNING
		}
		if free < 100000 {
			want = pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL
		}
		if r.Status.MemoryPressure != want {
			t.Fatal(r.Status)
		}
	}
	if _, e = ParseLinux("", "", "", ""); e == nil {
		t.Fatal("accepted corrupt counters")
	}
	if _, e = ParseLinuxProcess("42 (bad) S", 4096); e == nil {
		t.Fatal("accepted short stat")
	}
}
func TestProcessTreesAndRecycledPIDs(t *testing.T) {
	ps := []Process{{PID: 1, Name: "shell", Start: 1, RSS: 10}, {PID: 2, Parent: 1, Name: "build", Start: 2, CPUSeconds: 4, RSS: 20}, {PID: 3, Parent: 2, Name: "worker", Start: 3, CPUSeconds: 5, RSS: 30}, {PID: 4, Parent: 3, Name: "nested", Start: 4, RSS: 40}, {PID: 5, Parent: 6, Name: "cycle"}, {PID: 6, Parent: 5, Name: "cycle"}}
	old := map[int]Process{2: {Start: 2, CPUSeconds: 2}, 3: {Start: 0, CPUSeconds: 99}}
	ss, pp := Attribute(ps, old, map[int]string{1: "s", 4: "nested", 99: "gone"}, 2)
	byID := map[string]*pb.ResourceUsage{}
	for _, r := range ss {
		byID[r.Id] = r
	}
	if len(ss) != 2 || byID["s"].CpuPercent != 100 || byID["s"].MemoryBytes != 60 || byID["nested"].MemoryBytes != 40 {
		t.Fatalf("%v", ss)
	}
	for _, p := range pp {
		if p.Executable == "worker" && p.CpuPercent != 0 {
			t.Fatal("PID reused")
		}
	}
}
func TestRealRingAndLongPause(t *testing.T) {
	now := time.Unix(1000, 0)
	var n uint64
	c := NewReader(t.TempDir(), nil, func(context.Context) (Raw, error) {
		n += 100
		return Raw{Total: n, Idle: n / 2, Status: &pb.MachineStatus{MemoryUsedBytes: n}}, nil
	})
	c.now = func() time.Time { return now }
	c.primeInterval = 0
	for i := 0; i < 65; i++ {
		now = now.Add(Interval)
		c.sample(context.Background())
	}
	if len(c.latest.Samples) != 60 || c.latest.CpuPercent != 50 {
		t.Fatal(c.latest)
	}
	now = now.Add(3 * time.Minute)
	c.sample(context.Background())
	if len(c.latest.Samples) != 1 {
		t.Fatal("old points survived pause")
	}
}

// A test clock controls both timestamps and timer delivery, with no real sleeps.
type sampleClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []sampleTimer
}
type sampleTimer struct {
	due time.Time
	ch  chan time.Time
}

func (c *sampleClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *sampleClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
	} else {
		c.timers = append(c.timers, sampleTimer{c.now.Add(d), ch})
	}
	return ch
}
func (c *sampleClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	remaining := c.timers[:0]
	for _, t := range c.timers {
		if !t.due.After(c.now) {
			t.ch <- c.now
		} else {
			remaining = append(remaining, t)
		}
	}
	c.timers = remaining
}
func TestLeaseStopsAndVisibilityReusesHistory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := &sampleClock{now: time.Unix(1000, 0)}
		reads := 0
		c := NewReader(t.TempDir(), nil, func(context.Context) (Raw, error) {
			reads++
			return Raw{Total: uint64(reads * 100), Status: &pb.MachineStatus{}}, nil
		})
		c.now = clock.Now
		c.after = clock.After
		c.primeInterval = 0
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { c.Run(ctx); close(done) }()
		synctest.Wait()
		clock.Advance(time.Minute)
		synctest.Wait()
		if reads != 0 {
			t.Fatal("idle sampled")
		}
		first, e := c.Get(ctx)
		if e != nil || len(first.Samples) != 1 {
			t.Fatal(first, e)
		}
		synctest.Wait()
		for i := 0; i < 15; i++ {
			clock.Advance(2 * time.Second)
			synctest.Wait()
		}
		before := reads
		clock.Advance(10 * time.Second)
		synctest.Wait()
		if reads != before {
			t.Fatal("sampling after lease")
		}
		resumed, e := c.Get(ctx)
		if e != nil || len(resumed.Samples) <= 1 || resumed.Samples[0].TimeMs != first.Samples[0].TimeMs {
			t.Fatal("history not reused", resumed, e)
		}
		cancel()
		<-done
	})
}
