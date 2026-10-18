package server

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
)

type testChain struct {
	phoneEd  ed25519.PrivateKey
	phoneID  string
	phoneX   []byte
	recovery ed25519.PrivateKey
	secret   []byte
	states   []deviceauth.State
	proofs   []*deviceauth.RecoveryStatement
}

type testServer struct {
	enrollmentMu   sync.Mutex
	enrollmentKeys map[string]ed25519.PrivateKey
	// chains are the phone-signed membership chains the fixture maintains,
	// one per account, so authenticated calls pass the membership check.
	chainMu sync.Mutex
	chains  map[string]*testChain
	srv     *Server
	url     string
	keys    *e2e.Keys
	auth    remoteagentv1connect.AuthServiceClient
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	s, err := New(Config{DBPath: filepath.Join(t.TempDir(), "ra.db"), SetupToken: "setup", AllowedOrigins: []string{"http://localhost:5173"}})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { hs.Close(); s.Close() })
	keys, _ := e2e.Derive(e2e.NewKey())
	return &testServer{srv: s, url: hs.URL, keys: keys, auth: remoteagentv1connect.NewAuthServiceClient(http.DefaultClient, hs.URL)}
}

// acct is the ID of the test server's account, made with ts.keys.
func (ts *testServer) acct() string { return e2e.AccountID(ts.keys.AccountPublicKey()) }

// tenant is the store of the test server's account.
func (ts *testServer) tenant() *tstore { return ts.srv.store.tenant(ts.acct()) }

func (ts *testServer) register(t *testing.T, keys *e2e.Keys, kind pb.DeviceKind, token string) (ed25519.PrivateKey, string, error) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: keys.AccountPublicKey(), DevicePublicKey: pub, Kind: kind})
	req := &pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: reg, Signature: keys.SignRegistration(reg), SetupToken: token}
	ts.admitRegistration(t, req)
	res, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(req))
	if err != nil {
		return nil, "", err
	}
	ts.enrollmentMu.Lock()
	if ts.enrollmentKeys == nil {
		ts.enrollmentKeys = map[string]ed25519.PrivateKey{}
	}
	ts.enrollmentKeys[res.Msg.DeviceId] = priv
	ts.enrollmentMu.Unlock()
	ts.extendChain(t, res.Msg.DeviceId, priv, kind)
	return priv, res.Msg.DeviceId, nil
}

