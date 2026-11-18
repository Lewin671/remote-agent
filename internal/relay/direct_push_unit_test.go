package relay

import (
	"context"
	"crypto/ecdh"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
)

func TestDirectPeerQueueLimitsAndFreshnessRevocationGate(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	var nowMs atomic.Uint64
	nowMs.Store(uint64(time.Now().UnixMilli()))
	c.security.clock = nowMs.Load

	pin, head, selfID := c.security.endpoint.Snapshot()
	hash, err := head.Hash()
	if err != nil {
		t.Fatal(err)
	}
	c.security.directFresh.Store(&directFreshness{
		head:       hash,
		until:      nowMs.Load() + 20_000,
		leaseUntil: c.security.endpoint.LeaseExpiration(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	link := transport.NewLink(ctx)
	defer link.Close()
	c.acct.Security = &SecurityConfig{XPrivate: keys.x}
	c.r = &Relay{directLoopback: true}
	ep := &endpoint{r: c.r, acct: c.acct, security: c.security, lifetime: link.Context(), link: link}
	ep.admissionOwner()
	defer ep.admission.close()

	d := &dispatcher{ctx: ctx, cancel: cancel, link: link, endpoint: ep, security: c.security, acct: c.acct}
	m, err := newDirectManager(d)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()

	self, _ := head.Core.Member(selfID)
	peerMem, _ := head.Core.Member(head.Core.AuthorityID)
	boxPub, _ := ecdh.X25519().NewPublicKey(peerMem.XPublic[:])
	codec, _ := peerwire.NewCodec(pin.Account, pin.Domain, hash, head.Core.Epoch)
	sess, err := direct.NewSession(ctx, direct.SessionConfig{
		Self:       m.self,
		Peer:       peerwire.Public{ID: []byte(peerMem.ID), Role: 1, Sign: peerMem.EdPublic[:], Box: boxPub},
		Codec:      codec,
		Connection: make([]byte, 16),
		Deadline:   nowMs.Load() + 10_000,
		Pool:       direct.NewPool(4),
		Allow:      func() bool { return true },
		Send:       func(direct.Outbound) bool { return true },
		Now:        func() time.Time { return time.UnixMilli(int64(nowMs.Load())) },
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	dp := &directPeer{
		policyVersion: m.policy.version(peerMem.ID),
		binding:       deviceauth.PeerBinding{Self: self, Peer: peerMem, Head: hash},
		codec:         codec,
		session:       sess,
		writeWake:     make(chan struct{}, 1),
	}
	dp.processValid.Store(true)

	// 1. Low queue drops oversize (> MaxSingleFragmentBytes) and > 32 items without blocking high queue.
	if dp.enqueueLow(make([]byte, direct.MaxSingleFragmentBytes+1)) {
		t.Fatal("expected enqueueLow to reject oversize payload")
	}
	small := make([]byte, 128)
	for i := range directEventQueueMaxItems {
		if !dp.enqueueLow(small) {
			t.Fatalf("expected enqueueLow #%d to succeed", i)
		}
	}
	if dp.enqueueLow(small) {
		t.Fatal("expected enqueueLow #33 to drop when queue full")
	}
	if !dp.enqueueHigh([]byte("result"), time.Now().Add(time.Second)) {
		t.Fatal("high-priority CommandResult must succeed even when lowQueue is full")
	}

	// 2. Freshness expiry immediately disables peerAuthorized / peerAllowed.
	if !m.peerAuthorized(dp) {
		t.Fatal("expected peerAuthorized true before freshness expiry")
	}
	nowMs.Add(25_000) // past 20s directFresh window
	if m.peerAuthorized(dp) {
		t.Fatal("expected peerAuthorized false after freshness expiry")
	}

	// 3. Renew freshness, then verify member revocation (Install) stops peerAuthorized.
	c.security.directFresh.Store(&directFreshness{
		head:       hash,
		until:      nowMs.Load() + 20_000,
		leaseUntil: c.security.endpoint.LeaseExpiration(),
	})
	if !m.peerAuthorized(dp) {
		t.Fatal("expected peerAuthorized true after freshness renewal")
	}
	c.security.blocked.Store(true)
	if m.peerAuthorized(dp) {
		t.Fatal("expected peerAuthorized false when security blocked")
	}

	// 4. Low queue also enforces the 256 KiB total byte cap even with < 32 items.
	dp.writeMu.Lock()
	dp.lowQueue = nil
	dp.lowBytes = 0
	dp.writeMu.Unlock()
	chunk := make([]byte, direct.MaxSingleFragmentBytes) // ~16 KiB; 17 items would exceed 256 KiB
	accepted := 0
	for range 20 {
		if dp.enqueueLow(chunk) {
			accepted++
		}
	}
	if accepted == 0 || accepted >= 20 {
		t.Fatalf("expected byte cap to bound queued items, got accepted=%d", accepted)
	}
}
