package app

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/schedule"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

func setupOpaqueCacheTestCore(t *testing.T, inlineCrypto bool) (
	c *Core,
	phone, trusted, other appTestActor,
	secStore *appTestSecStore,
	dom devicesec.Hash,
	s1 devicesec.State,
	sec []byte,
	s2Raw, lease2Raw []byte,
	nowMs int64,
) {
	t.Helper()
	nowMs = 1_750_000_000_000
	phone = newAppTestActor(t, 0x11)
	trusted = newAppTestActor(t, 0x22)
	other = newAppTestActor(t, 0x33)
	recActor := newAppTestActor(t, 0x44)
	dom = devicesec.Digest("opaque-cache-domain", []byte{1})
	sec = bytes.Repeat([]byte{0x44}, 32)
	sec2 := bytes.Repeat([]byte{0x55}, 32)

	u0, err := devicesec.NewGenesis(phone.id, dom, phone.member(devicesec.Client), [32]byte(recActor.edP), sec, uint64(nowMs), appTestHPKE{})
	if err != nil {
		t.Fatalf("NewGenesis: %v", err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	gen, _ := s0.Hash()

	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{
		phone.member(devicesec.Client),
		trusted.member(devicesec.Machine),
	}, nil, sec, sec, uint64(nowMs+10), appTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatalf("NewMembershipState s1: %v", err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(phone.ed, sb1)
	s1Raw, _ := u1.MarshalBinary()
	s1, _ = devicesec.ParseState(s1Raw)
	lease1, _ := devicesec.NewLease(s1, uint64(nowMs+10))
	lb1, _ := lease1.SigningBytes()
	lease1.Signature = ed25519.Sign(phone.ed, lb1)
	lease1Raw, _ := lease1.MarshalBinary()

	u2, err := devicesec.NewMembershipState(s1, []devicesec.Member{
		phone.member(devicesec.Client),
	}, []string{trusted.id}, sec, sec2, uint64(nowMs+20), appTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatalf("NewMembershipState s2: %v", err)
	}
	sb2, _ := u2.SigningBytes()
	u2.Signature = ed25519.Sign(phone.ed, sb2)
	s2Raw, _ = u2.MarshalBinary()
	s2, _ := devicesec.ParseState(s2Raw)
	lease2, _ := devicesec.NewLease(s2, uint64(nowMs+20))
	lb2, _ := lease2.SigningBytes()
	lease2.Signature = ed25519.Sign(phone.ed, lb2)
	lease2Raw, _ = lease2.MarshalBinary()

	c = New()
	t.Cleanup(c.Close)
	c.Dispatch(&corev1.Event{
		NowMs:  nowMs,
		MonoMs: 1000,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{
				Account:      phone.id,
				DeviceId:     phone.id,
				Server:       "https://relay.example.com",
				InlineCrypto: inlineCrypto,
				Foreground:   true,
			},
		},
	})
	rec := &security.PersistedRecord{
		Server:   "https://relay.example.com",
		MemberID: phone.id,
		Member:   true,
		Pin:      devicesec.Pin{Account: phone.id, Domain: dom, Genesis: gen},
		States:   [][]byte{s0Raw, s1Raw},
		Lease:    lease1Raw,
	}
	recBytes, err := rec.Encode()
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	boot := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 10,
		MonoMs: 1010,
		Event: &corev1.Event_SecurityAction{
			SecurityAction: &corev1.SecurityWorkflowActionEvent{
				ActionId: 1,
				Action:   &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
			},
		},
	})
	var readOp uint64
	for _, eff := range boot.Effects {
		if st := eff.GetSecurityStorage(); st != nil && st.Op == corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP {
			readOp = st.OpId
		}
	}
	if readOp == 0 {
		t.Fatal("expected READ_BOOTSTRAP")
	}
	secStore = &appTestSecStore{blob: recBytes, rev: "rev-1"}
	b2 := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 11,
		MonoMs: 1011,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:        readOp,
				HasDeviceEd: true,
				DeviceEdPub: phone.edP,
				HasDeviceX:  true,
				DeviceXPub:  phone.xPub[:],
				Entries: []*corev1.SecurityStorageEntry{{
					Bucket:   corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
					Key:      "record",
					Revision: secStore.rev,
					Data:     secStore.blob,
				}},
			},
		},
	})
	pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+12, b2)
	if !c.m.sync.CanUseMachineCache(trusted.id) {
		t.Fatal("expected trusted machine to be cache-eligible after bootstrap")
	}
	return c, phone, trusted, other, secStore, dom, s1, sec, s2Raw, lease2Raw, nowMs
}

