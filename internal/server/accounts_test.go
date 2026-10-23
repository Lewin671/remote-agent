package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
)

// testAccount is an account on a test server with a machine and a client.
type testAccount struct {
	keys         *e2e.Keys
	mid, cid     string
	mtok, ctok   string
	machine      remoteagentv1connect.MachineServiceClient
	client       remoteagentv1connect.ClientServiceClient
	machineFrame *testStream
}

func (ts *testServer) account(t *testing.T, keys *e2e.Keys) *testAccount {
	t.Helper()
	mp, mid, err := ts.register(t, keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	cp, cid, err := ts.register(t, keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	a := &testAccount{keys: keys, mid: mid, cid: cid, mtok: ts.login(t, mp, mid), ctok: ts.login(t, cp, cid)}
	a.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, ts.url, withToken(a.mtok))
	a.client = remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(a.ctok))
	a.machineFrame = ts.stream(t, a.mtok)
	return a
}

func (a *testAccount) putSession(t *testing.T, sid string) {
	t.Helper()
	key, _ := e2e.Seal(a.keys.Wrap, &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: a.mid, SessionId: sid}, e2e.NewKey())
	if _, err := a.machine.PutSession(context.Background(), connect.NewRequest(&pb.PutSessionRequest{SessionId: sid, Key: key})); err != nil {
		t.Fatal(err)
	}
}

func (a *testAccount) putPrefs(t *testing.T, expected uint64) error {
	env, _ := e2e.Seal(e2e.NewKey(), &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_ACCOUNT_PREFS, RequestId: "project-prefs", Seq: expected + 1, Sender: a.cid}, []byte("prefs"))
	_, err := a.client.PutAccountPrefs(context.Background(), connect.NewRequest(&pb.PutAccountPrefsRequest{ObjectId: "project-prefs", ExpectedRevision: expected, Envelope: env}))
	return err
}

