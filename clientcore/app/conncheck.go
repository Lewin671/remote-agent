package app

import (
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/latency"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	connCheckTimerPrefix       = "conncheck:"
	connCheckSampleTimerPrefix = "conncheck:sample:"
	connCheckEchoTimerPrefix   = "conncheck:echo:"
	connCheckWorkTimerPrefix   = "conncheck:work:"
)

type connCheckPhase uint8

const (
	connCheckPhaseDone connCheckPhase = iota
	connCheckPhaseWaitServer
	connCheckPhaseWaitDirectSample
	connCheckPhaseRelayEcho
	connCheckPhaseWork
)

type connCheckMachineJob struct {
	machineID         string
	roundID           uint64
	phase             connCheckPhase
	startedDirect     bool
	activeReqID       string
	reqStartedMonoMs  int64
	workStartedMonoMs int64
	echoSamples       []int64
}

type connCheckController struct {
	active  bool
	roundID uint64
	jobs    map[string]*connCheckMachineJob
}

func (m *Model) ensureConnCheck() *connCheckController {
	if m.connCheck == nil {
		m.connCheck = &connCheckController{
			jobs: make(map[string]*connCheckMachineJob),
		}
	}
	return m.connCheck
}

func connCheckTimerKey(prefix string, roundID uint64, machineID string) string {
	return prefix + strconv.FormatUint(roundID, 10) + ":" + machineID
}

func parseConnCheckTimerKey(key string) (kind string, roundID uint64, machineID string, ok bool) {
	var rest string
	switch {
	case strings.HasPrefix(key, connCheckSampleTimerPrefix):
		kind = "sample"
		rest = strings.TrimPrefix(key, connCheckSampleTimerPrefix)
	case strings.HasPrefix(key, connCheckEchoTimerPrefix):
		kind = "echo"
		rest = strings.TrimPrefix(key, connCheckEchoTimerPrefix)
	case strings.HasPrefix(key, connCheckWorkTimerPrefix):
		kind = "work"
		rest = strings.TrimPrefix(key, connCheckWorkTimerPrefix)
	default:
		return "", 0, "", false
	}
	roundStr, mid, found := strings.Cut(rest, ":")
	if !found || mid == "" {
		return "", 0, "", false
	}
	rid, err := strconv.ParseUint(roundStr, 10, 64)
	if err != nil {
		return "", 0, "", false
	}
	return kind, rid, mid, true
}

func (c *Core) isMachineReadDirect(m *lists.Machine) bool {
	if m == nil || c.m.state == nil || c.m.state.DirectReportFn == nil {
		return false
	}
	_, report, ok := c.m.state.DirectReportFn(m.ID)
	if !ok || report == nil {
		return false
	}
	return report.State == corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED &&
		report.ReadRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
}

func (c *Core) directProbeMedianMs(machineID string) (int64, bool) {
	samples := c.m.sync.DirectProbeSamples(machineID)
	if len(samples) == 0 {
		return 0, false
	}
	vals := make([]float64, len(samples))
	for i, s := range samples {
		vals[i] = s.RttMs
	}
	return latency.MedianFloat64Ms(vals)
}