// extendChain adds the newly registered device to its account's signed
// membership chain, bootstrapping a synthetic phone authority on the first
// registration of that account.
func (ts *testServer) extendChain(t *testing.T, id string, priv ed25519.PrivateKey, kind pb.DeviceKind) {
	t.Helper()
	d, err := ts.srv.store.device(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ts.chainMu.Lock()
	defer ts.chainMu.Unlock()
	if ts.chains == nil {
		ts.chains = map[string]*testChain{}
	}
	c := ts.chains[d.account]
	if c == nil {
		ts.chains[d.account] = ts.bootstrapChain(t, d.account, priv, kind)
		return
	}
	_, xpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	m, err := deviceauth.NewMember(priv.Public().(ed25519.PublicKey), xpub, kindOf(kind))
	if err != nil {
		t.Fatal(err)
	}
	ts.addMemberLocked(t, d.account, c, m, xpub)
}

func (ts *testServer) bootstrapChain(t *testing.T, acct string, priv ed25519.PrivateKey, kind pb.DeviceKind) *testChain {
	t.Helper()
	ctx := context.Background()
	_, phoneEd, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	px, pxpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := deviceauth.NewMember(phoneEd.Public().(ed25519.PublicKey), pxpub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	approval, sig, err := e2e.NewPairApproval(acct, randomHex(16), priv, phone.EdPublic[:], pb.DeviceKind_DEVICE_KIND_CLIENT, time.Now().Add(time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	a, err := parseApproval(approval, sig)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.srv.store.approveAdmission(ctx, a, hashToken("synthetic-phone")); err != nil {
		t.Fatal(err)
	}
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: phone.EdPublic[:], Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: time.Now().UnixMilli()})
	if err := ts.srv.store.addDevice(ctx, acct, nil, phone.ID, &pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: phone.EdPublic[:], Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: time.Now().UnixMilli()}, reg, []byte{}, nil, a); err != nil {
		t.Fatal(err)
	}
	_, recovery, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var recoveryPub [32]byte
	copy(recoveryPub[:], recovery.Public().(ed25519.PublicKey))
	var domain deviceauth.Hash
	domain[0] = 7
	secret := make([]byte, 32)
	secret[0] = 3
	genesis, err := deviceauth.BuildGenesis(acct, domain, phone, recoveryPub, secret, phoneEd, 1_700_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.pinChain(t, acct, genesis); err != nil {
		t.Fatal(err)
	}
	c := &testChain{phoneEd: phoneEd, phoneID: phone.ID, phoneX: px, recovery: recovery, secret: secret, states: []deviceauth.State{genesis}, proofs: []*deviceauth.RecoveryStatement{nil}}
	ts.rememberKey(phone.ID, phoneEd)
	_, xpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	first, err := deviceauth.NewMember(priv.Public().(ed25519.PublicKey), xpub, kindOf(kind))
	if err != nil {
		t.Fatal(err)
	}
	ts.addMemberLocked(t, acct, c, first, xpub)
	return c
}

func kindOf(kind pb.DeviceKind) deviceauth.Kind {
	if kind == pb.DeviceKind_DEVICE_KIND_CLIENT {
		return deviceauth.Client
	}
	return deviceauth.Machine
}

func (ts *testServer) pinChain(t *testing.T, acct string, genesis deviceauth.State) error {
	t.Helper()
	raw, err := genesis.MarshalBinary()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if _, err := ts.srv.store.db.ExecContext(ctx, `UPDATE accounts SET security_genesis=?, recovery_ed=? WHERE id=?`, raw, genesis.Core.RecoveryEd[:], acct); err != nil {
		return err
	}
	h, _ := genesis.Hash()
	_, err = ts.srv.store.db.ExecContext(ctx, `INSERT INTO security_states (account_id, epoch, state, hash) VALUES (?, 1, ?, ?)`, acct, raw, h[:])
	return err
}

func (ts *testServer) addMemberLocked(t *testing.T, acct string, c *testChain, m deviceauth.Member, xpub []byte) {
	t.Helper()
	newSecret := make([]byte, 32)
	newSecret[0] = byte(len(c.states) + 1)
	parent := c.states[len(c.states)-1]
	s, err := deviceauth.BuildPairState(parent, m, xpub, c.secret, newSecret, c.phoneEd, 1_700_000_100_000+uint64(len(c.states))*1000)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := s.MarshalBinary()
	h, _ := s.Hash()
	if _, err := ts.srv.store.db.ExecContext(context.Background(), `INSERT INTO security_states (account_id, epoch, state, hash) VALUES (?, ?, ?, ?)`, acct, s.Core.Epoch, raw, h[:]); err != nil {
		t.Fatal(err)
	}
	c.states = append(c.states, s)
	c.proofs = append(c.proofs, nil)
	c.secret = newSecret
}

// removeViaState signs a removal state with the default account's phone and
// uploads it.
func (ts *testServer) removeViaState(t *testing.T, ids ...string) error {
	t.Helper()
	ts.chainMu.Lock()
	acct := ts.acct()
	c := ts.chains[acct]
	parent := c.states[len(c.states)-1]
	members := append([]deviceauth.Member(nil), parent.Core.Members...)
	removed := append([]string(nil), parent.Core.Removed...)
	for _, id := range ids {
		for i, m := range members {
			if m.ID == id {
				members = append(members[:i], members[i+1:]...)
				break
			}
		}
		removed = append(removed, id)
	}
	newSecret := make([]byte, 32)
	newSecret[0] = byte(len(c.states) + 1)
	state, err := deviceauth.BuildMembershipState(parent, members, removed, c.secret, newSecret, c.phoneEd, 1_700_000_200_000+uint64(len(c.states))*1000)
	if err != nil {
		ts.chainMu.Unlock()
		return err
	}
	c.states = append(c.states, state)
	c.proofs = append(c.proofs, nil)
	c.secret = newSecret
	ts.chainMu.Unlock()
	raw, _ := state.MarshalBinary()
	token := ts.login(t, c.phoneEd, c.phoneID)
	api := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(token))
	_, err = api.PutSecurityState(context.Background(), connect.NewRequest(&pb.PutSecurityStateRequest{States: []*pb.SecurityState{{State: raw}}}))
	return err
}

// pairTrust returns the default account's public pairing trust descriptor.
func (ts *testServer) pairTrust(t *testing.T, approver string) []byte {
	t.Helper()
	ts.chainMu.Lock()
	defer ts.chainMu.Unlock()
	c := ts.chains[ts.acct()]
	if c == nil {
		t.Fatal("no security chain")
	}
	pin, err := c.states[0].Pin()
	if err != nil {
		t.Fatal(err)
	}
	trust, err := deviceauth.PairTrustDescriptor(pin, approver)
	if err != nil {
		t.Fatal(err)
	}
	return trust
}

// authority returns the default account's test chain.
func (ts *testServer) authority(t *testing.T) *testChain {
	t.Helper()
	ts.chainMu.Lock()
	defer ts.chainMu.Unlock()
	c := ts.chains[ts.acct()]
	if c == nil {
		t.Fatal("no security chain")
	}
	return c
}

// putSession is the fixture's session write with a v3 key envelope epoch.
func (ts *testServer) putSession(ctx context.Context, machineID, sessionID string, key, meta []byte, revision uint64, endedMs int64, limit func(accountUsage) error) (*pb.RemoteSession, error) {
	return ts.tenant().putSession(ctx, machineID, sessionID, key, 1, meta, revision, endedMs, limit)
}

// Test fixtures explicitly arrange a valid device's signed admission. Tests of
// anonymous enrollment call RegisterDevice directly, so they cannot inherit it.
func (ts *testServer) admitRegistration(t *testing.T, req *pb.RegisterDeviceRequest) {
	t.Helper()
	var reg pb.DeviceRegistration
	if proto.Unmarshal(req.Registration, &reg) != nil {
		t.Fatal("test registration malformed")
	}
	acct := e2e.AccountID(reg.AccountPublicKey)
	ts.enrollmentMu.Lock()
	defer ts.enrollmentMu.Unlock()
	authority := ""
	if head, _, err := ts.srv.store.tenant(acct).securityHead(context.Background()); err == nil {
		authority = head.Core.AuthorityID
	}
	for id, priv := range ts.enrollmentKeys {
		if authority != "" && id != authority {
			continue
		}
		d, err := ts.srv.store.device(context.Background(), id)
		if err != nil || d.account != acct {
			continue
		}
		state, err := ts.srv.store.account(context.Background(), acct)
		if err != nil || state.status != accountActive {
			continue
		}
		raw, sig, err := e2e.NewPairApproval(acct, randomHex(16), priv, reg.DevicePublicKey, reg.Kind, time.Now().Add(time.Minute).UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		a, err := parseApproval(raw, sig)
		if err != nil {
			t.Fatal(err)
		}
		if err := ts.srv.store.approveAdmission(context.Background(), a, hashToken("synthetic-candidate-token")); err != nil {
			t.Fatal(err)
		}
		req.Approval, req.ApprovalSignature = raw, sig
		return
	}
}

func (ts *testServer) rememberKey(id string, priv ed25519.PrivateKey) {
	ts.enrollmentMu.Lock()
	defer ts.enrollmentMu.Unlock()
	if ts.enrollmentKeys == nil {
		ts.enrollmentKeys = map[string]ed25519.PrivateKey{}
	}
	ts.enrollmentKeys[id] = priv
}

func (ts *testServer) login(t *testing.T, priv ed25519.PrivateKey, id string) string {
	t.Helper()
	ctx := context.Background()
	ch, err := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id}))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: protocol.Version, DeviceId: id, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(priv, ch.Msg.Challenge)}))
	if err != nil {
		t.Fatal(err)
	}
	// A challenge works once.
	if _, err := ts.auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: protocol.Version, DeviceId: id, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(priv, ch.Msg.Challenge)})); err == nil {
		t.Fatal("challenge reused")
	}
	return res.Msg.Token
}