// Accounts on one relay see nothing of each other, even where they chose
// the same names.
func TestAccountsAreIsolated(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.cfg.MaxAccounts = 2
	ctx := context.Background()
	other, _ := e2e.Derive(e2e.NewKey())
	a, b := ts.account(t, ts.keys), ts.account(t, other)
	bStream := ts.stream(t, b.ctok)

	// Both accounts have a session s1; only a's has events and an image.
	a.putSession(t, "s1")
	b.putSession(t, "s1")
	ev, _ := e2e.Seal(e2e.NewKey(), &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: a.mid, SessionId: "s1", Seq: 1}, []byte("event"))
	if _, err := a.machine.UploadEvents(ctx, connect.NewRequest(&pb.UploadEventsRequest{SessionId: "s1", Events: []*pb.StoredEvent{{Seq: 1, Envelope: ev}}})); err != nil {
		t.Fatal(err)
	}
	const img = "00000000000000000000000000000001"
	if _, err := a.machine.PutImage(ctx, connect.NewRequest(&pb.PutImageRequest{SessionId: "s1", ImageId: img, Image: sealImage(t, a.mid, "s1", img, 100)})); err != nil {
		t.Fatal(err)
	}
	if err := a.putPrefs(t, 0); err != nil {
		t.Fatal(err)
	}

	if res, err := b.client.GetEvents(ctx, connect.NewRequest(&pb.GetEventsRequest{SessionId: "s1"})); err != nil || len(res.Msg.Events) != 0 {
		t.Fatalf("b reads a's events: %v %v", res, err)
	}
	if res, err := a.client.GetEvents(ctx, connect.NewRequest(&pb.GetEventsRequest{SessionId: "s1"})); err != nil || len(res.Msg.Events) != 1 {
		t.Fatalf("a's events: %v %v", res, err)
	}
	if res, err := b.client.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); err != nil || len(res.Msg.Machines) != 1 || res.Msg.Machines[0].Id != b.mid {
		t.Fatalf("b's machines: %v %v", res, err)
	}
	if res, err := b.client.ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{})); err != nil || len(res.Msg.Sessions) != 1 || res.Msg.Sessions[0].MachineId != b.mid {
		t.Fatalf("b's sessions: %v %v", res, err)
	}
	if res, err := b.client.ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{SessionIds: []string{"s1"}})); err != nil || len(res.Msg.Sessions) != 1 || res.Msg.Sessions[0].MachineId != b.mid {
		t.Fatalf("b's sessions by ID: %v %v", res, err)
	}
	if _, err := b.client.GetImage(ctx, connect.NewRequest(&pb.GetImageRequest{SessionId: "s1", ImageId: img})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("b reads a's image: %v", err)
	}
	if res, err := b.client.ListDevices(ctx, connect.NewRequest(&pb.ListDevicesRequest{})); err != nil || len(res.Msg.Devices) != 3 {
		t.Fatalf("b's devices: %v %v", res, err)
	} else {
		for _, d := range res.Msg.Devices {
			if d.Id == a.mid || d.Id == a.cid {
				t.Fatal("b lists a's devices")
			}
		}
	}
	meta, _ := e2e.Seal(e2e.NewKey(), &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_DEVICE_META, RequestId: a.cid, Seq: 1, Sender: b.cid}, []byte("name"))
	if _, err := b.client.PutDeviceMeta(ctx, connect.NewRequest(&pb.PutDeviceMetaRequest{DeviceId: a.cid, Meta: meta})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("b names a's device: %v", err)
	}
	if res, err := b.client.GetAccountPrefs(ctx, connect.NewRequest(&pb.GetAccountPrefsRequest{ObjectId: "project-prefs"})); err != nil || res.Msg.Object.Revision != 0 {
		t.Fatalf("b reads a's preferences: %v %v", res, err)
	}
	if _, err := ts.srv.store.device(ctx, a.mid); err != nil {
		t.Fatalf("a's machine after b removed it: %v", err)
	}
	cmd, _ := e2e.Seal(b.keys.Command, &pb.Header{Version: deviceauth.Version,
		Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: a.mid, RequestId: "0123456789abcdef0123456789abcdef",
		DeadlineMs: time.Now().Add(time.Minute).UnixMilli(), Sender: b.cid,
	}, []byte("cmd"))
	if _, err := b.client.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: a.mid, Command: cmd})); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("b reached a's machine: %v", err)
	}

	// b's stream carried none of it, up to b's own preferences.
	if err := b.putPrefs(t, 0); err != nil {
		t.Fatal(err)
	}
	for {
		f := bStream.read(t)
		if text := prototext.Format(f); strings.Contains(text, a.mid) || strings.Contains(text, a.cid) {
			t.Fatalf("b's stream carried a's %v", f)
		}
		if o := f.GetAccountPrefsUpdated(); o != nil {
			if o.Object.Revision != 1 {
				t.Fatalf("b's stream carried a's preferences: %v", o)
			}
			break
		}
	}
}

// A terminal stream pairs only the two sides of one account.
func TestTerminalStreamsDoNotJoinAcrossAccounts(t *testing.T) {
	h := &termHub{waiting: map[string]*termEnd{}}
	machine := &termEnd{dev: &device{id: "m", account: "a", kind: pb.DeviceKind_DEVICE_KIND_MACHINE}, joined: make(chan *termEnd, 1)}
	client := &termEnd{dev: &device{id: "c", account: "b", kind: pb.DeviceKind_DEVICE_KIND_CLIENT}, machine: "m", joined: make(chan *termEnd, 1)}
	if peer, err := h.join("x", machine); peer != nil || err != nil {
		t.Fatal(peer, err)
	}
	if _, err := h.join("x", client); err == nil {
		t.Fatal("a client joined another account's machine")
	}
}

func openRelay(t *testing.T) *testServer {
	t.Helper()
	ts := newTestServer(t)
	ts.srv.cfg.Signup, ts.srv.cfg.SignupBits, ts.srv.cfg.MaxAccounts = SignupOpen, 8, 100
	return ts
}

func solve(challenge []byte, bits uint) []byte {
	nonce := make([]byte, 8)
	for i := uint64(0); ; i++ {
		binary.BigEndian.PutUint64(nonce, i)
		if solved(challenge, nonce, bits) {
			return nonce
		}
	}
}