func TestOpaqueScheduleCache_CrossVerifyAndNegativeSecurityCases(t *testing.T) {
	c, _, trusted, other, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
	reqID := "req-sched-opaque-1"
	taskJSON := []byte(`{"tasks":[{"id":"t1","spec":{"title":"Morning backup"}}]}`)
	resBytes, err := (&v1.CommandResult{
		Kind: &v1.CommandResult_Schedule{
			Schedule: &v1.ScheduleResponse{ResultJson: taskJSON},
		},
	}).Marshal()
	if err != nil {
		t.Fatalf("marshal CommandResult: %v", err)
	}
	// Cross-verify: seal using daemon-format devicesec.PrepareCiphertext + AssembleCiphertext.
	sealedEnv := sealAppWireObj(t, trusted, dom, s1, sec, resBytes, devicesec.Result, trusted.id, "", reqID, 0)
	sealedBytes, err := sealedEnv.Marshal()
	if err != nil {
		t.Fatalf("marshal Envelope: %v", err)
	}
	validBlob := encodeScheduleCacheBlob(reqID, sealedBytes)

	// Negative 1: Tampered ciphertext must be rejected.
	tamperedEnv := &v1.Envelope{
		SecurityHeader: sealedEnv.SecurityHeader,
		Nonce:          sealedEnv.Nonce,
		Ciphertext:     append([]byte(nil), sealedEnv.Ciphertext...),
		Signature:      sealedEnv.Signature,
	}
	tamperedEnv.Ciphertext[0] ^= 0xff
	tamperedBytes, _ := tamperedEnv.Marshal()
	tamperedBlob := encodeScheduleCacheBlob(reqID, tamperedBytes)
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 100, MonoMs: 1100,
		Event: &corev1.Event_ScheduleCacheLoaded{
			ScheduleCacheLoaded: &corev1.ScheduleCacheLoadedEvent{OpId: 1, MachineId: trusted.id, RawBlob: tamperedBlob},
		},
	})
	if len(ensureSchedState(c.m.state).Machines[trusted.id].Tasks) != 0 {
		t.Fatal("tampered schedule cache ciphertext must be rejected")
	}

	// Negative 2: Wrong request_id binding must be rejected.
	wrongReqBlob := encodeScheduleCacheBlob("wrong-req-id", sealedBytes)
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 101, MonoMs: 1101,
		Event: &corev1.Event_ScheduleCacheLoaded{
			ScheduleCacheLoaded: &corev1.ScheduleCacheLoadedEvent{OpId: 2, MachineId: trusted.id, RawBlob: wrongReqBlob},
		},
	})
	if len(ensureSchedState(c.m.state).Machines[trusted.id].Tasks) != 0 {
		t.Fatal("wrong request_id schedule cache blob must be rejected")
	}

	// Negative 3: Wrong Purpose (MachineMeta instead of Result) signed by trusted machine must be rejected.
	wrongPurposeEnv := sealAppWireObj(t, trusted, dom, s1, sec, resBytes, devicesec.MachineMeta, trusted.id, "", "", 1)
	wrongPurposeBytes, _ := wrongPurposeEnv.Marshal()
	wrongPurposeBlob := encodeScheduleCacheBlob(reqID, wrongPurposeBytes)
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 102, MonoMs: 1102,
		Event: &corev1.Event_ScheduleCacheLoaded{
			ScheduleCacheLoaded: &corev1.ScheduleCacheLoadedEvent{OpId: 3, MachineId: trusted.id, RawBlob: wrongPurposeBlob},
		},
	})
	if len(ensureSchedState(c.m.state).Machines[trusted.id].Tasks) != 0 {
		t.Fatal("wrong purpose schedule cache blob must be rejected")
	}

	// Negative 4: Untrusted machine BEFORE open must reject and emit ScheduleDeleteCacheEffect.
	untrustedBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 103, MonoMs: 1103,
		Event: &corev1.Event_ScheduleCacheLoaded{
			ScheduleCacheLoaded: &corev1.ScheduleCacheLoadedEvent{OpId: 4, MachineId: other.id, RawBlob: validBlob},
		},
	})
	var deletedUntrusted bool
	for _, eff := range untrustedBatch.Effects {
		if del := eff.GetScheduleDeleteCache(); del != nil && del.MachineId == other.id {
			deletedUntrusted = true
		}
	}
	if !deletedUntrusted {
		t.Fatal("expected ScheduleDeleteCacheEffect for untrusted machine")
	}

	// Positive: valid opaque blob decrypts and populates schedule tasks.
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 104, MonoMs: 1104,
		Event: &corev1.Event_ScheduleCacheLoaded{
			ScheduleCacheLoaded: &corev1.ScheduleCacheLoadedEvent{OpId: 5, MachineId: trusted.id, RawBlob: validBlob},
		},
	})
	snap := ensureSchedState(c.m.state).Machines[trusted.id]
	if !snap.Cached || len(snap.Tasks) != 1 || snap.Tasks[0].ID != "t1" || snap.Tasks[0].Spec.Title != "Morning backup" {
		t.Fatalf("expected decrypted schedule task t1, got cached=%v tasks=%+v", snap.Cached, snap.Tasks)
	}
	if wantTasks, err := schedule.DecodeTaskList(taskJSON); err != nil || len(wantTasks) != len(snap.Tasks) {
		t.Fatalf("cross-check schedule decode failed: %v", err)
	}
}

