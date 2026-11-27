package app

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"slices"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/internal/test/testrelay"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/text"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

func sealDaemonWireWithSeed(
	t *testing.T,
	signer appTestActor,
	dom devicesec.Hash,
	st devicesec.State,
	sec, seed, plain []byte,
	purpose devicesec.Purpose,
	machineID, sessionID, reqID string,
	seq uint64,
) *v1.Envelope {
	t.Helper()
	sh, _ := st.Hash()
	hdr := devicesec.Header{
		Account:      st.Core.Account,
		Domain:       dom,
		Epoch:        st.Core.Epoch,
		State:        sh,
		ContentEpoch: st.Core.ContentEpoch,
		Sender:       signer.id,
		Kind:         purpose.Kind(),
		Purpose:      purpose,
		Machine:      machineID,
		Session:      sessionID,
		Request:      reqID,
		Seq:          seq,
	}
	hb, key, nonce, err := devicesec.PrepareCiphertext(sec, seed, hdr, len(plain), rand.Reader)
	if err != nil {
		t.Fatalf("PrepareCiphertext: %v", err)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		t.Fatalf("cipher.NewGCM: %v", err)
	}
	ct, om, err := devicesec.AssembleCiphertext(hb, nonce, aead.Seal(nil, nonce, plain, hb))
	if err != nil {
		t.Fatalf("AssembleCiphertext: %v", err)
	}
	ct.Origin = ed25519.Sign(signer.ed, om)
	env, err := security.ToProtoEnvelope(ct)
	if err != nil {
		t.Fatalf("ToProtoEnvelope: %v", err)
	}
	return env
}

func sealDaemonImageEnvelope(
	t *testing.T,
	dom devicesec.Hash,
	st devicesec.State,
	sec, seed []byte,
	signer appTestActor,
	machineID, sid, imageID string,
	purpose devicesec.Purpose,
	format stdv1.ImageFormat,
	w, h int32,
	data []byte,
) *v1.Envelope {
	t.Helper()
	plain, err := proto.Marshal(&stdv1.ImagePayload{
		Format: format,
		Width:  w,
		Height: h,
		Data:   data,
	})
	if err != nil {
		t.Fatalf("marshal ImagePayload: %v", err)
	}
	seq := uint64(0)
	reqID := imageID
	if purpose == devicesec.Event {
		seq = 1
		reqID = ""
	}
	return sealDaemonWireWithSeed(t, signer, dom, st, sec, seed, plain, purpose, machineID, sid, reqID, seq)
}

func seedSessionInCore(
	t *testing.T,
	c *Core,
	dom devicesec.Hash,
	st devicesec.State,
	sec []byte,
	machine appTestActor,
	sid string,
	seed []byte,
	repls []*stdv1.MachineReplacementProof,
	nowMs int64,
) {
	t.Helper()
	// SessionKey plaintext is the raw 32-byte seed (devicesec requires Seq >= 1).
	keyEnv := sealDaemonWireWithSeed(t, machine, dom, st, sec, nil, seed, devicesec.SessionKey, machine.id, sid, "", 1)
	metaBytes, err := proto.Marshal(&stdv1.Session{
		Id:                  sid,
		MachineReplacements: repls,
	})
	if err != nil {
		t.Fatalf("marshal Session: %v", err)
	}
	metaEnv := sealDaemonWireWithSeed(t, machine, dom, st, sec, nil, metaBytes, devicesec.SessionMeta, machine.id, sid, "", 1)
	c.Dispatch(&corev1.Event{
		NowMs:  nowMs,
		MonoMs: 1100,
		Event: &corev1.Event_CacheBootstrapLoaded{
			CacheBootstrapLoaded: &corev1.CacheBootstrapLoadedEvent{
				OpId: 1,
				RawSessions: testrelay.MustRawSlice([]*v1.RemoteSession{{
					SessionId: sid,
					MachineId: machine.id,
					Key:       keyEnv,
					Meta:      metaEnv,
				}}),
			},
		},
	})
}

