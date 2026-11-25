package app

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/text"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

func mustViewAppRoot(t *testing.T, c *Core) *corev1.AppRootViewModel {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_AppRoot{AppRoot: &corev1.AppRootScreenRequest{}}})
	if err != nil {
		t.Fatalf("View(AppRoot): %v", err)
	}
	return vm.GetAppRoot()
}

func mustViewOnboarding(t *testing.T, c *Core) *corev1.OnboardingViewModel {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Onboarding{Onboarding: &corev1.OnboardingScreenRequest{}}})
	if err != nil {
		t.Fatalf("View(Onboarding): %v", err)
	}
	return vm.GetOnboarding()
}

func mustViewAddDevice(t *testing.T, c *Core) *corev1.AddDeviceViewModel {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_AddDevice{AddDevice: &corev1.AddDeviceScreenRequest{}}})
	if err != nil {
		t.Fatalf("View(AddDevice): %v", err)
	}
	return vm.GetAddDevice()
}

func mustViewAccountSecurity(t *testing.T, c *Core) *corev1.AccountSecurityViewModel {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_AccountSecurity{AccountSecurity: &corev1.AccountSecurityScreenRequest{}}})
	if err != nil {
		t.Fatalf("View(AccountSecurity): %v", err)
	}
	return vm.GetAccountSecurity()
}

func mustViewHandoff(t *testing.T, c *Core) *corev1.HandoffViewModel {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Handoff{Handoff: &corev1.HandoffScreenRequest{}}})
	if err != nil {
		t.Fatalf("View(Handoff): %v", err)
	}
	return vm.GetHandoff()
}

func mustViewExitConfirm(t *testing.T, c *Core) *corev1.ExitConfirmView {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_ExitConfirm{ExitConfirm: &corev1.ExitConfirmScreenRequest{}}})
	if err != nil {
		t.Fatalf("View(ExitConfirm): %v", err)
	}
	return vm.GetExitConfirm()
}

func findStorageEffect(batch *corev1.EffectBatch, op corev1.SecurityStorageOp) *corev1.SecurityStorageEffect {
	if batch == nil {
		return nil
	}
	for _, eff := range batch.Effects {
		if st := eff.GetSecurityStorage(); st != nil && st.Op == op {
			return st
		}
	}
	return nil
}

func findPersistEffect(batch *corev1.EffectBatch) *corev1.RequestStoragePersistEffect {
	if batch == nil {
		return nil
	}
	for _, eff := range batch.Effects {
		if p := eff.GetRequestStoragePersist(); p != nil {
			return p
		}
	}
	return nil
}

func findUnregisterPushEffect(batch *corev1.EffectBatch) *corev1.UnregisterPushOnExitEffect {
	if batch == nil {
		return nil
	}
	for _, eff := range batch.Effects {
		if u := eff.GetUnregisterPushOnExit(); u != nil {
			return u
		}
	}
	return nil
}

func findClearCachesEffect(batch *corev1.EffectBatch) *corev1.ClearAccountCachesEffect {
	if batch == nil {
		return nil
	}
	for _, eff := range batch.Effects {
		if cl := eff.GetClearAccountCaches(); cl != nil {
			return cl
		}
	}
	return nil
}

func findRuntimeChangedEffect(batch *corev1.EffectBatch) *corev1.AccountRuntimeChangedEffect {
	if batch == nil {
		return nil
	}
	for _, eff := range batch.Effects {
		if rc := eff.GetAccountRuntimeChanged(); rc != nil {
			return rc
		}
	}
	return nil
}

func findSecurityRPCEffect(batch *corev1.EffectBatch, method corev1.SecurityRpcMethod) *corev1.SecurityRpcEffect {
	if batch == nil {
		return nil
	}
	for _, eff := range batch.Effects {
		if r := eff.GetSecurityRpc(); r != nil && r.Method == method {
			return r
		}
	}
	return nil
}

func makeRelayDetailBytes(reason pb.RelayErrorReason) []*corev1.RpcErrorDetailBytes {
	raw, err := proto.Marshal(&pb.RelayErrorDetail{Reason: reason})
	if err != nil {
		panic(err)
	}
	return []*corev1.RpcErrorDetailBytes{{
		TypeName: "remoteagent.v1.RelayErrorDetail",
		Value:    raw,
	}}
}

