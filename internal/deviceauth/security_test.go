package deviceauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

type testMember struct {
	m    Member
	ed   ed25519.PrivateKey
	x    []byte
	e    *Endpoint
	path string
}
type fixture struct {
	a       *Authority
	pin     Pin
	members []*testMember
	dir     string
}

func member(t *testing.T, kind Kind) *testMember {
	t.Helper()
	pub, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	x, xpub, err := NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMember(pub, xpub, kind)
	if err != nil {
		t.Fatal(err)
	}
	return &testMember{m: m, ed: ed, x: x}
}
func setup(t *testing.T, kinds ...Kind) *fixture {
	t.Helper()
	dir := t.TempDir()
	m := member(t, Machine)
	a, err := InitAuthority(filepath.Join(dir, "authority.db"), "0123456789abcdef0123456789abcdef", m.m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	pin, s := a.Snapshot()
	e, err := InitEndpoint(filepath.Join(dir, "member0.db"), pin, s, m.m.ID, m.ed.Seed(), m.x)
	if err != nil {
		t.Fatal(err)
	}
	m.e = e
	m.path = filepath.Join(dir, "member0.db")
	t.Cleanup(func() { m.e.Close() })
	f := &fixture{a: a, pin: pin, members: []*testMember{m}, dir: dir}
	for _, kind := range kinds {
		f.add(t, kind)
	}
	return f
}
func (f *fixture) add(t *testing.T, kind Kind) *testMember {
	t.Helper()
	m := member(t, kind)
	id := randomID(t)
	ctx, err := f.a.BeginPair(id, f.members[0].m.ID, m.m)
	if err != nil {
		t.Fatal(err)
	}
	as, err := SignPair(ctx, f.members[0].ed, false)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := SignPair(ctx, m.ed, true)
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.a.ApprovePair(id, as, cs)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range f.members {
		if err := other.e.Sync([]State{s}); err != nil {
			t.Fatal(err)
		}
	}
	m.path = filepath.Join(f.dir, "member-"+m.m.ID+".db")
	m.e, err = InitEndpoint(m.path, f.pin, s, m.m.ID, m.ed.Seed(), m.x)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.e.Close() })
	f.members = append(f.members, m)
	return m
}
func randomID(t *testing.T) string {
	t.Helper()
	h, err := randomHash()
	if err != nil {
		t.Fatal(err)
	}
	return h.String()[:32]
}
func encoded(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func rpc(t *testing.T, f *fixture, m *testMember, method Method, body, out any) error {
	t.Helper()
	q, w, err := Request(f.pin, m.ed, method, body)
	if err != nil {
		return err
	}
	r, err := f.a.Handle(q)
	if err != nil {
		return err
	}
	return w.Reply(r, m.x, out)
}
func authorized(t *testing.T, f *fixture, m *testMember, h Header, seed, plain []byte) Wire {
	t.Helper()
	c, err := m.e.Prepare(h, seed, plain)
	if err != nil {
		t.Fatal(err)
	}
	q, err := AuthorizationRequest(c)
	if err != nil {
		t.Fatal(err)
	}
	var reply AuthorizeReply
	if err := rpc(t, f, m, Authorize, q, &reply); err != nil {
		t.Fatal(err)
	}
	_, s := f.a.Snapshot()
	w, err := reply.Attach(c, f.pin, s)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func deleteReq(t *testing.T, f *fixture, requester *testMember, targets ...*testMember) DeleteRequest {
	t.Helper()
	_, s := f.a.Snapshot()
	sh, _ := s.Hash()
	nonce, _ := randomHash()
	r := DeleteRequest{Account: f.pin.Account, Domain: f.pin.Domain, Requester: requester.m.ID, Operation: randomID(t), Expected: sh, Nonce: nonce, ExactIDs: true}
	for _, m := range targets {
		r.Targets = append(r.Targets, Target{ID: m.m.ID})
	}
	slices.SortFunc(r.Targets, func(a, b Target) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	if err := r.Sign(requester.ed); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPinnedMembershipAndGrantIsolation(t *testing.T) {
	f := setup(t, Client)
	p, s := f.a.Snapshot()
	if err := p.Verify(s); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.members {
		key, err := p.OpenGrant(s, m.m.ID, m.x)
		if err != nil || len(key) != 32 {
			t.Fatal(err)
		}
	}
	if _, err := p.OpenGrant(s, f.members[0].m.ID, f.members[1].x); err == nil {
		t.Fatal("wrong recipient opened grant")
	}
	b, err := s.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseState(b)
	if err != nil || p.Verify(parsed) != nil {
		t.Fatal(err)
	}
	for i := range b {
		bad := bytes.Clone(b)
		bad[i] ^= 1
		bs, err := ParseState(bad)
		if err == nil && p.Verify(bs) == nil {
			t.Fatalf("mutation accepted at %d", i)
		}
	}
	if _, err := ParseState(append(b, 0)); err == nil {
		t.Fatal("trailing bytes")
	}
	bad := clone(s)
	bad.Core.AuthorityEd = bad.Core.Members[0].EdPublic
	if p.Verify(bad) == nil {
		t.Fatal("authority replacement")
	}
	bad = clone(s)
	slices.Reverse(bad.Core.Members)
	if _, err := bad.MarshalBinary(); err == nil {
		t.Fatal("unsorted roster")
	}
	oldPage, err := f.a.StatePage(0)
	if err != nil || len(oldPage) != 2 || p.Advance(oldPage[0], oldPage[1]) != nil {
		t.Fatal(err)
	}
}

func TestExactCipherAuthorizationAndScope(t *testing.T) {
	f := setup(t, Client)
	m := f.members[1]
	target := f.members[0]
	h := Header{Purpose: Command, Kind: 5, Machine: target.m.ID, Session: "session-demo", Request: randomID(t), Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}
	plain := encoded(t, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: h.Session, Text: "synthetic command"}}, ProtocolVersion: 8})
	w := authorized(t, f, m, h, nil, plain)
	p, s := f.a.Snapshot()
	if _, out, err := w.Open(p, s, f.a.data.Secrets[s.Core.Epoch], nil); err != nil || !bytes.Equal(out, plain) {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	if err := target.e.Execute(w, nil, uint64(time.Now().UnixMilli()), func(Header, []byte) error { accepted.Add(1); return nil }); err != nil || accepted.Load() != 1 {
		t.Fatal(err)
	}
	mutations := []func(*Wire){func(w *Wire) { w.Cipher[0] ^= 1 }, func(w *Wire) { w.Nonce[0] ^= 1 }, func(w *Wire) { w.Origin[0] ^= 1 }, func(w *Wire) { w.AuthSignature[0] ^= 1 }, func(w *Wire) {
		a, _ := ParseWriteAuth(w.Authorization)
		a.Header.Purpose = Result
		a.Header.Kind = 6
		hb, _ := a.Header.MarshalBinary()
		a.HeaderHash = digest("", hb)
		w.Authorization, _ = a.MarshalBinary()
		w.AuthSignature = ed25519.Sign(ed25519.NewKeyFromSeed(f.a.data.EdSeed), writeSignBytes(w.Authorization))
	}, func(w *Wire) {
		a, _ := ParseWriteAuth(w.Authorization)
		a.Header.Request = randomID(t)
		hb, _ := a.Header.MarshalBinary()
		a.HeaderHash = digest("", hb)
		w.Authorization, _ = a.MarshalBinary()
		w.AuthSignature = ed25519.Sign(ed25519.NewKeyFromSeed(f.a.data.EdSeed), writeSignBytes(w.Authorization))
	}, func(w *Wire) {
		a, _ := ParseWriteAuth(w.Authorization)
		a.WriterKind = Machine
		w.Authorization, _ = a.MarshalBinary()
		w.AuthSignature = ed25519.Sign(ed25519.NewKeyFromSeed(f.a.data.EdSeed), writeSignBytes(w.Authorization))
	}}
	for i, mutate := range mutations {
		bad := clone(w)
		mutate(&bad)
		if _, err := bad.Verify(p, s); err == nil {
			t.Fatalf("mutation %d accepted", i)
		}
	}
	// A retained root holder invents a device: even a perfect self-signature and
	// relay routing ID cannot reach authorization without current membership.
	attacker := member(t, Client)
	ch, _ := ParseHeader(w.Header)
	ch.Sender = attacker.m.ID
	ch.Request = randomID(t)
	c, err := Seal(f.a.data.Secrets[s.Core.Epoch], nil, ch, plain, attacker.ed)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := AuthorizationRequest(c)
	if err := rpc(t, f, attacker, Authorize, q, &AuthorizeReply{}); err == nil {
		t.Fatal("root-invented identity authorized")
	}
	// A valid output signature/auth cannot be used as input after decryption.
	seed := bytes.Repeat([]byte{3}, 32)
	badBody := encoded(t, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("x")}})
	outH := Header{Purpose: TerminalOutput, Kind: 7, Machine: target.m.ID, Session: "session-demo", Request: randomID(t), Seq: 1}
	ow := authorized(t, f, target, outH, seed, badBody)
	if _, _, err := ow.Open(p, s, f.a.data.Secrets[s.Core.Epoch], seed); err == nil {
		t.Fatal("output authorization accepted input body")
	}
}