func withToken(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}))
}

func TestRegistration(t *testing.T) {
	ts := newTestServer(t)
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, ""); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("no setup token: %v", err)
	}
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup"); err != nil {
		t.Fatal(err)
	}
	// Later devices require explicit approval by an enrolled device, not a setup token.
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, ""); err != nil {
		t.Fatal(err)
	}
	other, _ := e2e.Derive(e2e.NewKey())
	// A relay of one's own holds one account, setup token or not.
	if _, _, err := ts.register(t, other, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("second account: %v", err)
	}
	// A registration signed by someone else is refused.
	pub, _, _ := ed25519.GenerateKey(nil)
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: pub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT})
	_, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(&pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: reg, Signature: other.SignRegistration(reg)}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("forged registration: %v", err)
	}
}

func TestLoginAndAuthorization(t *testing.T) {
	ts := newTestServer(t)
	mpriv, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	cpriv, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	ctx := context.Background()

	// Wrong key.
	ch, _ := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: mid}))
	if _, err := ts.auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: protocol.Version, DeviceId: mid, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(cpriv, ch.Msg.Challenge)})); err == nil {
		t.Fatal("login with another device's key")
	}

	anon := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url)
	if _, err := anon.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous: %v", err)
	}
	client := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cpriv, cid)))
	if _, err := client.ListSessionHeads(ctx, connect.NewRequest(&pb.ListSessionHeadsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("client calling machine API: %v", err)
	}
	machine := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, mpriv, mid)))
	if _, err := machine.ListSessionHeads(ctx, connect.NewRequest(&pb.ListSessionHeadsRequest{})); err != nil {
		t.Fatal(err)
	}
}

