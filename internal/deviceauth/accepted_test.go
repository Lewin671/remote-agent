package deviceauth

import (
	"bytes"
	"crypto/ed25519"
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
	ep, err := InitEndpoint(path, c.pin, c.states(), c.proofs(), c.machine.ID, c.machineEd.Seed(), c.machineX)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func (c *currentChain) phoneEndpoint(t *testing.T) *Endpoint {
	t.Helper()
	ep, err := InitEndpoint("", c.pin, []State{c.genesis}, []*RecoveryStatement{nil}, c.genesis.Core.AuthorityID, c.phoneEd.Seed(), c.phoneX)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func (c *currentChain) clientEndpoint(t *testing.T) *Endpoint {
	t.Helper()
	ep, err := InitEndpoint("", c.pin, c.states(), c.proofs(), c.client.ID, c.clientEd.Seed(), c.clientX)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func commandBody(t *testing.T) []byte {
	t.Helper()
	b, err := proto.Marshal(&pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
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
	// The web retries by re-sealing the same request with a new counter.
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

func TestReplayHighWaterResetsWithContentEpoch(t *testing.T) {
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
	fn := func(Header, []byte) ([]byte, error) { return []byte("ok"), nil }
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
	if err := VerifyTransition(c.withClient, next, nil); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := machine.Install([]State{next}, []*RecoveryStatement{nil}); err != nil {
		t.Fatal(err)
	}
	if err := client.Install([]State{next}, []*RecoveryStatement{nil}); err != nil {
		t.Fatal(err)
	}
	h.Request = "fedcba9876543210"
	w, err = client.Prepare(h, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machine.Accept(w, nil, now, fn); err != nil {
		t.Fatalf("first command of the new content epoch: %v", err)
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
	before := client.data.BlockEnd
	bad := Header{Purpose: Command, Kind: 5, Machine: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Request: "0123456789abcdef", Deadline: now + 60_000}
	if _, err := client.Prepare(bad, nil, []byte("x")); err == nil {
		t.Fatal("bad scope accepted")
	}
	if client.data.BlockEnd != before {
		t.Fatalf("block advanced on failed scope: %d -> %d", before, client.data.BlockEnd)
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
