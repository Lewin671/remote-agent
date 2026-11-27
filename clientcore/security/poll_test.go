package security

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

type recordingPollObserver struct {
	updates        []PollUpdate
	devicesRefresh int
}

func (r *recordingPollObserver) OnSecurityPollUpdate(u PollUpdate) { r.updates = append(r.updates, u) }
func (r *recordingPollObserver) OnSecurityDevicesMayHaveChanged()  { r.devicesRefresh++ }

func findPollTimerDelay(effs []*corev1.Effect) (int64, bool) {
	for _, eff := range effs {
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_SECURITY_POLL {
			return st.DelayMs, true
		}
	}
	return 0, false
}

func hasPollTimerCancel(effs []*corev1.Effect) bool {
	for _, eff := range effs {
		if ct := eff.GetCancelTimer(); ct != nil && ct.Kind == corev1.TimerKind_TIMER_KIND_SECURITY_POLL {
			return true
		}
	}
	return false
}

func activePollKindForTest(e *Engine) ActivePollKind {
	if e == nil || e.wf == nil || e.wf.activePoll == nil {
		return PollNone
	}
	return e.wf.activePoll.kind
}

func TestManagedInitiatorJoinAndMetaPollCap(t *testing.T) {
	e := NewEngine(nil, 17)
	obs := &recordingPollObserver{}
	e.SetPollObserver(obs)
	wf := e.ensureWF()
	wf.server = "https://relay.example.com"
	wf.cachedToken = "tok-1"
	wf.tokenExpiresMs = 999_999_999
	e.loaded, e.hasHead, e.isMember, e.selfDeviceID = true, true, true, "dev-auth"
	e.head.Core.AuthorityID = "dev-auth"

	// 1. PollInitiatorJoined expires cleanly when nowMs >= expiresMs.
	wf.initPairings["p-exp"] = &initiatorPairingSession{
		pairingID: "p-exp",
		server:    "https://relay.example.com",
		expiresMs: 5_000,
		ephemSlot: "ephem-exp",
	}
	effs := e.StartInitiatorJoinedPoll(1_000, "p-exp")
	if delay, ok := findPollTimerDelay(effs); !ok || delay != pairInitJoinedPollIntervalMs {
		t.Fatalf("StartInitiatorJoinedPoll delay = (%d, %v), want (%d, true)", delay, ok, pairInitJoinedPollIntervalMs)
	}
	effs = e.HandleSecurityPollTick(5_000)
	if !hasPollTimerCancel(effs) {
		t.Fatal("expected poll timer cancel on expiry")
	}
	if activePollKindForTest(e) != PollNone {
		t.Fatalf("ActivePollKind = %v, want PollNone", activePollKindForTest(e))
	}
	if len(obs.updates) != 1 || obs.updates[0].Err == nil || obs.updates[0].Meta.ErrReason != WorkflowReasonPairingExpiredOrCanceled {
		t.Fatalf("expiry update = %+v, want PairingExpiredOrCanceled", obs.updates)
	}

	// 2. PollInitiatorJoinerMeta stops silently after 15 polls while keeping pairing approvable.
	obs.updates = nil
	wf.initPairings["p-meta"] = &initiatorPairingSession{
		pairingID:          "p-meta",
		server:             "https://relay.example.com",
		joined:             true,
		joinerMetaReceived: false,
		candidateKind:      v1.DeviceKind_DEVICE_KIND_CLIENT,
		sasEmoji:           []string{"🦊", "🐳", "🌿", "⭐"},
		pairKey:            make([]byte, 32),
	}
	wf.activePoll = &activePoll{kind: PollInitiatorJoinerMeta, pairingID: "p-meta"}

	revealedBytes, _ := proto.Marshal(&pb.GetPairingResponse{State: pb.PairingState_PAIRING_STATE_REVEALED})
	for i := 1; i <= pairInitMetaMaxPolls; i++ {
		tickEffs := e.HandleSecurityPollTick(int64(10_000 + i*1_000))
		rpcEff := findRPCEffect(tickEffs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_PAIRING)
		if rpcEff == nil {
			t.Fatalf("poll %d: expected GET_PAIRING RPC", i)
		}
		resEffs := e.HandleRelayRpcResult(int64(10_000+i*1_000), &corev1.RelayRpcResultEvent{
			OpId:          rpcEff.OpId,
			ResponseBytes: revealedBytes,
		})
		if i < pairInitMetaMaxPolls {
			if delay, ok := findPollTimerDelay(resEffs); !ok || delay != pairInitMetaPollIntervalMs {
				t.Fatalf("poll %d: delay = (%d, %v), want (%d, true)", i, delay, ok, pairInitMetaPollIntervalMs)
			}
			if activePollKindForTest(e) != PollInitiatorJoinerMeta {
				t.Fatalf("poll %d: ActivePollKind = %v, want PollInitiatorJoinerMeta", i, activePollKindForTest(e))
			}
		} else {
			if !hasPollTimerCancel(resEffs) {
				t.Fatal("expected poll timer cancel on 15th poll")
			}
			if activePollKindForTest(e) != PollNone {
				t.Fatalf("after 15th poll: ActivePollKind = %v, want PollNone", activePollKindForTest(e))
			}
			if len(obs.updates) != 1 || !obs.updates[0].MetaDone || obs.updates[0].Err != nil {
				t.Fatalf("after 15th poll: updates = %+v, want MetaDone with nil err", obs.updates)
			}
			if wf.initPairings["p-meta"] == nil {
				t.Fatal("pairing session must remain approvable after 15 metadata polls")
			}
		}
	}
}

