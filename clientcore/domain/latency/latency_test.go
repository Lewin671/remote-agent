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

func TestNetRoundTripAndWaitDirectClampZero(t *testing.T) {
	if got := NetRoundTripMs(100, 25_000); got != 75 {
		t.Fatalf("NetRoundTripMs(100, 25000) = %d, want 75", got)
	}
	if got := NetRoundTripMs(10, 25_000); got != 0 {
		t.Fatalf("NetRoundTripMs(10, 25000) = %d, want 0", got)
	}
	if got := NetRoundTripMs(10.6, 500); got != 10 {
		t.Fatalf("NetRoundTripMs(10.6, 500) = %d, want 10", got)
	}
	if got := WaitDirectMs(3420, 374); got != 3046 {
		t.Fatalf("WaitDirectMs(3420, 374) = %d, want 3046", got)
	}
	if got := WaitDirectMs(200, 300); got != 0 {
		t.Fatalf("WaitDirectMs(200, 300) = %d, want 0", got)
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
