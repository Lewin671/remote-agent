package screens

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/latency"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// BuildMachineLatencyView projects a MachineLatencySnapshot into the shell-facing MachineLatencyView.
func BuildMachineLatencyView(machineName string, directRow *corev1.ConnCheckDirectRowView, snap *MachineLatencySnapshot) *corev1.MachineLatencyView {
	if snap == nil || snap.State == corev1.MachineLatencyState_MACHINE_LATENCY_STATE_UNSPECIFIED ||
		snap.State == corev1.MachineLatencyState_MACHINE_LATENCY_STATE_NOT_CHECKED {
		v := &corev1.MachineLatencyView{
			State:     corev1.MachineLatencyState_MACHINE_LATENCY_STATE_NOT_CHECKED,
			ValueText: text.Msg(text.KeyConnectionNotChecked),
		}
		v.A11YText = buildMachineLatencyA11y(machineName, directRow, v, 0, false)
		return v
	}

	eval := latency.Evaluate(latency.EvalInput{
		HasNetwork:      snap.HasNetwork,
		NetworkMs:       snap.NetworkMs,
		NetworkIsDirect: snap.NetworkIsDirect,
		HasComputer:     snap.HasComputer,
		ComputerMs:      snap.ComputerMs,
		QueueMs:         snap.QueueMs,
		ExecuteMs:       snap.ExecuteMs,
		HasWaitDirect:   snap.HasWaitDirect,
		WaitDirectMs:    snap.WaitDirectMs,
		HasLocal:        snap.HasLocal,
		LocalMs:         snap.LocalMs,
		HasTotal:        snap.HasTotal,
		TotalMs:         snap.TotalMs,
	})

	switch snap.State {
	case corev1.MachineLatencyState_MACHINE_LATENCY_STATE_MEASURING:
		netSeg := &corev1.MachineLatencySegment{
			LabelText: text.Msg(text.KeyConnectionLatencyNetwork),
			ValueText: text.Msg(text.KeyConnectionLatencyPending),
		}
		if snap.HasNetwork {
			netSeg.ValueText = formatLatencyMsText(snap.NetworkMs)
			netSeg.Slow = eval.NetworkSlow
			netSeg.SlowLabelText = slowLabelText(eval.NetworkSlow)
		}
		compSeg := &corev1.MachineLatencySegment{
			LabelText: text.Msg(text.KeyConnectionLatencyComputer),
			ValueText: text.Msg(text.KeyConnectionLatencyPending),
		}
		localSeg := &corev1.MachineLatencySegment{
			LabelText: text.Msg(text.KeyConnectionLatencyLocal),
			ValueText: text.Msg(text.KeyConnectionLatencyPending),
		}
		v := &corev1.MachineLatencyView{
			State:     corev1.MachineLatencyState_MACHINE_LATENCY_STATE_MEASURING,
			ValueText: text.Msg(text.KeyConnectionChecking),
			Segments:  []*corev1.MachineLatencySegment{netSeg, compSeg, localSeg},
		}
		v.A11YText = buildMachineLatencyA11y(machineName, directRow, v, 0, false)
		return v

	case corev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE:
		totalStr := latency.FormatMs(snap.TotalMs)
		valText := text.Msg(text.KeyConnectionLatencyMs, text.Str("ms", totalStr))
		if eval.TotalSlow {
			valText = text.Msg(text.KeyConnectionLatencyMsSlow, text.Str("ms", totalStr))
		}
		netSeg := &corev1.MachineLatencySegment{
			LabelText:     text.Msg(text.KeyConnectionLatencyNetwork),
			ValueText:     formatLatencyMsText(snap.NetworkMs),
			Slow:          eval.NetworkSlow,
			SlowLabelText: slowLabelText(eval.NetworkSlow),
		}
		compSeg := &corev1.MachineLatencySegment{
			LabelText:     text.Msg(text.KeyConnectionLatencyComputer),
			ValueText:     formatLatencyMsText(snap.ComputerMs),
			Slow:          eval.ComputerSlow,
			SlowLabelText: slowLabelText(eval.ComputerSlow),
		}
		if eval.ComputerSlow {
			switch eval.ComputerCause {
			case latency.CauseQueue:
				compSeg.DetailText = text.Msg(text.KeyConnectionLatencyMostlyQueued, text.Str("ms", latency.FormatMs(eval.ComputerCauseMs)))
			case latency.CauseExecute:
				compSeg.DetailText = text.Msg(text.KeyConnectionLatencyMostlyRunning, text.Str("ms", latency.FormatMs(eval.ComputerCauseMs)))
			}
		}
		segs := []*corev1.MachineLatencySegment{netSeg, compSeg}
		if snap.HasLocal {
			segs = append(segs, &corev1.MachineLatencySegment{
				LabelText:     text.Msg(text.KeyConnectionLatencyLocal),
				ValueText:     formatLatencyMsText(snap.LocalMs),
				Slow:          eval.LocalSlow,
				SlowLabelText: slowLabelText(eval.LocalSlow),
			})
		}
		var summaryOverride *corev1.Text
		if snap.Rerouted {
			summaryOverride = text.Msg(text.KeyConnectionLatencyRerouted)
			if snap.HasWaitDirect {
				segs = append(segs, &corev1.MachineLatencySegment{
					LabelText:     text.Msg(text.KeyConnectionLatencyWaitDirect),
					ValueText:     formatLatencyMsText(snap.WaitDirectMs),
					Slow:          eval.WaitDirectSlow,
					SlowLabelText: slowLabelText(eval.WaitDirectSlow),
				})
			}
		}
		v := &corev1.MachineLatencyView{
			State:               corev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE,
			ValueText:           valText,
			Slow:                eval.TotalSlow,
			Segments:            segs,
			SummaryOverrideText: summaryOverride,
		}
		if snap.SampleCount > 1 {
			v.NoteText = text.Msg(text.KeyConnectionLatencyRunsRange,
				text.Int("count", int64(snap.SampleCount)),
				text.Str("min", latency.FormatMs(snap.MinTotalMs)),
				text.Str("max", latency.FormatMs(snap.MaxTotalMs)),
			)
		}
		v.A11YText = buildMachineLatencyA11y(machineName, directRow, v, snap.TotalMs, true)
		return v

	case corev1.MachineLatencyState_MACHINE_LATENCY_STATE_UNSUPPORTED:
		v := &corev1.MachineLatencyView{
			State:     corev1.MachineLatencyState_MACHINE_LATENCY_STATE_UNSUPPORTED,
			ValueText: text.Msg(text.KeyConnectionLatencyOutdated),
		}
		if snap.StartedDirect && snap.HasNetwork {
			v.Segments = []*corev1.MachineLatencySegment{
				{
					LabelText:     text.Msg(text.KeyConnectionLatencyNetwork),
					ValueText:     formatLatencyMsText(snap.NetworkMs),
					Slow:          eval.NetworkSlow,
					SlowLabelText: slowLabelText(eval.NetworkSlow),
				},
			}
			v.NoteText = text.Msg(text.KeyConnectionLatencyOutdatedComputer)
		} else {
			v.NoteText = text.Msg(text.KeyConnectionLatencyOutdatedAll)
		}
		v.A11YText = buildMachineLatencyA11y(machineName, directRow, v, 0, false)
		return v

	case corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED:
		v := &corev1.MachineLatencyView{
			State: corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED,
		}
		switch {
		case snap.NetworkTimedOut:
			v.ValueText = text.Msg(text.KeyConnectionLatencyNoReply)
			v.Segments = []*corev1.MachineLatencySegment{
				{
					LabelText: text.Msg(text.KeyConnectionLatencyNetwork),
					ValueText: text.Msg(text.KeyConnectionLatencyOverSeconds, text.Int("seconds", latency.EchoTimeoutMs/1000)),
					Stuck:     true,
				},
				{
					LabelText: text.Msg(text.KeyConnectionLatencyComputer),
					ValueText: text.Msg(text.KeyConnectionNotChecked),
				},
			}
		case snap.ComputerTimedOut:
			v.ValueText = text.Msg(text.KeyConnectionLatencyComputerNoReply)
			netSeg := &corev1.MachineLatencySegment{
				LabelText: text.Msg(text.KeyConnectionLatencyNetwork),
				ValueText: text.Msg(text.KeyConnectionLatencyPending),
			}
			if snap.HasNetwork {
				netSeg.ValueText = formatLatencyMsText(snap.NetworkMs)
				netSeg.Slow = eval.NetworkSlow
				netSeg.SlowLabelText = slowLabelText(eval.NetworkSlow)
			}
			v.Segments = []*corev1.MachineLatencySegment{
				netSeg,
				{
					LabelText: text.Msg(text.KeyConnectionLatencyComputer),
					ValueText: text.Msg(text.KeyConnectionLatencyOverSeconds, text.Int("seconds", latency.WorkTimeoutMs/1000)),
					Stuck:     true,
				},
			}
		default:
			if !text.IsEmpty(snap.ErrorText) {
				v.ValueText = snap.ErrorText
			} else {
				v.ValueText = text.Msg(text.KeyErrorsCommandFailed)
			}
			if snap.HasNetwork {
				v.Segments = []*corev1.MachineLatencySegment{
					{
						LabelText:     text.Msg(text.KeyConnectionLatencyNetwork),
						ValueText:     formatLatencyMsText(snap.NetworkMs),
						Slow:          eval.NetworkSlow,
						SlowLabelText: slowLabelText(eval.NetworkSlow),
					},
					{
						LabelText: text.Msg(text.KeyConnectionLatencyComputer),
						ValueText: text.Msg(text.KeyConnectionNotChecked),
					},
				}
			} else {
				v.Segments = []*corev1.MachineLatencySegment{
					{
						LabelText: text.Msg(text.KeyConnectionLatencyNetwork),
						ValueText: text.Msg(text.KeyConnectionLatencyPending),
						Stuck:     true,
					},
					{
						LabelText: text.Msg(text.KeyConnectionLatencyComputer),
						ValueText: text.Msg(text.KeyConnectionNotChecked),
					},
				}
			}
		}
		v.A11YText = buildMachineLatencyA11y(machineName, directRow, v, 0, false)
		return v
	}

	v := &corev1.MachineLatencyView{
		State:     corev1.MachineLatencyState_MACHINE_LATENCY_STATE_NOT_CHECKED,
		ValueText: text.Msg(text.KeyConnectionNotChecked),
	}
	v.A11YText = buildMachineLatencyA11y(machineName, directRow, v, 0, false)
	return v
}