func TestManagedRecoveryWindowPollAdaptiveIntervalsAndForeground(t *testing.T) {
	e := NewEngine(nil, 17)
	obs := &recordingPollObserver{}
	e.SetPollObserver(obs)
	wf := e.ensureWF()
	wf.pendingOp = &PersistedOperation{
		Kind:   PendingOpKindRecovery,
		Server: "https://relay.example.com",
		Recovery: &PersistedRecoveryDraft{
			RecoveryID: "rec-1",
			ReadyMs:    100_000,
			EdSlot:     "rec-ed-1",
		},
	}

	// Far from ready (>10s): 30s interval.
	effs := e.StartRecoveryWindowPoll(50_000, 100_000)
	if delay, ok := findPollTimerDelay(effs); !ok || delay != recoveryPollSlowIntervalMs {
		t.Fatalf("slow recovery delay = (%d, %v), want (%d, true)", delay, ok, recoveryPollSlowIntervalMs)
	}
	if activePollKindForTest(e) != PollRecoveryWindow {
		t.Fatalf("ActivePollKind = %v, want PollRecoveryWindow", activePollKindForTest(e))
	}

	// Background cancels timer but keeps activePoll for resume.
	bgEffs := e.HandleForeground(55_000, false)
	if !hasPollTimerCancel(bgEffs) {
		t.Fatal("background must cancel TIMER_KIND_SECURITY_POLL")
	}
	if activePollKindForTest(e) != PollRecoveryWindow {
		t.Fatalf("background must preserve activePoll kind, got %v", activePollKindForTest(e))
	}

	// Near ready (<=10s): foreground triggers immediate tick, and response schedules 2s interval.
	fgEffs := e.HandleForeground(92_000, true)
	chalRPC := findRPCEffect(fgEffs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_RECOVERY_CHALLENGE)
	if chalRPC == nil {
		t.Fatal("foreground resume must fire immediate recovery poll")
	}
	chalBytes, _ := proto.Marshal(&pb.GetRecoveryChallengeResponse{Challenge: []byte("0123456789abcdef0123456789abcdef")})
	signEffs := e.HandleRelayRpcResult(92_000, &corev1.RelayRpcResultEvent{OpId: chalRPC.OpId, ResponseBytes: chalBytes})
	var signOpID uint64
	for _, eff := range signEffs {
		if s := eff.GetSecurityKeySign(); s != nil {
			signOpID = s.OpId
		}
	}
	if signOpID == 0 {
		t.Fatal("expected SecurityKeySignEffect")
	}
	ctxEffs := e.HandleKeySigned(92_000, &corev1.SecurityKeySignedEvent{OpId: signOpID, Signature: make([]byte, 64)})
	ctxRPC := findRPCEffect(ctxEffs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_RECOVERY_CONTEXT)
	if ctxRPC == nil {
		t.Fatal("expected GET_RECOVERY_CONTEXT RPC")
	}
	pendingBytes, _ := proto.Marshal(&pb.GetRecoveryContextResponse{
		RecoveryId: "rec-1",
		Phase:      pb.GetRecoveryContextResponse_PHASE_PENDING,
		ReadyMs:    100_000,
	})
	nextEffs := e.HandleRelayRpcResult(92_000, &corev1.RelayRpcResultEvent{OpId: ctxRPC.OpId, ResponseBytes: pendingBytes})
	if delay, ok := findPollTimerDelay(nextEffs); !ok || delay != recoveryPollFastIntervalMs {
		t.Fatalf("near-ready recovery delay = (%d, %v), want (%d, true)", delay, ok, recoveryPollFastIntervalMs)
	}

	// Next tick returns READY -> stops polling and notifies observer.
	tickEffs := e.HandleSecurityPollTick(100_000)
	chalRPC2 := findRPCEffect(tickEffs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_RECOVERY_CHALLENGE)
	signEffs2 := e.HandleRelayRpcResult(100_000, &corev1.RelayRpcResultEvent{OpId: chalRPC2.OpId, ResponseBytes: chalBytes})
	signOpID = 0
	for _, eff := range signEffs2 {
		if s := eff.GetSecurityKeySign(); s != nil {
			signOpID = s.OpId
		}
	}
	ctxEffs2 := e.HandleKeySigned(100_000, &corev1.SecurityKeySignedEvent{OpId: signOpID, Signature: make([]byte, 64)})
	ctxRPC2 := findRPCEffect(ctxEffs2, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_RECOVERY_CONTEXT)
	readyBytes, _ := proto.Marshal(&pb.GetRecoveryContextResponse{
		RecoveryId: "rec-1",
		Phase:      pb.GetRecoveryContextResponse_PHASE_READY,
		ReadyMs:    100_000,
	})
	doneEffs := e.HandleRelayRpcResult(100_000, &corev1.RelayRpcResultEvent{OpId: ctxRPC2.OpId, ResponseBytes: readyBytes})
	if !hasPollTimerCancel(doneEffs) {
		t.Fatal("READY phase must cancel poll timer")
	}
	if activePollKindForTest(e) != PollNone {
		t.Fatalf("READY phase must clear activePoll, got %v", activePollKindForTest(e))
	}
	last := obs.updates[len(obs.updates)-1]
	if last.Recovery == nil || last.Recovery.RecoveryPhase != corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_READY_TO_COMPLETE {
		t.Fatalf("last update = %+v, want SECURITY_RECOVERY_PHASE_READY_TO_COMPLETE", last)
	}
}