func TestUploadRules(t *testing.T) {
	ts := newTestServer(t)
	priv, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	priv2, mid2, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "")
	m := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, priv, mid)))
	m2 := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, priv2, mid2)))
	ctx := context.Background()
	skey := e2e.NewKey()
	ev := func(machine string, seq uint64) *pb.StoredEvent {
		env, _ := e2e.Seal(skey, &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: machine, SessionId: "s", Seq: seq}, []byte("x"))
		return &pb.StoredEvent{Seq: seq, Envelope: env}
	}
	upload := func(c remoteagentv1connect.MachineServiceClient, evs ...*pb.StoredEvent) (uint64, error) {
		res, err := c.UploadEvents(ctx, connect.NewRequest(&pb.UploadEventsRequest{SessionId: "s", Events: evs}))
		if err != nil {
			return 0, err
		}
		return res.Msg.LastSeq, nil
	}
	if _, err := upload(m, ev(mid, 1)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
	keyEnv, _ := e2e.Seal(ts.keys.Wrap, &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: mid, SessionId: "s"}, skey)
	if _, err := m.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: "s"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("session without key: %v", err)
	}
	if _, err := m.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: "s", Key: keyEnv})); err != nil {
		t.Fatal(err)
	}
	if last, err := upload(m, ev(mid, 1), ev(mid, 2)); err != nil || last != 2 {
		t.Fatalf("upload: %d %v", last, err)
	}
	// Retries are skipped, gaps rejected.
	if last, err := upload(m, ev(mid, 2), ev(mid, 3)); err != nil || last != 3 {
		t.Fatalf("overlapping upload: %d %v", last, err)
	}
	if _, err := upload(m, ev(mid, 5)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("gap: %v", err)
	}
	// A header naming another place is rejected.
	moved := ev(mid, 4)
	moved.Seq = 9
	if _, err := upload(m, moved); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("mismatched seq: %v", err)
	}
	// Another machine cannot write to the session.
	if _, err := upload(m2, ev(mid2, 4)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("other machine: %v", err)
	}
	keyEnv2, _ := e2e.Seal(ts.keys.Wrap, &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: mid2, SessionId: "s"}, skey)
	if _, err := m2.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: "s", Key: keyEnv2})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("other machine put: %v", err)
	}
}

func TestCORS(t *testing.T) {
	ts := newTestServer(t)
	for origin, want := range map[string]string{"http://localhost:5173": "http://localhost:5173", "https://evil.example": ""} {
		req, _ := http.NewRequest(http.MethodOptions, ts.url+"/remoteagent.v1.ClientService/ListMachines", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %s: allow-origin %q", origin, got)
		}
	}
}

func TestChallengesAreBounded(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	for _, id := range []string{"", "short", string(make([]byte, 32)), strings.Repeat("x", 1<<16)} {
		want := connect.CodeInvalidArgument
		if len(id) > 16<<10 {
			want = connect.CodeResourceExhausted
		}
		if _, err := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id})); connect.CodeOf(err) != want {
			t.Fatalf("device ID %.10q: %v", id, err)
		}
	}
	priv, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	// Unknown devices get challenges, but nothing is stored for them.
	for i := range 2 * ts.srv.challengeRoom() {
		unknown := fmt.Sprintf("%032x", i)
		if _, err := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: unknown})); err != nil {
			t.Fatal(err)
		}
	}
	// A device's own flood only pushes out its own oldest challenges.
	for range 3 * maxDeviceChallenges {
		if _, err := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id})); err != nil {
			t.Fatal(err)
		}
	}
	if n := ts.srv.pendingChallenges(); n != maxDeviceChallenges {
		t.Fatalf("%d challenges stored", n)
	}
	ts.login(t, priv, id)
}

