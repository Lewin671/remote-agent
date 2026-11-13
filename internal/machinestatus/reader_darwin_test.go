package machinestatus

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

func TestMacCounterFixture(t *testing.T) {
	vm := make([]uint32, 38)
	vm[35] = 400
	vm[3] = 100
	vm[32] = 50
	vm[22] = 25
	if n := MacMemoryUsed(vm, 16384, 1<<30); n != 525*16384 {
		t.Fatal(n)
	}
	for i, p := range []pb.ResourcePressure{pb.ResourcePressure_RESOURCE_PRESSURE_UNSPECIFIED, pb.ResourcePressure_RESOURCE_PRESSURE_NORMAL, pb.ResourcePressure_RESOURCE_PRESSURE_WARNING} {
		if MacPressure(uint32(i)) != p {
			t.Fatal(i)
		}
	}
	if MacPressure(4) != pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL || MacPressurePosition(MacPressure(1), 1) != .7 || MacPressurePosition(MacPressure(4), 0) != .9 {
		t.Fatal("pressure mapping")
	}
}
func TestNativeMacCounters(t *testing.T) {
	r, e := readSystem(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if r.Total == 0 || r.Status.Cores == 0 || r.Status.LoadOne == nil || r.Status.MemoryTotalBytes == 0 || r.Status.MemoryUsedBytes > r.Status.MemoryTotalBytes || len(r.Processes) == 0 {
		t.Fatal("invalid native counters")
	}
}
func cpuTime() time.Duration {
	var self, children syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
	_ = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children)
	return time.Duration(self.Utime.Nano() + self.Stime.Nano() + children.Utime.Nano() + children.Stime.Nano())
}
func BenchmarkNativeMacSample(b *testing.B) {
	before := cpuTime()
	for b.Loop() {
		if _, e := readSystem(context.Background()); e != nil {
			b.Fatal(e)
		}
	}
	b.ReportMetric(float64(cpuTime()-before)/float64(b.N), "cpu-ns/op")
}

func BenchmarkCompleteSample(b *testing.B) {
	socket := os.Getenv("RA_STATUS_BENCH_TMUX_SOCKET")
	if socket == "" {
		socket = fmt.Sprintf("ra-status-benchmark-%d", os.Getpid())
	}
	tm := &tmux.Client{Bin: "tmux", Socket: socket}
	roots := func() map[int]string {
		out := map[int]string{}
		infos, _ := tm.ListSessions()
		for _, i := range infos {
			out[i.PanePID] = i.SessionID
		}
		return out
	}
	c := New(b.TempDir(), roots)
	// Measure the production cadence, not invalid tight-loop counter deltas.
	// Exclude the initial baseline and timer waits from wall latency; CPU
	// accounting still includes this process and its pmset/tmux children.
	c.sample(context.Background())
	before := cpuTime()
	for b.Loop() {
		b.StopTimer()
		time.Sleep(Interval)
		b.StartTimer()
		c.sample(context.Background())
		if c.failure != nil {
			b.Fatal(c.failure)
		}
	}
	b.ReportMetric(float64(cpuTime()-before)/float64(b.N), "cpu-ns/op")
}

func TestMacMachTimebaseFixture(t *testing.T) {
	if got := MacCPUSeconds(24_000_000, 125, 3); got != 1 {
		t.Fatal(got)
	}
	if got := MacCPUSeconds(1_000_000_000, 1, 1); got != 1 {
		t.Fatal(got)
	}
}