func TestRevocationBarrierAndPendingGroupExtension(t *testing.T) {
	f := setup(t, Machine, Machine, Client)
	a0, m1, m2, client := f.members[0], f.members[1], f.members[2], f.members[3]
	req := deleteReq(t, f, client, client)
	oldPin, oldState := f.a.Snapshot()
	oldSecret := bytes.Clone(f.a.data.Secrets[oldState.Core.Epoch])
	h := Header{Purpose: Command, Kind: 5, Machine: m1.m.ID, Session: "session-demo", Request: randomID(t), Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}
	plain := encoded(t, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: h.Session, Text: "old"}}})
	oldWire := authorized(t, f, client, h, nil, plain)
	g, err := f.a.Delete(req)
	if err != nil || len(g.Required) != 3 {
		t.Fatal(err)
	}
	if _, err := f.a.Completion(req.Operation); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if _, err := rpcCompletion(f, client, req.Operation); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	newSecret, err := f.pin.OpenGrant(g.Head, m1.m.ID, m1.x)
	if err != nil || bytes.Equal(oldSecret, newSecret) {
		t.Fatal("secret did not rotate", err)
	}
	if _, err := f.pin.OpenGrant(g.Head, client.m.ID, client.x); err == nil {
		t.Fatal("removed member received grant")
	}
	if _, err := f.a.BeginPair(randomID(t), a0.m.ID, member(t, Client).m); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	fences, err := f.a.FenceRequests()
	if err != nil {
		t.Fatal(err)
	}
	var oldAck FenceReceipt
	for _, fr := range fences {
		if fr.Executor == m1.m.ID {
			oldAck, err = m1.e.Fence(fr, []State{g.Head})
			if err != nil {
				t.Fatal(err)
			}
			if done, err := f.a.Ack(oldAck); err != nil || done != nil {
				t.Fatal(err)
			}
		}
	}
	if err := m1.e.Execute(oldWire, nil, uint64(time.Now().UnixMilli()), func(Header, []byte) error { t.Fatal("old backend call accepted"); return nil }); err == nil {
		t.Fatal("old operation accepted")
	}
	// Offline m2 is not silently excluded. A new explicit operation removes it,
	// advances epoch, discards old ACKs, and reduces the required set to a0,m1.
	req2 := deleteReq(t, f, a0, m2)
	g2, err := f.a.Delete(req2)
	if err != nil || len(g2.Required) != 2 || len(g2.Receipts) != 0 || g2.Head.Core.Epoch != g.Head.Core.Epoch+1 {
		t.Fatal(err)
	}
	if _, err := f.a.Ack(oldAck); err == nil {
		t.Fatal("old ACK completed extended group")
	}
	fences, _ = f.a.FenceRequests()
	var completion *Completion
	for _, fr := range fences {
		m := a0
		chain := []State{g.Head, g2.Head}
		if fr.Executor == m1.m.ID {
			m = m1
			chain = []State{g2.Head}
		}
		ack, err := m.e.Fence(fr, chain)
		if err != nil {
			t.Fatal(err)
		}
		completion, err = f.a.Ack(ack)
		if err != nil {
			t.Fatal(err)
		}
	}
	if completion == nil {
		t.Fatal("all required receipts did not complete")
	}
	if err := completion.Verify(f.pin, req, []State{g.Head, g2.Head}); err != nil {
		t.Fatal(err)
	}
	if err := completion.Verify(f.pin, req2, []State{g.Head, g2.Head}); err != nil {
		t.Fatal(err)
	}
	if err := completion.Verify(oldPin, req, []State{oldState, g2.Head}); err == nil {
		t.Fatal("invalid completion chain")
	}
	polled, err := rpcCompletion(f, client, req.Operation)
	if err != nil || polled.Verify(f.pin, req, []State{g.Head, g2.Head}) != nil {
		t.Fatal(err)
	}
	if err := rpc(t, f, client, HistoryKeys, KeyQuery{}, &KeyReply{}); err == nil {
		t.Fatal("revoked member read new keys")
	}
	if _, err := f.a.Delete(req); err != nil {
		t.Fatal("exact delete retry lost completion", err)
	}
}
func rpcCompletion(f *fixture, m *testMember, id string) (*Completion, error) {
	q, w, err := Request(f.pin, m.ed, GetCompletion, CompletionQuery{Operation: id})
	if err != nil {
		return nil, err
	}
	r, err := f.a.Handle(q)
	if err != nil {
		return nil, err
	}
	var out Completion
	if err = w.Reply(r, m.x, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func TestFenceWaitsForActualBackendAndSurvivesRestart(t *testing.T) {
	f := setup(t, Client)
	target, client := f.members[0], f.members[1]
	h := Header{Purpose: Command, Kind: 5, Machine: target.m.ID, Session: "s", Request: randomID(t), Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}
	w := authorized(t, f, client, h, nil, encoded(t, &pb.Command{Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: "s"}}}))
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- target.e.Execute(w, nil, uint64(time.Now().UnixMilli()), func(Header, []byte) error { close(entered); <-release; return nil })
	}()
	<-entered
	req := deleteReq(t, f, target, client)
	g, err := f.a.Delete(req)
	if err != nil {
		t.Fatal(err)
	}
	frs, _ := f.a.FenceRequests()
	ackCh := make(chan FenceReceipt, 1)
	ferr := make(chan error, 1)
	go func() {
		r, err := target.e.Fence(frs[0], []State{g.Head})
		if err != nil {
			ferr <- err
			return
		}
		ackCh <- r
	}()
	select {
	case <-ackCh:
		t.Fatal("ACK before backend call completed")
	case err := <-ferr:
		t.Fatal(err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	ack := <-ackCh
	if _, err := f.a.Ack(ack); err != nil {
		t.Fatal(err)
	}
	if err := target.e.Close(); err != nil {
		t.Fatal(err)
	}
	target.e, err = OpenEndpoint(target.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.e.Execute(w, nil, uint64(time.Now().UnixMilli()), func(Header, []byte) error { t.Fatal("old control after restart"); return nil }); err == nil {
		t.Fatal("old control accepted")
	}
	if _, err := OpenEndpoint(target.path); err == nil {
		t.Fatal("second endpoint owner opened live DB")
	}
}

func TestCatalogCASLostReplyAndTargetNameBinding(t *testing.T) {
	f := setup(t, Client)
	editor := f.members[0]
	subject := f.members[1]
	h := Header{Purpose: DeviceMeta, Kind: 13, Request: subject.m.ID, Seq: 1}
	w := authorized(t, f, editor, h, nil, encoded(t, &pb.DeviceMeta{Name: "Phone A"}))
	q, _ := AuthorizationRequest(w.Ciphertext)
	var retry AuthorizeReply
	if err := rpc(t, f, editor, Authorize, q, &retry); err != nil || !bytes.Equal(retry.Signature, w.AuthSignature) {
		t.Fatal("lost reply retry", err)
	}
	var catalog CatalogReply
	if err := rpc(t, f, editor, Catalog, CatalogQuery{}, &catalog); err != nil || len(catalog.Entries) != 1 {
		t.Fatal(err)
	}
	commit, _ := w.Commitment()
	req := deleteReq(t, f, editor, subject)
	req.ExactIDs = false
	req.Targets[0].MetadataCommit = commit
	req.Sign(editor.ed)
	h.Seq = 2
	w2 := authorized(t, f, editor, h, nil, encoded(t, &pb.DeviceMeta{Name: "Phone B"}))
	if _, err := f.a.Delete(req); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted after concurrent rename", err)
	}
	var newer CatalogReply
	if err := rpc(t, f, editor, Catalog, CatalogQuery{}, &newer); err != nil {
		t.Fatal(err)
	}
	got, _ := newer.Entries[0].Wire.Commitment()
	want, _ := w2.Commitment()
	if got != want {
		t.Fatal("catalog replayed stale name")
	}
	if err := rpc(t, f, editor, Authorize, q, &retry); !errors.Is(err, ErrConflict) {
		t.Fatal("old revision overwrote latest", err)
	}
	// A genuinely signed response for the old query cannot satisfy a fresh nonce.
	c, wtr, err := Request(f.pin, editor.ed, Catalog, CatalogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.a.Handle(c)
	if err != nil {
		t.Fatal(err)
	}
	_, fresh, _ := Request(f.pin, editor.ed, Catalog, CatalogQuery{})
	if err := fresh.Reply(r, editor.x, &CatalogReply{}); err == nil {
		t.Fatal("fresh query accepted old response")
	}
	if err := wtr.Reply(r, editor.x, &CatalogReply{}); err != nil {
		t.Fatal(err)
	}
}

func TestPairContextsCancelExpiryRevocationAndRestart(t *testing.T) {
	f := setup(t, Client)
	approver := f.members[1]
	candidate := member(t, Client)
	p, err := f.a.BeginPair(randomID(t), approver.m.ID, candidate.m)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.Binding()
	cancel := ed25519.Sign(approver.ed, append([]byte("ra2/pair-cancel\x00"), b...))
	if err := f.a.CancelPair(p.ID, cancel); err != nil {
		t.Fatal(err)
	}
	as, _ := SignPair(p, approver.ed, false)
	cs, _ := SignPair(p, candidate.ed, true)
	if _, err := f.a.ApprovePair(p.ID, as, cs); err == nil {
		t.Fatal("canceled context revived")
	}
	p, err = f.a.BeginPair(randomID(t), approver.m.ID, candidate.m)
	if err != nil {
		t.Fatal(err)
	}
	as, _ = SignPair(p, approver.ed, false)
	cs, _ = SignPair(p, candidate.ed, true)
	req := deleteReq(t, f, f.members[0], approver)
	if _, err := f.a.Delete(req); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.dir, "authority.db")
	if err := f.a.Close(); err != nil {
		t.Fatal(err)
	}
	f.a, err = OpenAuthority(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.a.Close() })
	if _, err := f.a.ApprovePair(p.ID, as, cs); err == nil {
		t.Fatal("revoked approver's context revived after restart")
	}
}