// pushSubscription makes a browser-like subscription pointing at endpoint.
func pushSubscription(t *testing.T, endpoint string) string {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	b, _ := json.Marshal(map[string]any{"endpoint": endpoint, "keys": map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		"auth":   base64.RawURLEncoding.EncodeToString(auth),
	}})
	return string(b)
}

func TestPushAndRemoveDevice(t *testing.T) {
	ts := newTestServer(t)
	mpriv, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	cpriv, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	ctx := context.Background()
	mtoken := ts.login(t, mpriv, mid)
	m := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(mtoken))
	c := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cpriv, cid)))

	skey := e2e.NewKey()
	keyEnv, _ := e2e.Seal(ts.keys.Wrap, &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: mid, SessionId: "s"}, skey)
	if _, err := m.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: "s", Key: keyEnv})); err != nil {
		t.Fatal(err)
	}

	// A fake push service: the first delivery works, later ones say the
	// subscription is gone.
	var mu sync.Mutex
	var got []string
	gone := false
	pushSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r.Header.Get("Content-Encoding"))
		if gone {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer pushSvc.Close()
	// The fake delivery transport is local; production rejects private hosts.
	ts.srv.push.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		u, _ := url.Parse(pushSvc.URL)
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(got) }

	key, err := c.GetPushKey(ctx, connect.NewRequest(&pb.GetPushKeyRequest{}))
	if err != nil || key.Msg.PublicKey == "" {
		t.Fatalf("push key: %v %v", key, err)
	}
	if _, err := c.SetPushSubscription(ctx, connect.NewRequest(&pb.SetPushSubscriptionRequest{
		SubscriptionJson: pushSubscription(t, "https://push.example/endpoint"), Kinds: []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION},
	})); err != nil {
		t.Fatal(err)
	}
	notify := func(kind pb.NotifyKind) {
		if _, err := m.Notify(ctx, connect.NewRequest(&pb.NotifyRequest{SessionId: "s", Kind: kind})); err != nil {
			t.Fatal(err)
		}
	}
	notify(pb.NotifyKind_NOTIFY_KIND_TURN_ENDED) // not subscribed
	notify(pb.NotifyKind_NOTIFY_KIND_ATTENTION)
	notify(pb.NotifyKind_NOTIFY_KIND_ATTENTION) // coalesced
	deadline := time.Now().Add(5 * time.Second)
	for count() < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if n := count(); n != 1 {
		t.Fatalf("%d pushes, want 1", n)
	}
	if got[0] != "aes128gcm" {
		t.Fatalf("payload encoding %q", got[0])
	}
	// A subscription the push service dropped is deleted.
	mu.Lock()
	gone = true
	mu.Unlock()
	ts.srv.push.mu.Lock()
	clear(ts.srv.push.recent)
	ts.srv.push.mu.Unlock()
	notify(pb.NotifyKind_NOTIFY_KIND_ATTENTION)
	var n int
	for i := 0; i < 100; i++ {
		ts.srv.store.db.QueryRow(`SELECT COUNT(*) FROM push_subs`).Scan(&n)
		if n == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n != 0 {
		t.Fatal("dropped subscription kept")
	}

	// Removing the machine through a signed state deletes its sessions and
	// ends its logins.
	if err := ts.removeViaState(t, mid); err != nil {
		t.Fatal(err)
	}
	ss, _ := c.ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{}))
	if len(ss.Msg.Sessions) != 0 {
		t.Fatalf("sessions left: %v", ss.Msg.Sessions)
	}
	if _, err := m.ListSessionHeads(ctx, connect.NewRequest(&pb.ListSessionHeadsRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("removed machine still logged in: %v", err)
	}
}