func (c *Core) onConnCheckStart(ctx *sync.StepContext) {
	c.stopConnCheck(ctx, false)
	cc := c.m.ensureConnCheck()
	extra := c.m.ensureSettings()
	if extra.MachineLatency == nil {
		extra.MachineLatency = make(map[string]*screens.MachineLatencySnapshot)
	} else {
		clear(extra.MachineLatency)
	}
	// Reset this round's server probe state so relay machines always wait for
	// the shell's fresh ConnCheckStatus verdict for the current round.
	// Shell contract: any status reported before Start does not count for this
	// round; this round's /healthz verdict must arrive after Start.
	extra.ConnCheck.ServerState = corev1.ConnProbeState_CONN_PROBE_STATE_CHECKING
	extra.ConnCheck.LatencyMs = 0
	extra.ConnCheck.LastFailure = ""

	cc.roundID++
	cc.active = true
	roundID := cc.roundID

	clientVer := c.m.state.World.ClientVersion
	removed := c.m.state.Removed
	var targets []*lists.Machine
	for _, m := range c.m.state.World.Machines {
		if m == nil {
			continue
		}
		online := lists.MachineReachable(m)
		unlinked := lists.MachineUnlinked(m, &c.m.state.World)
		archived := m.Archived || removed
		mismatch := lists.ProtocolMismatchDirection(m, clientVer)
		if !online || unlinked || archived || mismatch != 0 {
			continue
		}
		targets = append(targets, m)
	}
	sortutil.Sort(len(targets), func(i, j int) bool {
		return targets[i].ID < targets[j].ID
	}, func(i, j int) {
		targets[i], targets[j] = targets[j], targets[i]
	})

	for _, m := range targets {
		direct := c.isMachineReadDirect(m)
		snap := &screens.MachineLatencySnapshot{
			State:           corev1.MachineLatencyState_MACHINE_LATENCY_STATE_MEASURING,
			StartedDirect:   direct,
			NetworkIsDirect: direct,
		}
		extra.MachineLatency[m.ID] = snap

		job := &connCheckMachineJob{
			machineID:     m.ID,
			roundID:       roundID,
			startedDirect: direct,
		}
		cc.jobs[m.ID] = job

		if direct {
			if med, ok := c.directProbeMedianMs(m.ID); ok {
				snap.HasNetwork = true
				snap.NetworkMs = med
				c.startConnCheckWork(ctx, job, snap)
			} else {
				job.phase = connCheckPhaseWaitDirectSample
				c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, connCheckTimerKey(connCheckSampleTimerPrefix, roundID, m.ID), latency.DirectSampleWaitMs)
			}
			continue
		}

		// Relay machine: always waits for this round's shell-reported server verdict.
		job.phase = connCheckPhaseWaitServer
	}

	c.m.settingsDirty.Settings = true
}

func (c *Core) stopConnCheck(ctx *sync.StepContext, resetMeasuring bool) {
	cc := c.m.connCheck
	if cc == nil {
		return
	}
	extra := c.m.settings
	ids := make([]string, 0, len(cc.jobs))
	for id := range cc.jobs {
		ids = append(ids, id)
	}
	sortutil.Strings(ids)
	for _, id := range ids {
		job := cc.jobs[id]
		if job == nil {
			continue
		}
		if job.activeReqID != "" {
			delete(c.m.pendingCmdMeta, job.activeReqID)
			if ctx != nil {
				c.m.sync.CancelCommand(ctx, job.activeReqID)
			}
			job.activeReqID = ""
		}
		if ctx != nil {
			c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, connCheckTimerKey(connCheckSampleTimerPrefix, job.roundID, id))
			c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, connCheckTimerKey(connCheckEchoTimerPrefix, job.roundID, id))
			c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, connCheckTimerKey(connCheckWorkTimerPrefix, job.roundID, id))
		}
		if resetMeasuring && extra != nil && extra.MachineLatency != nil {
			if snap := extra.MachineLatency[id]; snap != nil && snap.State == corev1.MachineLatencyState_MACHINE_LATENCY_STATE_MEASURING {
				snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_NOT_CHECKED
			}
		}
	}
	clear(cc.jobs)
	cc.active = false
	if resetMeasuring {
		c.m.settingsDirty.Settings = true
	}
}

func (c *Core) onConnCheckStatusUpdated(ctx *sync.StepContext) {
	cc := c.m.connCheck
	if cc == nil || !cc.active {
		return
	}
	extra := c.m.ensureSettings()
	serverState := extra.ConnCheck.ServerState
	if serverState != corev1.ConnProbeState_CONN_PROBE_STATE_WORKING &&
		serverState != corev1.ConnProbeState_CONN_PROBE_STATE_SLOW &&
		serverState != corev1.ConnProbeState_CONN_PROBE_STATE_UNAVAILABLE {
		return
	}

	ids := make([]string, 0, len(cc.jobs))
	for id := range cc.jobs {
		ids = append(ids, id)
	}
	sortutil.Strings(ids)

	changed := false
	for _, id := range ids {
		job := cc.jobs[id]
		if job == nil || job.phase != connCheckPhaseWaitServer {
			continue
		}
		snap := extra.MachineLatency[id]
		if snap == nil {
			job.phase = connCheckPhaseDone
			continue
		}
		if serverState == corev1.ConnProbeState_CONN_PROBE_STATE_UNAVAILABLE {
			snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_NOT_CHECKED
			job.phase = connCheckPhaseDone
			changed = true
			continue
		}
		c.startConnCheckEcho(ctx, job, snap)
		changed = true
	}
	if changed {
		c.m.settingsDirty.Settings = true
	}
}