func (ts *testServer) signup(t *testing.T, keys *e2e.Keys, kind pb.DeviceKind, proof *pb.SignupProof) error {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: keys.AccountPublicKey(), DevicePublicKey: pub, Kind: kind})
	res, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(&pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: reg, Signature: keys.SignRegistration(reg), SignupProof: proof}))
	if err == nil {
		ts.rememberKey(res.Msg.DeviceId, priv)
	}
	return err
}

func (ts *testServer) proof(t *testing.T) *pb.SignupProof {
	t.Helper()
	res, err := ts.auth.GetSignupChallenge(context.Background(), connect.NewRequest(&pb.GetSignupChallengeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return &pb.SignupProof{Challenge: res.Msg.Challenge, Nonce: solve(res.Msg.Challenge, uint(res.Msg.DifficultyBits))}
}

func TestOpenSignup(t *testing.T) {
	ts := openRelay(t)
	ctx := context.Background()
	newKeys := func() *e2e.Keys { k, _ := e2e.Derive(e2e.NewKey()); return k }
	machine := pb.DeviceKind_DEVICE_KIND_MACHINE

	if err := ts.signup(t, newKeys(), machine, nil); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("no proof: %v", err)
	}
	p := ts.proof(t)
	wrong := &pb.SignupProof{Challenge: p.Challenge, Nonce: []byte("wrong")}
	if solved(wrong.Challenge, wrong.Nonce, ts.srv.cfg.SignupBits) {
		t.Skip("the wrong nonce happens to solve the challenge")
	}
	if err := ts.signup(t, newKeys(), machine, wrong); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("wrong proof: %v", err)
	}
	if err := ts.signup(t, newKeys(), machine, &pb.SignupProof{Challenge: []byte("made up"), Nonce: p.Nonce}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("made-up challenge: %v", err)
	}
	trial := newKeys()
	if err := ts.signup(t, trial, machine, p); err != nil {
		t.Fatal(err)
	}
	// A proof makes one account.
	if err := ts.signup(t, newKeys(), machine, p); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("proof reused: %v", err)
	}
	if a, err := ts.srv.store.account(ctx, e2e.AccountID(trial.AccountPublicKey())); err != nil || a.plan != "trial" {
		t.Fatalf("signed-up account: %v %v", a, err)
	}
	// The trial plan bounds the account: one more machine, then no more.
	if _, _, err := ts.register(t, trial, machine, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ts.register(t, trial, machine, ""); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("trial machine over the plan: %v", err)
	}
	// The setup token still makes the owner's accounts, without a proof.
	owner := newKeys()
	if _, _, err := ts.register(t, owner, machine, "setup"); err != nil {
		t.Fatal(err)
	}
	if a, err := ts.srv.store.account(ctx, e2e.AccountID(owner.AccountPublicKey())); err != nil || a.plan != "owner" {
		t.Fatalf("owner's account: %v %v", a, err)
	}
	// A registration that makes no account uses up neither its proof nor
	// the address's allowance: here its device key is the owner's machine.
	_, ownerMachine, _ := ts.register(t, owner, machine, "")
	taken, _ := ts.srv.store.tenant(e2e.AccountID(owner.AccountPublicKey())).registration(ctx, ownerMachine)
	takenReg := &pb.DeviceRegistration{}
	proto.Unmarshal(taken.raw, takenReg)
	thief := newKeys()
	stolen, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: thief.AccountPublicKey(), DevicePublicKey: takenReg.DevicePublicKey, Kind: machine})
	again := ts.proof(t)
	for range signupsPerHour + 1 {
		_, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: stolen, Signature: thief.SignRegistration(stolen), SignupProof: again}))
		if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "another account") {
			t.Fatalf("another account's device key: %v", err)
		}
	}
	if err := ts.signup(t, newKeys(), machine, again); err != nil {
		t.Fatalf("proof after a failed signup: %v", err)
	}
	// One address makes only a few accounts an hour.
	for range signupsPerHour - 2 {
		if err := ts.signup(t, newKeys(), machine, ts.proof(t)); err != nil {
			t.Fatal(err)
		}
	}
	if err := ts.signup(t, newKeys(), machine, ts.proof(t)); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("signups per address: %v", err)
	}
	// Nor do challenges come without bound.
	for {
		_, err := ts.auth.GetSignupChallenge(ctx, connect.NewRequest(&pb.GetSignupChallengeRequest{}))
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRelayAccountCap(t *testing.T) {
	ts := openRelay(t)
	n, err := ts.srv.store.accountCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ts.srv.cfg.MaxAccounts = n + 1
	k1, _ := e2e.Derive(e2e.NewKey())
	if _, _, err := ts.register(t, k1, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup"); err != nil {
		t.Fatal(err)
	}
	k2, _ := e2e.Derive(e2e.NewKey())
	if err := ts.signup(t, k2, pb.DeviceKind_DEVICE_KIND_MACHINE, ts.proof(t)); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("account over the relay's cap: %v", err)
	}
	if _, _, err := ts.register(t, k2, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("owner's account over the relay's cap: %v", err)
	}
	// Devices of an account already there are still welcome.
	if _, _, err := ts.register(t, k1, pb.DeviceKind_DEVICE_KIND_CLIENT, ""); err != nil {
		t.Fatal(err)
	}
}

