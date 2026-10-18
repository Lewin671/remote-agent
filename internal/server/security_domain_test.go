package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

type testSecurityDomain struct {
	authority *deviceauth.Authority
	host      string
	private   ed25519.PrivateKey
	exchange  map[string][]byte
}

// Existing transport tests now arrange actual synthetic signed authority
// completions. Raw bearer-only calls remain available to rejection tests.
func (ts *testServer) removalProof(t *testing.T, requester, target string) *pb.RemoveDeviceRequest {
	t.Helper()
	ctx := context.Background()
	if ts.securityDomain == nil {
		ed, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "")
		if err != nil {
			t.Fatal(err)
		}
		x, xp, err := deviceauth.NewXKey()
		if err != nil {
			t.Fatal(err)
		}
		member, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xp, deviceauth.Machine)
		if err != nil {
			t.Fatal(err)
		}
		a, err := deviceauth.InitAuthority(filepath.Join(t.TempDir(), "authority.db"), ts.acct(), member)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		_, genesis := a.Snapshot()
		raw, err := genesis.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		machine := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, ed, id)))
		if _, err = machine.PinSecurityDomain(ctx, connect.NewRequest(&pb.PinSecurityDomainRequest{GenesisState: raw})); err != nil {
			t.Fatal(err)
		}
		ts.securityDomain = &testSecurityDomain{a, id, ed, map[string][]byte{id: x}}
	}
	f := ts.securityDomain
	ts.enrollmentMu.Lock()
	keys := map[string]ed25519.PrivateKey{}
	for id, key := range ts.enrollmentKeys {
		keys[id] = key
	}
	ts.enrollmentMu.Unlock()
	for id, ed := range keys {
		_, state := f.authority.Snapshot()
		if _, ok := state.Core.Member(id); ok {
			continue
		}
		device, err := ts.srv.store.device(ctx, id)
		if err != nil || device.account != ts.acct() {
			continue
		}
		x, xp, err := deviceauth.NewXKey()
		if err != nil {
			t.Fatal(err)
		}
		member, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xp, deviceauth.Kind(device.kind))
		if err != nil {
			t.Fatal(err)
		}
		pair, err := f.authority.BeginPair(randomHex(16), f.host, member)
		if err != nil {
			t.Fatal(err)
		}
		approve, err := deviceauth.SignPair(pair, f.private, false)
		if err != nil {
			t.Fatal(err)
		}
		accept, err := deviceauth.SignPair(pair, ed, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.authority.ApprovePair(pair.ID, approve, accept); err != nil {
			t.Fatal(err)
		}
		f.exchange[id] = x
	}
	pin, state := f.authority.Snapshot()
	hash, _ := state.Hash()
	request := deviceauth.DeleteRequest{Account: pin.Account, Domain: pin.Domain, Requester: requester, Operation: randomHex(16), Expected: hash, Nonce: deviceauth.Hash{1}, ExactIDs: true, Targets: []deviceauth.Target{{ID: target}}}
	if err := request.Sign(keys[requester]); err != nil {
		t.Fatal(err)
	}
	group, err := f.authority.Delete(request)
	if err != nil {
		t.Fatal(err)
	}
	fences, err := f.authority.FenceRequests()
	if err != nil {
		t.Fatal(err)
	}
	for _, fence := range fences {
		ed := keys[fence.Executor]
		endpoint, err := deviceauth.InitEndpoint(filepath.Join(t.TempDir(), "endpoint.db"), pin, group.Head, fence.Executor, ed.Seed(), f.exchange[fence.Executor])
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := endpoint.Fence(fence, nil)
		endpoint.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.authority.Ack(receipt); err != nil {
			t.Fatal(err)
		}
	}
	completion, err := f.authority.Completion(request.Operation)
	if err != nil {
		t.Fatal(err)
	}
	c, err := json.Marshal(completion)
	if err != nil {
		t.Fatal(err)
	}
	r, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := group.Head.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return &pb.RemoveDeviceRequest{DeviceId: target, SecurityCompletion: c, SecurityRequest: r, SecurityChain: [][]byte{raw}}
}

func TestRemovalRequiresPinnedCompletionAndProtectsAuthority(t *testing.T) {
	ts, mid, cid, _, client, _ := newV2Env(t)
	ctx := context.Background()
	if _, err := client.RemoveDevice(ctx, connect.NewRequest(&pb.RemoveDeviceRequest{DeviceId: mid})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("bearer-only removal: %v", err)
	}
	request := ts.removalProof(t, cid, mid)
	// A signed completion for one target cannot be redirected to the authority.
	bad := *request
	bad.DeviceId = ts.securityDomain.host
	if _, err := client.RemoveDevice(ctx, connect.NewRequest(&bad)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("authority removed: %v", err)
	}
	bad = *request
	bad.SecurityCompletion = append([]byte{}, request.SecurityCompletion...)
	bad.SecurityCompletion[0] ^= 1
	if _, err := client.RemoveDevice(ctx, connect.NewRequest(&bad)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("malformed proof accepted: %v", err)
	}
	if _, err := client.RemoveDevice(ctx, connect.NewRequest(request)); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.device(ctx, mid); err == nil {
		t.Fatal("completed removal did not delete transport identity")
	}
	if _, err := ts.srv.store.device(ctx, ts.securityDomain.host); err != nil {
		t.Fatal("authority transport identity disappeared")
	}
}

func TestOfflinePinnedGenesisRejectsLegacyMachineFirstClaim(t *testing.T) {
	ts, mid, cid, machine, client, _ := newV2Env(t)
	ctx := context.Background()
	// Operator pins the chosen trusted authority before accepting network traffic.
	makeGenesis := func(id string) []byte {
		t.Helper()
		ed := ts.enrollmentKeys[id]
		_, xp, err := deviceauth.NewXKey()
		if err != nil {
			t.Fatal(err)
		}
		member, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xp, deviceauth.Machine)
		if err != nil {
			t.Fatal(err)
		}
		a, err := deviceauth.InitAuthority(filepath.Join(t.TempDir(), "authority.db"), ts.acct(), member)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		_, state := a.Snapshot()
		raw, err := state.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	trusted := makeGenesis(mid)
	if _, err := ts.srv.store.db.ExecContext(ctx, `UPDATE accounts SET security_genesis=? WHERE id=?`, trusted, ts.acct()); err != nil {
		t.Fatal(err)
	}
	ed, legacy, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "")
	if err != nil {
		t.Fatal(err)
	}
	stale := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, ed, legacy)))
	fake := makeGenesis(legacy)
	if _, err := stale.PinSecurityDomain(ctx, connect.NewRequest(&pb.PinSecurityDomainRequest{GenesisState: fake})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("legacy first claim: %v", err)
	}
	if _, err := machine.PinSecurityDomain(ctx, connect.NewRequest(&pb.PinSecurityDomainRequest{GenesisState: trusted})); err != nil {
		t.Fatal("trusted idempotent pin", err)
	}
	// Target a non-authority device with a request from the caller so the check reaches completion verification.
	original, err := json.Marshal(deviceauth.DeleteRequest{Requester: cid, Targets: []deviceauth.Target{{ID: legacy}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RemoveDevice(ctx, connect.NewRequest(&pb.RemoveDeviceRequest{DeviceId: legacy, SecurityChain: [][]byte{fake}, SecurityCompletion: []byte(`{}`), SecurityRequest: original})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("fake-domain cleanup: %v", err)
	}
	for _, id := range []string{mid, cid, legacy} {
		if _, err := ts.srv.store.device(ctx, id); err != nil {
			t.Fatal("identity deleted", err)
		}
	}
}