func TestSecurityStoreFailsClosedAndCounterNeverReused(t *testing.T) {
	f := setup(t)
	m := f.members[0]
	h := Header{Purpose: MachineMeta, Kind: 4, Machine: m.m.ID, Seq: 1}
	plain := encoded(t, &pb.MachineMeta{Hostname: "test host"})
	c, err := m.e.Prepare(h, nil, plain)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := ParseHeader(c.Header)
	if err := m.e.Close(); err != nil {
		t.Fatal(err)
	}
	m.e, err = OpenEndpoint(m.path)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := m.e.Prepare(h, nil, plain)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := ParseHeader(c2.Header)
	if h2.Counter <= h1.Counter {
		t.Fatal("counter reused")
	}
	key1, _ := DeriveKey(m.e.data.Secret, nil, h1)
	key2, _ := DeriveKey(m.e.data.Secret, nil, h2)
	if bytes.Equal(key1, key2) {
		t.Fatal("message key reused")
	}
	if _, err := OpenEndpoint(filepath.Join(f.dir, "missing.db")); err == nil {
		t.Fatal("missing state created default floor")
	}
	// Inject an indeterminate local write failure: no old-head signing afterward.
	if err := f.a.disk.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.BeginPair(randomID(t), m.m.ID, member(t, Client).m); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	q, w, err := Request(f.pin, m.ed, States, StateQuery{})
	_ = w
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Handle(q); !errors.Is(err, ErrStorage) {
		t.Fatal("poisoned authority answered", err)
	}
	// Random snapshot corruption of a valid JSON value is caught independently
	// of JSON parsing and signatures. No plaintext/key data is logged.
	if _, err = m.e.disk.db.Exec("UPDATE security SET snapshot=?", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	m.e.Close()
	if _, err := OpenEndpoint(m.path); err == nil {
		t.Fatal("corrupt snapshot loaded")
	}
	if err := os.Remove(m.path); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEndpoint(m.path); err == nil {
		t.Fatal("deleted state recreated")
	}
}

func TestCanonicalBoundsAndKDFIsolation(t *testing.T) {
	f := setup(t, Client)
	_, s := f.a.Snapshot()
	sh, _ := s.Hash()
	h := Header{Account: f.pin.Account, Domain: f.pin.Domain, Epoch: s.Core.Epoch, ContentEpoch: s.Core.Epoch, State: sh, Sender: f.members[0].m.ID, Purpose: SessionMeta, Kind: 2, Machine: f.members[0].m.ID, Session: "s", Seq: 1, Counter: 1}
	b, err := h.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(b); i++ {
		if _, err := ParseHeader(b[:i]); err == nil {
			t.Fatalf("truncation %d", i)
		}
	}
	if _, err := ParseHeader(append(b, 0)); err == nil {
		t.Fatal("trailing bytes")
	}
	secret := bytes.Repeat([]byte{9}, 32)
	key, _ := DeriveKey(secret, nil, h)
	mutations := []func(*Header){func(h *Header) { h.Account = "11111111111111111111111111111111" }, func(h *Header) { h.Domain[0] ^= 1 }, func(h *Header) { h.ContentEpoch++ }, func(h *Header) { h.Sender = f.members[1].m.ID }, func(h *Header) { h.Session = "other" }, func(h *Header) { h.Seq++ }, func(h *Header) { h.Counter++ }}
	for i, mutate := range mutations {
		other := h
		mutate(&other)
		k, err := DeriveKey(secret, nil, other)
		if err == nil && bytes.Equal(k, key) {
			t.Fatalf("KDF mutation %d shared key", i)
		}
	}
	if _, err := DeriveKey(secret, nil, Header{}); err == nil {
		t.Fatal("invalid header key")
	}
	raw, _ := json.Marshal(f.a.data)
	if bytes.Contains(raw, []byte("root")) {
		t.Fatal("authority state references legacy root")
	}
}

func TestAcceptedOperationCannotRepeatAfterLostReplyCrashOrLogCorruption(t *testing.T) {
	f := setup(t, Client)
	target, client := f.members[0], f.members[1]
	h := Header{Purpose: Command, Kind: 5, Machine: target.m.ID, Session: "s", Request: randomID(t), Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}
	plain := encoded(t, &pb.Command{Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: "s"}}})
	wire := authorized(t, f, client, h, nil, plain)
	var calls atomic.Int32
	backend := func(Header, []byte) ([]byte, error) { calls.Add(1); return []byte("synthetic-result"), nil }
	for range 2 {
		out, err := target.e.Accept(wire, nil, uint64(time.Now().UnixMilli()), backend)
		if err != nil || string(out) != "synthetic-result" {
			t.Fatal(err)
		}
	}
	if err := target.e.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	target.e, err = OpenEndpoint(target.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.e.Accept(wire, nil, uint64(time.Now().UnixMilli()), backend); err != nil || calls.Load() != 1 {
		t.Fatal("operation repeated after reopen", calls.Load(), err)
	}
	h.Request = randomID(t)
	crashWire := authorized(t, f, client, h, nil, plain)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("test backend did not panic")
			}
		}()
		target.e.Accept(crashWire, nil, uint64(time.Now().UnixMilli()), func(Header, []byte) ([]byte, error) { panic("synthetic process interruption") })
	}()
	target.e.Close()
	target.e, err = OpenEndpoint(target.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = target.e.Accept(crashWire, nil, uint64(time.Now().UnixMilli()), backend); !errors.Is(err, ErrUnknown) || calls.Load() != 1 {
		t.Fatal("unknown operation repeated", calls.Load(), err)
	}
	if _, err = target.e.disk.db.Exec("DELETE FROM accepted"); err != nil {
		t.Fatal(err)
	}
	target.e.Close()
	if _, err = OpenEndpoint(target.path); !errors.Is(err, ErrStorage) {
		t.Fatal("missing accepted log silently reset", err)
	}
}