func TestTokenRelayHasNoSignup(t *testing.T) {
	ts := newTestServer(t)
	if _, err := ts.auth.GetSignupChallenge(context.Background(), connect.NewRequest(&pb.GetSignupChallengeRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("signup challenge from a relay of one's own: %v", err)
	}
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "wrong"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("wrong setup token: %v", err)
	}
}

func TestSignupProof(t *testing.T) {
	c := []byte("challenge")
	n := solve(c, 12)
	sum := sha256.Sum256(append(append([]byte{}, c...), n...))
	if sum[0] != 0 || sum[1]>>4 != 0 {
		t.Fatalf("solved with %x", sum[:2])
	}
	if !solved(c, n, 12) || solved(c, n, 64) {
		t.Fatal("solved disagrees with the hash")
	}
}

func TestClientAddr(t *testing.T) {
	s := &Server{cfg: Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}}}
	for _, c := range []struct{ peer, xff, want string }{
		{"203.0.113.7:1234", "", "203.0.113.7"},
		// Only a trusted proxy is believed.
		{"203.0.113.7:1234", "198.51.100.1", "203.0.113.7"},
		{"172.18.0.2:1234", "198.51.100.1", "198.51.100.1"},
		// The client's own claims before the proxy's are not.
		{"172.18.0.2:1234", "192.0.2.9, 198.51.100.1", "198.51.100.1"},
		{"172.18.0.2:1234", "198.51.100.1, 172.18.0.3", "198.51.100.1"},
		{"172.18.0.2:1234", "garbage", "172.18.0.2"},
		// IPv6 counts by its /64.
		{"[2001:db8:1:2:3:4:5:6]:1234", "", "2001:db8:1:2::/64"},
		{"[::ffff:203.0.113.7]:1234", "", "203.0.113.7"},
	} {
		h := http.Header{}
		if c.xff != "" {
			h.Set("X-Forwarded-For", c.xff)
		}
		if got := s.clientAddr(c.peer, h); got != c.want {
			t.Errorf("clientAddr(%q, %q) = %q, want %q", c.peer, c.xff, got, c.want)
		}
	}
}