func formatLatencyMsText(ms int64) *corev1.Text {
	return text.Msg(text.KeyConnectionLatencyMs, text.Str("ms", latency.FormatMs(ms)))
}

func slowLabelText(slow bool) *corev1.Text {
	if !slow {
		return nil
	}
	return text.Msg(text.KeyConnectionLatencySlow)
}

func buildMachineLatencyA11y(machineName string, directRow *corev1.ConnCheckDirectRowView, v *corev1.MachineLatencyView, totalMs int64, hasTotal bool) *corev1.Text {
	parts := make([]*corev1.Text, 0, 8)
	if hasTotal {
		msStr := latency.FormatMs(totalMs)
		if v.Slow {
			parts = append(parts, text.Msg(text.KeyConnectionLatencyA11YTotalSlow, text.Str("ms", msStr)))
		} else {
			parts = append(parts, text.Msg(text.KeyConnectionLatencyA11YTotal, text.Str("ms", msStr)))
		}
	} else if !text.IsEmpty(v.ValueText) {
		parts = append(parts, v.ValueText)
	}
	for _, seg := range v.Segments {
		if seg == nil {
			continue
		}
		if seg.Slow {
			parts = append(parts, text.Msg(
				text.KeyConnectionLatencyA11YSegmentSlow,
				text.ArgText("label", seg.LabelText),
				text.ArgText("value", seg.ValueText),
			))
		} else {
			parts = append(parts, text.Msg(
				text.KeyConnectionLatencyA11YSegment,
				text.ArgText("label", seg.LabelText),
				text.ArgText("value", seg.ValueText),
			))
		}
		if !text.IsEmpty(seg.DetailText) {
			parts = append(parts, seg.DetailText)
		}
	}
	if !text.IsEmpty(v.NoteText) {
		parts = append(parts, v.NoteText)
	}
	if !text.IsEmpty(v.SummaryOverrideText) {
		parts = append(parts, v.SummaryOverrideText)
	} else if directRow != nil && directRow.Visible {
		if !text.IsEmpty(directRow.StatusText) {
			parts = append(parts, directRow.StatusText)
		}
		if !text.IsEmpty(directRow.DetailText) {
			parts = append(parts, directRow.DetailText)
		}
	}
	details := text.SpokenJoin(parts...)
	if machineName == "" {
		return details
	}
	return text.Msg(
		text.KeyConnectionLatencyA11Y,
		text.Str("name", machineName),
		text.ArgText("details", details),
	)
}