func TestPairingLimits(t *testing.T) {
	ts := newTestServer(t)
	_, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pc := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, ts.authority(t).phoneEd, ts.authority(t).phoneID)))
	anon := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url)
	ikey, _ := e2e.NewPairKey()
	if _, err := anon.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: e2e.PairDomainCommit(ikey.Public(), ts.pairTrust(t, ts.authority(t).phoneID))})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous create: %v", err)
	}
	created, err := pc.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: e2e.PairDomainCommit(ikey.Public(), ts.pairTrust(t, ts.authority(t).phoneID))}))
	if err != nil {
		t.Fatal(err)
	}
	jkey, _ := e2e.NewPairKey()
	candidate, _, _ := ed25519.GenerateKey(nil)
	join := func(code string) (*connect.Response[pb.JoinPairingResponse], error) {
		return anon.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{ProtocolVersion: protocol.Version, Code: code, JoinerPublic: jkey.Public(), DeviceHpkePublicKey: jkey.Public(), DevicePublicKey: candidate, JoinerKind: pb.DeviceKind_DEVICE_KIND_CLIENT}))
	}
	joined, err := join(created.Msg.Code)
	if err != nil {
		t.Fatal(err)
	}
	// One candidate at a time.
	if _, err := join(created.Msg.Code); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("second joiner: %v", err)
	}
	// Another token sees nothing.
	if _, err := anon.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: joined.Msg.PairingId, CandidateToken: "wrong"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("wrong token: %v", err)
	}
	// The reveal must match the commitment.
	other, _ := e2e.NewPairKey()
	if _, err := pc.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{Trust: ts.pairTrust(t, ts.authority(t).phoneID), PairingId: joined.Msg.PairingId, InitiatorPublic: other.Public()})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("reveal of another key: %v", err)
	}
	if _, err := pc.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{Trust: ts.pairTrust(t, ts.authority(t).phoneID), PairingId: joined.Msg.PairingId, InitiatorPublic: ikey.Public()})); err != nil {
		t.Fatal(err)
	}
	env, _ := e2e.Seal(e2e.NewKey(), &pb.Header{Version: 1, Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: joined.Msg.PairingId}, []byte("x"))
	approval, approvalSig, _ := e2e.NewPairApproval(ts.acct(), joined.Msg.PairingId, ts.authority(t).phoneEd, candidate, pb.DeviceKind_DEVICE_KIND_CLIENT, created.Msg.ExpiresMs)
	if _, err := pc.ApprovePairing(ctx, connect.NewRequest(&pb.ApprovePairingRequest{PairingId: joined.Msg.PairingId, Payload: env, Approval: approval, ApprovalSignature: approvalSig})); err != nil {
		t.Fatal(err)
	}
	get := func(received bool) (*connect.Response[pb.GetPairingResponse], error) {
		return anon.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: joined.Msg.PairingId, CandidateToken: joined.Msg.CandidateToken, Received: received}))
	}
	initiatorState := func() pb.PairingState {
		res, err := pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: joined.Msg.PairingId}))
		if err != nil || res.Msg.Payload != nil {
			t.Fatalf("initiator: %v %v", res, err)
		}
		return res.Msg.State
	}
	// A lost response can be fetched again; the initiator waits for the
	// joiner to confirm.
	for range 2 {
		if res, err := get(false); err != nil || res.Msg.Payload == nil {
			t.Fatalf("payload: %v %v", res, err)
		}
	}
	if st := initiatorState(); st != pb.PairingState_PAIRING_STATE_APPROVED {
		t.Fatalf("initiator before confirmation: %v", st)
	}
	if _, err := get(true); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unregistered candidate claimed delivery: %v", err)
	}
	raw, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: candidate, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT})
	if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: raw, Signature: ts.keys.SignRegistration(raw), Approval: approval, ApprovalSignature: approvalSig})); err != nil {
		t.Fatal(err)
	}
	if res, err := get(true); err != nil || res.Msg.State != pb.PairingState_PAIRING_STATE_DELIVERED {
		t.Fatalf("confirm: %v %v", res, err)
	}
	if res, err := get(false); err != nil || res.Msg.Payload != nil {
		t.Fatalf("payload after confirmation: %v %v", res, err)
	}
	if st := initiatorState(); st != pb.PairingState_PAIRING_STATE_DELIVERED {
		t.Fatalf("initiator after confirmation: %v", st)
	}
	// An approval reply may be lost even after the candidate confirmed delivery.
	if _, err := pc.ApprovePairing(ctx, connect.NewRequest(&pb.ApprovePairingRequest{PairingId: joined.Msg.PairingId, Payload: env, Approval: approval, ApprovalSignature: approvalSig})); err != nil {
		t.Fatalf("completed approval retry rejected: %v", err)
	}
	// Wrong codes are limited.
	var last error
	for i := 0; i < badCodesPerMinute+1; i++ {
		_, last = join(fmt.Sprintf("9%07d", i))
	}
	if connect.CodeOf(last) != connect.CodeResourceExhausted {
		t.Fatalf("after many wrong codes: %v", last)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