func TestAdmin(t *testing.T) {
	ts := newTestServerWithConfig(t, Config{MaxAccounts: 2, SweepInterval: 10 * time.Millisecond})
	ctx := context.Background()
	a := ts.account(t, ts.keys)
	other, _ := e2e.Derive(e2e.NewKey())
	b := ts.account(t, other)
	a.putSession(t, "s1")
	adm, err := OpenAdmin(ts.srv.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close()
	list, err := adm.Accounts(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("accounts: %v %v", list, err)
	}
	for _, info := range list {
		if info.ID == ts.acct() && (info.Devices != 3 || info.Machines != 1 || info.Sessions != 1 || info.Plan != "owner") {
			t.Fatalf("a as listed: %+v", info)
		}
	}
	if err := adm.SetSuspended(ctx, "nobody", true); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("suspend nobody: %v", err)
	}
	if err := adm.SetPlan(ctx, ts.acct(), "gold"); err == nil {
		t.Fatal("unknown plan set")
	}

	// A terminal stream waiting for its machine, the only stream of its device.
	term, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.url, "http")+"/v1/term", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer term.CloseNow()
	hello, _ := proto.Marshal(&pb.TermHello{Token: a.ctok, MachineId: a.mid, StreamId: "admin-term-stream-1"})
	if err := term.Write(ctx, websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the terminal stream", func() bool {
		ts.srv.streamMu.Lock()
		defer ts.srv.streamMu.Unlock()
		return len(ts.srv.streams[a.cid]) == 1
	})

	// A suspended account cannot do anything; the other one can.
	if err := adm.SetSuspended(ctx, ts.acct(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("suspended account: %v", err)
	}
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, ""); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("suspended account registers: %v", err)
	}
	if _, err := b.client.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); err != nil {
		t.Fatal(err)
	}
	// Its streams end with the sweep.
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, conn := range []*websocket.Conn{a.machineFrame.ws, term} {
		for {
			if _, _, err := conn.Read(rctx); err != nil {
				if rctx.Err() != nil {
					t.Fatal("a suspended account's stream stayed open")
				}
				break
			}
		}
	}
	if err := adm.SetSuspended(ctx, ts.acct(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); err != nil {
		t.Fatalf("resumed account: %v", err)
	}

	// Deleting an account leaves nothing of it, and nothing of b is lost.
	if err := adm.Delete(ctx, ts.acct()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("deleted account: %v", err)
	}
	for _, table := range accountTables {
		var n int
		if err := ts.srv.store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE account_id = ?`, ts.acct()).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%d rows of the deleted account in %s: %v", n, table, err)
		}
	}
	if _, err := b.client.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); err != nil {
		t.Fatal(err)
	}
	if err := adm.Delete(ctx, ts.acct()); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("delete again: %v", err)
	}
}

// A database of the relay before accounts is refused, not half-upgraded.
func TestOldLayoutIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY, public_key BLOB)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := openStore(path); !errors.Is(err, errOldLayout) {
		t.Fatalf("old layout: %v", err)
	}
	var tables int
	db, _ = sql.Open("sqlite", "file:"+path)
	defer db.Close()
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("%d tables after refusing: %v", tables, err)
	}
}

// A notification reaches the devices of its own account only.
func TestNotificationsStayInTheirAccount(t *testing.T) {
	ts := newTestServer(t)
	p := ts.srv.push
	hits := make(chan string, 4)
	p.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		hits <- r.URL.Host
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Header: make(http.Header)}, nil
	})}
	kinds := []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION}
	ctx := context.Background()
	if err := p.subscribe(ctx, ts.srv.store.tenant("a"), "client-a", pushSubscription(t, "https://a.example/push"), kinds); err != nil {
		t.Fatal(err)
	}
	if err := p.subscribe(ctx, ts.srv.store.tenant("b"), "client-b", pushSubscription(t, "https://b.example/push"), kinds); err != nil {
		t.Fatal(err)
	}
	if err := p.notify(context.Background(), ts.srv.store.tenant("b"), "s", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "k"); err != nil {
		t.Fatal(err)
	}
	// Nor can one account's device take another's subscription.
	if err := p.subscribe(ctx, ts.srv.store.tenant("b"), "client-a", pushSubscription(t, "https://b.example/push"), kinds); err != nil {
		t.Fatal(err)
	}
	var acct string
	if err := ts.srv.store.db.QueryRow(`SELECT account_id FROM push_subs WHERE device_id = 'client-a'`).Scan(&acct); err != nil || acct != "a" {
		t.Fatalf("a's subscription belongs to %q: %v", acct, err)
	}
	if host := <-hits; host != "b.example" {
		t.Fatalf("b's notification went to %s", host)
	}
	waitFor(t, "deliveries", func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.deliveries) == 0 })
	if len(hits) != 0 {
		t.Fatalf("b's notification went to %s too", <-hits)
	}
}

// Writes that need no plan of their own are bounded all the same.
func TestAccountGrowthIsBounded(t *testing.T) {
	ts := openRelay(t)
	ctx := context.Background()
	keys, _ := e2e.Derive(e2e.NewKey())
	if err := ts.signup(t, keys, pb.DeviceKind_DEVICE_KIND_MACHINE, ts.proof(t)); err != nil {
		t.Fatal(err)
	}
	cp, cid, err := ts.register(t, keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	client := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cp, cid)))
	put := func(id string, expected uint64) error {
		env, _ := e2e.Seal(e2e.NewKey(), &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_ACCOUNT_PREFS, RequestId: id, Seq: expected + 1, Sender: cid}, []byte("prefs"))
		_, err := client.PutAccountPrefs(ctx, connect.NewRequest(&pb.PutAccountPrefsRequest{ObjectId: id, ExpectedRevision: expected, Envelope: env}))
		return err
	}
	limit := planOf("trial").prefs
	for i := range limit {
		if err := put(fmt.Sprint("object-", i), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := put("one-more", 0); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("preference objects beyond the plan: %v", err)
	}
	if err := put("object-0", 1); err != nil {
		t.Fatalf("a new revision of an object: %v", err)
	}
	// Logging in again and again keeps a bounded number of tokens.
	for range maxDeviceTokens + 4 {
		ts.login(t, cp, cid)
	}
	var tokens int
	if err := ts.srv.store.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE device_id = ?`, cid).Scan(&tokens); err != nil || tokens != maxDeviceTokens {
		t.Fatalf("%d tokens of one device: %v", tokens, err)
	}
	// Removed devices are remembered up to the plan's bound.
	if err := planOf("trial").check(accountUsage{revoked: planOf("trial").revoked + 1}, growsDevices); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("removed devices beyond the plan: %v", err)
	}
	// and stop only new devices.
	if err := planOf("trial").check(accountUsage{revoked: planOf("trial").revoked + 1}, growsEvents); err != nil {
		t.Fatalf("removed devices stop events: %v", err)
	}
}

