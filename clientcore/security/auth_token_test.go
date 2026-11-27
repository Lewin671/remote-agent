package security_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/entropy"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

type authSinkRecorder struct {
	results map[uint64]security.AuthTokenResult
}

func (r *authSinkRecorder) OnOpenBatchDone(uint64, []security.OpenResult) {}
func (r *authSinkRecorder) OnSealEnvelopeDone(uint64, security.SealResult, error) {
}
func (r *authSinkRecorder) OnSyncStatesDone(uint64, error) {}
func (r *authSinkRecorder) OnAuthTokenDone(tag uint64, res security.AuthTokenResult) {
	if r.results == nil {
		r.results = make(map[uint64]security.AuthTokenResult)
	}
	r.results[tag] = res
}
func (r *authSinkRecorder) OnKeySignDone(uint64, []byte, error)  {}
func (r *authSinkRecorder) OnKeyAgreeDone(uint64, []byte, error) {}
func (r *authSinkRecorder) OnSecurityStateChanged()              {}

func mustMarshalStd(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func newAuthTestEngine(t *testing.T) (*security.Engine, *authSinkRecorder, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen ed25519: %v", err)
	}
	eng := security.NewEngine(&entropy.Pool{}, 17)
	eng.Configure(&corev1.SecurityConfigEvent{
		Account:  "acct-1",
		DeviceId: "dev-1",
		Server:   "https://relay.example.com",
	})
	sink := &authSinkRecorder{}
	eng.SetAsyncSink(sink)
	return eng, sink, pub, priv
}

func findRelayRpc(effs []*corev1.Effect, method corev1.RelayRpcMethod) *corev1.RelayRpcEffect {
	for _, eff := range effs {
		if r := eff.GetRelayRpc(); r != nil && r.Method == method {
			return r
		}
	}
	return nil
}

func findStorageReadBootstrap(effs []*corev1.Effect) *corev1.SecurityStorageEffect {
	for _, eff := range effs {
		if st := eff.GetSecurityStorage(); st != nil && st.Op == corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP {
			return st
		}
	}
	return nil
}

func findKeySign(effs []*corev1.Effect) *corev1.SecurityKeySignEffect {
	for _, eff := range effs {
		if ks := eff.GetSecurityKeySign(); ks != nil {
			return ks
		}
	}
	return nil
}

func findTokenUpdated(effs []*corev1.Effect) *corev1.SecurityTokenUpdatedEffect {
	for _, eff := range effs {
		if tu := eff.GetSecurityTokenUpdated(); tu != nil {
			return tu
		}
	}
	return nil
}