func runTestCryptoBatch(t *testing.T, cb *corev1.SecurityCryptoBatchEffect) *corev1.SecurityCryptoBatchDoneEvent {
	t.Helper()
	results := make([]*corev1.SecurityCryptoBatchResult, len(cb.Items))
	for i, item := range cb.Items {
		switch k := item.Kind.(type) {
		case *corev1.SecurityCryptoBatchItem_AeadOpen:
			blk, err := aes.NewCipher(k.AeadOpen.Key)
			if err != nil {
				results[i] = &corev1.SecurityCryptoBatchResult{Ok: false}
				continue
			}
			aead, err := cipher.NewGCM(blk)
			if err != nil {
				results[i] = &corev1.SecurityCryptoBatchResult{Ok: false}
				continue
			}
			pt, err := aead.Open(nil, k.AeadOpen.Nonce, k.AeadOpen.Ciphertext, k.AeadOpen.Aad)
			if err != nil {
				results[i] = &corev1.SecurityCryptoBatchResult{Ok: false}
				continue
			}
			results[i] = &corev1.SecurityCryptoBatchResult{Ok: true, Data: pt}
		case *corev1.SecurityCryptoBatchItem_AeadSeal:
			blk, err := aes.NewCipher(k.AeadSeal.Key)
			if err != nil {
				results[i] = &corev1.SecurityCryptoBatchResult{Ok: false}
				continue
			}
			aead, err := cipher.NewGCM(blk)
			if err != nil {
				results[i] = &corev1.SecurityCryptoBatchResult{Ok: false}
				continue
			}
			ct := aead.Seal(nil, k.AeadSeal.Nonce, k.AeadSeal.Plaintext, k.AeadSeal.Aad)
			results[i] = &corev1.SecurityCryptoBatchResult{Ok: true, Data: ct}
		default:
			t.Fatalf("unexpected crypto batch item %T", k)
		}
	}
	return &corev1.SecurityCryptoBatchDoneEvent{OpId: cb.OpId, Results: results}
}

func marshalStdGetImageResp(t *testing.T, env *v1.Envelope, expired bool) []byte {
	t.Helper()
	var stdEnv *stdv1.Envelope
	if env != nil {
		raw, err := env.Marshal()
		if err != nil {
			t.Fatalf("marshal lite Envelope: %v", err)
		}
		stdEnv = &stdv1.Envelope{}
		if err := proto.Unmarshal(raw, stdEnv); err != nil {
			t.Fatalf("unmarshal std Envelope: %v", err)
		}
	}
	b, err := proto.Marshal(&stdv1.GetImageResponse{Image: stdEnv, Expired: expired})
	if err != nil {
		t.Fatalf("marshal GetImageResponse: %v", err)
	}
	return b
}

func runImageFetchAndDrain(
	t *testing.T,
	c *Core,
	act *corev1.SessionFetchImageAction,
	nowMs int64,
	respond func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent,
) (*corev1.SessionImageLoadedEffect, int, int) {
	t.Helper()
	b := c.Dispatch(&corev1.Event{
		NowMs:  nowMs,
		MonoMs: 2000,
		Event:  &corev1.Event_SessionFetchImage{SessionFetchImage: act},
	})
	var got *corev1.SessionImageLoadedEffect
	rpcCount := 0
	cryptoBatches := 0
	queue := b.Effects
	for len(queue) > 0 {
		eff := queue[0]
		queue = queue[1:]
		switch k := eff.Effect.(type) {
		case *corev1.Effect_SessionImageLoaded:
			got = k.SessionImageLoaded
		case *corev1.Effect_RelayRpc:
			rpcCount++
			if respond == nil {
				t.Fatalf("unexpected RelayRpcEffect %v", k.RelayRpc.Method)
			}
			res := respond(k.RelayRpc)
			next := c.Dispatch(&corev1.Event{
				NowMs:  nowMs + 5,
				MonoMs: 2005,
				Event:  &corev1.Event_RelayRpcResult{RelayRpcResult: res},
			})
			queue = append(queue, next.Effects...)
		case *corev1.Effect_SecurityCryptoBatch:
			cryptoBatches++
			next := c.Dispatch(&corev1.Event{
				NowMs:  nowMs + 10,
				MonoMs: 2010,
				Event: &corev1.Event_SecurityCryptoBatchDone{
					SecurityCryptoBatchDone: runTestCryptoBatch(t, k.SecurityCryptoBatch),
				},
			})
			queue = append(queue, next.Effects...)
		}
	}
	return got, rpcCount, cryptoBatches
}

