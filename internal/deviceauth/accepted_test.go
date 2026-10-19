package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

type currentChain struct {
	genesis, added, withClient State
	pin                        Pin
	phoneEd                    ed25519.PrivateKey
	phoneX                     []byte
	machine                    Member
	machineEd                  ed25519.PrivateKey
	machineX                   []byte
	client                     Member
	clientEd                   ed25519.PrivateKey
	clientX                    []byte
}

func newCurrentChain(t *testing.T) *currentChain {
	t.Helper()
	now := uint64(time.Now().UnixMilli())
	phone, phoneEd, phoneX := testMember(t, 0x11, Client)
	var rec [32]byte
	rec[0] = 0x22
	secret1 := bytes.Repeat([]byte{0x33}, 32)
	genesis, err := BuildGenesis("0123456789abcdef0123456789abcdef", digest("test/domain", []byte("current")), phone, rec, secret1, phoneEd, now-3000)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := genesis.Pin()
	if err != nil {
		t.Fatal(err)
	}
	machine, machineEd, machineX := testMember(t, 0x44, Machine)
	secret2 := bytes.Repeat([]byte{0x55}, 32)
	added := mustBuildCurrent(t, genesis, []Member{phone, machine}, nil, secret2, secret1, phoneEd, now-2000)
	client, clientEd, clientX := testMember(t, 0x66, Client)
	secret3 := bytes.Repeat([]byte{0x77}, 32)
	withClient := mustBuildCurrent(t, added, []Member{phone, machine, client}, nil, secret3, secret2, phoneEd, now-1000)
	return &currentChain{genesis: genesis, added: added, withClient: withClient, pin: pin, phoneEd: phoneEd, phoneX: phoneX,
		machine: machine, machineEd: machineEd, machineX: machineX,
		client: client, clientEd: clientEd, clientX: clientX}
}

