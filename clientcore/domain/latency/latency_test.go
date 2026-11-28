package latency

import "testing"

func TestMedianInt64(t *testing.T) {
	cases := []struct {
		name string
		in   []int64
		want int64
		ok   bool
	}{
		{"empty", nil, 0, false},
		{"one", []int64{42}, 42, true},
		{"two_upper_middle", []int64{10, 30}, 30, true},
		{"three_unsorted", []int64{300, 12, 85}, 85, true},
		{"five_unsorted", []int64{90, 5, 12, 8, 150}, 12, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MedianInt64(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("MedianInt64(%v) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestNetRoundTripClampZero(t *testing.T) {
	if got := NetRoundTripMs(100, 25_000); got != 75 {
		t.Fatalf("NetRoundTripMs(100, 25000) = %d, want 75", got)
	}
	if got := NetRoundTripMs(10, 25_000); got != 0 {
		t.Fatalf("NetRoundTripMs(10, 25000) = %d, want 0", got)
	}
	if got := NetRoundTripMs(10.6, 500); got != 10 {
		t.Fatalf("NetRoundTripMs(10.6, 500) = %d, want 10", got)
	}
	if got := MicrosToMs(-10); got != 0 {
		t.Fatalf("MicrosToMs(-10) = %d, want 0", got)
	}
	if got := MicrosToMs(21_400); got != 21 {
		t.Fatalf("MicrosToMs(21400) = %d, want 21", got)
	}
	if got := MicrosToMs(21_500); got != 22 {
		t.Fatalf("MicrosToMs(21500) = %d, want 22", got)
	}
	// A direct keepalive median (13 ms) larger than this round trip (8 ms total).
	for _, c := range []struct{ net, total, computer, want int64 }{
		{13, 8, 0, 8},
		{13, 8, 3, 5},
		{6, 38, 21, 6},
		{5, 2, 9, 0},
	} {
		if got := ClampNetworkMs(c.net, c.total, c.computer); got != c.want {
			t.Fatalf("ClampNetworkMs(%d, %d, %d) = %d, want %d", c.net, c.total, c.computer, got, c.want)
		}
	}
}

func TestFormatMs(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{-5, "0"},
		{0, "0"},
		{8, "8"},
		{999, "999"},
		{1000, "1,000"},
		{1250, "1,250"},
		{12480, "12,480"},
		{1234567, "1,234,567"},
	}
	for _, tc := range cases {
		if got := FormatMs(tc.in); got != tc.want {
			t.Errorf("FormatMs(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEvaluateThresholdsAndCause(t *testing.T) {
	cases := []struct {
		name string
		in   EvalInput
		want EvalResult
	}{
		{
			name: "direct_below_all_thresholds",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 499, NetworkIsDirect: true,
				HasComputer: true, ComputerMs: 499, QueueMs: 400, ExecuteMs: 50,
				HasTotal: true, TotalMs: 1499,
			},
			want: EvalResult{},
		},
		{
			name: "direct_network_at_threshold",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 500, NetworkIsDirect: true,
				HasComputer: true, ComputerMs: 20, ExecuteMs: 18,
				HasTotal: true, TotalMs: 540,
			},
			want: EvalResult{NetworkSlow: true, TotalSlow: true},
		},
		{
			name: "relay_network_below_threshold",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 799, NetworkIsDirect: false,
				HasComputer: true, ComputerMs: 50, ExecuteMs: 40,
				HasTotal: true, TotalMs: 880,
			},
			want: EvalResult{},
		},
		{
			name: "relay_network_at_threshold",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 800, NetworkIsDirect: false,
				HasComputer: true, ComputerMs: 50, ExecuteMs: 40,
				HasTotal: true, TotalMs: 880,
			},
			want: EvalResult{NetworkSlow: true, TotalSlow: true},
		},
		{
			name: "computer_slow_mostly_queued",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 8, NetworkIsDirect: true,
				HasComputer: true, ComputerMs: 1190, QueueMs: 1150, ExecuteMs: 25,
				HasTotal: true, TotalMs: 1250,
			},
			want: EvalResult{
				ComputerSlow: true, TotalSlow: true,
				ComputerCause: CauseQueue, ComputerCauseMs: 1150,
			},
		},
		{
			name: "computer_slow_mostly_running_at_boundary",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 296, NetworkIsDirect: false,
				HasComputer: true, ComputerMs: 500, QueueMs: 100, ExecuteMs: 380,
				HasTotal: true, TotalMs: 820,
			},
			want: EvalResult{
				ComputerSlow: true, TotalSlow: true,
				ComputerCause: CauseExecute, ComputerCauseMs: 380,
			},
		},
		{
			name: "wait_direct_below_threshold",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 300, NetworkIsDirect: false,
				HasComputer: true, ComputerMs: 60, ExecuteMs: 50,
				HasWaitDirect: true, WaitDirectMs: 499,
				HasTotal: true, TotalMs: 870,
			},
			want: EvalResult{},
		},
		{
			name: "wait_direct_at_threshold",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 310, NetworkIsDirect: false,
				HasComputer: true, ComputerMs: 64, ExecuteMs: 50,
				HasWaitDirect: true, WaitDirectMs: 500,
				HasTotal: true, TotalMs: 890,
			},
			want: EvalResult{WaitDirectSlow: true, TotalSlow: true},
		},
		{
			name: "local_at_threshold",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 11, NetworkIsDirect: true,
				HasComputer: true, ComputerMs: 3, ExecuteMs: 2,
				HasLocal: true, LocalMs: 500,
				HasTotal: true, TotalMs: 514,
			},
			want: EvalResult{LocalSlow: true, TotalSlow: true},
		},
		{
			name: "total_only_slow_fallback",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 700, NetworkIsDirect: false,
				HasComputer: true, ComputerMs: 400, QueueMs: 300, ExecuteMs: 50,
				HasTotal: true, TotalMs: 1500,
			},
			want: EvalResult{TotalSlow: true},
		},
		{
			name: "all_three_segments_slow",
			in: EvalInput{
				HasNetwork: true, NetworkMs: 1204, NetworkIsDirect: false,
				HasComputer: true, ComputerMs: 10655, QueueMs: 100, ExecuteMs: 10480,
				HasWaitDirect: true, WaitDirectMs: 3046,
				HasTotal: true, TotalMs: 14905,
			},
			want: EvalResult{
				NetworkSlow: true, ComputerSlow: true, WaitDirectSlow: true, TotalSlow: true,
				ComputerCause: CauseExecute, ComputerCauseMs: 10480,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.in)
			if got != tc.want {
				t.Fatalf("Evaluate(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// Guards the promise on screen: the shown parts add up to the shown total.
func TestSplitWorkPartsAddUp(t *testing.T) {
	for _, c := range []struct {
		name                     string
		total                    int64
		wire                     float64
		processUs                int64
		wantNet, wantComp, wantR int64
	}{
		{"direct", 76, 14, 3_000, 11, 3, 62},
		{"rerouted_rest_is_wait", 3420, 374, 64_000, 310, 64, 3046},
		{"unknown_wire_all_network", 40, 0, 10_000, 30, 10, 0},
		{"wire_longer_than_total", 20, 35, 5_000, 15, 5, 0},
		{"daemon_longer_than_total", 8, 9, 12_000, 0, 12, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, comp, r := SplitWork(c.total, c.wire, c.processUs)
			if n != c.wantNet || comp != c.wantComp || r != c.wantR {
				t.Fatalf("SplitWork(%d, %v, %d) = %d, %d, %d; want %d, %d, %d", c.total, c.wire, c.processUs, n, comp, r, c.wantNet, c.wantComp, c.wantR)
			}
		})
	}
}

func TestPickSampleAndRange(t *testing.T) {
	totals := func(ms ...int64) []WorkSample {
		out := make([]WorkSample, len(ms))
		for i, v := range ms {
			out[i] = WorkSample{TotalMs: v}
		}
		return out
	}
	if i, _ := PickSample(totals(76, 14, 18, 15, 20)); i != 2 {
		t.Fatalf("median pick = %d, want 2 (18 ms)", i)
	}
	if i, _ := PickSample(totals(30, 10)); i != 0 {
		t.Fatalf("even pick = %d, want upper middle 0", i)
	}
	s := totals(20, 3420)
	s[1].Rerouted = true
	if i, _ := PickSample(s); i != 1 {
		t.Fatalf("rerouted pick = %d, want 1", i)
	}
	if _, ok := PickSample(nil); ok {
		t.Fatal("PickSample(nil) ok = true")
	}
	if lo, hi, _ := TotalRange(totals(76, 14, 18)); lo != 14 || hi != 76 {
		t.Fatalf("TotalRange = %d..%d, want 14..76", lo, hi)
	}
}
