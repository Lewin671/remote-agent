package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func rotateForTest(parent devicesec.State, secret []byte, signer ed25519.PrivateKey, now uint64) (devicesec.State, error) {
	h, _ := parent.Hash()
	seed := devicesec.Digest("test/recovery-rotation", h[:])
	pub := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	var next [32]byte
	copy(next[:], pub)
	return BuildRecoveryKeyRotation(parent, secret, next, signer, now)
}

func initTestEndpoint(path string, pin devicesec.Pin, states []devicesec.State, proofs []*devicesec.RecoveryStatement, id string, edSeed, x []byte) (*Endpoint, error) {
	head := states[len(states)-1]
	for n := 0; n < 256; n++ {
		signer := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(n)}, 32))
		if devicesec.ID(signer.Public().(ed25519.PublicKey)) == head.Core.AuthorityID {
			l, err := BuildLease(head, signer, head.Core.IssuedAt)
			if err != nil {
				return nil, err
			}
			return InitEndpoint(path, pin, states, proofs, id, edSeed, x, l)
		}
	}
	return nil, devicesec.ErrInvalid
}

func TestLeaseIndependentDurableAndBounded(t *testing.T) {
	g, pin, ed, x, _ := testGenesis(t)
	original, _ := g.MarshalBinary()
	ep, err := InitEndpoint(filepath.Join(t.TempDir(), "security.db"), pin, []devicesec.State{g}, []*devicesec.RecoveryStatement{nil}, g.Core.AuthorityID, ed.Seed(), x)
	if err != nil {
		t.Fatal(err)
	}
	now := g.Core.IssuedAt
	if err := ep.CheckLease(now); err == nil {
		t.Fatal("missing lease accepted")
	}
	for i := uint64(0); i < 300; i++ {
		l, err := BuildLease(g, ed, now+i*devicesec.Lease/2)
		if err != nil {
			t.Fatal(err)
		}
		if err := ep.InstallLease(l, l.IssuedAt); err != nil {
			t.Fatal(err)
		}
	}
	if ep.HeadEpoch() != 1 {
		t.Fatal("renewal grew membership chain")
	}
	raw, _ := g.MarshalBinary()
	if !bytes.Equal(raw, original) {
		t.Fatal("renewal rewrote state/grants")
	}
	last := ep.Lease()
	path := ep.path
	ep.Close()
	ep, err = OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	if !bytes.Equal(ep.Lease().Signature, last.Signature) {
		t.Fatal("lease lost after restart")
	}
	if err := ep.CheckLease(last.NotAfter); !errors.Is(err, devicesec.ErrStale) {
		t.Fatalf("expired = %v", err)
	}
	if err := ep.Touch(last.NotAfter + devicesec.MaxClockSkew + 1); err != nil {
		t.Fatal(err)
	}
	if err := ep.CheckLease(last.NotAfter - 1); err == nil {
		t.Fatal("clock rollback revived lease")
	}
	old, _ := BuildLease(g, ed, last.IssuedAt-1)
	if err := ep.InstallLease(old, last.NotAfter); err == nil {
		t.Fatal("lease rollback accepted")
	}
	bad := *last
	bad.NotAfter = bad.IssuedAt + devicesec.Lease + 1
	b, _ := bad.SigningBytes()
	bad.Signature = ed25519.Sign(ed, b)
	if err := ep.InstallLease(bad, last.NotAfter); err == nil {
		t.Fatal("overlong lease accepted")
	}
	other := g
	other.Core.Domain[0] ^= 1
	if last.Verify(other) == nil {
		t.Fatal("wrong domain lease accepted")
	}
}