func (c *Core) onConnCheckDirectProbeReceived(effects *[]*corev1.Effect) {
	cc := c.m.connCheck
	if cc == nil || !cc.active {
		return
	}
	extra := c.m.settings
	if extra == nil || extra.MachineLatency == nil {
		return
	}
	var waitingIDs []string
	for id, job := range cc.jobs {
		if job != nil && job.phase == connCheckPhaseWaitDirectSample {
			waitingIDs = append(waitingIDs, id)
		}
	}
	if len(waitingIDs) == 0 {
		return
	}
	sortutil.Strings(waitingIDs)

	prev, stepCtx := c.m.sync.BeginStep()
	changed := false
	for _, id := range waitingIDs {
		job := cc.jobs[id]
		snap := extra.MachineLatency[id]
		if job == nil || snap == nil || job.phase != connCheckPhaseWaitDirectSample {
			continue
		}
		if med, ok := c.directProbeMedianMs(id); ok {
			c.m.sync.CancelAppTimer(stepCtx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, connCheckTimerKey(connCheckSampleTimerPrefix, job.roundID, id))
			snap.HasNetwork = true
			snap.NetworkMs = med
			snap.NetworkIsDirect = true
			c.startConnCheckWork(stepCtx, job, snap)
			changed = true
		}
	}
	stepRes := c.m.sync.EndStep(prev, stepCtx)
	if len(stepRes.Effects) > 0 {
		*effects = append(*effects, stepRes.Effects...)
	}
	if changed {
		c.m.settingsDirty.Settings = true
	}
}

func (c *Core) startConnCheckEcho(ctx *sync.StepContext, job *connCheckMachineJob, snap *screens.MachineLatencySnapshot) {
	job.phase = connCheckPhaseRelayEcho
	job.reqStartedMonoMs = c.m.sync.MonoMs()
	reqID, ok := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID:                 job.machineID,
		Command:                   &v1.Command{Kind: &v1.Command_ProbeLatency{ProbeLatency: &v1.ProbeLatencyRequest{Work: false}}},
		ForceRelay:                true,
		NoControlSample:           true,
		SkipConnectivityPreflight: true,
	})
	if !ok || reqID == "" {
		job.phase = connCheckPhaseDone
		snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED
		snap.ErrorText = text.Msg(text.KeyErrorsCommandFailed)
		return
	}
	job.activeReqID = reqID
	c.m.pendingCmdMeta[reqID] = cmdMeta{
		kind:      cmdKindConnCheckEcho,
		machineID: job.machineID,
		roundID:   job.roundID,
	}
	c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, connCheckTimerKey(connCheckEchoTimerPrefix, job.roundID, job.machineID), latency.EchoTimeoutMs)
}

func (c *Core) startConnCheckWork(ctx *sync.StepContext, job *connCheckMachineJob, snap *screens.MachineLatencySnapshot) {
	job.phase = connCheckPhaseWork
	job.workStartedMonoMs = c.m.sync.MonoMs()
	reqID, ok := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID:                 job.machineID,
		Command:                   &v1.Command{Kind: &v1.Command_ProbeLatency{ProbeLatency: &v1.ProbeLatencyRequest{Work: true}}},
		ForceRelay:                !job.startedDirect,
		NoControlSample:           true,
		SkipConnectivityPreflight: true,
	})
	if !ok || reqID == "" {
		job.phase = connCheckPhaseDone
		snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED
		snap.ErrorText = text.Msg(text.KeyErrorsCommandFailed)
		return
	}
	job.activeReqID = reqID
	c.m.pendingCmdMeta[reqID] = cmdMeta{
		kind:      cmdKindConnCheckWork,
		machineID: job.machineID,
		roundID:   job.roundID,
	}
	c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, connCheckTimerKey(connCheckWorkTimerPrefix, job.roundID, job.machineID), latency.WorkTimeoutMs)
}