func TestOpaqueScheduleCache_AsyncWebCryptoBatchAndRevocationMidFlight(t *testing.T) {
	// inlineCrypto=false + >16 KiB payload forces async SecurityCryptoBatchEffect on Web.
	c, phone, trusted, _, secStore, dom, s1, sec, s2Raw, lease2Raw, nowMs := setupOpaqueCacheTestCore(t, false)
	reqID := "req-sched-large-1"
	padding := strings.Repeat("x", 20*1024)
	taskJSON := []byte(`{"tasks":[{"id":"big-1","spec":{"title":"Big task","message":"` + padding + `"}}]}`)
	resBytes, err := (&v1.CommandResult{
		Kind: &v1.CommandResult_Schedule{
			Schedule: &v1.ScheduleResponse{ResultJson: taskJSON},
		},
	}).Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sealedEnv := sealAppWireObj(t, trusted, dom, s1, sec, resBytes, devicesec.Result, trusted.id, "", reqID, 0)
	sealedBytes, _ := sealedEnv.Marshal()
	blob := encodeScheduleCacheBlob(reqID, sealedBytes)

	b := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 100, MonoMs: 1100,
		Event: &corev1.Event_ScheduleCacheLoaded{
			ScheduleCacheLoaded: &corev1.ScheduleCacheLoadedEvent{OpId: 10, MachineId: trusted.id, RawBlob: blob},
		},
	})
	var cryptoBatch *corev1.SecurityCryptoBatchEffect
	for _, eff := range b.Effects {
		if cb := eff.GetSecurityCryptoBatch(); cb != nil {
			cryptoBatch = cb
		}
	}
	if cryptoBatch == nil || len(cryptoBatch.Items) == 0 {
		t.Fatalf("expected async SecurityCryptoBatchEffect for >16 KiB schedule cache blob")
	}

	// Revoke machine trust mid-flight by advancing membership state to epoch 2 (which removes trusted).
	s2RespBytes, err := proto.Marshal(&stdv1.GetSecurityStatesResponse{
		HeadEpoch: 2,
		States:    []*stdv1.SecurityState{{State: s2Raw}},
		Lease:     lease2Raw,
	})
	if err != nil {
		t.Fatalf("marshal GetSecurityStatesResponse: %v", err)
	}
	syncBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 105, MonoMs: 1105,
		Event: &corev1.Event_SecurityAction{
			SecurityAction: &corev1.SecurityWorkflowActionEvent{
				ActionId: 2,
				Action: &corev1.SecurityWorkflowActionEvent_SyncNow{
					SyncNow: &corev1.SecuritySyncNowAction{AuthToken: "tok"},
				},
			},
		},
	})
	for _, eff := range pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+105, syncBatch) {
		if rpc := eff.GetSecurityRpc(); rpc != nil {
			r2 := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 106, MonoMs: 1106,
				Event: &corev1.Event_SecurityRpcResult{
					SecurityRpcResult: &corev1.SecurityRpcResultEvent{OpId: rpc.OpId, ResponseBytes: s2RespBytes},
				},
			})
			pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+106, r2)
		}
	}
	if c.m.sync.CanUseMachineCache(trusted.id) {
		t.Fatal("expected trusted machine to be revoked at epoch 2")
	}

	doneBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 110, MonoMs: 1110,
		Event: &corev1.Event_SecurityCryptoBatchDone{
			SecurityCryptoBatchDone: &corev1.SecurityCryptoBatchDoneEvent{
				OpId: cryptoBatch.OpId,
				Results: []*corev1.SecurityCryptoBatchResult{{
					Ok: true,
				}},
			},
		},
	})
	if len(ensureSchedState(c.m.state).Machines[trusted.id].Tasks) != 0 {
		t.Fatal("revoked machine must not apply schedule tasks after async decrypt")
	}
	var deletedAfterRevoke bool
	for _, eff := range doneBatch.Effects {
		if del := eff.GetScheduleDeleteCache(); del != nil && del.MachineId == trusted.id {
			deletedAfterRevoke = true
		}
	}
	if !deletedAfterRevoke {
		t.Fatal("expected ScheduleDeleteCacheEffect when machine revoked mid-flight")
	}
}