func TestClientInitBootstrapStages(t *testing.T) {
	t.Run("EmptyStorageLandsInOnboardingEntry", func(t *testing.T) {
		c := New()
		batch := c.Dispatch(&corev1.Event{
			NowMs: 1000,
			Event: &corev1.Event_ClientInit{
				ClientInit: &corev1.ClientInitEvent{
					UiLocale:              "zh-CN",
					Platform:              "web",
					DeviceDescription:     "Chrome",
					Foreground:            true,
					NetworkAvailable:      true,
					DefaultServer:         "https://ra.example.com",
					RequestStoragePersist: true,
				},
			},
		})
		if got := mustViewAppRoot(t, c).Stage; got != corev1.AppRootStage_APP_ROOT_STAGE_LOADING {
			t.Fatalf("stage before storage reply = %v, want LOADING", got)
		}
		bootEff := findStorageEffect(batch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
		if bootEff == nil {
			t.Fatal("expected READ_BOOTSTRAP storage effect")
		}
		resBatch := c.Dispatch(&corev1.Event{
			NowMs: 1050,
			Event: &corev1.Event_SecurityStorageResult{
				SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: bootEff.OpId},
			},
		})
		// Internal workflow result must NOT leak to shell.
		for _, eff := range resBatch.Effects {
			if eff.GetSecurityWorkflowResult() != nil {
				t.Fatalf("internal SecurityWorkflowResult leaked to shell: %+v", eff.GetSecurityWorkflowResult())
			}
		}
		root := mustViewAppRoot(t, c)
		if root.Stage != corev1.AppRootStage_APP_ROOT_STAGE_ONBOARDING {
			t.Fatalf("stage = %v, want ONBOARDING", root.Stage)
		}
		ob := mustViewOnboarding(t, c)
		if ob.Mode != corev1.OnboardingMode_ONBOARDING_MODE_ENTRY {
			t.Fatalf("mode = %v, want ENTRY", ob.Mode)
		}
		if ob.Create.ServerInput != "https://ra.example.com" || ob.Join.ServerInput != "https://ra.example.com" {
			t.Fatalf("default server not prefilled: create=%q join=%q", ob.Create.ServerInput, ob.Join.ServerInput)
		}
	})

	t.Run("StorageReadErrorSetsCanResetFalse", func(t *testing.T) {
		c := New()
		batch := c.Dispatch(&corev1.Event{
			NowMs: 1000,
			Event: &corev1.Event_ClientInit{
				ClientInit: &corev1.ClientInitEvent{Platform: "android", Foreground: true, NetworkAvailable: true},
			},
		})
		bootEff := findStorageEffect(batch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
		c.Dispatch(&corev1.Event{
			NowMs: 1050,
			Event: &corev1.Event_SecurityStorageResult{
				SecurityStorageResult: &corev1.SecurityStorageResultEvent{
					OpId:  bootEff.OpId,
					Error: "keystore locked",
				},
			},
		})
		root := mustViewAppRoot(t, c)
		if root.Stage != corev1.AppRootStage_APP_ROOT_STAGE_BOOTSTRAP_ERROR {
			t.Fatalf("stage = %v, want BOOTSTRAP_ERROR", root.Stage)
		}
		if root.BootstrapError == nil || !root.BootstrapError.CanRetry || root.BootstrapError.CanReset {
			t.Fatalf("storage error must set can_retry=true, can_reset=false; got %+v", root.BootstrapError)
		}
	})

	t.Run("CorruptRecordAllowsReset", func(t *testing.T) {
		c := New()
		batch := c.Dispatch(&corev1.Event{
			NowMs: 1000,
			Event: &corev1.Event_ClientInit{
				ClientInit: &corev1.ClientInitEvent{Platform: "web", Foreground: true, NetworkAvailable: true},
			},
		})
		bootEff := findStorageEffect(batch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
		c.Dispatch(&corev1.Event{
			NowMs: 1050,
			Event: &corev1.Event_SecurityStorageResult{
				SecurityStorageResult: &corev1.SecurityStorageResultEvent{
					OpId: bootEff.OpId,
					Entries: []*corev1.SecurityStorageEntry{{
						Bucket:   corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
						Key:      "record",
						Revision: "1",
						Data:     []byte("corrupt-bytes"),
					}},
				},
			},
		})
		root := mustViewAppRoot(t, c)
		if root.Stage != corev1.AppRootStage_APP_ROOT_STAGE_BOOTSTRAP_ERROR {
			t.Fatalf("stage = %v, want BOOTSTRAP_ERROR", root.Stage)
		}
		if root.BootstrapError == nil || !root.BootstrapError.CanReset {
			t.Fatalf("corrupt record must allow reset; got %+v", root.BootstrapError)
		}
	})

	t.Run("InitialPairURLParsesAndAutoSubmits", func(t *testing.T) {
		c := New()
		var commit [32]byte
		commit[0] = 0xab
		pairURL := security.FormatPairLink("https://remote-agent.top", "https://ra.example.com", "12345678", commit)
		batch := c.Dispatch(&corev1.Event{
			NowMs: 1000,
			Event: &corev1.Event_ClientInit{
				ClientInit: &corev1.ClientInitEvent{
					Platform:         "web",
					Foreground:       true,
					NetworkAvailable: true,
					InitialUrl:       pairURL,
				},
			},
		})
		bootEff := findStorageEffect(batch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
		resBatch := c.Dispatch(&corev1.Event{
			NowMs: 1050,
			Event: &corev1.Event_SecurityStorageResult{
				SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: bootEff.OpId},
			},
		})
		ob := mustViewOnboarding(t, c)
		if ob.Mode != corev1.OnboardingMode_ONBOARDING_MODE_JOIN {
			t.Fatalf("mode = %v, want JOIN", ob.Mode)
		}
		if ob.Join.FormattedCode != "1234 5678" || ob.Join.ServerInput != "https://ra.example.com" {
			t.Fatalf("parsed join fields = (%q, %q)", ob.Join.FormattedCode, ob.Join.ServerInput)
		}
		if ob.Join.Step != corev1.JoinPairingStep_JOIN_PAIRING_STEP_WORKING {
			t.Fatalf("step = %v, want WORKING", ob.Join.Step)
		}
		// Provide entropy and answer the 3 KeyGenerate effects (Ed, X, EphemX) with real keys before PEEK_PAIRING.
		entBytes := make([]byte, 256)
		_, _ = rand.Read(entBytes)
		curBatch := c.Dispatch(&corev1.Event{
			NowMs: 1060,
			Event: &corev1.Event_EntropyProvided{
				EntropyProvided: &corev1.EntropyProvided{RandomBytes: entBytes},
			},
		})
		for step := 0; step < 4; step++ {
			var kg *corev1.SecurityKeyGenerateEffect
			for _, eff := range curBatch.Effects {
				if g := eff.GetSecurityKeyGenerate(); g != nil {
					kg = g
					break
				}
			}
			if kg == nil {
				break
			}
			pub, _, _ := ed25519.GenerateKey(rand.Reader)
			curBatch = c.Dispatch(&corev1.Event{
				NowMs: int64(1070 + step),
				Event: &corev1.Event_SecurityKeyGenerated{
					SecurityKeyGenerated: &corev1.SecurityKeyGeneratedEvent{
						OpId:      kg.OpId,
						PublicKey: pub,
					},
				},
			})
		}
		// Should have issued PEEK_PAIRING RPC.
		peek := findSecurityRPCEffect(resBatch, corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PEEK_PAIRING)
		if peek == nil {
			peek = findSecurityRPCEffect(curBatch, corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PEEK_PAIRING)
		}
		if peek == nil {
			t.Fatal("expected PEEK_PAIRING RPC on auto-submit")
		}
		// Simulate structured PAIRING_INVALID_OR_EXPIRED error -> structured REFILL banner.
		c.Dispatch(&corev1.Event{
			NowMs: 1100,
			Event: &corev1.Event_SecurityRpcResult{
				SecurityRpcResult: &corev1.SecurityRpcResultEvent{
					OpId:         peek.OpId,
					ConnectCode:  5,
					Error:        "opaque",
					ErrorDetails: makeRelayDetailBytes(pb.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED),
				},
			},
		})
		ob2 := mustViewOnboarding(t, c)
		if ob2.Join.Banner == nil || !ob2.Join.Banner.Visible || ob2.Join.Banner.Action != corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_REFILL {
			t.Fatalf("expected REFILL banner, got %+v", ob2.Join.Banner)
		}
		if ob2.Join.Banner.Text.GetKey() != text.KeySecurityThePairingCodeIsWrongOrHasAlreadyBeen {
			t.Fatalf("banner key = %q, want pairing invalid key", ob2.Join.Banner.Text.GetKey())
		}
	})
}

func TestOnboardingStoragePersistGatingAndManualCodeEditClearsCommitHint(t *testing.T) {
	c := New()
	batch := c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_ClientInit{
			ClientInit: &corev1.ClientInitEvent{
				Platform:              "web",
				Foreground:            true,
				NetworkAvailable:      true,
				DefaultServer:         "https://ra.example.com",
				RequestStoragePersist: true,
			},
		},
	})
	bootEff := findStorageEffect(batch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
	c.Dispatch(&corev1.Event{
		NowMs: 1010,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: bootEff.OpId},
		},
	})

	// Switch to CREATE and submit -> must emit RequestStoragePersistEffect first.
	c.Dispatch(&corev1.Event{
		NowMs: 1020,
		Event: &corev1.Event_OnboardingSetMode{
			OnboardingSetMode: &corev1.OnboardingSetModeAction{Mode: corev1.OnboardingMode_ONBOARDING_MODE_CREATE},
		},
	})
	subBatch := c.Dispatch(&corev1.Event{
		NowMs: 1030,
		Event: &corev1.Event_OnboardingCreateSubmit{
			OnboardingCreateSubmit: &corev1.OnboardingCreateSubmitAction{ServerInput: "https://ra.example.com"},
		},
	})
	persistEff := findPersistEffect(subBatch)
	if persistEff == nil {
		t.Fatal("expected RequestStoragePersistEffect before prepareCreation when request_storage_persist=true")
	}
	// Ack persist result -> core records persistence state and proceeds with key generation / entropy.
	c.Dispatch(&corev1.Event{
		NowMs: 1040,
		Event: &corev1.Event_StoragePersistResult{
			StoragePersistResult: &corev1.StoragePersistResultEvent{OpId: persistEff.OpId, Persisted: false},
		},
	})
	ob := mustViewOnboarding(t, c)
	if !ob.Create.HasPersistenceResult || ob.Create.StoragePersisted {
		t.Fatalf("persistence flags = (%v, %v), want (true, false)", ob.Create.HasPersistenceResult, ob.Create.StoragePersisted)
	}

	// Verify manual code edit clears commit hint.
	sc := c.m.ensureSec()
	sc.ui.JoinCommitHint = []byte{1, 2, 3, 4}
	sc.ui.JoinCodeDigits = "12345678"
	c.Dispatch(&corev1.Event{
		NowMs: 1050,
		Event: &corev1.Event_OnboardingJoinEdit{
			OnboardingJoinEdit: &corev1.OnboardingJoinEditAction{HasCodeInput: true, CodeInput: "87654321"},
		},
	})
	if len(sc.ui.JoinCommitHint) != 0 {
		t.Fatal("manual code edit must clear JoinCommitHint")
	}
	if mustViewOnboarding(t, c).Join.FormattedCode != "8765 4321" {
		t.Fatalf("formatted code = %q, want '8765 4321'", mustViewOnboarding(t, c).Join.FormattedCode)
	}
}