func TestAutomaticStaleSyncFenceAndHistoricalEpochRead(t *testing.T) {
	f := setup(t, Client, Client)
	machine, client, removed := f.members[0], f.members[1], f.members[2]
	exchange := func(_ context.Context, c Capsule) (Capsule, error) { return f.a.Handle(c) }
	mr := Remote{Endpoint: machine.e, Exchange: exchange}
	cr := Remote{Endpoint: client.e, Exchange: exchange}
	ctx := context.Background()
	seed := bytes.Repeat([]byte{5}, 32)
	oldEvent := encoded(t, &pb.Event{SessionId: "s", Seq: 1, Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "past"}}})
	old, err := mr.Seal(ctx, Header{Purpose: Event, Kind: 1, Machine: machine.m.ID, Session: "s", Seq: 1}, seed, oldEvent)
	if err != nil {
		t.Fatal(err)
	}
	req := deleteReq(t, f, client, removed)
	if _, err := f.a.Delete(req); err != nil {
		t.Fatal(err)
	}
	cmd := encoded(t, &pb.Command{Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: "s"}}})
	wire, err := cr.Seal(ctx, Header{Purpose: Command, Kind: 5, Machine: machine.m.ID, Session: "s", Request: randomID(t), Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}, nil, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := mr.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Completion(req.Operation); err != nil {
		t.Fatal("automatic executor fence did not complete", err)
	}
	if err := machine.e.Execute(wire, nil, uint64(time.Now().UnixMilli()), func(Header, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cr.OpenCurrent(ctx, old, seed); !errors.Is(err, ErrStale) {
		t.Fatal("past data claimed as current", err)
	}
	if _, plain, err := cr.OpenHistory(ctx, old, seed); err != nil || !bytes.Equal(plain, oldEvent) {
		t.Fatal(err)
	}
	_, current, _ := client.e.Snapshot()
	if current.Core.Epoch != 4 {
		t.Fatal("history rolled back membership")
	}
}

func TestMaximumGroupedCompletionFitsBoundedHPKERPC(t *testing.T) {
	// Independent maxima overestimate every legal group: sum of removed targets
	// and active survivors actually cannot both reach the membership cap.
	// This catches JSON/base64 expansion, not only the binary proof size.
	var domain Hash
	for i := range domain {
		domain[i] = 255
	}
	core := Core{Account: "ffffffffffffffffffffffffffffffff", Domain: domain, Parent: Hash{1}, Epoch: 2, ContentEpoch: 2, AuthorityEd: [32]byte{253}, AuthorityX: [32]byte{252}}
	for i := 1; i <= MaxMembers; i++ {
		ed := bytes.Repeat([]byte{byte(i)}, 32)
		m, err := NewMember(ed, bytes.Repeat([]byte{byte(i)}, 32), Machine)
		if err != nil {
			t.Fatal(err)
		}
		core.Members = append(core.Members, m)
	}
	slices.SortFunc(core.Members, func(a, b Member) int {
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	core.AuthorityID = core.Members[0].ID
	state := State{Core: core, Signature: bytes.Repeat([]byte{255}, 64)}
	for _, m := range core.Members {
		state.Grants = append(state.Grants, Grant{Recipient: m.ID, Enc: [32]byte{255}, Cipher: bytes.Repeat([]byte{255}, 48)})
	}
	g := RevocationGroup{ID: strings.Repeat("g", 128), Base: state, Head: state, Complete: true}
	for i, m := range core.Members {
		op := DeleteRequest{Account: core.Account, Domain: domain, Requester: m.ID, Operation: strings.Repeat("o", 124) + fmt.Sprintf("%04d", i), Expected: domain, Nonce: domain, Targets: []Target{{ID: m.ID, MetadataCommit: domain}}, Signature: bytes.Repeat([]byte{255}, 64)}
		g.Operations = append(g.Operations, GroupOperation{Request: op, Digest: domain, NewState: domain})
		g.Required = append(g.Required, m.ID)
		g.Receipts = append(g.Receipts, FenceReceipt{Account: core.Account, Domain: domain, AuthorityID: core.AuthorityID, Executor: m.ID, Group: g.ID, Revision: 128, Epoch: 2, State: domain, Signature: bytes.Repeat([]byte{255}, 64)})
	}
	body, err := json.Marshal(Completion{Group: g, Signature: bytes.Repeat([]byte{255}, 64)})
	if err != nil {
		t.Fatal(err)
	}
	reply := rpcResponse{Nonce: domain, RequestHash: domain, Body: body, Signature: bytes.Repeat([]byte{255}, 64)}
	plain, err := json.Marshal(reply)
	if err != nil || len(plain) > MaxRPCBytes-16 {
		t.Fatalf("max grouped completion exceeds RPC bound: %d > %d, %v", len(plain), MaxRPCBytes-16, err)
	}
}

func TestRemovedRequesterCanFetchOnlyItsPublicCompletionChain(t *testing.T) {
	f := setup(t, Client)
	requester := f.members[1]
	request := deleteReq(t, f, requester, requester)
	group, err := f.a.Delete(request)
	if err != nil {
		t.Fatal(err)
	}
	fr, _ := f.a.FenceRequests()
	ack, err := f.members[0].e.Fence(fr[0], []State{group.Head})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Ack(ack); err != nil {
		t.Fatal(err)
	}
	var page StateReply
	if err := rpc(t, f, requester, CompletionStates, CompletionQuery{Operation: request.Operation, After: group.Base.Core.Epoch}, &page); err != nil || len(page.States) != 1 {
		t.Fatal(err)
	}
	if err := rpc(t, f, requester, States, StateQuery{}, &page); err == nil {
		t.Fatal("removed requester read unrelated state pages")
	}
	if err := rpc(t, f, requester, CompletionStates, CompletionQuery{Operation: "unrelated-operation", After: group.Base.Core.Epoch}, &page); err == nil {
		t.Fatal("unrelated completion chain disclosed")
	}
	var keys KeyReply
	if err := rpc(t, f, requester, HistoryKeys, KeyQuery{}, &keys); err == nil {
		t.Fatal("completion access granted keys")
	}
}

func TestUnnamedGUIIdentityBindingRejectsConcurrentFirstName(t *testing.T) {
	f := setup(t, Client)
	actor, target := f.members[0], f.members[1]
	_, s := f.a.Snapshot()
	req := deleteReq(t, f, actor, target)
	req.ExactIDs = false
	req.Targets[0].MetadataCommit = UnnamedMemberCommit(s, target.m)
	if err := req.Sign(actor.ed); err != nil {
		t.Fatal(err)
	}
	authorized(t, f, target, Header{Purpose: DeviceMeta, Kind: 13, Request: target.m.ID, Seq: 1}, nil, encoded(t, &pb.DeviceMeta{Name: "newly named"}))
	if _, err := f.a.Delete(req); !errors.Is(err, ErrConflict) {
		t.Fatal("stale unnamed GUI target removed", err)
	}
}

func TestExpiredAcceptedRequestCannotResurrectAfterClockCorrectionOrRestart(t *testing.T) {
	f := setup(t, Client)
	f.a.now = func() time.Time { return time.UnixMilli(900) }
	owner, client := f.members[0], f.members[1]
	body := encoded(t, &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
	original := authorized(t, f, client, Header{Kind: 5, Purpose: Command, Machine: owner.m.ID, Request: "original-before-clock-correction", Deadline: 1000}, nil, body)
	next := authorized(t, f, client, Header{Kind: 5, Purpose: Command, Machine: owner.m.ID, Request: "new-request-prunes-expired-log", Deadline: 3000}, nil, body)
	calls := 0
	run := func(Header, []byte) ([]byte, error) { calls++; return []byte("synthetic result"), nil }
	if _, err := owner.e.Accept(original, nil, 900, run); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.e.Accept(next, nil, 1500, run); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.e.Accept(original, nil, 900, run); !errors.Is(err, ErrExpired) {
		t.Fatalf("clock correction revived an expired command: %v", err)
	}
	if err := owner.e.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	owner.e, err = OpenEndpoint(owner.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.e.Accept(original, nil, 900, run); !errors.Is(err, ErrExpired) {
		t.Fatalf("restart lost expiration floor: %v", err)
	}
	if calls != 2 {
		t.Fatalf("backend ran %d times", calls)
	}
}

func TestReauthorizedSameIntentAcrossEpochCannotExecuteTwice(t *testing.T) {
	f := setup(t, Client)
	target, client := f.members[0], f.members[1]
	h := Header{Purpose: Command, Kind: 5, Machine: target.m.ID, Session: "s", Request: randomID(t), Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}
	plain := encoded(t, &pb.Command{Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: "s"}}})
	first := authorized(t, f, client, h, nil, plain)
	calls := 0
	backend := func(Header, []byte) ([]byte, error) { calls++; return []byte("accepted-once"), nil }
	if _, err := target.e.Accept(first, nil, uint64(time.Now().UnixMilli()), backend); err != nil {
		t.Fatal(err)
	}
	f.add(t, Client)
	retry := authorized(t, f, client, h, nil, plain)
	if bytes.Equal(first.Header, retry.Header) || bytes.Equal(first.Cipher, retry.Cipher) {
		t.Fatal("epoch change did not rekey retry")
	}
	out, err := target.e.Accept(retry, nil, uint64(time.Now().UnixMilli()), backend)
	if err != nil || string(out) != "accepted-once" || calls != 1 {
		t.Fatal("reauthorized retry repeated or lost accepted operation", calls, err)
	}
}