func TestOpaqueUsageCache_RoundTripAndUntrustedRejection(t *testing.T) {
	c, _, trusted, other, _, _, _, _, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
	orig := &v1.UsageStatsResponse{
		Preset:        v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
		GeneratedAtMs: nowMs,
		StartMs:       nowMs - 7*24*3600_000,
		EndMs:         nowMs,
	}
	rawSnap := orig.AppendMarshal(nil)

	// Corrupt bytes must be ignored safely.
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 50, MonoMs: 1050,
		Event: &corev1.Event_UsageCacheLoaded{
			UsageCacheLoaded: &corev1.UsageCacheLoadedEvent{
				MachineId:   trusted.id,
				Preset:      v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
				RawSnapshot: []byte{0xff, 0xff, 0xff},
			},
		},
	})
	key := usageCacheKey(trusted.id, v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS, 0, 0)
	if e := c.m.state.UsageSnapshots[key]; e != nil && e.Cached != nil {
		t.Fatal("corrupt usage cache bytes must not populate entry")
	}

	// Valid opaque bytes hydrate Cached snapshot cleanly.
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 51, MonoMs: 1051,
		Event: &corev1.Event_UsageCacheLoaded{
			UsageCacheLoaded: &corev1.UsageCacheLoadedEvent{
				MachineId:   trusted.id,
				Preset:      v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
				RawSnapshot: rawSnap,
			},
		},
	})
	entry := c.m.state.UsageSnapshots[key]
	if entry == nil || entry.Cached == nil || entry.Cached.GeneratedAtMs != nowMs || entry.Cached.StartMs != orig.StartMs {
		t.Fatalf("expected hydrated opaque usage snapshot matching orig, got %+v", entry)
	}

	// Untrusted machine must reject and emit UsageCacheRequestEffect(delete_cache=true).
	b := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 52, MonoMs: 1052,
		Event: &corev1.Event_UsageCacheLoaded{
			UsageCacheLoaded: &corev1.UsageCacheLoadedEvent{
				MachineId:   other.id,
				Preset:      v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
				RawSnapshot: rawSnap,
			},
		},
	})
	var deleted bool
	for _, eff := range b.Effects {
		if r := eff.GetUsageCacheRequest(); r != nil && r.MachineId == other.id && r.DeleteCache {
			deleted = true
		}
	}
	if !deleted {
		t.Fatal("expected UsageCacheRequestEffect(delete_cache=true) for untrusted machine")
	}
}