func isProbeUnsupported(sc sync.SettledCommand) bool {
	return sc.Category == outcome.Rejected && sc.Result != nil && sc.Result.Reason == "invalid_body"
}

func (c *Core) onConnCheckCommandSettled(sc sync.SettledCommand, meta cmdMeta) []*corev1.Effect {
	cc := c.m.connCheck
	if cc == nil || !cc.active || cc.roundID != meta.roundID {
		return nil
	}
	job := cc.jobs[meta.machineID]
	if job == nil || job.roundID != meta.roundID || job.activeReqID != sc.RequestID {
		return nil
	}
	extra := c.m.ensureSettings()
	snap := extra.MachineLatency[meta.machineID]
	if snap == nil {
		job.phase = connCheckPhaseDone
		job.activeReqID = ""
		return nil
	}
	job.activeReqID = ""

	prev, stepCtx := c.m.sync.BeginStep()
	switch meta.kind {
	case cmdKindConnCheckEcho:
		c.m.sync.CancelAppTimer(stepCtx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, connCheckTimerKey(connCheckEchoTimerPrefix, job.roundID, job.machineID))
		if isProbeUnsupported(sc) {
			snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_UNSUPPORTED
			job.phase = connCheckPhaseDone
			break
		}
		if sc.Category != outcome.Executed || sc.ErrorCode != "" || sc.Result == nil || sc.Result.GetProbeLatency() == nil {
			snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED
			snap.ErrorText = screens.ReasonText(sc.ErrorReason)
			if text.IsEmpty(snap.ErrorText) {
				snap.ErrorText = text.Msg(text.KeyErrorsCommandFailed)
			}
			job.phase = connCheckPhaseDone
			break
		}
		wireMs := sc.WireElapsedMs
		if wireMs <= 0 {
			wireMs = float64(max(int64(0), c.m.sync.MonoMs()-job.reqStartedMonoMs))
		}
		sampleMs := latency.NetRoundTripMs(wireMs, sc.Result.GetProbeLatency().GetProcessUs())
		job.echoSamples = append(job.echoSamples, sampleMs)
		if len(job.echoSamples) < latency.RelayEchoSamples {
			c.startConnCheckEcho(stepCtx, job, snap)
		} else {
			if med, ok := latency.MedianInt64(job.echoSamples); ok {
				snap.HasNetwork = true
				snap.NetworkMs = med
				snap.NetworkIsDirect = false
			}
			c.startConnCheckWork(stepCtx, job, snap)
		}

	case cmdKindConnCheckWork:
		c.m.sync.CancelAppTimer(stepCtx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, connCheckTimerKey(connCheckWorkTimerPrefix, job.roundID, job.machineID))
		if isProbeUnsupported(sc) {
			snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_UNSUPPORTED
			job.phase = connCheckPhaseDone
			break
		}
		if sc.Category != outcome.Executed || sc.ErrorCode != "" || sc.Result == nil || sc.Result.GetProbeLatency() == nil {
			snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED
			snap.ErrorText = screens.ReasonText(sc.ErrorReason)
			if text.IsEmpty(snap.ErrorText) {
				snap.ErrorText = text.Msg(text.KeyErrorsCommandFailed)
			}
			job.phase = connCheckPhaseDone
			break
		}
		totalMs := max(int64(0), c.m.sync.MonoMs()-job.workStartedMonoMs)
		pl := sc.Result.GetProbeLatency()
		snap.HasTotal = true
		snap.TotalMs = totalMs
		snap.HasComputer = true
		snap.ComputerMs = latency.MicrosToMs(pl.ProcessUs)
		snap.QueueMs = latency.MicrosToMs(pl.QueueUs)
		snap.ExecuteMs = latency.MicrosToMs(pl.ExecuteUs)

		if job.startedDirect && !sc.RouteWasDirect {
			// Direct machine settled over relay. Always recalculate network RTT
			// on the relay leg; only mark Rerouted + WaitDirect when sync made
			// >1 attempt and actually tried direct first.
			snap.NetworkIsDirect = false
			wireMs := sc.WireElapsedMs
			if wireMs <= 0 {
				wireMs = float64(totalMs)
			}
			snap.HasNetwork = true
			snap.NetworkMs = latency.NetRoundTripMs(wireMs, pl.ProcessUs)
			if sc.AttemptCount > 1 && sc.TriedDirect {
				snap.Rerouted = true
				snap.HasWaitDirect = true
				snap.WaitDirectMs = latency.WaitDirectMs(totalMs, wireMs)
			} else {
				snap.Rerouted = false
				snap.HasWaitDirect = false
				snap.WaitDirectMs = 0
			}
		} else if job.startedDirect && !snap.HasNetwork {
			wireMs := sc.WireElapsedMs
			if wireMs <= 0 {
				wireMs = float64(totalMs)
			}
			snap.HasNetwork = true
			snap.NetworkMs = latency.NetRoundTripMs(wireMs, pl.ProcessUs)
			snap.NetworkIsDirect = true
		}

		// The network figure is measured separately from this command (a
		// direct keepalive median, or the relay echoes), so it can exceed what
		// this round trip left for the network; never show a part larger than
		// the whole, on either path.
		if snap.HasNetwork {
			snap.NetworkMs = latency.ClampNetworkMs(snap.NetworkMs, totalMs, snap.ComputerMs)
		}
		// The computer's self-reported time happens inside this round trip,
		// so the total can never be smaller than it.
		snap.TotalMs = max(snap.TotalMs, snap.ComputerMs)

		snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE
		job.phase = connCheckPhaseDone
	}

	stepRes := c.m.sync.EndStep(prev, stepCtx)
	c.m.settingsDirty.Settings = true
	return stepRes.Effects
}