func TestSessionImageFetchVerifySniffAndCache(t *testing.T) {
	c, _, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
	seed := bytes.Repeat([]byte{0x7A}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-img", seed, nil, nowMs+50)

	cases := []struct {
		name   string
		format stdv1.ImageFormat
		mime   string
		bytes  []byte
	}{
		{"png", stdv1.ImageFormat_IMAGE_FORMAT_PNG, "image/png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2}},
		{"jpeg", stdv1.ImageFormat_IMAGE_FORMAT_JPEG, "image/jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0, 16}},
		{"webp", stdv1.ImageFormat_IMAGE_FORMAT_WEBP, "image/webp", []byte{'R', 'I', 'F', 'F', 12, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' '}},
		{"gif", stdv1.ImageFormat_IMAGE_FORMAT_GIF, "image/gif", []byte{'G', 'I', 'F', '8', '9', 'a', 1, 0, 1, 0}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := sealDaemonImageEnvelope(t, dom, s1, sec, seed, trusted, trusted.id, "s-img", "img-"+tc.name, devicesec.Image, tc.format, 640, 480, tc.bytes)
			respBytes := marshalStdGetImageResp(t, env, false)
			got, rpcs, _ := runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{
				OpId:      uint64(10 + i),
				SessionId: "s-img",
				ImageId:   "img-" + tc.name,
			}, nowMs+100, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
				if rpc.Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_IMAGE {
					t.Fatalf("method = %v, want GET_IMAGE", rpc.Method)
				}
				return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: respBytes}
			})
			if rpcs != 1 || got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_OK {
				t.Fatalf("expected OK via 1 RPC, got rpcs=%d res=%+v", rpcs, got)
			}
			if got.MimeType != tc.mime || !bytes.Equal(got.Data, tc.bytes) || got.Width != 640 || got.Height != 480 {
				t.Fatalf("unexpected decoded image: mime=%q w=%d h=%d len=%d", got.MimeType, got.Width, got.Height, len(got.Data))
			}
			if len(got.CacheEnvelopeBytes) == 0 {
				t.Fatal("expected CacheEnvelopeBytes for freshly fetched trusted machine image")
			}

			// Re-opening from opaque cached envelope performs zero RPCs and does not re-emit CacheEnvelopeBytes.
			cached, cachedRpcs, _ := runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{
				OpId:           uint64(100 + i),
				SessionId:      "s-img",
				ImageId:        "img-" + tc.name,
				CachedEnvelope: got.CacheEnvelopeBytes,
			}, nowMs+120, nil)
			if cachedRpcs != 0 || cached == nil || cached.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_OK {
				t.Fatalf("expected cached OK with 0 RPCs, got rpcs=%d res=%+v", cachedRpcs, cached)
			}
			if len(cached.CacheEnvelopeBytes) != 0 {
				t.Fatal("cached image open must not re-emit CacheEnvelopeBytes")
			}
		})
	}
}

func TestSessionImageWebCryptoBatchOver16KiB(t *testing.T) {
	c, _, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, false)
	seed := bytes.Repeat([]byte{0x6B}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-big", seed, nil, nowMs+50)

	bigPNG := append([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{0xCD}, 24*1024)...)
	env := sealDaemonImageEnvelope(t, dom, s1, sec, seed, trusted, trusted.id, "s-big", "img-big", devicesec.Image, stdv1.ImageFormat_IMAGE_FORMAT_PNG, 1024, 768, bigPNG)
	respBytes := marshalStdGetImageResp(t, env, false)

	got, rpcs, batches := runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{
		OpId:      1,
		SessionId: "s-big",
		ImageId:   "img-big",
	}, nowMs+100, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
		return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: respBytes}
	})
	if rpcs != 1 || batches != 1 {
		t.Fatalf("expected 1 RPC and 1 WebCrypto CryptoBatch offload, got rpcs=%d batches=%d", rpcs, batches)
	}
	if got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_OK || got.MimeType != "image/png" || !bytes.Equal(got.Data, bigPNG) {
		t.Fatalf("unexpected big image result: %+v", got)
	}
}