func TestHandoffKeepsHistoryAndRemovesRetiringAuthority(t *testing.T) {
	c := newCurrentChain(t)
	newSecret := bytes.Repeat([]byte{0x9a}, 32)
	newRec := [32]byte{0x9b}
	s, err := BuildHandoff(c.withClient, c.client.ID, newRec, bytes.Repeat([]byte{0x77}, 32), newSecret, c.phoneEd, c.withClient.Core.IssuedAt+1)
	if err != nil {
		t.Fatal(err)
	}
	if err := devicesec.VerifyTransition(c.withClient, s, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Core.Member(c.withClient.Core.AuthorityID); ok {
		t.Fatal("retiring phone retained")
	}
	key, err := OpenGrant(c.pin, s, c.client.ID, c.clientX)
	if err != nil {
		t.Fatal(err)
	}
	old, err := devicesec.OpenPrevSecret(key, s.PrevSecret)
	if err != nil || !bytes.Equal(old, bytes.Repeat([]byte{0x77}, 32)) {
		t.Fatal("handoff lost history")
	}
	l, err := BuildLease(s, c.clientEd, s.Core.IssuedAt)
	if err != nil || l.Verify(s) != nil {
		t.Fatal("new authority cannot lease")
	}
	if _, err := BuildLease(s, c.phoneEd, s.Core.IssuedAt); err == nil {
		t.Fatal("old authority can lease")
	}
}

func TestLeaseClockRollbackBindsToStateTime(t *testing.T) {
	g, _, ed, _, _ := testGenesis(t)
	l, err := BuildLease(g, ed, g.Core.IssuedAt-1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(g); err != nil {
		t.Fatal(err)
	}
	if l.IssuedAt != g.Core.IssuedAt {
		t.Fatal("lease predates state")
	}
}

func TestIndependentEndpointHandlesCannotRollbackLeaseOrFloor(t *testing.T) {
	g, pin, ed, x, _ := testGenesis(t)
	path := filepath.Join(t.TempDir(), "security.db")
	a, err := InitEndpoint(path, pin, []devicesec.State{g}, []*devicesec.RecoveryStatement{nil}, g.Core.AuthorityID, ed.Seed(), x)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	newer, _ := BuildLease(g, ed, g.Core.IssuedAt+2000)
	newer.NotAfter = newer.IssuedAt + 2000
	body, _ := newer.SigningBytes()
	newer.Signature = ed25519.Sign(ed, body)
	older, _ := BuildLease(g, ed, g.Core.IssuedAt+1000)
	if err := a.InstallLease(newer, newer.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := b.InstallLease(older, newer.IssuedAt); err == nil {
		t.Fatal("stale handle rolled back committed lease")
	}
	b.data.Lease = &older
	if err := b.persistRecord(false); err != nil {
		t.Fatal(err)
	}
	check, err := OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if check.Lease().IssuedAt != newer.IssuedAt {
		check.Close()
		t.Fatal("full-record CAS rolled back issuance while extending expiration")
	}
	check.Close()
	if err := a.Touch(newer.NotAfter + 1000); err != nil {
		t.Fatal(err)
	}
	if err := b.Touch(newer.NotAfter); err != nil {
		t.Fatal(err)
	}
	c, err := OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Floor() != newer.NotAfter+1000 {
		t.Fatal("stale handle rolled back time floor")
	}
}

func TestInstallLeaseSkipsSQLiteWriteWhenUnchanged(t *testing.T) {
	g, pin, ed, x, _ := testGenesis(t)
	path := filepath.Join(t.TempDir(), "security.db")
	ep, err := InitEndpoint(path, pin, []devicesec.State{g}, []*devicesec.RecoveryStatement{nil}, g.Core.AuthorityID, ed.Seed(), x)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	now := g.Core.IssuedAt
	l, err := BuildLease(g, ed, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.InstallLease(l, now); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.db.Exec(`CREATE TABLE lease_writes (n INTEGER NOT NULL); INSERT INTO lease_writes VALUES (0); CREATE TRIGGER count_lease_writes AFTER INSERT ON lease BEGIN UPDATE lease_writes SET n = n + 1; END;`); err != nil {
		t.Fatal(err)
	}
	if err := ep.InstallLease(l, now+12_000); err != nil {
		t.Fatal(err)
	}
	var writes int
	if err := ep.db.QueryRow(`SELECT n FROM lease_writes`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("unchanged lease triggered %d SQLite writes, want 0", writes)
	}
	if ep.Floor() < now+12_000 {
		t.Fatalf("floor = %d, want >= %d", ep.Floor(), now+12_000)
	}
	newer, err := BuildLease(g, ed, now+24_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.InstallLease(newer, now+24_000); err != nil {
		t.Fatal(err)
	}
	if err := ep.db.QueryRow(`SELECT n FROM lease_writes`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("updated lease triggered %d SQLite writes, want 1", writes)
	}
}

func TestInstallWithLeaseAtomicAndFallback(t *testing.T) {
	g, pin, ed, x, _ := testGenesis(t)
	secret := bytes.Repeat([]byte{0x33}, 32)
	now := g.Core.IssuedAt
	l1, err := BuildLease(g, ed, now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "security.db")
	ep, err := InitEndpoint(path, pin, []devicesec.State{g}, []*devicesec.RecoveryStatement{nil}, g.Core.AuthorityID, ed.Seed(), x, l1)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()

	// Advance state + lease atomically and ensure CheckLease succeeds immediately
	// and persists across reopen.
	s2, err := rotateForTest(g, secret, ed, now+1000)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := BuildLease(s2, ed, now+1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.InstallWithLease([]devicesec.State{s2}, []*devicesec.RecoveryStatement{nil}, &l2, now+1000); err != nil {
		t.Fatalf("InstallWithLease: %v", err)
	}
	if err := ep.CheckLease(now + 1000); err != nil {
		t.Fatalf("CheckLease after atomic install: %v", err)
	}
	if ep.HeadEpoch() != s2.Core.Epoch {
		t.Fatalf("head epoch = %d, want %d", ep.HeadEpoch(), s2.Core.Epoch)
	}

	reopened, err := OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.CheckLease(now + 1000); err != nil {
		reopened.Close()
		t.Fatalf("reopened CheckLease: %v", err)
	}
	reopened.Close()

	// If a verified state transition is paired with a mismatched lease, the
	// verified state must still be persisted while returning the lease error.
	s3, err := rotateForTest(s2, secret, ed, now+2000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.InstallWithLease([]devicesec.State{s3}, []*devicesec.RecoveryStatement{nil}, &l2, now+2000); err == nil {
		t.Fatalf("expected lease verification error for mismatched lease, got nil")
	}
	if ep.HeadEpoch() != s3.Core.Epoch {
		t.Fatalf("verified state was not persisted on lease rejection: head=%d, want=%d", ep.HeadEpoch(), s3.Core.Epoch)
	}

	// If the lease write itself hits ErrStorage inside the combined transaction,
	// InstallWithLease must still fall back to persisting the verified state so
	// state advances/removals are never dropped, while returning ErrStorage.
	if _, err := ep.db.Exec(`CREATE TRIGGER block_lease_write BEFORE INSERT ON lease BEGIN SELECT RAISE(ABORT, 'lease blocked'); END;`); err != nil {
		t.Fatal(err)
	}
	s4, err := rotateForTest(s3, secret, ed, now+3000)
	if err != nil {
		t.Fatal(err)
	}
	l4, err := BuildLease(s4, ed, now+3000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.InstallWithLease([]devicesec.State{s4}, []*devicesec.RecoveryStatement{nil}, &l4, now+3000); !errors.Is(err, ErrStorage) {
		t.Fatalf("expected ErrStorage when lease table write fails, got %v", err)
	}
	if ep.HeadEpoch() != s4.Core.Epoch {
		t.Fatalf("verified state was not persisted after lease ErrStorage fallback: head=%d, want=%d", ep.HeadEpoch(), s4.Core.Epoch)
	}
	reopened2, err := OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened2.Close()
	if reopened2.HeadEpoch() != s4.Core.Epoch {
		t.Fatalf("reopened head epoch = %d after lease ErrStorage fallback, want %d", reopened2.HeadEpoch(), s4.Core.Epoch)
	}
}