// buildRealReadyBootstrap builds a cryptographically valid v4 PersistedRecord (signed genesis +
// lease with real Ed25519/X25519 keys) and returns the storage entries and device keys needed to
// bootstrap Core cleanly into APP_ROOT_STAGE_READY without mutating internal UI fields.
func buildRealReadyBootstrap(t *testing.T, server string, nowMs int64) (
	recBytes []byte,
	devEdPub []byte,
	devXPub []byte,
	devXPriv *ecdh.PrivateKey,
	deviceID string,
) {
	t.Helper()
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	xPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	xPub := xPriv.PublicKey().Bytes()
	recEdPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	member, err := devicesec.NewMember(edPub, xPub, devicesec.Client)
	if err != nil {
		t.Fatal(err)
	}
	deviceID = member.ID
	domain := devicesec.Digest("test-domain", edPub)
	var epochSecret [32]byte
	if _, err := rand.Read(epochSecret[:]); err != nil {
		t.Fatal(err)
	}
	u0, err := devicesec.NewGenesis(deviceID, domain, member, [32]byte(recEdPub), epochSecret[:], uint64(nowMs-1_000), appTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, err := u0.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	u0.Signature = ed25519.Sign(edPriv, sb0)
	rawState, err := u0.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	s0, err := devicesec.ParseState(rawState)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := s0.Pin()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := devicesec.NewLease(s0, uint64(nowMs-1_000))
	if err != nil {
		t.Fatal(err)
	}
	lb, err := lease.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	lease.Signature = ed25519.Sign(edPriv, lb)
	leaseRaw, err := lease.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	rec := &security.PersistedRecord{
		Server:         server,
		Pin:            pin,
		MemberID:       deviceID,
		Member:         true,
		States:         [][]byte{rawState},
		RecoveryProofs: [][]byte{nil},
		Lease:          leaseRaw,
	}
	recBytes, err = rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return recBytes, edPub, xPub, xPriv, deviceID
}

// bootstrapCoreToReady runs ClientInit + READ_BOOTSTRAP + real X25519 KeyAgree to land Core in READY.
func bootstrapCoreToReady(t *testing.T, c *Core, server, locale string, nowMs int64) (
	recBytes []byte,
	devEdPub []byte,
	devXPub []byte,
	devXPriv *ecdh.PrivateKey,
	deviceID string,
) {
	t.Helper()
	recBytes, devEdPub, devXPub, devXPriv, deviceID = buildRealReadyBootstrap(t, server, nowMs)
	batch := c.Dispatch(&corev1.Event{
		NowMs:  nowMs,
		MonoMs: nowMs,
		Event: &corev1.Event_ClientInit{
			ClientInit: &corev1.ClientInitEvent{
				UiLocale:              locale,
				Platform:              "web",
				DeviceDescription:     "Chrome",
				InlineCrypto:          true,
				Foreground:            true,
				NetworkAvailable:      true,
				DefaultServer:         server,
				RequestStoragePersist: false,
			},
		},
	})
	bootEff := findStorageEffect(batch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
	if bootEff == nil {
		t.Fatal("expected READ_BOOTSTRAP on ClientInit")
	}
	cur := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 10,
		MonoMs: nowMs + 10,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:        bootEff.OpId,
				HasDeviceEd: true,
				DeviceEdPub: devEdPub,
				HasDeviceX:  true,
				DeviceXPub:  devXPub,
				Entries: []*corev1.SecurityStorageEntry{{
					Bucket:   corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
					Key:      "record",
					Revision: "1",
					Data:     recBytes,
				}},
			},
		},
	})
	for step := 0; step < 4; step++ {
		var ka *corev1.SecurityKeyAgreeEffect
		for _, eff := range cur.Effects {
			if g := eff.GetSecurityKeyAgree(); g != nil {
				ka = g
				break
			}
		}
		if ka == nil {
			break
		}
		peerPub, err := ecdh.X25519().NewPublicKey(ka.PeerPublic)
		if err != nil {
			t.Fatalf("peer pubkey: %v", err)
		}
		shared, err := devXPriv.ECDH(peerPub)
		if err != nil {
			t.Fatalf("ecdh: %v", err)
		}
		cur = c.Dispatch(&corev1.Event{
			NowMs:  nowMs + 20 + int64(step),
			MonoMs: nowMs + 20 + int64(step),
			Event: &corev1.Event_SecurityKeyAgreed{
				SecurityKeyAgreed: &corev1.SecurityKeyAgreedEvent{
					OpId:   ka.OpId,
					Shared: shared,
				},
			},
		})
	}
	if got := mustViewAppRoot(t, c).Stage; got != corev1.AppRootStage_APP_ROOT_STAGE_READY {
		t.Fatalf("stage after real bootstrap = %v, want READY", got)
	}
	return recBytes, devEdPub, devXPub, devXPriv, deviceID
}

