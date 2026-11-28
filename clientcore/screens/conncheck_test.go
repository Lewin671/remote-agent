package screens

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func TestBuildMachineLatencyViewStates(t *testing.T) {
	directRow := &corev1.ConnCheckDirectRowView{
		Visible:    true,
		StatusText: text.Msg(text.KeyTransportStatusDirect),
	}

	t.Run("not_checked_when_nil_or_not_checked", func(t *testing.T) {
		v := BuildMachineLatencyView("MacBook", directRow, nil)
		if v.State != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_NOT_CHECKED ||
			v.GetValueText().GetKey() != text.KeyConnectionNotChecked ||
			len(v.Segments) != 0 ||
			v.GetA11YText().GetKey() != text.KeyConnectionLatencyA11Y {
			t.Fatalf("unexpected NOT_CHECKED view: %+v", v)
		}
	})

	t.Run("measuring_direct_with_network_and_relay_pending", func(t *testing.T) {
		vDirect := BuildMachineLatencyView("MacBook", directRow, &MachineLatencySnapshot{
			State:           corev1.MachineLatencyState_MACHINE_LATENCY_STATE_MEASURING,
			StartedDirect:   true,
			HasNetwork:      true,
			NetworkMs:       6,
			NetworkIsDirect: true,
		})
		if vDirect.State != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_MEASURING ||
			vDirect.GetValueText().GetKey() != text.KeyConnectionChecking ||
			len(vDirect.Segments) != 3 ||
			vDirect.Segments[0].GetValueText().GetKey() != text.KeyConnectionLatencyMs ||
			vDirect.Segments[1].GetValueText().GetKey() != text.KeyConnectionLatencyPending ||
			vDirect.Segments[2].GetLabelText().GetKey() != text.KeyConnectionLatencyLocal ||
			vDirect.Segments[2].GetValueText().GetKey() != text.KeyConnectionLatencyPending {
			t.Fatalf("unexpected direct MEASURING view: %+v", vDirect)
		}

		vRelay := BuildMachineLatencyView("VPS-dev", nil, &MachineLatencySnapshot{
			State: corev1.MachineLatencyState_MACHINE_LATENCY_STATE_MEASURING,
		})
		if len(vRelay.Segments) != 3 ||
			vRelay.Segments[0].GetValueText().GetKey() != text.KeyConnectionLatencyPending ||
			vRelay.Segments[1].GetValueText().GetKey() != text.KeyConnectionLatencyPending {
			t.Fatalf("unexpected relay MEASURING view: %+v", vRelay)
		}
	})

	t.Run("done_normal_direct_and_relay", func(t *testing.T) {
		v := BuildMachineLatencyView("MacBook", directRow, &MachineLatencySnapshot{
			State:           corev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE,
			StartedDirect:   true,
			HasNetwork:      true,
			NetworkMs:       6,
			NetworkIsDirect: true,
			HasComputer:     true,
			ComputerMs:      21,
			ExecuteMs:       20,
			HasTotal:        true,
			TotalMs:         38,
		})
		if v.Slow || v.GetValueText().GetKey() != text.KeyConnectionLatencyMs || len(v.Segments) != 2 || v.Segments[0].Slow || v.Segments[1].Slow || v.Segments[1].DetailText != nil {
			t.Fatalf("unexpected normal DONE view: %+v", v)
		}
	})

	t.Run("done_slow_computer_queued_and_running", func(t *testing.T) {
		vQueued := BuildMachineLatencyView("MacBook", directRow, &MachineLatencySnapshot{
			State:           corev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE,
			StartedDirect:   true,
			HasNetwork:      true,
			NetworkMs:       8,
			NetworkIsDirect: true,
			HasComputer:     true,
			ComputerMs:      1190,
			QueueMs:         1150,
			ExecuteMs:       25,
			HasTotal:        true,
			TotalMs:         1250,
		})
		if !vQueued.Slow || vQueued.GetValueText().GetKey() != text.KeyConnectionLatencyMsSlow ||
			vQueued.GetValueText().GetArgs()[0].GetStrValue() != "1,250" ||
			vQueued.Segments[0].Slow || !vQueued.Segments[1].Slow ||
			vQueued.Segments[1].GetDetailText().GetKey() != text.KeyConnectionLatencyMostlyQueued ||
			vQueued.Segments[1].GetDetailText().GetArgs()[0].GetStrValue() != "1,150" {
			t.Fatalf("unexpected slow-queued view: %+v", vQueued)
		}

		vRun := BuildMachineLatencyView("VPS-dev", nil, &MachineLatencySnapshot{
			State:       corev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE,
			HasNetwork:  true,
			NetworkMs:   296,
			HasComputer: true,
			ComputerMs:  1790,
			QueueMs:     10,
			ExecuteMs:   1770,
			HasTotal:    true,
			TotalMs:     2140,
		})
		if !vRun.Slow || vRun.Segments[0].Slow || !vRun.Segments[1].Slow ||
			vRun.Segments[1].GetDetailText().GetKey() != text.KeyConnectionLatencyMostlyRunning ||
			vRun.Segments[1].GetDetailText().GetArgs()[0].GetStrValue() != "1,770" {
			t.Fatalf("unexpected slow-running view: %+v", vRun)
		}
	})

	t.Run("done_rerouted_and_total_only_fallback", func(t *testing.T) {
		vReroute := BuildMachineLatencyView("MacBook", directRow, &MachineLatencySnapshot{
			State:           corev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE,
			StartedDirect:   true,
			Rerouted:        true,
			HasNetwork:      true,
			NetworkMs:       310,
			NetworkIsDirect: false,
			HasComputer:     true,
			ComputerMs:      64,
			ExecuteMs:       50,
			HasWaitDirect:   true,
			WaitDirectMs:    3046,
			HasTotal:        true,
			TotalMs:         3420,
		})
		if !vReroute.Slow || vReroute.GetSummaryOverrideText().GetKey() != text.KeyConnectionLatencyRerouted ||
			len(vReroute.Segments) != 3 ||
			vReroute.Segments[2].GetLabelText().GetKey() != text.KeyConnectionLatencyWaitDirect ||
			!vReroute.Segments[2].Slow || vReroute.Segments[0].Slow || vReroute.Segments[1].Slow {
			t.Fatalf("unexpected rerouted view: %+v", vReroute)
		}

		vFallback := BuildMachineLatencyView("VPS-dev", nil, &MachineLatencySnapshot{
			State:       corev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE,
			HasNetwork:  true,
			NetworkMs:   700,
			HasComputer: true,
			ComputerMs:  400,
			HasTotal:    true,
			TotalMs:     1500,
		})
		if !vFallback.Slow || vFallback.Segments[0].Slow || vFallback.Segments[1].Slow {
			t.Fatalf("unexpected total-only fallback view: %+v", vFallback)
		}
	})

	t.Run("unsupported_direct_and_relay", func(t *testing.T) {
		vDirect := BuildMachineLatencyView("MacBook", directRow, &MachineLatencySnapshot{
			State:           corev1.MachineLatencyState_MACHINE_LATENCY_STATE_UNSUPPORTED,
			StartedDirect:   true,
			HasNetwork:      true,
			NetworkMs:       6,
			NetworkIsDirect: true,
		})
		if vDirect.GetValueText().GetKey() != text.KeyConnectionLatencyOutdated ||
			len(vDirect.Segments) != 1 ||
			vDirect.GetNoteText().GetKey() != text.KeyConnectionLatencyOutdatedComputer {
			t.Fatalf("unexpected direct UNSUPPORTED view: %+v", vDirect)
		}

		vRelay := BuildMachineLatencyView("Lab", nil, &MachineLatencySnapshot{
			State: corev1.MachineLatencyState_MACHINE_LATENCY_STATE_UNSUPPORTED,
		})
		if vRelay.GetValueText().GetKey() != text.KeyConnectionLatencyOutdated ||
			len(vRelay.Segments) != 0 ||
			vRelay.GetNoteText().GetKey() != text.KeyConnectionLatencyOutdatedAll {
			t.Fatalf("unexpected relay UNSUPPORTED view: %+v", vRelay)
		}
	})

	t.Run("timeouts_work_15s_and_echo_5s", func(t *testing.T) {
		vWork := BuildMachineLatencyView("VPS-dev", nil, &MachineLatencySnapshot{
			State:            corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED,
			HasNetwork:       true,
			NetworkMs:        900,
			NetworkIsDirect:  false,
			ComputerTimedOut: true,
		})
		if vWork.GetValueText().GetKey() != text.KeyConnectionLatencyComputerNoReply ||
			len(vWork.Segments) != 2 ||
			!vWork.Segments[0].Slow ||
			!vWork.Segments[1].Stuck || vWork.Segments[1].Slow ||
			vWork.Segments[1].GetValueText().GetKey() != text.KeyConnectionLatencyOverSeconds ||
			vWork.Segments[1].GetValueText().GetArgs()[0].GetIntValue() != 15 {
			t.Fatalf("unexpected work timeout view: %+v", vWork)
		}

		vEcho := BuildMachineLatencyView("Studio", nil, &MachineLatencySnapshot{
			State:           corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED,
			NetworkTimedOut: true,
		})
		if vEcho.GetValueText().GetKey() != text.KeyConnectionLatencyNoReply ||
			len(vEcho.Segments) != 2 ||
			!vEcho.Segments[0].Stuck || vEcho.Segments[0].Slow ||
			vEcho.Segments[0].GetValueText().GetArgs()[0].GetIntValue() != 5 ||
			vEcho.Segments[1].GetValueText().GetKey() != text.KeyConnectionNotChecked {
			t.Fatalf("unexpected echo timeout view: %+v", vEcho)
		}
	})
}