func mustBuildCurrent(t *testing.T, parent State, members []Member, removed []string, newSecret, oldSecret []byte, signer ed25519.PrivateKey, now uint64) State {
	t.Helper()
	s, err := BuildState(parent, members, removed, newSecret, mustPrev(t, newSecret, oldSecret), signer, now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (c *currentChain) states() []State { return []State{c.genesis, c.added, c.withClient} }
func (c *currentChain) proofs() []*RecoveryStatement {
	return []*RecoveryStatement{nil, nil, nil}
}

func (c *currentChain) machineEndpoint(t *testing.T, path string) *Endpoint {
	t.Helper()
	ep, err := initTestEndpoint(path, c.pin, c.states(), c.proofs(), c.machine.ID, c.machineEd.Seed(), c.machineX)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func (c *currentChain) phoneEndpoint(t *testing.T) *Endpoint {
	t.Helper()
	ep, err := initTestEndpoint("", c.pin, []State{c.genesis}, []*RecoveryStatement{nil}, c.genesis.Core.AuthorityID, c.phoneEd.Seed(), c.phoneX)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func (c *currentChain) clientEndpoint(t *testing.T) *Endpoint {
	t.Helper()
	ep, err := initTestEndpoint("", c.pin, c.states(), c.proofs(), c.client.ID, c.clientEd.Seed(), c.clientX)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func commandBody(t *testing.T) []byte {
	t.Helper()
	b, err := proto.Marshal(&pb.Command{Kind: &pb.Command_UpdateSettings{UpdateSettings: &pb.UpdateSettingsRequest{}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCommandAcceptedLogSurvivesRestartAndExpiredRetry(t *testing.T) {
	c := newCurrentChain(t)
	path := filepath.Join(t.TempDir(), "machine.db")
	machine := c.machineEndpoint(t, path)
	client := c.clientEndpoint(t)
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	h := Header{Account: c.genesis.Core.Account, Domain: c.genesis.Core.Domain, Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "0123456789abcdef", Deadline: now + 60_000}
	w, err := client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fn := func(Header, []byte) ([]byte, error) { calls++; return []byte("result"), nil }
	if out, err := machine.Accept(w, nil, now, fn); err != nil || string(out) != "result" {
		t.Fatalf("first: %q %v", out, err)
	}
	if err := machine.Close(); err != nil {
		t.Fatal(err)
	}
	machine, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer machine.Close()
	// The web retries by re-sealing the same request with a fresh random nonce.
	w, err = client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := machine.Accept(w, nil, now+1000, fn); err != nil || string(out) != "result" || calls != 1 {
		t.Fatalf("re-sealed retry: %q %v calls=%d", out, err, calls)
	}

	// An expired retry still gets the recorded result, not ErrExpired.
	short := h
	short.Request = "fedcba9876543210"
	short.Deadline = now + 2000
	w, err = client.Prepare(short, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machine.Accept(w, nil, now, fn); err != nil {
		t.Fatal(err)
	}
	if out, err := machine.Accept(w, nil, now+10_000, fn); err != nil || string(out) != "result" {
		t.Fatalf("expired retry: %q %v", out, err)
	}
}

func TestTwoEpochChangesInOneInstall(t *testing.T) {
	c := newCurrentChain(t)
	ep := c.phoneEndpoint(t)
	defer ep.Close()
	if err := ep.Install([]State{c.added, c.withClient}, []*RecoveryStatement{nil, nil}); err != nil {
		t.Fatalf("two content epochs in one batch: %v", err)
	}
	if _, err := ep.ContentSecret(1); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.ContentSecret(3); err != nil {
		t.Fatal(err)
	}
}

func TestCommandsFromOneDeviceMayArriveOutOfOrder(t *testing.T) {
	c := newCurrentChain(t)
	machine := c.machineEndpoint(t, "")
	client := c.clientEndpoint(t)
	defer machine.Close()
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	h := Header{Account: c.genesis.Core.Account, Domain: c.genesis.Core.Domain, Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "0123456789abcdef", Deadline: now + 60_000}
	first, err := client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	h.Request = "fedcba9876543210"
	second, err := client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fn := func(Header, []byte) ([]byte, error) { calls++; return []byte("ok"), nil }
	// Concurrent senders reach the daemon in either order; a later request
	// arriving first must not make an earlier one look stale.
	if _, err := machine.Accept(second, nil, now, fn); err != nil {
		t.Fatalf("later request first: %v", err)
	}
	if _, err := machine.Accept(first, nil, now, fn); err != nil {
		t.Fatalf("earlier request second: %v", err)
	}
	if calls != 2 {
		t.Fatalf("commands ran %d times", calls)
	}
}

func TestAcceptedRequestSurvivesAContentEpochChange(t *testing.T) {
	c := newCurrentChain(t)
	machine := c.machineEndpoint(t, "")
	client := c.clientEndpoint(t)
	defer machine.Close()
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	h := Header{Account: c.genesis.Core.Account, Domain: c.genesis.Core.Domain, Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "0123456789abcdef", Deadline: now + 600_000}
	w, err := client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fn := func(Header, []byte) ([]byte, error) { calls++; return []byte("ok"), nil }
	if _, err := machine.Accept(w, nil, now, fn); err != nil {
		t.Fatal(err)
	}
	extra, _, _ := testMember(t, 0x88, Machine)
	secret4 := bytes.Repeat([]byte{0x99}, 32)
	phone, _ := c.genesis.Core.Member(c.genesis.Core.AuthorityID)
	next, err := BuildState(c.withClient, []Member{c.machine, c.client, extra, phone}, nil, secret4, mustPrev(t, secret4, bytes.Repeat([]byte{0x77}, 32)), c.phoneEd, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.Install([]State{next}, []*RecoveryStatement{nil}); err != nil {
		t.Fatal(err)
	}
	if err := client.Install([]State{next}, []*RecoveryStatement{nil}); err != nil {
		t.Fatal(err)
	}
	// The web retries with the same request ID after the key change: the
	// command must not run a second time.
	w, err = client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := machine.Accept(w, nil, now, fn); err != nil || string(out) != "ok" || calls != 1 {
		t.Fatalf("re-sealed retry after epoch change: %q %v calls=%d", out, err, calls)
	}
	// A new epoch requires its own independently signed head lease.
	lease, _ := BuildLease(next, c.phoneEd, next.Core.IssuedAt)
	if err := machine.InstallLease(lease, now); err != nil {
		t.Fatal(err)
	}
	// A different request in the new epoch still runs.
	h.Request = "fedcba9876543210"
	w, err = client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machine.Accept(w, nil, now, fn); err != nil || calls != 2 {
		t.Fatalf("first command of the new content epoch: %v calls=%d", err, calls)
	}
}

func TestAcceptedLogFullDoesNotEvictLiveEntries(t *testing.T) {
	c := newCurrentChain(t)
	machine := c.machineEndpoint(t, "")
	client := c.clientEndpoint(t)
	defer machine.Close()
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	filler := bytes.Repeat([]byte{0x5a}, maxAcceptedBytes-300)
	machine.data.Accepted["filler"] = accepted{Deadline: now + 600_000, Done: true, Result: filler, Persisted: true}
	h := Header{Account: c.genesis.Core.Account, Domain: c.genesis.Core.Domain, Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "0123456789abcdef", Deadline: now + 60_000}
	w, err := client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	result := bytes.Repeat([]byte{0x6b}, 400)
	if _, err := machine.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { return result, nil }); err != ErrCapacity {
		t.Fatalf("finish over the byte cap: %v", err)
	}
	if _, ok := machine.data.Accepted["filler"]; !ok {
		t.Fatal("an unexpired accepted entry was evicted")
	}
}

func TestClockBehindTheFloorPausesControl(t *testing.T) {
	c := newCurrentChain(t)
	machine := c.machineEndpoint(t, "")
	client := c.clientEndpoint(t)
	defer machine.Close()
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	if err := machine.Touch(now + 10*60*1000); err != nil {
		t.Fatal(err)
	}
	h := Header{Account: c.genesis.Core.Account, Domain: c.genesis.Core.Domain, Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "0123456789abcdef", Deadline: now + 24*60*60*1000}
	w, err := client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := machine.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { called = true; return nil, nil }); !errors.Is(err, ErrStale) {
		t.Fatalf("control with a clock behind the floor: %v", err)
	}
	if called {
		t.Fatal("a command ran while the clock was behind the floor")
	}
}

func TestAuthenticatedOnlyLookupDoesNotAnswerForgedRequests(t *testing.T) {
	c := newCurrentChain(t)
	machine := c.machineEndpoint(t, "")
	client := c.clientEndpoint(t)
	defer machine.Close()
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	h := Header{Account: c.genesis.Core.Account, Domain: c.genesis.Core.Domain, Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "0123456789abcdef", Deadline: now + 60_000}
	w, err := client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machine.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { return []byte("secret-result"), nil }); err != nil {
		t.Fatal(err)
	}
	// The header still names the accepted request, but the ciphertext is not
	// authentic. It must not learn the recorded result.
	forged := w
	forged.Cipher = bytes.Clone(w.Cipher)
	forged.Cipher[0] ^= 1
	if out, err := machine.Accept(forged, nil, now, func(Header, []byte) ([]byte, error) { return nil, nil }); err == nil || string(out) == "secret-result" {
		t.Fatalf("forged request got a cached answer: %q %v", out, err)
	}
}

func TestAcceptedLogCapsBeforeExecution(t *testing.T) {
	c := newCurrentChain(t)
	machine := c.machineEndpoint(t, "")
	client := c.clientEndpoint(t)
	defer machine.Close()
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	for i := 0; i < maxAccepted; i++ {
		machine.data.Accepted[string(rune(i))] = accepted{Deadline: now + 600_000, Done: true}
	}
	h := Header{Account: c.genesis.Core.Account, Domain: c.genesis.Core.Domain, Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "0123456789abcdef", Deadline: now + 60_000}
	w, err := client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := machine.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { called = true; return nil, nil }); err != ErrCapacity {
		t.Fatalf("cap: %v", err)
	}
	if called {
		t.Fatal("ran before the capacity check")
	}
}

func TestPrepareScopeFailureDoesNotReserve(t *testing.T) {
	c := newCurrentChain(t)
	client := c.clientEndpoint(t)
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	bad := Header{Purpose: Command, Kind: 5, Machine: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Request: "0123456789abcdef", Deadline: now + 60_000}
	if _, err := client.Prepare(bad, nil, []byte("x")); err == nil {
		t.Fatal("bad scope accepted")
	}
	good := Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "0123456789abcdef", Deadline: now + 60_000}
	if _, err := client.Prepare(good, nil, []byte("x")); err != nil {
		t.Fatal(err)
	}
}

func TestNonCanonicalStateRejected(t *testing.T) {
	c := newCurrentChain(t)
	raw, _ := c.genesis.MarshalBinary()
	if _, err := ParseState(append(raw, 0)); err == nil {
		t.Fatal("trailing state bytes accepted")
	}
	if _, err := ParseState(append([]byte{0}, raw...)); err == nil {
		t.Fatal("prefixed state accepted")
	}
	core, _ := c.genesis.Core.MarshalBinary()
	if _, err := ParseCore(append(core, 0)); err == nil {
		t.Fatal("trailing core bytes accepted")
	}
}

// TestARe recovery keeps every derivable content key: before a recovery
// boundary stops the PrevSecret walk, the retained device caches what it can
// still open.
func TestRecoveryKeepsEveryDerivableContentKey(t *testing.T) {
	now := uint64(time.Now().UnixMilli())
	phone, phoneEd, _ := testMember(t, 0x21, Client)
	_, recKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var recPub [32]byte
	copy(recPub[:], recKey.Public().(ed25519.PublicKey))
	secret1 := bytes.Repeat([]byte{0x31}, 32)
	genesis, err := BuildGenesis("0123456789abcdef0123456789abcdef", digest("test/domain", []byte("keys")), phone, recPub, secret1, phoneEd, now-4000)
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := genesis.Pin()
	machine, machineEd, machineX := testMember(t, 0x41, Machine)
	secret2 := bytes.Repeat([]byte{0x32}, 32)
	machineXPub, err := XPublic(machineX)
	if err != nil {
		t.Fatal(err)
	}
	added, err := BuildPairState(genesis, machine, machineXPub, secret1, secret2, phoneEd, now-3000)
	if err != nil {
		t.Fatal(err)
	}
	client, _, _ := testMember(t, 0x51, Client)
	secret3 := bytes.Repeat([]byte{0x33}, 32)
	withClient, err := BuildState(added, []Member{phone, machine, client}, nil, secret3, mustPrev(t, secret3, secret2), phoneEd, now-2000)
	if err != nil {
		t.Fatal(err)
	}
	machineEp, err := initTestEndpoint("", pin, []State{genesis, added, withClient}, []*RecoveryStatement{nil, nil, nil}, machine.ID, machineEd.Seed(), machineX)
	if err != nil {
		t.Fatal(err)
	}
	defer machineEp.Close()
	// Simulate a running device that has not asked for the older keys yet.
	machineEp.data.ContentKeys = map[uint64][]byte{withClient.Core.ContentEpoch: secret3}

	newPhone, newPhoneEd, _ := testMember(t, 0x61, Client)
	secret4 := bytes.Repeat([]byte{0x34}, 32)
	_, newRecKey, _ := testMember(t, 0x71, Client)
	var newRecovery [32]byte
	copy(newRecovery[:], newRecKey.Public().(ed25519.PublicKey))
	rec, stmt, err := BuildRecovery(withClient, newPhone, []Member{machine, client, newPhone}, secret4, newPhoneEd, recKey, newRecovery, now-1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := machineEp.Install([]State{rec}, []*RecoveryStatement{&stmt}); err != nil {
		t.Fatal(err)
	}
	if _, err := machineEp.ContentSecret(1); err != nil {
		t.Fatalf("content key 1 lost at recovery: %v", err)
	}
	if _, err := machineEp.ContentSecret(2); err != nil {
		t.Fatalf("content key 2 lost at the recovery boundary: %v", err)
	}
	if _, err := machineEp.ContentSecret(3); err != nil {
		t.Fatalf("content key 3 lost at the recovery boundary: %v", err)
	}
}