func TestTwoPhaseExitRiskReCheckAndFullIsolationReset(t *testing.T) {
	c := New()
	// 1. Reach READY through real ClientInit + signed v4 PersistedRecord + X25519 unlock (never mutating sc.ui.Stage).
	recBytes, devEdPub, devXPub, _, deviceID := bootstrapCoreToReady(t, c, "https://acct-a.example.com", "en", 10_000)
	c.m.sync.SecurityEngine().SetAuthToken("secret-token-acct-a", 999_999)
	if _, ok := c.m.sync.SecurityEngine().CurrentHeadHash(); !ok {
		t.Fatal("expected verified head hash after real bootstrap")
	}

	// Populate Account A runtime machine/session and an active timer.
	c.Dispatch(&corev1.Event{
		NowMs:  10_100,
		MonoMs: 10_100,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	prev, sctx := c.m.sync.BeginStep()
	c.m.sync.ScheduleAppTimer(sctx, corev1.TimerKind_TIMER_KIND_SESSION_UNDO, "sess-a-1", 5000)
	c.m.sync.EndStep(prev, sctx)

	// 2. Request exit (LEAVE_ACCOUNT) -> checkExitAdviceOp re-reads storage -> ONLY_MANAGER (sole managing client).
	reqBatch := c.Dispatch(&corev1.Event{
		NowMs: 10_200,
		Event: &corev1.Event_ExitRequest{
			ExitRequest: &corev1.ExitRequestAction{
				Intent: corev1.ExitIntentKind_EXIT_INTENT_KIND_LEAVE_ACCOUNT,
				Server: "https://acct-a.example.com",
			},
		},
	})
	check1 := findStorageEffect(reqBatch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
	if check1 == nil {
		t.Fatal("initial CheckExitAdvice must re-read storage")
	}
	c.Dispatch(&corev1.Event{
		NowMs: 10_205,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:        check1.OpId,
				HasDeviceEd: true,
				DeviceEdPub: devEdPub,
				HasDeviceX:  true,
				DeviceXPub:  devXPub,
				Entries: []*corev1.SecurityStorageEntry{{
					Bucket:   corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
					Key:      "record",
					Revision: "1",
					Data:     recBytes,
				}},
			},
		},
	})
	exView := mustViewExitConfirm(t, c)
	if exView.Step != corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_AWAITING_CONFIRM ||
		exView.WarningBody.GetKey() != text.KeySecurityThisDeviceIsTheOnlyManagingClientTransferManagement {
		t.Fatalf("after initial check: step=%v warning=%+v", exView.Step, exView.WarningBody)
	}

	// 3. Before user clicks confirm, another tab writes a pending security op to storage!
	// Two-phase re-check MUST re-read storage, detect escalation (ONLY_MANAGER -> UNRESOLVED_SECURITY_OP),
	// and hold in AWAITING_CONFIRM instead of executing exit.
	pendingOpData, err := (&security.PersistedOperation{
		ID:       "op-other-tab",
		Kind:     security.PendingOpKindState,
		Phase:    security.PendingOpPhasePrepared,
		Server:   "https://acct-a.example.com",
		MemberID: deviceID,
		Revision: 1,
	}).Encode()
	if err != nil {
		t.Fatalf("Encode pending op: %v", err)
	}
	recheckBatch := c.Dispatch(&corev1.Event{
		NowMs: 10_220,
		Event: &corev1.Event_ExitConfirm{ExitConfirm: &corev1.ExitConfirmAction{Confirm: true}},
	})
	check2 := findStorageEffect(recheckBatch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
	if check2 == nil {
		t.Fatal("second-phase exit confirm must re-read storage to detect cross-tab pending ops")
	}
	c.Dispatch(&corev1.Event{
		NowMs: 10_225,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:        check2.OpId,
				HasDeviceEd: true,
				DeviceEdPub: devEdPub,
				HasDeviceX:  true,
				DeviceXPub:  devXPub,
				Entries: []*corev1.SecurityStorageEntry{
					{
						Bucket:   corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
						Key:      "record",
						Revision: "1",
						Data:     recBytes,
					},
					{
						Bucket:   corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION,
						Key:      "pending",
						Revision: "1",
						Data:     pendingOpData,
					},
				},
			},
		},
	})
	exView2 := mustViewExitConfirm(t, c)
	if exView2.Step != corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_AWAITING_CONFIRM ||
		exView2.WarningBody.GetKey() != text.KeySecurityTheSecurityOperationSOutcomeIsUnverifiedContinueThe {
		t.Fatalf("risk escalation must require re-confirm with unresolved warning: step=%v warning=%+v", exView2.Step, exView2.WarningBody)
	}

	// 4. User confirms again with matching risk -> re-reads storage -> emits UnregisterPushOnExitEffect with cached token.
	confirm2Batch := c.Dispatch(&corev1.Event{
		NowMs: 10_240,
		Event: &corev1.Event_ExitConfirm{ExitConfirm: &corev1.ExitConfirmAction{Confirm: true}},
	})
	check3 := findStorageEffect(confirm2Batch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
	if check3 == nil {
		t.Fatal("expected storage re-read on second confirm")
	}
	pushBatch := c.Dispatch(&corev1.Event{
		NowMs: 10_245,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:        check3.OpId,
				HasDeviceEd: true,
				DeviceEdPub: devEdPub,
				HasDeviceX:  true,
				DeviceXPub:  devXPub,
				Entries: []*corev1.SecurityStorageEntry{
					{Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD, Key: "record", Revision: "1", Data: recBytes},
					{Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION, Key: "pending", Revision: "1", Data: pendingOpData},
				},
			},
		},
	})
	pushEff := findUnregisterPushEffect(pushBatch)
	if pushEff == nil || pushEff.Server != "https://acct-a.example.com" || pushEff.AuthToken != "secret-token-acct-a" {
		t.Fatalf("expected UnregisterPushOnExitEffect with cached token, got %+v", pushEff)
	}

	// 5. Shell acks PushUnregistered -> Core runs ClearSecurity.
	clearSecBatch := c.Dispatch(&corev1.Event{
		NowMs: 10_260,
		Event: &corev1.Event_PushUnregistered{PushUnregistered: &corev1.PushUnregisteredEvent{OpId: pushEff.OpId}},
	})
	clearAllEff := findStorageEffect(clearSecBatch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_CLEAR_ALL)
	if clearAllEff == nil {
		t.Fatal("expected CLEAR_ALL storage effect after PushUnregistered")
	}
	lastOldSecOpID := clearAllEff.OpId

	// 6. Shell acks CLEAR_ALL -> Core emits PersistLastServer + ClearAccountCachesEffect.
	clearCacheBatch := c.Dispatch(&corev1.Event{
		NowMs: 10_270,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: clearAllEff.OpId},
		},
	})
	cacheEff := findClearCachesEffect(clearCacheBatch)
	if cacheEff == nil || cacheEff.PreserveServer != "https://acct-a.example.com" {
		t.Fatalf("expected ClearAccountCachesEffect preserving server, got %+v", cacheEff)
	}

	// 7. Shell acks AccountCachesCleared -> Core executes IN-PLACE RESET!
	resetBatch := c.Dispatch(&corev1.Event{
		NowMs: 10_280,
		Event: &corev1.Event_AccountCachesCleared{
			AccountCachesCleared: &corev1.AccountCachesClearedEvent{OpId: cacheEff.OpId},
		},
	})
	canceledUndo := false
	for _, eff := range resetBatch.Effects {
		if ct := eff.GetCancelTimer(); ct != nil && ct.Kind == corev1.TimerKind_TIMER_KIND_SESSION_UNDO && ct.Key == "sess-a-1" {
			canceledUndo = true
		}
	}
	if !canceledUndo {
		t.Fatal("in-place exit reset must emit CancelTimerEffect for Account A's active timers")
	}
	rtEff := findRuntimeChangedEffect(resetBatch)
	if rtEff == nil || rtEff.Active || rtEff.Removed {
		t.Fatalf("expected AccountRuntimeChangedEffect(active=false, removed=false), got %+v", rtEff)
	}

	// Verify keys, cached auth token, verified head, and Account A state are ALL completely cleared!
	if tok := c.m.sync.SecurityEngine().CachedAuthToken(); tok != "" {
		t.Fatalf("CachedAuthToken after reset = %q, want empty", tok)
	}
	if _, ok := c.m.sync.SecurityEngine().CurrentHeadHash(); ok {
		t.Fatal("CurrentHeadHash must be cleared after exit reset")
	}
	rootAfter := mustViewAppRoot(t, c)
	if rootAfter.Stage != corev1.AppRootStage_APP_ROOT_STAGE_ONBOARDING || rootAfter.DeviceId != "" || rootAfter.AccountId != "" {
		t.Fatalf("AppRoot after exit reset = %+v", rootAfter)
	}
	obAfter := mustViewOnboarding(t, c)
	if obAfter.Mode != corev1.OnboardingMode_ONBOARDING_MODE_ENTRY || obAfter.Create.ServerInput != "https://acct-a.example.com" {
		t.Fatalf("Onboarding after exit reset = mode:%v server:%q", obAfter.Mode, obAfter.Create.ServerInput)
	}
	if c.m.state.UILocale != "en" {
		t.Fatalf("non-account UILocale must be preserved across exit reset, got %q", c.m.state.UILocale)
	}

	// 8. Verify monotonic op_id preservation: starting a new op on Account B allocates op_id > lastOldSecOpID,
	// and delivering a late old op_id from Account A is silently ignored!
	retryBatch := c.Dispatch(&corev1.Event{
		NowMs: 10_300,
		Event: &corev1.Event_AppRootRetryBootstrap{AppRootRetryBootstrap: &corev1.AppRootRetryBootstrapAction{}},
	})
	newBootEff := findStorageEffect(retryBatch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
	if newBootEff == nil || newBootEff.OpId <= lastOldSecOpID {
		t.Fatalf("post-reset op_id = %v must be > pre-reset op_id %d", newBootEff, lastOldSecOpID)
	}
	// Late storage error carrying an old Account A op_id must be ignored and NOT fail Account B's bootstrap!
	c.Dispatch(&corev1.Event{
		NowMs: 10_305,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: check1.OpId, Error: "stale-account-a-error"},
		},
	})
	if mustViewAppRoot(t, c).Stage != corev1.AppRootStage_APP_ROOT_STAGE_LOADING {
		t.Fatalf("late Account A result must be ignored; stage=%v", mustViewAppRoot(t, c).Stage)
	}
	// Now deliver Account B's matching op_id -> lands cleanly in ONBOARDING.
	c.Dispatch(&corev1.Event{
		NowMs: 10_310,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: newBootEff.OpId},
		},
	})
	if mustViewAppRoot(t, c).Stage != corev1.AppRootStage_APP_ROOT_STAGE_ONBOARDING {
		t.Fatalf("Account B bootstrap should complete cleanly, stage=%v", mustViewAppRoot(t, c).Stage)
	}
}

func TestExternalLogoutResetsCoreInPlaceAndIsolatesNewAccount(t *testing.T) {
	c := New()
	bootstrapCoreToReady(t, c, "https://acct-a.example.com", "zh-CN", 20_000)
	c.m.sync.SecurityEngine().SetAuthToken("tok-a", 999_999)
	// Also seed a machine and session into Account A's world.
	c.Dispatch(&corev1.Event{
		NowMs: 20_100,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{MachineId: "mach-a", Online: true, Meta: &v1.MachineMeta{Hostname: "Mac-A"}},
				},
			},
		},
	})
	prev, sctx := c.m.sync.BeginStep()
	c.m.sync.ScheduleAppTimer(sctx, corev1.TimerKind_TIMER_KIND_SESSION_UNDO, "sess-ext-a", 5000)
	c.m.sync.EndStep(prev, sctx)

	// Another tab broadcasts logout -> ExternalLogoutEvent runs the exact same executeInPlaceExitReset path.
	extBatch := c.Dispatch(&corev1.Event{
		NowMs: 20_200,
		Event: &corev1.Event_ExternalLogout{ExternalLogout: &corev1.ExternalLogoutEvent{}},
	})
	rtEff := findRuntimeChangedEffect(extBatch)
	if rtEff == nil || rtEff.Active || rtEff.Removed {
		t.Fatalf("ExternalLogout must emit AccountRuntimeChanged(active=false, removed=false), got %+v", rtEff)
	}
	if mustViewAppRoot(t, c).Stage != corev1.AppRootStage_APP_ROOT_STAGE_ONBOARDING {
		t.Fatalf("stage after ExternalLogout = %v, want ONBOARDING", mustViewAppRoot(t, c).Stage)
	}
	if c.m.sync.SecurityEngine().CachedAuthToken() != "" {
		t.Fatal("ExternalLogout must clear cached auth token")
	}
	if _, ok := c.m.sync.SecurityEngine().CurrentHeadHash(); ok {
		t.Fatal("ExternalLogout must clear verified head hash")
	}
	if c.m.state.UILocale != "zh-CN" {
		t.Fatalf("ExternalLogout must preserve UILocale, got %q", c.m.state.UILocale)
	}
	// Seed Account B -> zero Account A machines/sessions/security state visible.
	c.Dispatch(&corev1.Event{
		NowMs: 21_000,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-b",
					AuthorityDeviceId: "dev-b",
					TrustedMachineIds: []string{"mach-b"},
					TrustedClientIds:  []string{"dev-b"},
				},
				Machines: []*corev1.DemoMachine{{MachineId: "mach-b", Online: true, Meta: &v1.MachineMeta{Hostname: "Mac-B"}}},
			},
		},
	})
	sv, err := c.View(&corev1.Screen{Screen: &corev1.Screen_SyncState{SyncState: &corev1.SyncStateScreenRequest{}}})
	if err != nil {
		t.Fatal(err)
	}
	if sv.GetSyncState().SelfDeviceId != "dev-b" || len(sv.GetSyncState().Machines) != 1 || sv.GetSyncState().Machines[0].MachineId != "mach-b" {
		t.Fatalf("Account B contaminated after ExternalLogout: %+v", sv.GetSyncState().Machines)
	}
}

