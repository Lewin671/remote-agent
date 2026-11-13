package machinestatus

import (
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestConcurrentGetAndSamplingFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := &sampleClock{now: time.Unix(1000, 0)}
		reads := 0
		fail := false
		c := NewReader(t.TempDir(), nil, func(context.Context) (Raw, error) {
			reads++
			if fail {
				return Raw{}, errors.New("fixture")
			}
			return Raw{Total: uint64(reads * 100), Status: &pb.MachineStatus{}}, nil
		})
		c.now = clock.Now
		c.after = clock.After
		c.primeInterval = 0
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { c.Run(ctx); close(done) }()
		var wg sync.WaitGroup
		wg.Add(8)
		for i := 0; i < 8; i++ {
			go func() {
				defer wg.Done()
				if _, e := c.Get(ctx); e != nil {
					t.Error(e)
				}
			}()
		}
		wg.Wait()
		synctest.Wait()
		if reads != 2 {
			t.Fatal("concurrent cold samplers", reads)
		}
		fail = true
		clock.Advance(5 * time.Second)
		synctest.Wait()
		waiting := make(chan error, 1)
		go func() { _, e := c.Get(ctx); waiting <- e }()
		synctest.Wait()
		clock.Advance(2 * time.Second)
		synctest.Wait()
		if e := <-waiting; e == nil {
			t.Fatal("failure hidden")
		}
		cancel()
		<-done
	})
}
func TestSampleSlowRootsAndWalkDoNotHoldCacheMutex(t *testing.T) {
	for _, kind := range []string{"roots", "walk"} {
		t.Run(kind, func(t *testing.T) {
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
				c.sample(context.Background())
				clock.Advance(time.Second)
				entered, release := make(chan struct{}), make(chan struct{})
				if kind == "roots" {
					c.roots = func() map[int]string { close(entered); <-release; return nil }
				} else {
					c.agentAt = time.Time{}
					c.allocated = func(context.Context, string) (uint64, error) { close(entered); <-release; return 0, nil }
				}
				done := make(chan struct{})
				go func() { c.sample(context.Background()); close(done) }()
				<-entered
				if _, e := c.Get(context.Background()); e != nil {
					t.Fatal(e)
				}
				close(release)
				<-done
			})
		})
	}
}
func TestCounterRegressionDropsPointAndRebaselines(t *testing.T) {
	clock := &sampleClock{now: time.Unix(1000, 0)}
	values := []Raw{{Total: 100, Idle: 50}, {Total: 200, Idle: 100}, {Total: 90, Idle: 40}, {Total: 190, Idle: 90}, {Total: 290, Idle: 80}, {Total: 390, Idle: 130}}
	c := NewReader(t.TempDir(), nil, func(context.Context) (Raw, error) {
		r := values[0]
		values = values[1:]
		r.Status = &pb.MachineStatus{}
		return r, nil
	})
	c.now = clock.Now
	c.after = clock.After
	c.primeInterval = 0
	c.sample(context.Background())
	for _, want := range []int{1, 2, 2, 3} {
		clock.Advance(2 * time.Second)
		c.sample(context.Background())
		if len(c.samples) != want {
			t.Fatal("counter reset produced a fake point", len(c.samples), want)
		}
	}
	if c.latest.CpuPercent != 50 {
		t.Fatal(c.latest.CpuPercent)
	}
}
func TestBatteryUsesThirtySecondCache(t *testing.T) {
	now := time.Unix(1000, 0)
	calls := 0
	c := batteryCache{now: func() time.Time { return now }}
	fetch := func(context.Context) *pb.MachineBattery { calls++; return &pb.MachineBattery{Percent: 90} }
	for i := 0; i < 15; i++ {
		_ = c.get(context.Background(), fetch)
		now = now.Add(2 * time.Second)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	_ = c.get(context.Background(), fetch)
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestColdSamplerWaitsForARealCounterDelta(t *testing.T) {
	clock := &sampleClock{now: time.Unix(1000, 0)}
	values := []uint64{100, 100, 100, 200}
	reads := 0
	c := NewReader(t.TempDir(), nil, func(context.Context) (Raw, error) {
		n := values[reads]
		reads++
		return Raw{Total: n, Idle: n / 2, Status: &pb.MachineStatus{}}, nil
	})
	c.now = clock.Now
	c.after = clock.After
	c.primeInterval = 0
	c.sample(context.Background())
	if reads != 4 || c.latest == nil || len(c.samples) != 1 || c.latest.CpuPercent != 50 {
		t.Fatal("fake cold point", reads, c.latest)
	}
}