func TestSessionImageNegativeCryptoSniffAndUntrustedCache(t *testing.T) {
	c, phone, trusted, _, secStore, dom, s1, sec, s2Raw, lease2Raw, nowMs := setupOpaqueCacheTestCore(t, true)
	seed := bytes.Repeat([]byte{0x5C}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-neg", seed, nil, nowMs+50)
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}

	// 1. SVG claiming PNG -> BAD format_mismatch.
	svgEnv := sealDaemonImageEnvelope(t, dom, s1, sec, seed, trusted, trusted.id, "s-neg", "svg", devicesec.Image, stdv1.ImageFormat_IMAGE_FORMAT_PNG, 10, 10, []byte("<svg onload=x>"))
	got, _, _ := runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{OpId: 1, SessionId: "s-neg", ImageId: "svg"}, nowMs+100, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
		return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: marshalStdGetImageResp(t, svgEnv, false)}
	})
	if got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_BAD || got.ErrorCode != "format_mismatch" || got.GetErrorText().GetKey() != text.KeyErrorsImageContentDoesNotMatchItsFormat {
		t.Fatalf("expected format_mismatch BAD for SVG, got %+v", got)
	}

	// 2. PNG bytes claiming JPEG -> BAD format_mismatch.
	mismatchEnv := sealDaemonImageEnvelope(t, dom, s1, sec, seed, trusted, trusted.id, "s-neg", "mismatch", devicesec.Image, stdv1.ImageFormat_IMAGE_FORMAT_JPEG, 10, 10, png)
	got, _, _ = runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{OpId: 2, SessionId: "s-neg", ImageId: "mismatch"}, nowMs+100, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
		return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: marshalStdGetImageResp(t, mismatchEnv, false)}
	})
	if got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_BAD || got.ErrorCode != "format_mismatch" {
		t.Fatalf("expected format_mismatch BAD for PNG-as-JPEG, got %+v", got)
	}

	// 3. Tampered ciphertext -> BAD.
	tampered := sealDaemonImageEnvelope(t, dom, s1, sec, seed, trusted, trusted.id, "s-neg", "tamper", devicesec.Image, stdv1.ImageFormat_IMAGE_FORMAT_PNG, 10, 10, png)
	tampered.Ciphertext[0] ^= 0xff
	got, _, _ = runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{OpId: 3, SessionId: "s-neg", ImageId: "tamper"}, nowMs+100, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
		return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: marshalStdGetImageResp(t, tampered, false)}
	})
	if got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_BAD {
		t.Fatalf("expected BAD for tampered ciphertext, got %+v", got)
	}

	// 4. Wrong image_id (request_id replay) -> BAD.
	wrongID := sealDaemonImageEnvelope(t, dom, s1, sec, seed, trusted, trusted.id, "s-neg", "other-img", devicesec.Image, stdv1.ImageFormat_IMAGE_FORMAT_PNG, 10, 10, png)
	got, _, _ = runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{OpId: 4, SessionId: "s-neg", ImageId: "moved"}, nowMs+100, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
		return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: marshalStdGetImageResp(t, wrongID, false)}
	})
	if got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_BAD {
		t.Fatalf("expected BAD for mismatched image_id, got %+v", got)
	}

	// 5. Wrong Purpose (Event instead of Image) -> BAD.
	wrongPurpose := sealDaemonImageEnvelope(t, dom, s1, sec, seed, trusted, trusted.id, "s-neg", "wrong-p", devicesec.Event, stdv1.ImageFormat_IMAGE_FORMAT_PNG, 10, 10, png)
	got, _, _ = runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{OpId: 5, SessionId: "s-neg", ImageId: "wrong-p"}, nowMs+100, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
		return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: marshalStdGetImageResp(t, wrongPurpose, false)}
	})
	if got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_BAD {
		t.Fatalf("expected BAD for wrong purpose, got %+v", got)
	}

	// 6. NotFound -> MISSING; expired -> EXPIRED; missing session seed -> ERROR("not_found").
	got, _, _ = runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{OpId: 6, SessionId: "s-neg", ImageId: "missing"}, nowMs+100, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
		return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ConnectCode: 5}
	})
	if got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_MISSING {
		t.Fatalf("expected MISSING, got %+v", got)
	}
	got, _, _ = runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{OpId: 7, SessionId: "s-neg", ImageId: "expired"}, nowMs+100, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
		return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: marshalStdGetImageResp(t, nil, true)}
	})
	if got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_EXPIRED {
		t.Fatalf("expected EXPIRED, got %+v", got)
	}
	got, rpcs, _ := runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{OpId: 8, SessionId: "unknown-session", ImageId: "x"}, nowMs+100, nil)
	if rpcs != 0 || got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_ERROR || got.ErrorCode != "not_found" {
		t.Fatalf("expected not_found ERROR with 0 RPCs, got rpcs=%d res=%+v", rpcs, got)
	}

	// 7. After machine is revoked in s2, historical image decrypt still works for archived history,
	//    but CacheEnvelopeBytes MUST be suppressed because CanUseMachineCache(trusted.id) is false.
	s2RespBytes, err := proto.Marshal(&stdv1.GetSecurityStatesResponse{
		HeadEpoch: 2,
		States:    []*stdv1.SecurityState{{State: s2Raw}},
		Lease:     lease2Raw,
	})
	if err != nil {
		t.Fatalf("marshal GetSecurityStatesResponse: %v", err)
	}
	syncBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 200, MonoMs: 2200,
		Event: &corev1.Event_SecurityAction{
			SecurityAction: &corev1.SecurityWorkflowActionEvent{
				ActionId: 2,
				Action: &corev1.SecurityWorkflowActionEvent_SyncNow{
					SyncNow: &corev1.SecuritySyncNowAction{AuthToken: "tok"},
				},
			},
		},
	})
	for _, eff := range pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+200, syncBatch) {
		if rpc := eff.GetRelayRpc(); rpc != nil {
			r2 := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 201, MonoMs: 2201,
				Event: &corev1.Event_RelayRpcResult{
					RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: s2RespBytes},
				},
			})
			pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+201, r2)
		}
	}
	if c.m.sync.CanUseMachineCache(trusted.id) {
		t.Fatal("expected trusted machine to be revoked at epoch 2")
	}
	archEnv := sealDaemonImageEnvelope(t, dom, s1, sec, seed, trusted, trusted.id, "s-neg", "archived", devicesec.Image, stdv1.ImageFormat_IMAGE_FORMAT_PNG, 10, 10, png)
	got, _, _ = runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{OpId: 9, SessionId: "s-neg", ImageId: "archived", MachineId: trusted.id}, nowMs+250, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
		return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: marshalStdGetImageResp(t, archEnv, false)}
	})
	if got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_OK {
		t.Fatalf("expected archived historical image OK, got %+v", got)
	}
	if len(got.CacheEnvelopeBytes) != 0 {
		t.Fatal("revoked/archived machine image MUST NOT emit CacheEnvelopeBytes")
	}
}

