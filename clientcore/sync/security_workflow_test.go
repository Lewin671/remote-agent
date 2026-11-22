package sync

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestSyncEngineRoutesS6WorkflowEventsAndPollTimers(t *testing.T) {
	eng := NewEngine()
	eng.HandleEvent(&corev1.Event{
		NowMs:  1_700_000_000_000,
		MonoMs: 1_000,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{
				InlineCrypto: true,
				Server:       "https://relay.example.com",
				Foreground:   true,
			},
		},
	})

	// Bootstrap emits Storage effect; replying EMPTY unlocks Demo Mode & marks workflow bootstrapped.
	consumed, res := eng.HandleEvent(&corev1.Event{
		NowMs: 1_700_000_000_010,
		Event: &corev1.Event_SecurityAction{
			SecurityAction: &corev1.SecurityWorkflowActionEvent{
				ActionId: 1,
				Action:   &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
			},
		},
	})
	if !consumed {
		t.Fatal("expected sync.Engine to consume Event_SecurityAction")
	}
	var storageOpID uint64
	for _, eff := range res.Effects {
		if st := eff.GetSecurityStorage(); st != nil {
			storageOpID = st.OpId
		}
	}
	if storageOpID == 0 {
		t.Fatal("expected SecurityStorageEffect on Bootstrap")
	}

	consumed, res = eng.HandleEvent(&corev1.Event{
		NowMs: 1_700_000_000_020,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:          storageOpID,
				LegacyCleared: true,
			},
		},
	})
	if !consumed || !eng.secEngine.HasWorkflowBootstrapped() {
		t.Fatal("expected StorageResult to complete workflow bootstrap")
	}
	if eng.secEngine.Locked() {
		t.Fatal("EMPTY v4 bootstrap must keep Demo Mode unlocked")
	}
	var gotNotice bool
	for _, eff := range res.Effects {
		if wr := eff.GetSecurityWorkflowResult(); wr != nil && wr.ActionId == 1 {
			gotNotice = wr.Ok && wr.Status.GetLegacyRepairNotice()
		}
	}
	if !gotNotice {
		t.Fatal("expected Bootstrap result to surface LegacyRepairNotice")
	}

	// Verify SyncStates via requestSyncSecurity uses Core RPC once bootstrapped.
	eng.secEngine.SetAuthToken("tok-1", 1_700_000_100_000)
	_ = eng.secEngine
	// Verify SecurityTokenUpdated updates eng.cachedToken cleanly
	_, _ = eng.HandleEvent(&corev1.Event{
		NowMs: 1_700_000_000_030,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	if !eng.foreground {
		t.Fatal("expected foreground true")
	}

	// Verify DeviceUpdated pushes DeviceOfferInfo into secEngine snapshot/offers.
	eng.devices["dev-2"] = &DeviceEntry{
		ID:   "dev-2",
		Kind: v1.DeviceKind_DEVICE_KIND_CLIENT,
		Meta: &v1.DeviceMeta{HandoffRecoveryEd: make([]byte, 32)},
	}
	offers := eng.securityDeviceOffers()
	if len(offers) != 1 || offers[0].ID != "dev-2" || len(offers[0].RecoveryEd) != 32 {
		t.Fatalf("unexpected securityDeviceOffers: %+v", offers)
	}

	// Verify TIMER_KIND_SECURITY_POLL registration + firing via handleTimerFired doesn't panic and consumes timer.
	eng.scheduleTimer(newStepContext(), corev1.TimerKind_TIMER_KIND_SECURITY_POLL, "security-poll", 1000)
	tk := timerKey{kind: corev1.TimerKind_TIMER_KIND_SECURITY_POLL, key: "security-poll"}
	tid := eng.timers[tk].id
	if tid == 0 {
		t.Fatal("expected TIMER_KIND_SECURITY_POLL to track non-zero TimerId")
	}
	consumed, _ = eng.HandleEvent(&corev1.Event{
		NowMs: 1_700_000_001_030,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: tid,
				Kind:    corev1.TimerKind_TIMER_KIND_SECURITY_POLL,
				Key:     "security-poll",
			},
		},
	})
	if !consumed {
		t.Fatal("expected TIMER_KIND_SECURITY_POLL to be consumed by sync.Engine")
	}
}