func TestManagedDeliveryPollAutoResumesOnForeground(t *testing.T) {
	e := NewEngine(nil, 17)
	obs := &recordingPollObserver{}
	e.SetPollObserver(obs)
	wf := e.ensureWF()
	wf.foreground = false
	wf.pendingOp = &PersistedOperation{
		Kind:     PendingOpKindState,
		Action:   PendingOpActionPair,
		Server:   "https://relay.example.com",
		Delivery: []byte{1, 2, 3},
		Pair:     &PersistedOpPair{PairingID: "pair-del-1"},
	}

	effs := e.HandleForeground(1_000, true)
	if activePollKindForTest(e) != PollInitiatorDelivery {
		t.Fatalf("ActivePollKind = %v, want PollInitiatorDelivery", activePollKindForTest(e))
	}
	if delay, ok := findPollTimerDelay(effs); !ok || delay != pairInitDeliveryPollIntervalMs {
		t.Fatalf("delivery poll delay = (%d, %v), want (%d, true)", delay, ok, pairInitDeliveryPollIntervalMs)
	}

	// Clearing pendingOp completes delivery on next tick.
	wf.pendingOp = nil
	doneEffs := e.HandleSecurityPollTick(2_200)
	if !hasPollTimerCancel(doneEffs) || activePollKindForTest(e) != PollNone {
		t.Fatalf("expected delivery poll completion and cancel, kind=%v", activePollKindForTest(e))
	}
	if len(obs.updates) != 1 || !obs.updates[0].Delivered {
		t.Fatalf("updates = %+v, want Delivered=true", obs.updates)
	}
}