func TestAuthTokenChallengeSignCrossVerifyAnd24hRefreshMargin(t *testing.T) {
	eng, sink, pub, priv := newAuthTestEngine(t)
	const dayMs int64 = 24 * 60 * 60 * 1000
	now := int64(1_700_000_000_000)

	// 1. Cold start: READ_BOOTSTRAP returns no saved token -> GetChallenge -> KeySign -> Login.
	effs := eng.EnsureAuthTokenTag(now, false, 10_000, 1)
	boot := findStorageReadBootstrap(effs)
	if boot == nil {
		t.Fatalf("expected READ_BOOTSTRAP warm storage effect, got %d effects", len(effs))
	}
	effs = eng.HandleStorageResult(now, &corev1.SecurityStorageResultEvent{OpId: boot.OpId})
	chalRpc := findRelayRpc(effs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE)
	if chalRpc == nil || chalRpc.TimeoutMs != 10_000 {
		t.Fatalf("expected GET_CHALLENGE RelayRpc with timeout=10000, got %+v", chalRpc)
	}
	var chalReq stdv1.GetChallengeRequest
	if err := proto.Unmarshal(chalRpc.RequestBytes, &chalReq); err != nil || chalReq.DeviceId != "dev-1" {
		t.Fatalf("unexpected GetChallengeRequest: %+v err=%v", &chalReq, err)
	}

	challenge := make([]byte, 32)
	for i := range challenge {
		challenge[i] = byte(i + 1)
	}
	effs = eng.HandleRelayRpcResult(now, &corev1.RelayRpcResultEvent{
		OpId:          chalRpc.OpId,
		ResponseBytes: mustMarshalStd(t, &stdv1.GetChallengeResponse{Challenge: challenge}),
	})
	signEff := findKeySign(effs)
	if signEff == nil || signEff.KeyKind != corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED {
		t.Fatalf("expected DEVICE_ED SecurityKeySignEffect, got %+v", signEff)
	}
	// Cross-verify exact login challenge message prefix: "remote-agent/v1/login\x00" || challenge.
	wantMsg := append([]byte("remote-agent/v1/login\x00"), challenge...)
	if !bytes.Equal(signEff.Message, wantMsg) {
		t.Fatalf("login sign message mismatch: got %x want %x", signEff.Message, wantMsg)
	}
	sig := ed25519.Sign(priv, signEff.Message)
	if !ed25519.Verify(pub, wantMsg, sig) {
		t.Fatal("ed25519 verify failed")
	}

	effs = eng.HandleKeySigned(now, &corev1.SecurityKeySignedEvent{OpId: signEff.OpId, Signature: sig})
	loginRpc := findRelayRpc(effs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN)
	if loginRpc == nil {
		t.Fatalf("expected LOGIN RelayRpc")
	}
	var loginReq stdv1.LoginRequest
	if err := proto.Unmarshal(loginRpc.RequestBytes, &loginReq); err != nil {
		t.Fatalf("unmarshal LoginRequest: %v", err)
	}
	if loginReq.DeviceId != "dev-1" || loginReq.ProtocolVersion != 17 || !bytes.Equal(loginReq.Challenge, challenge) || !bytes.Equal(loginReq.Signature, sig) {
		t.Fatalf("unexpected LoginRequest: %+v", &loginReq)
	}
	// Verify server-side verification of LoginRequest signature succeeds against pubkey.
	if !ed25519.Verify(pub, append([]byte("remote-agent/v1/login\x00"), loginReq.Challenge...), loginReq.Signature) {
		t.Fatal("server-side verification of LoginRequest signature failed")
	}

	expiresAt := now + 7*dayMs
	effs = eng.HandleRelayRpcResult(now, &corev1.RelayRpcResultEvent{
		OpId:          loginRpc.OpId,
		ResponseBytes: mustMarshalStd(t, &stdv1.LoginResponse{Token: "tok-1", ExpiresMs: expiresAt}),
	})
	tu := findTokenUpdated(effs)
	if tu == nil || tu.Cleared || tu.Token != "tok-1" || tu.ExpiresMs != expiresAt {
		t.Fatalf("expected SecurityTokenUpdatedEffect with tok-1, got %+v", tu)
	}
	if res := sink.results[1]; res.Token != "tok-1" || res.ExpiresMs != expiresAt || res.Removed || res.Error != "" {
		t.Fatalf("unexpected sink result 1: %+v", res)
	}

	// 2. At/outside 24h margin (expiresAt >= now + dayMs): reuses cached token with 0 RPCs (matches main).
	effs = eng.EnsureAuthTokenTag(expiresAt-dayMs, false, 5_000, 2)
	if len(effs) != 0 {
		t.Fatalf("expected 0 effects at exact 24h boundary (expires >= now+dayMs), got %d", len(effs))
	}
	if sink.results[2].Token != "tok-1" {
		t.Fatalf("expected cached tok-1, got %+v", sink.results[2])
	}

	// 3. Inside 24h refresh margin (expiresAt < now + dayMs): clears stale token and starts refresh.
	effs = eng.EnsureAuthTokenTag(expiresAt-dayMs+1, false, 5_000, 3)
	if tu := findTokenUpdated(effs); tu == nil || !tu.Cleared {
		t.Fatalf("expected cleared SecurityTokenUpdatedEffect inside 24h margin, got %+v", tu)
	}
	if findRelayRpc(effs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE) == nil {
		t.Fatalf("expected GET_CHALLENGE refresh inside 24h margin")
	}

	// 4. Token with expiresMs == 0 must NOT be treated as permanently valid.
	eng0, _, _, _ := newAuthTestEngine(t)
	eng0.SetAuthToken("tok-zero-exp", 0)
	effs0 := eng0.EnsureAuthTokenTag(now, false, 5_000, 4)
	if findRelayRpc(effs0, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE) == nil {
		t.Fatalf("expected GET_CHALLENGE refresh when tokenExpiresMs == 0, got %+v", effs0)
	}
}