func TestPendingOpDiscardMatrixAndWorkflowConsistency(t *testing.T) {
	// Cross-verify EvaluatePendingOpDiscard against the REAL security.Engine discardPendingTask:
	// for every pending op shape, canDiscard == true iff workflow accepts discard (with flag=requiresConfirm),
	// and when requiresConfirm && Signed && CodeSaved, flag=false is rejected while flag=true succeeds.
	cases := []struct {
		name        string
		persisted   security.PersistedOperation
		wantKind    corev1.PendingSecurityOpKind
		wantDiscard bool
		wantConfirm bool
		rejectNoAck bool
	}{
		{
			name:        "offer_prepared_unsigned_requires_confirm",
			persisted:   security.PersistedOperation{ID: "op-1", Revision: 1, Kind: security.PendingOpKindOffer, Phase: security.PendingOpPhasePrepared},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_HANDOFF_OFFER,
			wantDiscard: true,
			wantConfirm: true,
		},
		{
			name:        "offer_prepared_signed_and_saved_requires_confirm",
			persisted:   security.PersistedOperation{ID: "op-2", Revision: 1, Kind: security.PendingOpKindOffer, Phase: security.PendingOpPhasePrepared, Signed: []byte{1}, CodeSaved: true},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_HANDOFF_OFFER,
			wantDiscard: true,
			wantConfirm: true,
			rejectNoAck: true,
		},
		{
			name:        "offer_published_not_discardable",
			persisted:   security.PersistedOperation{ID: "op-3", Revision: 1, Kind: security.PendingOpKindOffer, Phase: security.PendingOpPhasePublished, Signed: []byte{1}, CodeSaved: true},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_HANDOFF_OFFER,
			wantDiscard: false,
			wantConfirm: false,
		},
		{
			name:        "recovery_rotation_prepared_unsigned_unsaved_discardable",
			persisted:   security.PersistedOperation{ID: "op-4", Revision: 1, Kind: security.PendingOpKindState, Action: security.PendingOpActionRecovery, Phase: security.PendingOpPhasePrepared},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_RECOVERY_ROTATION,
			wantDiscard: true,
			wantConfirm: false,
		},
		{
			name:        "recovery_rotation_prepared_code_saved_not_discardable",
			persisted:   security.PersistedOperation{ID: "op-5", Revision: 1, Kind: security.PendingOpKindState, Action: security.PendingOpActionRecovery, Phase: security.PendingOpPhasePrepared, CodeSaved: true},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_RECOVERY_ROTATION,
			wantDiscard: false,
			wantConfirm: false,
		},
		{
			name:        "recovery_rotation_prepared_signed_not_discardable",
			persisted:   security.PersistedOperation{ID: "op-6", Revision: 1, Kind: security.PendingOpKindState, Action: security.PendingOpActionRecovery, Phase: security.PendingOpPhasePrepared, Signed: []byte{1}},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_RECOVERY_ROTATION,
			wantDiscard: false,
			wantConfirm: false,
		},
		{
			name:        "state_change_prepared_unsigned_discardable",
			persisted:   security.PersistedOperation{ID: "op-7", Revision: 1, Kind: security.PendingOpKindState, Action: security.PendingOpActionRemove, Phase: security.PendingOpPhasePrepared},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_STATE_CHANGE,
			wantDiscard: true,
			wantConfirm: false,
		},
		{
			name:        "state_change_published_not_discardable",
			persisted:   security.PersistedOperation{ID: "op-8", Revision: 1, Kind: security.PendingOpKindState, Action: security.PendingOpActionRemove, Phase: security.PendingOpPhasePublished},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_STATE_CHANGE,
			wantDiscard: false,
			wantConfirm: false,
		},
		{
			name:        "signed_state_change_with_delivery_not_discardable",
			persisted:   security.PersistedOperation{ID: "op-9", Revision: 1, Kind: security.PendingOpKindState, Action: security.PendingOpActionPair, Phase: security.PendingOpPhasePrepared, Signed: []byte{1}, Delivery: []byte{1}},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_PAIRING_DELIVERY,
			wantDiscard: false,
			wantConfirm: false,
		},
		{
			name:        "lease_prepared_not_discardable",
			persisted:   security.PersistedOperation{ID: "op-10", Revision: 1, Kind: security.PendingOpKindLease, Phase: security.PendingOpPhasePrepared},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_LEASE,
			wantDiscard: false,
			wantConfirm: false,
		},
		{
			name:        "delete_records_prepared_not_discardable",
			persisted:   security.PersistedOperation{ID: "op-11", Revision: 1, Kind: security.PendingOpKindDeleteRecords, Phase: security.PendingOpPhasePrepared},
			wantKind:    corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_DELETE_RECORDS,
			wantDiscard: false,
			wantConfirm: false,
		},
	}

	tryDiscard := func(t *testing.T, recBytes, devEdPub, devXPub, opBytes []byte, ack bool) (*corev1.SecurityWorkflowStatus, bool) {
		t.Helper()
		c := New()
		initBatch := c.Dispatch(&corev1.Event{
			NowMs: 10_000,
			Event: &corev1.Event_ClientInit{ClientInit: &corev1.ClientInitEvent{UiLocale: "en", Platform: "web", InlineCrypto: true, Foreground: true, NetworkAvailable: true}},
		})
		bootEff := findStorageEffect(initBatch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
		c.Dispatch(&corev1.Event{
			NowMs: 10_010,
			Event: &corev1.Event_SecurityStorageResult{SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId: bootEff.OpId, HasDeviceEd: true, DeviceEdPub: devEdPub, HasDeviceX: true, DeviceXPub: devXPub,
				Entries: []*corev1.SecurityStorageEntry{
					{Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD, Key: "record", Revision: "1", Data: recBytes},
					{Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION, Key: "pending", Revision: "1", Data: opBytes},
				},
			}},
		})
		st := c.m.sync.SecurityEngine().BuildWorkflowStatus()
		eng := c.m.sync.SecurityEngine()
		effs := eng.HandleWorkflowAction(10_020, &corev1.SecurityWorkflowActionEvent{
			ActionId: 42,
			Action:   &corev1.SecurityWorkflowActionEvent_DiscardPendingOp{DiscardPendingOp: &corev1.SecurityBoolAction{Flag: ack}},
		}, nil, nil)
		for _, eff := range effs {
			if se := eff.GetSecurityStorage(); se != nil && se.Op == corev1.SecurityStorageOp_SECURITY_STORAGE_OP_DELETE_CAS && se.Bucket == corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION {
				return st, true
			}
			if res := eff.GetSecurityWorkflowResult(); res != nil && res.ActionId == 42 {
				return st, res.Ok
			}
		}
		return st, false
	}

	cBase := New()
	recBytes, devEdPub, devXPub, _, deviceID := bootstrapCoreToReady(t, cBase, "https://ra.example.com", "en", 9_000)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := tc.persisted
			op.Server, op.MemberID = "https://ra.example.com", deviceID
			opBytes, err := op.Encode()
			if err != nil {
				t.Fatalf("encode op: %v", err)
			}
			st, acceptedWithAck := tryDiscard(t, recBytes, devEdPub, devXPub, opBytes, tc.wantConfirm)
			if st == nil || st.PendingOp == nil {
				t.Fatalf("expected PendingOp in engine status, got %+v", st)
			}
			if got := screens.PendingOpKindFromString(st.PendingOp); got != tc.wantKind {
				t.Errorf("PendingOpKindFromString = %v, want %v", got, tc.wantKind)
			}
			canDiscard, reqConfirm := screens.EvaluatePendingOpDiscard(st.PendingOp)
			if canDiscard != tc.wantDiscard || reqConfirm != tc.wantConfirm {
				t.Errorf("EvaluatePendingOpDiscard = (%v, %v), want (%v, %v)", canDiscard, reqConfirm, tc.wantDiscard, tc.wantConfirm)
			}
			if acceptedWithAck != canDiscard {
				t.Errorf("workflow discardPendingTask(ack=%v) accepted=%v, diverges from EvaluatePendingOpDiscard canDiscard=%v", tc.wantConfirm, acceptedWithAck, canDiscard)
			}
			if !canDiscard {
				// Confirm even flag=true is rejected by workflow when EvaluatePendingOpDiscard says false.
				if _, acceptedForced := tryDiscard(t, recBytes, devEdPub, devXPub, opBytes, true); acceptedForced {
					t.Errorf("workflow unexpectedly accepted discard with ack=true when EvaluatePendingOpDiscard=false")
				}
			}
			if tc.rejectNoAck {
				if _, acceptedNoAck := tryDiscard(t, recBytes, devEdPub, devXPub, opBytes, false); acceptedNoAck {
					t.Errorf("workflow must reject signed+saved offer discard when ack=false")
				}
			}
			vm := screens.BuildAccountSecurity(&screens.SecurityUIState{Status: st}, screens.SecurityViewContext{SyncLoaded: true, SyncIsManager: true})
			if vm.PendingKind != tc.wantKind || vm.CanDiscardPending != tc.wantDiscard || vm.DiscardRequiresConfirm != tc.wantConfirm {
				t.Errorf("BuildAccountSecurity = (kind=%v discard=%v confirm=%v), want (%v, %v, %v)",
					vm.PendingKind, vm.CanDiscardPending, vm.DiscardRequiresConfirm, tc.wantKind, tc.wantDiscard, tc.wantConfirm)
			}
		})
	}
}