func (c *Core) handleConnCheckTimer(ctx *sync.StepContext, key string) []*corev1.Effect {
	kind, roundID, machineID, ok := parseConnCheckTimerKey(key)
	if !ok {
		return nil
	}
	cc := c.m.connCheck
	if cc == nil || !cc.active || cc.roundID != roundID {
		return nil
	}
	job := cc.jobs[machineID]
	if job == nil || job.roundID != roundID {
		return nil
	}
	extra := c.m.ensureSettings()
	snap := extra.MachineLatency[machineID]
	if snap == nil {
		job.phase = connCheckPhaseDone
		return nil
	}

	switch kind {
	case "sample":
		if job.phase != connCheckPhaseWaitDirectSample {
			return nil
		}
		if med, ok := c.directProbeMedianMs(machineID); ok {
			snap.HasNetwork = true
			snap.NetworkMs = med
			snap.NetworkIsDirect = true
		}
		c.startConnCheckWork(ctx, job, snap)
		c.m.settingsDirty.Settings = true

	case "echo":
		if job.phase != connCheckPhaseRelayEcho {
			return nil
		}
		if job.activeReqID != "" {
			delete(c.m.pendingCmdMeta, job.activeReqID)
			c.m.sync.CancelCommand(ctx, job.activeReqID)
			job.activeReqID = ""
		}
		snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED
		snap.NetworkTimedOut = true
		job.phase = connCheckPhaseDone
		c.m.settingsDirty.Settings = true

	case "work":
		if job.phase != connCheckPhaseWork {
			return nil
		}
		if job.activeReqID != "" {
			delete(c.m.pendingCmdMeta, job.activeReqID)
			c.m.sync.CancelCommand(ctx, job.activeReqID)
			job.activeReqID = ""
		}
		snap.State = corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED
		snap.ComputerTimedOut = true
		job.phase = connCheckPhaseDone
		c.m.settingsDirty.Settings = true
	}
	return nil
}