// One account's logins cannot take the relay's login challenges.
func TestLoginChallengesAreBoundedPerAccount(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.cfg.MaxAccounts = 6
	for i := range maxAccountChallenges * 2 {
		if _, err := ts.srv.newChallenge(fmt.Sprintf("device-%d", i), "a"); err != nil {
			t.Fatal(err)
		}
	}
	if n := ts.srv.pendingChallenges(); n != maxAccountChallenges {
		t.Fatalf("one account holds %d challenges", n)
	}
	if _, err := ts.srv.newChallenge("device-b", "b"); err != nil {
		t.Fatal(err)
	}
	// The relay has room for every account's share.
	for a := range 4 {
		for i := range maxAccountChallenges {
			if _, err := ts.srv.newChallenge(fmt.Sprintf("device-%d-%d", a, i), fmt.Sprint("acct-", a)); err != nil {
				t.Fatalf("account %d, challenge %d: %v", a, i, err)
			}
		}
	}
}

// Expired images keep their rows, which a plan bounds by count.
func TestImageRowsAreBounded(t *testing.T) {
	ts := newTestServer(t)
	a := ts.account(t, ts.keys)
	a.putSession(t, "s")
	st := ts.tenant()
	ctx := context.Background()
	for i := range 2 {
		if _, err := st.putImage(ctx, a.mid, "s", fmt.Sprintf("%032d", i), []byte("x"), -time.Second, 1<<20, 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.putImage(ctx, a.mid, "s", fmt.Sprintf("%032d", 2), []byte("x"), time.Hour, 1<<20, 2); !errors.Is(err, errQuota) {
		t.Fatalf("an image beyond the rows of expired ones: %v", err)
	}
}