func TestSessionImageWithReplacementProof(t *testing.T) {
	now := int64(1_750_000_000_000)
	phone := newAppTestActor(t, 0x91)
	oldM := newAppTestActor(t, 0x92)
	newM := newAppTestActor(t, 0x93)
	rec := newAppTestActor(t, 0x94)
	d := devicesec.Digest("repl-img-domain", []byte{2})
	s0Sec := bytes.Repeat([]byte{0x31}, 32)
	s1Sec := bytes.Repeat([]byte{0x32}, 32)

	u0, err := devicesec.NewGenesis(phone.id, d, phone.member(devicesec.Client), [32]byte(rec.edP), s0Sec, uint64(now), appTestHPKE{})
	if err != nil {
		t.Fatalf("NewGenesis: %v", err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	gen, _ := s0.Hash()

	sortMembers := func(ms ...devicesec.Member) []devicesec.Member {
		slices.SortFunc(ms, func(a, b devicesec.Member) int {
			if a.ID < b.ID {
				return -1
			}
			if a.ID > b.ID {
				return 1
			}
			return 0
		})
		return ms
	}
	u1, err := devicesec.NewMembershipState(s0, sortMembers(
		phone.member(devicesec.Client),
		oldM.member(devicesec.Machine),
	), nil, s0Sec, s0Sec, uint64(now+10), appTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatalf("s1: %v", err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(phone.ed, sb1)
	s1Raw, _ := u1.MarshalBinary()
	st1, _ := devicesec.ParseState(s1Raw)

	u2, err := devicesec.NewMembershipState(st1, sortMembers(
		phone.member(devicesec.Client),
	), []string{oldM.id}, s0Sec, s1Sec, uint64(now+20), appTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatalf("s2: %v", err)
	}
	sb2, _ := u2.SigningBytes()
	u2.Signature = ed25519.Sign(phone.ed, sb2)
	s2Raw, _ := u2.MarshalBinary()
	st2, _ := devicesec.ParseState(s2Raw)

	u3, err := devicesec.NewMembershipState(st2, sortMembers(
		phone.member(devicesec.Client),
		newM.member(devicesec.Machine),
	), []string{oldM.id}, s1Sec, s1Sec, uint64(now+25), appTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatalf("s3: %v", err)
	}
	sb3, _ := u3.SigningBytes()
	u3.Signature = ed25519.Sign(phone.ed, sb3)
	s3Raw, _ := u3.MarshalBinary()
	st3, _ := devicesec.ParseState(s3Raw)
	lease3, _ := devicesec.NewLease(st3, uint64(now+25))
	lb3, _ := lease3.SigningBytes()
	lease3.Signature = ed25519.Sign(phone.ed, lb3)
	lease3Raw, _ := lease3.MarshalBinary()

	c := New()
	t.Cleanup(c.Close)
	c.Dispatch(&corev1.Event{
		NowMs:  now,
		MonoMs: 1000,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{
				Account:      phone.id,
				DeviceId:     phone.id,
				Server:       "https://relay.example.com",
				InlineCrypto: true,
				Foreground:   true,
			},
		},
	})
	recBytes, err := (&security.PersistedRecord{
		Server:   "https://relay.example.com",
		MemberID: phone.id,
		Member:   true,
		Pin:      devicesec.Pin{Account: phone.id, Domain: d, Genesis: gen},
		States:   [][]byte{s0Raw, s1Raw, s2Raw, s3Raw},
		Lease:    lease3Raw,
	}).Encode()
	if err != nil {
		t.Fatalf("encode rec: %v", err)
	}
	secStore := &appTestSecStore{blob: recBytes, rev: "rev-1"}
	boot := c.Dispatch(&corev1.Event{
		NowMs:  now + 25,
		MonoMs: 1025,
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
	b2 := c.Dispatch(&corev1.Event{
		NowMs:  now + 26,
		MonoMs: 1026,
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
	pumpCoreSecurityEffects(t, c, phone, secStore, now+27, b2)

	// Build authority-signed PairApproval proving newM replaced oldM.
	approvalBytes, err := proto.Marshal(&stdv1.PairApproval{
		AccountId:                phone.id,
		PairingId:                phone.id,
		ApproverDeviceId:         phone.id,
		ApproverPublicKey:        phone.edP,
		CandidateDevicePublicKey: newM.edP,
		CandidateKind:            stdv1.DeviceKind_DEVICE_KIND_MACHINE,
		ReplacesDeviceId:         oldM.id,
		Nonce:                    bytes.Repeat([]byte{0x42}, 16),
		ExpiresMs:                now + 60_000,
	})
	if err != nil {
		t.Fatalf("marshal PairApproval: %v", err)
	}
	sigMsg := append([]byte(security.PairApprovalContext), approvalBytes...)
	proof := &stdv1.MachineReplacementProof{
		Approval:  approvalBytes,
		Signature: ed25519.Sign(phone.ed, sigMsg),
	}

	seed := bytes.Repeat([]byte{0x8D}, 32)
	seedSessionInCore(t, c, d, st3, s1Sec, newM, "s-repl", seed, []*stdv1.MachineReplacementProof{proof}, now+30)

	// Seal historical image under oldM at st1 (before replacement)!
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 9, 8, 7}
	oldImgEnv := sealDaemonImageEnvelope(t, d, st1, s0Sec, seed, oldM, oldM.id, "s-repl", "hist-1", devicesec.Image, stdv1.ImageFormat_IMAGE_FORMAT_PNG, 32, 32, png)
	got, _, _ := runImageFetchAndDrain(t, c, &corev1.SessionFetchImageAction{
		OpId: 1, SessionId: "s-repl", ImageId: "hist-1",
	}, now+50, func(rpc *corev1.RelayRpcEffect) *corev1.RelayRpcResultEvent {
		return &corev1.RelayRpcResultEvent{OpId: rpc.OpId, ResponseBytes: marshalStdGetImageResp(t, oldImgEnv, false)}
	})
	if got == nil || got.Status != corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_OK || !bytes.Equal(got.Data, png) {
		t.Fatalf("expected historical image from replaced machine to decrypt via replacement proof, got %+v", got)
	}
}