func TestSessionStreamingDoesNotRenderSecurityScreens(t *testing.T) {
	c := bootstrapReadyApp(t,
		[]*corev1.PlainMachine{{MachineId: "m-1", Online: true, Meta: &v1.MachineMeta{Hostname: "Mac"}}},
		[]*corev1.PlainSession{{SessionId: "s-1", MachineId: "m-1", Meta: &v1.Session{Id: "s-1", Cwd: "/tmp/proj", Agent: v1.Agent_AGENT_CLAUDE, Status: v1.SessionStatus_SESSION_STATUS_RUNNING}}},
	)
	sc := c.m.ensureSec()
	sc.ui.Initialized = true
	sc.ui.Stage = corev1.AppRootStage_APP_ROOT_STAGE_READY
	sc.ui.AddOpen = true
	sc.dirty = securityDirtyFlags{}

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-1", Watched: true}},
	})
	// Deliver real streaming EventsAppended frames (UserMessage + AgentMessage) and verify Session renders while security screens do NOT.
	batch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_200,
		Event: &corev1.Event_StreamFrame{StreamFrame: &corev1.StreamFrameEvent{
			LegId: 1,
			Frame: &corev1.StreamFrameEvent_EventsAppended{EventsAppended: &corev1.EventsAppendedFrame{
				SessionId: "s-1",
				Events: []*corev1.PlainEvent{
					{Seq: 1, Verified: true, Event: &v1.Event{Seq: 1, Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "hello"}}}},
					{Seq: 2, Verified: true, Event: &v1.Event{Seq: 2, Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "hi"}}}},
				},
			}},
		}},
	})
	sawSessionRender := false
	for _, eff := range batch.Effects {
		if r := eff.GetRender(); r != nil {
			for _, target := range r.Screens {
				if target == corev1.ScreenTarget_SCREEN_TARGET_SESSION {
					sawSessionRender = true
				}
				if target == corev1.ScreenTarget_SCREEN_TARGET_APP_ROOT ||
					target == corev1.ScreenTarget_SCREEN_TARGET_ONBOARDING ||
					target == corev1.ScreenTarget_SCREEN_TARGET_ACCOUNT_SECURITY ||
					target == corev1.ScreenTarget_SCREEN_TARGET_HANDOFF ||
					target == corev1.ScreenTarget_SCREEN_TARGET_ADD_DEVICE ||
					target == corev1.ScreenTarget_SCREEN_TARGET_EXIT_CONFIRM {
					t.Fatalf("session EventsAppended stream frame must not dirty security screen %v", target)
				}
			}
		}
	}
	if !sawSessionRender {
		t.Fatalf("expected EventsAppended stream frame to render SCREEN_TARGET_SESSION")
	}
}

func TestExitPushUnregisterTimeoutLateSyncNowDiscarded(t *testing.T) {
	c := New()
	recBytes, devEdPub, devXPub, _, _ := bootstrapCoreToReady(t, c, "https://acct-a.example.com", "en", 40_000)
	// Ensure no cached auth token so exit triggers best-effort SyncNow bounded by the 3s exit timer.
	if tok := c.m.sync.SecurityEngine().CachedAuthToken(); tok != "" {
		t.Fatalf("expected empty initial cached token, got %q", tok)
	}
	// Request exit -> CheckExitAdvice -> Confirm exit.
	reqBatch := c.Dispatch(&corev1.Event{
		NowMs: 40_100,
		Event: &corev1.Event_ExitRequest{ExitRequest: &corev1.ExitRequestAction{Intent: corev1.ExitIntentKind_EXIT_INTENT_KIND_LEAVE_ACCOUNT}},
	})
	advBoot := findStorageEffect(reqBatch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
	c.Dispatch(&corev1.Event{
		NowMs: 40_110,
		Event: &corev1.Event_SecurityStorageResult{SecurityStorageResult: &corev1.SecurityStorageResultEvent{
			OpId: advBoot.OpId, HasDeviceEd: true, DeviceEdPub: devEdPub, HasDeviceX: true, DeviceXPub: devXPub,
			Entries: []*corev1.SecurityStorageEntry{{Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD, Key: "record", Revision: "1", Data: recBytes}},
		}},
	})
	confBatch := c.Dispatch(&corev1.Event{
		NowMs: 40_200,
		Event: &corev1.Event_ExitConfirm{ExitConfirm: &corev1.ExitConfirmAction{Confirm: true}},
	})
	advBoot2 := findStorageEffect(confBatch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
	execBatch := c.Dispatch(&corev1.Event{
		NowMs: 40_210,
		Event: &corev1.Event_SecurityStorageResult{SecurityStorageResult: &corev1.SecurityStorageResultEvent{
			OpId: advBoot2.OpId, HasDeviceEd: true, DeviceEdPub: devEdPub, HasDeviceX: true, DeviceXPub: devXPub,
			Entries: []*corev1.SecurityStorageEntry{{Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD, Key: "record", Revision: "1", Data: recBytes}},
		}},
	})

	// Capture the 3s timer and the in-flight GET_CHALLENGE RPC from SyncNow.
	var timerEff *corev1.ScheduleTimerEffect
	var chalOpID uint64
	for _, eff := range execBatch.Effects {
		if te := eff.GetScheduleTimer(); te != nil && te.Key == exitPushUnregisterTimerKey {
			timerEff = te
		}
		if rpc := eff.GetSecurityRpc(); rpc != nil && rpc.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_CHALLENGE {
			chalOpID = rpc.OpId
		}
	}
	if timerEff == nil || chalOpID == 0 {
		t.Fatalf("expected 3s exit timer and GET_CHALLENGE RPC, got timer=%v chalOpID=%d", timerEff, chalOpID)
	}

	// Advance GET_CHALLENGE -> SIGN -> in-flight LOGIN before the 3s timeout fires.
	chalBytes, _ := proto.Marshal(&pb.GetChallengeResponse{Challenge: []byte("late-challenge-1234567890")})
	signBatch := c.Dispatch(&corev1.Event{
		NowMs: 40_250,
		Event: &corev1.Event_SecurityRpcResult{SecurityRpcResult: &corev1.SecurityRpcResultEvent{
			OpId: chalOpID, ResponseBytes: chalBytes,
		}},
	})
	var signOpID uint64
	for _, eff := range signBatch.Effects {
		if sig := eff.GetSecurityKeySign(); sig != nil {
			signOpID = sig.OpId
		}
	}
	if signOpID == 0 {
		t.Fatalf("expected SecurityKeySignEffect after GET_CHALLENGE")
	}
	loginReqBatch := c.Dispatch(&corev1.Event{
		NowMs: 40_260,
		Event: &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{
			OpId: signOpID, Signature: make([]byte, ed25519.SignatureSize),
		}},
	})
	var loginOpID uint64
	for _, eff := range loginReqBatch.Effects {
		if rpc := eff.GetSecurityRpc(); rpc != nil && rpc.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LOGIN {
			loginOpID = rpc.OpId
		}
	}
	if loginOpID == 0 {
		t.Fatalf("expected in-flight SECURITY_RPC_METHOD_LOGIN before timeout")
	}

	// Advance past 3s timeout BEFORE LOGIN returns -> Core invalidates account auth and runs ClearSecurity.
	timeoutBatch := c.Dispatch(&corev1.Event{
		NowMs: 43_210,
		Event: &corev1.Event_TimerFired{TimerFired: &corev1.TimerFiredEvent{
			TimerId: timerEff.TimerId, Kind: timerEff.Kind, Key: timerEff.Key,
		}},
	})
	clearEff := findStorageEffect(timeoutBatch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_CLEAR_ALL)
	if clearEff == nil {
		t.Fatalf("expected SECURITY_STORAGE_OP_CLEAR_ALL after 3s timeout")
	}

	// Complete ClearSecurity storage op.
	c.Dispatch(&corev1.Event{
		NowMs: 43_220,
		Event: &corev1.Event_SecurityStorageResult{SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: clearEff.OpId}},
	})

	// Now deliver the LATE LOGIN response carrying a valid old-account token!
	loginRespBytes, _ := proto.Marshal(&pb.LoginResponse{Token: "stale-old-account-token", ExpiresMs: 99_999_999})
	lateBatch := c.Dispatch(&corev1.Event{
		NowMs: 43_250,
		Event: &corev1.Event_SecurityRpcResult{SecurityRpcResult: &corev1.SecurityRpcResultEvent{
			OpId: loginOpID, ResponseBytes: loginRespBytes,
		}},
	})
	for _, eff := range lateBatch.Effects {
		if tu := eff.GetSecurityTokenUpdated(); tu != nil && tu.Token != "" {
			t.Fatalf("late SyncNow LOGIN after exit timeout must NOT emit non-empty SecurityTokenUpdated: %+v", tu)
		}
		if se := eff.GetSecurityStorage(); se != nil {
			t.Fatalf("late SyncNow LOGIN after exit timeout must NOT touch storage: %+v", se)
		}
		if u := eff.GetUnregisterPushOnExit(); u != nil {
			t.Fatalf("late SyncNow LOGIN after exit timeout must NOT emit UnregisterPushOnExit: %+v", u)
		}
	}
	if tok := c.m.sync.SecurityEngine().CachedAuthToken(); tok != "" {
		t.Fatalf("late SyncNow LOGIN must NOT repopulate CachedAuthToken, got %q", tok)
	}
}