func TestOpaqueCache_SingleFormatByRelayPipeMode(t *testing.T) {
	for _, pipeMode := range []bool{false, true} {
		name := "legacy"
		if pipeMode {
			name = "relayPipe"
		}
		t.Run(name, func(t *testing.T) {
			c, _, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
			c.Dispatch(&corev1.Event{
				NowMs: nowMs + 1, MonoMs: 1001,
				Event: &corev1.Event_RelayPipeConfig{RelayPipeConfig: &corev1.RelayPipeConfigEvent{Enabled: pipeMode}},
			})

			// 1. Schedule cache write: check single-format contract via real settle handler.
			snapBytes := []byte(`{"tasks":[{"id":"t1","spec":{"title":"Check"}}]}`)
			schedRes := &v1.CommandResult{
				Kind: &v1.CommandResult_Schedule{Schedule: &v1.ScheduleResponse{ResultJson: snapBytes}},
			}
			schedPlain, _ := schedRes.Marshal()
			reqID := "req-sched-single"
			env := sealAppWireObj(t, trusted, dom, s1, sec, schedPlain, devicesec.Result, trusted.id, "", reqID, 0)
			c.m.pendingCmdMeta[reqID] = cmdMeta{kind: cmdKindScheduleList, machineID: trusted.id}
			var schedEffs []*corev1.Effect
			c.processSchedulesSyncStep(sync.StepResult{
				Settled: []sync.SettledCommand{{
					RequestID:    reqID,
					Category:     outcome.Executed,
					Result:       schedRes,
					SealedResult: env,
				}},
			}, &schedEffs)
			var wc *corev1.ScheduleWriteCacheEffect
			for _, eff := range schedEffs {
				if w := eff.GetScheduleWriteCache(); w != nil {
					wc = w
				}
			}
			if wc == nil {
				t.Fatal("expected ScheduleWriteCacheEffect")
			}
			if pipeMode {
				if len(wc.Blob) == 0 {
					t.Fatal("relayPipe mode must populate ScheduleWriteCacheEffect.Blob")
				}
				if wc.RequestId != "" || len(wc.SealedResultBytes) != 0 {
					t.Fatalf("relayPipe mode must NOT duplicate legacy fields: reqId=%q sealedLen=%d", wc.RequestId, len(wc.SealedResultBytes))
				}
			} else {
				if len(wc.Blob) != 0 {
					t.Fatal("legacy mode must NOT populate ScheduleWriteCacheEffect.Blob")
				}
				if wc.RequestId != reqID || len(wc.SealedResultBytes) == 0 {
					t.Fatalf("legacy mode must populate legacy fields: reqId=%q sealedLen=%d", wc.RequestId, len(wc.SealedResultBytes))
				}
			}

			// 2. Usage cache write: check single-format contract via real settle handler.
			usageSnap := &v1.UsageStatsResponse{
				Preset:        v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
				GeneratedAtMs: nowMs + 20,
				StartMs:       nowMs - 7*24*3600_000,
				EndMs:         nowMs + 20,
			}
			var dirty sync.DirtyFlags
			uEffs := c.onUsageStatsSettled(sync.SettledCommand{
				RequestID: "req-usage-single",
				Category:  outcome.Executed,
				Result:    &v1.CommandResult{Kind: &v1.CommandResult_UsageStats{UsageStats: usageSnap}},
			}, cmdMeta{
				kind:      cmdKindGetUsageStats,
				machineID: trusted.id,
				usageKey:  screens.UsageCacheKey{MachineID: trusted.id, Preset: v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS},
			}, &dirty)
			var uw *corev1.UsageCacheWriteEffect
			for _, eff := range uEffs {
				if w := eff.GetUsageCacheWrite(); w != nil {
					uw = w
				}
			}
			if uw == nil {
				t.Fatal("expected UsageCacheWriteEffect")
			}
			if pipeMode {
				if len(uw.RawSnapshot) == 0 {
					t.Fatal("relayPipe mode must populate UsageCacheWriteEffect.RawSnapshot")
				}
				if uw.Snapshot != nil {
					t.Fatal("relayPipe mode must NOT duplicate legacy UsageCacheWriteEffect.Snapshot")
				}
			} else {
				if len(uw.RawSnapshot) != 0 {
					t.Fatal("legacy mode must NOT populate UsageCacheWriteEffect.RawSnapshot")
				}
				if uw.Snapshot == nil {
					t.Fatal("legacy mode must populate legacy UsageCacheWriteEffect.Snapshot")
				}
			}
		})
	}
}

func TestSessionImageErrorMappedToI18nKey(t *testing.T) {
	cases := []struct {
		code    string
		wantKey string
	}{
		{"not_found", text.KeyCommonCannotFindThisSession},
		{"timeout", text.KeyErrorsConnectionTimedOut},
		{"deadline_exceeded", text.KeyErrorsConnectionTimedOut},
		{"unavailable", text.KeyConnectionNoServer},
		{"network", text.KeyConnectionNoServer},
		{"unauthenticated", text.KeyErrorsTheServerDoesNotRecognizeThisDeviceItMay},
		{"canceled", text.KeyErrorsCommandFailed},
	}
	for _, tc := range cases {
		eff := &corev1.Effect{
			Effect: &corev1.Effect_SessionImageLoaded{
				SessionImageLoaded: &corev1.SessionImageLoadedEffect{
					OpId:      1,
					Status:    corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_ERROR,
					ErrorCode: tc.code,
				},
			},
		}
		decorateSessionImageEffects([]*corev1.Effect{eff})
		got := eff.GetSessionImageLoaded().GetErrorText()
		if got == nil || got.Key != tc.wantKey || got.Literal != "" {
			t.Errorf("ErrorCode=%q: got ErrorText=%+v, want Key=%q with empty Literal", tc.code, got, tc.wantKey)
		}
	}
}
