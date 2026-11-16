package relay

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"testing"
	"time"
)

func TestRetiredDirectChooserCannotReplaceCurrentPeer(t *testing.T) {
	makeIdentity := func(role uint64) peerwire.Identity {
		pub, key, _ := ed25519.GenerateKey(rand.Reader)
		box, _ := ecdh.X25519().GenerateKey(rand.Reader)
		return peerwire.Identity{ID: []byte(hex.EncodeToString(peerwire.Hash(pub)[:16])), Role: role, Sign: key, Box: box, Instance: make([]byte, 16), Boot: make([]byte, 32)}
	}
	self, peer := makeIdentity(2), makeIdentity(1)
	codec, _ := peerwire.NewCodec("00000000000000000000000000000001", [32]byte{1}, [32]byte{2}, 1)
	makePeer := func(value byte) *directPeer {
		connection := make([]byte, 16)
		connection[0] = value
		s, err := direct.NewSession(context.Background(), direct.SessionConfig{Self: self, Peer: peer.Public(), Codec: codec, Connection: connection, Deadline: uint64(time.Now().Add(10 * time.Second).UnixMilli()), Allow: func() bool { return true }, Send: func(direct.Outbound) bool { return true }}, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(); s.Wait() })
		p := &directPeer{session: s, connection: connection}
		p.binding.Peer.ID = string(peer.ID)
		p.processValid.Store(true)
		return p
	}
	old, current := makePeer(1), makePeer(2)
	id := string(peer.ID)
	m := &directManager{active: map[string]*directPeer{id: current}, sessions: map[string]*directPeer{string(old.connection): old, string(current.connection): current}}
	old.session.Retire()
	m.choose(old)
	if m.active[id] != current || !current.session.Active() {
		t.Fatal("retired chooser replaced or retired the current peer")
	}
}