func TestCheckExitAdviceDoesNotMutateEngineState(t *testing.T) {
	c := New()
	recBytes, devEdPub, devXPub, _, deviceID := bootstrapCoreToReady(t, c, "https://acct-a.example.com", "en", 50_000)
	before := c.m.sync.SecurityEngine().BuildWorkflowStatus()
	if before.PendingOp != nil {
		t.Fatalf("expected clean initial PendingOp=nil, got %+v", before.PendingOp)
	}

	// Request exit; respond to CheckExitAdvice READ_BOOTSTRAP with storage containing a pending op.
	reqBatch := c.Dispatch(&corev1.Event{
		NowMs: 50_100,
		Event: &corev1.Event_ExitRequest{ExitRequest: &corev1.ExitRequestAction{Intent: corev1.ExitIntentKind_EXIT_INTENT_KIND_LEAVE_ACCOUNT}},
	})
	advBoot := findStorageEffect(reqBatch, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
	if advBoot == nil {
		t.Fatal("expected READ_BOOTSTRAP for CheckExitAdvice")
	}
	opBytes, err := (&security.PersistedOperation{
		ID: "ext-op", Revision: 1, Kind: security.PendingOpKindState, Action: security.PendingOpActionRemove,
		Phase: security.PendingOpPhasePrepared, Server: "https://acct-a.example.com", MemberID: deviceID,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	c.Dispatch(&corev1.Event{
		NowMs: 50_110,
		Event: &corev1.Event_SecurityStorageResult{SecurityStorageResult: &corev1.SecurityStorageResultEvent{
			OpId: advBoot.OpId, HasDeviceEd: true, DeviceEdPub: devEdPub, HasDeviceX: true, DeviceXPub: devXPub,
			Entries: []*corev1.SecurityStorageEntry{
				{Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD, Key: "record", Revision: "1", Data: recBytes},
				{Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION, Key: "pending", Revision: "1", Data: opBytes},
			},
		}},
	})
	if ex := mustViewExitConfirm(t, c); ex.Step != corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_AWAITING_CONFIRM {
		t.Fatalf("expected AWAITING_CONFIRM, got %v", ex.Step)
	}
	// Cancel exit and confirm Engine state was NOT overwritten by CheckExitAdvice!
	c.Dispatch(&corev1.Event{
		NowMs: 50_120,
		Event: &corev1.Event_ExitConfirm{ExitConfirm: &corev1.ExitConfirmAction{Confirm: false}},
	})
	after := c.m.sync.SecurityEngine().BuildWorkflowStatus()
	if after.PendingOp != nil {
		t.Fatalf("CheckExitAdvice must NOT mutate Engine pendingOp after canceling exit; got %+v", after.PendingOp)
	}
	if after.IsAuthority != before.IsAuthority || after.DeviceId != before.DeviceId || after.Server != before.Server {
		t.Fatalf("CheckExitAdvice mutated Engine status: before=%+v after=%+v", before, after)
	}
}

func TestEnterReadyAccountAdoptedOnlyEmitsSecurityStatusChanged(t *testing.T) {
	c := New()
	prev, stepCtx := c.m.sync.BeginStep()
	out := c.enterReadyAccount(stepCtx, nil, &corev1.SecurityAccountAdopted{
		Server:                "https://adopted.example.com",
		DeviceId:              "dev-adopted",
		AccountId:             "acct-adopted",
		NeedsRecoveryRotation: true,
	})
	c.m.sync.EndStep(prev, stepCtx)

	statusIdx, runtimeIdx := -1, -1
	for i, eff := range out {
		if sc := eff.GetSecurityStatusChanged(); sc != nil && sc.Status != nil {
			statusIdx = i
			if sc.Status.Server != "https://adopted.example.com" || sc.Status.DeviceId != "dev-adopted" || sc.Status.AccountId != "acct-adopted" {
				t.Fatalf("unexpected SecurityStatusChanged payload: %+v", sc.Status)
			}
		}
		if rc := eff.GetAccountRuntimeChanged(); rc != nil && rc.Active {
			runtimeIdx = i
		}
	}
	if statusIdx < 0 || runtimeIdx < 0 || statusIdx >= runtimeIdx {
		t.Fatalf("expected SecurityStatusChanged before AccountRuntimeChanged when st==nil && adopted!=nil; statusIdx=%d runtimeIdx=%d", statusIdx, runtimeIdx)
	}
}

func TestAccountSecurityAndHandoffAndAddDeviceViewModels(t *testing.T) {
	c := New()
	recBytes, devEdPub, devXPub, _, deviceID := bootstrapCoreToReady(t, c, "https://ra.example.com", "en", 5_000)
	// Produce a real persisted recovery-rotation op with the actual workflow Action constant ("recovery").
	rotOpData, err := (&security.PersistedOperation{
		ID:       "rot-op-1",
		Kind:     security.PendingOpKindState,
		Action:   security.PendingOpActionRecovery,
		Phase:    security.PendingOpPhasePrepared,
		Server:   "https://ra.example.com",
		MemberID: deviceID,
		Revision: 1,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Reload via real bootstrap storage so security.Engine builds the genuine SecurityPendingOpSummary!
	reBoot := c.Dispatch(&corev1.Event{
		NowMs: 5_050,
		Event: &corev1.Event_AppRootRetryBootstrap{AppRootRetryBootstrap: &corev1.AppRootRetryBootstrapAction{}},
	})
	bootEff := findStorageEffect(reBoot, corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP)
	c.Dispatch(&corev1.Event{
		NowMs: 5_060,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:        bootEff.OpId,
				HasDeviceEd: true,
				DeviceEdPub: devEdPub,
				HasDeviceX:  true,
				DeviceXPub:  devXPub,
				Entries: []*corev1.SecurityStorageEntry{
					{Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD, Key: "record", Revision: "1", Data: recBytes},
					{Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION, Key: "pending", Revision: "1", Data: rotOpData},
				},
			},
		},
	})

	secVM := mustViewAccountSecurity(t, c)
	if !secVM.IsManager || !secVM.HasPendingOp {
		t.Fatalf("unexpected AccountSecurityVM: %+v", secVM)
	}
	if secVM.PendingKind != corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_RECOVERY_ROTATION {
		t.Fatalf("PendingKind = %v, want RECOVERY_ROTATION", secVM.PendingKind)
	}
	if !secVM.CanDiscardPending || secVM.DiscardRequiresConfirm {
		t.Fatalf("unsigned rotation must be directly discardable: canDiscard=%v reqConfirm=%v", secVM.CanDiscardPending, secVM.DiscardRequiresConfirm)
	}

	// Renew lease while pending op exists must surface structured error banner immediately.
	c.Dispatch(&corev1.Event{
		NowMs: 5100,
		Event: &corev1.Event_AccountSecurityAction{
			AccountSecurityAction: &corev1.AccountSecurityAction{
				Kind: corev1.AccountSecurityActionKind_ACCOUNT_SECURITY_ACTION_KIND_RENEW_LEASE,
			},
		},
	})
	secVM2 := mustViewAccountSecurity(t, c)
	if secVM2.ErrorBanner == nil || secVM2.ErrorBanner.Text.GetKey() != text.KeySecurityFinishTheCurrentSecurityOperationBeforeRenewing {
		t.Fatalf("expected finish-current-op error banner, got %+v", secVM2.ErrorBanner)
	}

	// Closing AccountSecurity clears any in-memory prepared rotation code.
	sc := c.m.ensureSec()
	sc.ui.SecRotationCode = "TEMP-RECOVERY-CODE"
	sc.ui.SecRotationCodeSaved = true
	c.Dispatch(&corev1.Event{
		NowMs: 5150,
		Event: &corev1.Event_AccountSecurityClose{AccountSecurityClose: &corev1.AccountSecurityCloseAction{}},
	})
	if mustViewAccountSecurity(t, c).PreparedCode != "" {
		t.Fatal("AccountSecurityClose must clear plaintext prepared recovery code from Core memory")
	}

	// Handoff: CONFIRM_TRANSFER without prior REQUEST_TRANSFER must be ignored by Core.
	sc.ui.Status.PendingOp = nil
	sc.ui.HandoffCandidates = []*corev1.SecurityHandoffCandidate{{DeviceId: "client-2", DisplayName: "Pixel 9"}}
	c.Dispatch(&corev1.Event{
		NowMs: 5180,
		Event: &corev1.Event_HandoffAction{
			HandoffAction: &corev1.HandoffAction{
				Kind:           corev1.HandoffActionKind_HANDOFF_ACTION_KIND_CONFIRM_TRANSFER,
				TargetDeviceId: "client-2",
			},
		},
	})
	if mustViewHandoff(t, c).Busy {
		t.Fatal("CONFIRM_TRANSFER without prior REQUEST_TRANSFER must be rejected by Core")
	}
	c.Dispatch(&corev1.Event{
		NowMs: 5200,
		Event: &corev1.Event_HandoffAction{
			HandoffAction: &corev1.HandoffAction{
				Kind:           corev1.HandoffActionKind_HANDOFF_ACTION_KIND_REQUEST_TRANSFER,
				TargetDeviceId: "client-2",
			},
		},
	})
	hVM := mustViewHandoff(t, c)
	if hVM.Role != corev1.HandoffRole_HANDOFF_ROLE_OUTGOING_MANAGER || hVM.ConfirmingTargetId != "client-2" || hVM.ConfirmDialogBody == nil {
		t.Fatalf("unexpected HandoffVM: %+v", hVM)
	}

	// AddDevice: WAITING_JOINER rejects SET_SAS_CONFIRMED; entering COMPARE_SAS clears SAS confirmed;
	// joiner metadata recomputes default candidate selection until user manually toggles.
	sc.ui.AddOpen = true
	sc.ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_WAITING_JOINER
	sc.ui.AddPairingID = "pair-1"
	c.Dispatch(&corev1.Event{
		NowMs: 5250,
		Event: &corev1.Event_AddDeviceAction{
			AddDeviceAction: &corev1.AddDeviceAction{
				Kind: corev1.AddDeviceActionKind_ADD_DEVICE_ACTION_KIND_SET_SAS_CONFIRMED,
				Flag: true,
			},
		},
	})
	if mustViewAddDevice(t, c).SasConfirmedChecked {
		t.Fatal("WAITING_JOINER must reject SET_SAS_CONFIRMED")
	}
	c.OnSecurityPollUpdate(security.PollUpdate{
		Kind:      security.PollInitiatorJoined,
		PairingID: "pair-1",
		Pairing: &corev1.SecurityPairingInfo{
			PairingId:  "pair-1",
			Joined:     true,
			SasEmoji:   []string{"🦊", "🐳", "🌿", "⭐"},
			JoinerKind: v1.DeviceKind_DEVICE_KIND_CLIENT,
			ReplacementCandidates: []*corev1.SecurityPairingReplacementCandidate{{
				DeviceId:        "old-client-1",
				DisplayName:     "Old Chrome",
				DefaultSelected: false,
			}},
		},
	})
	// Metadata arrives while user has NOT manually toggled -> default selection updates to true!
	c.OnSecurityPollUpdate(security.PollUpdate{
		Kind:      security.PollInitiatorJoinerMeta,
		PairingID: "pair-1",
		MetaDone:  true,
		Pairing: &corev1.SecurityPairingInfo{
			PairingId:  "pair-1",
			Joined:     true,
			SasEmoji:   []string{"🦊", "🐳", "🌿", "⭐"},
			JoinerKind: v1.DeviceKind_DEVICE_KIND_CLIENT,
			ReplacementCandidates: []*corev1.SecurityPairingReplacementCandidate{{
				DeviceId:        "old-client-1",
				DisplayName:     "Old Chrome",
				DefaultSelected: true,
			}},
		},
	})
	addVM := mustViewAddDevice(t, c)
	if addVM.Step != corev1.AddDeviceStep_ADD_DEVICE_STEP_COMPARE_SAS || len(addVM.SasEmoji) != 4 || addVM.SasConfirmedChecked {
		t.Fatalf("unexpected AddDeviceVM after joiner poll: %+v", addVM)
	}
	if len(addVM.ReplacementCandidates) != 1 || !addVM.ReplacementCandidates[0].Selected {
		t.Fatalf("expected default-selected candidate to recompute on joiner meta, got %+v", addVM.ReplacementCandidates)
	}
	if addVM.CanApprove {
		t.Fatal("CanApprove must be false until SAS confirmed checkbox is checked")
	}
	c.Dispatch(&corev1.Event{
		NowMs: 5300,
		Event: &corev1.Event_AddDeviceAction{
			AddDeviceAction: &corev1.AddDeviceAction{
				Kind: corev1.AddDeviceActionKind_ADD_DEVICE_ACTION_KIND_SET_SAS_CONFIRMED,
				Flag: true,
			},
		},
	})
	if !mustViewAddDevice(t, c).CanApprove {
		t.Fatal("CanApprove must be true after SAS confirmed")
	}

	// Non-manager AddDevice shows NOT_MANAGER with hint.
	sc.ui.Status.IsAuthority = false
	nonMgrAdd := mustViewAddDevice(t, c)
	if nonMgrAdd.Step != corev1.AddDeviceStep_ADD_DEVICE_STEP_NOT_MANAGER || nonMgrAdd.NonManagerHint == nil {
		t.Fatalf("non-manager AddDeviceVM = %+v", nonMgrAdd)
	}

	// BuildJoinBannerState unit coverage (no string matching).
	bClock := screens.BuildJoinBannerState(nil, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CLOCK_SKEWED, 0, true, false, "retry")
	if bClock.Action != corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_RETRY || bClock.Tone != corev1.SecurityBannerTone_SECURITY_BANNER_TONE_WARN {
		t.Fatalf("clock banner = %+v", bClock)
	}
	bCap := screens.BuildJoinBannerState(nil, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CAPACITY, 0, false, false, "")
	if bCap.Action != corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_NONE {
		t.Fatalf("capacity banner = %+v", bCap)
	}
}