func TestAuthTokenSingleFlightAndNetworkReset(t *testing.T) {
	eng, sink, _, priv := newAuthTestEngine(t)
	now := int64(1_700_000_000_000)

	// Warm storage empty first via forceLogin=true; 3 concurrent callers coalesce into 1 GET_CHALLENGE.
	effs1 := eng.EnsureAuthTokenTag(now, true, 8_000, 10)
	effs2 := eng.EnsureAuthTokenTag(now, false, 8_000, 11)
	effs3 := eng.EnsureAuthTokenTag(now, false, 8_000, 12)
	chal1 := findRelayRpc(effs1, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE)
	if chal1 == nil {
		t.Fatal("expected initial GET_CHALLENGE")
	}
	if len(effs2) != 0 || len(effs3) != 0 {
		t.Fatalf("expected single-flight coalescing with 0 extra effects, got %d and %d", len(effs2), len(effs3))
	}

	// Network generation bump cancels chal1 and immediately issues chal2 on the new network.
	eng.SetNetworkGeneration(2)
	resetEffs := eng.DrainEffects()
	var canceledID uint64
	for _, eff := range resetEffs {
		if c := eff.GetRelayRpcCancel(); c != nil {
			canceledID = c.OpId
		}
	}
	if canceledID != chal1.OpId {
		t.Fatalf("expected RelayRpcCancelEffect for op %d, got %d", chal1.OpId, canceledID)
	}
	chal2 := findRelayRpc(resetEffs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE)
	if chal2 == nil || chal2.OpId == chal1.OpId {
		t.Fatalf("expected fresh GET_CHALLENGE after network reset, got %+v", chal2)
	}

	// Complete chal2 -> sign -> login -> all 3 coalesced waiters receive the same token.
	effs := eng.HandleRelayRpcResult(now, &corev1.RelayRpcResultEvent{
		OpId:          chal2.OpId,
		ResponseBytes: mustMarshalStd(t, &stdv1.GetChallengeResponse{Challenge: bytes.Repeat([]byte{0x42}, 32)}),
	})
	signEff := findKeySign(effs)
	effs = eng.HandleKeySigned(now, &corev1.SecurityKeySignedEvent{OpId: signEff.OpId, Signature: ed25519.Sign(priv, signEff.Message)})
	loginRpc := findRelayRpc(effs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN)
	eng.HandleRelayRpcResult(now, &corev1.RelayRpcResultEvent{
		OpId:          loginRpc.OpId,
		ResponseBytes: mustMarshalStd(t, &stdv1.LoginResponse{Token: "tok-coalesced", ExpiresMs: now + 7*24*3600_000}),
	})
	for _, tag := range []uint64{10, 11, 12} {
		if sink.results[tag].Token != "tok-coalesced" {
			t.Fatalf("waiter %d did not receive coalesced token: %+v", tag, sink.results[tag])
		}
	}
}

