package sync

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestPruneUntrustedCaches(t *testing.T) {
	eng := NewEngine()
	eng.machines["m-trusted"] = &MachineEntry{Machine: lists.Machine{ID: "m-trusted", Online: true}}
	eng.machines["m-untrusted"] = &MachineEntry{Machine: lists.Machine{ID: "m-untrusted", Online: true}}
	eng.security.Loaded = true
	eng.security.IsMember = true
	eng.security.TrustedMachines = map[string]bool{"m-trusted": true}

	ctx := &StepContext{}
	eng.pruneUntrustedCaches(ctx)

	var schedDel, usageDel []string
	for _, eff := range ctx.Raw {
		if sd := eff.GetScheduleDeleteCache(); sd != nil {
			schedDel = append(schedDel, sd.MachineId)
		}
		if uc := eff.GetUsageCacheRequest(); uc != nil && uc.DeleteCache {
			usageDel = append(usageDel, uc.MachineId)
		}
	}
	if len(schedDel) != 1 || schedDel[0] != "m-untrusted" {
		t.Fatalf("expected schedule delete for m-untrusted only, got %v", schedDel)
	}
	if len(usageDel) != 1 || usageDel[0] != "m-untrusted" {
		t.Fatalf("expected usage delete for m-untrusted only, got %v", usageDel)
	}

	// Second call is idempotent (no duplicate deletes).
	ctx2 := &StepContext{}
	eng.pruneUntrustedCaches(ctx2)
	if len(ctx2.Raw) != 0 {
		t.Fatalf("expected no duplicate prune effects, got %d", len(ctx2.Raw))
	}

	// Removal prunes remaining trusted machines too.
	eng.removed = true
	ctx3 := &StepContext{}
	eng.pruneUntrustedCaches(ctx3)
	schedDel = nil
	usageDel = nil
	for _, eff := range ctx3.Raw {
		if sd := eff.GetScheduleDeleteCache(); sd != nil {
			schedDel = append(schedDel, sd.MachineId)
		}
		if uc := eff.GetUsageCacheRequest(); uc != nil && uc.DeleteCache {
			usageDel = append(usageDel, uc.MachineId)
		}
	}
	if len(schedDel) != 1 || schedDel[0] != "m-trusted" || len(usageDel) != 1 || usageDel[0] != "m-trusted" {
		t.Fatalf("expected removal to prune m-trusted, got sched=%v usage=%v", schedDel, usageDel)
	}
}

func TestMaybeDescribeSelfGuards(t *testing.T) {
	eng := NewEngine()
	// Complete workflow bootstrap so HasWorkflowBootstrapped() is true.
	eng.HandleEvent(&corev1.Event{
		NowMs: 1_000,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{
				InlineCrypto:      true,
				DeviceId:          "self-1",
				Platform:          "web",
				DeviceDescription: "Safari on macOS",
			},
		},
	})
	_, res := eng.HandleEvent(&corev1.Event{
		NowMs: 1_010,
		Event: &corev1.Event_SecurityAction{
			SecurityAction: &corev1.SecurityWorkflowActionEvent{
				ActionId: 1,
				Action:   &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
			},
		},
	})
	var storageOp uint64
	for _, eff := range res.Effects {
		if st := eff.GetSecurityStorage(); st != nil {
			storageOp = st.OpId
		}
	}
	eng.HandleEvent(&corev1.Event{
		NowMs: 1_020,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: storageOp},
		},
	})

	eng.running = true
	eng.w.World.Connection = lists.ConnConnected
	eng.security.Loaded = true
	eng.security.IsMember = true
	eng.security.SelfDeviceID = "self-1"
	eng.security.LeaseEndMs = 9_999

	// When device already matches platform & has description, no attempt is triggered.
	eng.devices["self-1"] = &DeviceEntry{
		ID:      "self-1",
		MetaRev: 1,
		Meta:    &v1.DeviceMeta{Platform: "web", Description: "Safari on macOS"},
	}
	ctx := &StepContext{}
	eng.maybeDescribeSelf(ctx)
	if eng.selfDescribeActionID != 0 || eng.selfDescribeAttempted {
		t.Fatal("expected no self-describe when meta already matches")
	}

	// When platform/description is missing, attempt is recorded and internal action is dispatched.
	eng.devices["self-1"] = &DeviceEntry{
		ID:      "self-1",
		MetaRev: 1,
		Meta:    &v1.DeviceMeta{},
	}
	ctx2 := &StepContext{}
	eng.maybeDescribeSelf(ctx2)
	if !eng.selfDescribeAttempted {
		t.Fatal("expected self-describe attempt when meta is empty")
	}
	// Internal action result must not leak a SecurityWorkflowResultEffect to the shell.
	for _, eff := range ctx2.Raw {
		if wr := eff.GetSecurityWorkflowResult(); wr != nil {
			t.Fatalf("internal self-describe must not leak SecurityWorkflowResultEffect: %+v", wr)
		}
	}
	// Re-calling at the same MetaRev/LeaseEndMs does not loop.
	prevAction := eng.selfDescribeActionID
	eng.selfDescribeActionID = 0
	ctx3 := &StepContext{}
	eng.maybeDescribeSelf(ctx3)
	if eng.selfDescribeActionID != 0 {
		t.Fatalf("expected self-describe not to retry at same rev/lease (prev=%d, new=%d)", prevAction, eng.selfDescribeActionID)
	}

	// Reconnecting (StreamReady) resets selfDescribeAttempted so a prior failure retries on reconnect.
	eng.legID = 7
	eng.running = true
	eng.selfDescribeAttemptedRev = 999
	ctx4 := &StepContext{}
	eng.handleStreamReady(ctx4, &corev1.StreamReadyEvent{LegId: 7, ServerTimeMs: 2_000})
	if !eng.selfDescribeAttempted || eng.selfDescribeAttemptedRev != 1 {
		t.Fatalf("expected self-describe to retry on reconnect after prior failure (attempted=%v, rev=%d)", eng.selfDescribeAttempted, eng.selfDescribeAttemptedRev)
	}
}

func TestPruneUntrustedCachesEmptyTrustedMapPrunesAll(t *testing.T) {
	eng := NewEngine()
	eng.machines["m1"] = &MachineEntry{Machine: lists.Machine{ID: "m1", Online: true}}
	eng.machines["m2"] = &MachineEntry{Machine: lists.Machine{ID: "m2", Online: true}}
	eng.security.Loaded = true
	eng.security.IsMember = true
	eng.security.TrustedMachines = map[string]bool{}

	ctx := &StepContext{}
	eng.pruneUntrustedCaches(ctx)
	var schedDel []string
	for _, eff := range ctx.Raw {
		if sd := eff.GetScheduleDeleteCache(); sd != nil {
			schedDel = append(schedDel, sd.MachineId)
		}
	}
	if len(schedDel) != 2 || schedDel[0] != "m1" || schedDel[1] != "m2" {
		t.Fatalf("expected all machines pruned when TrustedMachines is empty, got %v", schedDel)
	}
}