func TestAuthTokenStaleChallengeRetryOnceAndRemovalOnSecondDenial(t *testing.T) {
	eng, sink, _, priv := newAuthTestEngine(t)
	now := int64(1_700_000_000_000)

	// Start login (forceLogin skips warm storage).
	effs := eng.EnsureAuthTokenTag(now, true, 5_000, 20)
	chal1 := findRelayRpc(effs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE)
	effs = eng.HandleRelayRpcResult(now, &corev1.RelayRpcResultEvent{
		OpId:          chal1.OpId,
		ResponseBytes: mustMarshalStd(t, &stdv1.GetChallengeResponse{Challenge: bytes.Repeat([]byte{1}, 32)}),
	})
	sign1 := findKeySign(effs)
	effs = eng.HandleKeySigned(now, &corev1.SecurityKeySignedEvent{OpId: sign1.OpId, Signature: ed25519.Sign(priv, sign1.Message)})
	login1 := findRelayRpc(effs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN)

	// First denial (e.g. server forgot challenge): must retry once with a fresh GetChallenge.
	detailBytes := mustMarshalStd(t, &stdv1.RelayErrorDetail{Reason: stdv1.RelayErrorReason_RELAY_ERROR_REASON_LOGIN_FAILED})
	effs = eng.HandleRelayRpcResult(now, &corev1.RelayRpcResultEvent{
		OpId:              login1.OpId,
		ConnectCode:       7, // permission_denied
		DiagnosticMessage: "login failed",
		ErrorDetails: []*corev1.RpcErrorDetailBytes{
			{TypeName: "remoteagent.v1.RelayErrorDetail", Value: detailBytes},
		},
	})
	if _, done := sink.results[20]; done {
		t.Fatal("should NOT fail after first stale-challenge denial")
	}
	chal2 := findRelayRpc(effs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE)
	if chal2 == nil {
		t.Fatal("expected automatic retry GET_CHALLENGE after first login denial")
	}
	effs = eng.HandleRelayRpcResult(now, &corev1.RelayRpcResultEvent{
		OpId:          chal2.OpId,
		ResponseBytes: mustMarshalStd(t, &stdv1.GetChallengeResponse{Challenge: bytes.Repeat([]byte{2}, 32)}),
	})
	sign2 := findKeySign(effs)
	effs = eng.HandleKeySigned(now, &corev1.SecurityKeySignedEvent{OpId: sign2.OpId, Signature: ed25519.Sign(priv, sign2.Message)})
	login2 := findRelayRpc(effs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN)

	// Second denial: must report Removed=true and emit cleared SecurityTokenUpdatedEffect.
	effs = eng.HandleRelayRpcResult(now, &corev1.RelayRpcResultEvent{
		OpId:              login2.OpId,
		ConnectCode:       7,
		DiagnosticMessage: "login failed",
		ErrorDetails: []*corev1.RpcErrorDetailBytes{
			{TypeName: "remoteagent.v1.RelayErrorDetail", Value: detailBytes},
		},
	})
	res := sink.results[20]
	if !res.Removed || res.ErrorCode != "permission_denied" {
		t.Fatalf("expected Removed=true after second denial, got %+v", res)
	}
	if tu := findTokenUpdated(effs); tu == nil || !tu.Cleared {
		t.Fatalf("expected cleared SecurityTokenUpdatedEffect on removal, got %+v", tu)
	}
}

func TestAuthTokenWarmHydratesValidPersistedTokenWithZeroRPCs(t *testing.T) {
	eng, sink, _, _ := newAuthTestEngine(t)
	const dayMs int64 = 24 * 60 * 60 * 1000
	now := int64(1_700_000_000_000)

	effs := eng.EnsureAuthTokenTag(now, false, 5_000, 30)
	boot := findStorageReadBootstrap(effs)
	if boot == nil {
		t.Fatal("expected READ_BOOTSTRAP")
	}
	effs = eng.HandleStorageResult(now, &corev1.SecurityStorageResultEvent{
		OpId:                boot.OpId,
		LoginToken:          "saved-tok",
		LoginTokenExpiresMs: now + 3*dayMs,
	})
	if len(effs) != 0 {
		t.Fatalf("expected 0 network effects when persisted token is valid, got %d", len(effs))
	}
	if res := sink.results[30]; res.Token != "saved-tok" || res.ExpiresMs != now+3*dayMs {
		t.Fatalf("expected hydrated saved-tok, got %+v", res)
	}
}

func TestAuthTokenNetworkChangeDiscardsStaleSignAndError(t *testing.T) {
	for _, staleErr := range []bool{false, true} {
		name := "stale_sig_success"
		if staleErr {
			name = "stale_sig_error"
		}
		t.Run(name, func(t *testing.T) {
			eng, sink, pub, priv := newAuthTestEngine(t)
			now := int64(1_700_000_000_000)

			// Attempt 1: GetChallenge #1 -> SignEffect #1.
			effs := eng.EnsureAuthTokenTag(now, true, 8_000, 40)
			chal1Rpc := findRelayRpc(effs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE)
			if chal1Rpc == nil {
				t.Fatal("expected initial GET_CHALLENGE")
			}
			chal1Bytes := bytes.Repeat([]byte{0x11}, 32)
			effs = eng.HandleRelayRpcResult(now+1, &corev1.RelayRpcResultEvent{
				OpId:          chal1Rpc.OpId,
				ResponseBytes: mustMarshalStd(t, &stdv1.GetChallengeResponse{Challenge: chal1Bytes}),
			})
			sign1 := findKeySign(effs)
			if sign1 == nil {
				t.Fatal("expected SignEffect #1")
			}

			// Network changes while SignEffect #1 is in flight -> GetChallenge #2 -> SignEffect #2.
			eng.SetNetworkGeneration(2)
			resetEffs := eng.DrainEffects()
			chal2Rpc := findRelayRpc(resetEffs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE)
			if chal2Rpc == nil {
				t.Fatal("expected fresh GET_CHALLENGE after network change")
			}
			chal2Bytes := bytes.Repeat([]byte{0x22}, 32)
			effs = eng.HandleRelayRpcResult(now+2, &corev1.RelayRpcResultEvent{
				OpId:          chal2Rpc.OpId,
				ResponseBytes: mustMarshalStd(t, &stdv1.GetChallengeResponse{Challenge: chal2Bytes}),
			})
			sign2 := findKeySign(effs)
			if sign2 == nil || sign2.OpId == sign1.OpId {
				t.Fatalf("expected distinct SignEffect #2, got %+v", sign2)
			}

			// Deliver stale SignEffect #1 FIRST (either signed or failed): must be silently discarded!
			var staleEv *corev1.SecurityKeySignedEvent
			if staleErr {
				staleEv = &corev1.SecurityKeySignedEvent{OpId: sign1.OpId, Error: "canceled"}
			} else {
				staleEv = &corev1.SecurityKeySignedEvent{OpId: sign1.OpId, Signature: ed25519.Sign(priv, sign1.Message)}
			}
			staleEffs := eng.HandleKeySigned(now+3, staleEv)
			if findRelayRpc(staleEffs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN) != nil {
				t.Fatal("stale sign #1 must NOT trigger LOGIN RPC")
			}
			if _, resolved := sink.results[40]; resolved {
				t.Fatalf("stale sign #1 must NOT resolve or fail login waiter, got %+v", sink.results[40])
			}

			// Deliver active SignEffect #2: must emit LOGIN with challenge #2 + valid sig #2 and succeed.
			effs = eng.HandleKeySigned(now+4, &corev1.SecurityKeySignedEvent{
				OpId:      sign2.OpId,
				Signature: ed25519.Sign(priv, sign2.Message),
			})
			loginRpc := findRelayRpc(effs, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN)
			if loginRpc == nil {
				t.Fatal("expected LOGIN RPC after active sign #2")
			}
			var loginReq stdv1.LoginRequest
			if err := proto.Unmarshal(loginRpc.RequestBytes, &loginReq); err != nil {
				t.Fatalf("unmarshal LoginRequest: %v", err)
			}
			if !bytes.Equal(loginReq.Challenge, chal2Bytes) {
				t.Fatalf("LoginRequest used wrong challenge: got %x want %x", loginReq.Challenge, chal2Bytes)
			}
			if !ed25519.Verify(pub, append([]byte("remote-agent/v1/login\x00"), chal2Bytes...), loginReq.Signature) {
				t.Fatal("LoginRequest signature does not verify against challenge #2")
			}
			eng.HandleRelayRpcResult(now+5, &corev1.RelayRpcResultEvent{
				OpId:          loginRpc.OpId,
				ResponseBytes: mustMarshalStd(t, &stdv1.LoginResponse{Token: "tok-net2", ExpiresMs: now + 7*24*3600_000}),
			})
			if res := sink.results[40]; res.Token != "tok-net2" || res.Removed || res.Error != "" {
				t.Fatalf("expected tok-net2, got %+v", res)
			}
		})
	}
}
