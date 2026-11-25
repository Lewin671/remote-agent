package relay

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func directBinding(t *testing.T, c *conn) (devicesec.Member, devicesec.Member, devicesec.Hash) {
	t.Helper()
	_, head, id := c.security.endpoint.Snapshot()
	self, _ := head.Core.Member(id)
	peer, _ := head.Core.Member(head.Core.AuthorityID)
	hash, err := head.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return self, peer, hash
}
func TestDirectSyncWholeRoundDeadlineAndUnavailable(t *testing.T) {
	for _, mode := range []string{"timeout", "unavailable", "partial-pages"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := &conn{}
				keys := initializeTestSecurityKeys(t, c)
				s := c.security
				self, peer, hash := directBinding(t, c)
				s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
					return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
				}}
				if err := s.syncOnce(context.Background()); err != nil || !s.directCurrent(self, peer, hash) {
					t.Fatal("fresh binding unavailable", err)
				}
				calls := 0
				var rotation devicesec.State
				if mode == "partial-pages" {
					_, recovery, _ := ed25519.GenerateKey(nil)
					var public [32]byte
					copy(public[:], recovery.Public().(ed25519.PublicKey))
					var err error
					rotation, err = deviceauth.BuildRecoveryKeyRotation(keys.states[len(keys.states)-1], keys.secret, public, keys.phone, s.now())
					if err != nil {
						t.Fatal(err)
					}
				}
				start := time.Now()
				s.client = ownershipSecurityClient{read: func(ctx context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
					calls++
					if mode == "unavailable" {
						return nil, connect.NewError(connect.CodeUnavailable, errors.New("test outage"))
					}
					if mode == "partial-pages" && calls == 1 {
						time.Sleep(4 * time.Second)
						raw, _ := rotation.MarshalBinary()
						return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: rotation.Core.Epoch + 1, States: []*pb.SecurityState{{State: raw}}}), nil
					}
					if mode == "partial-pages" {
						_, head, _ := s.endpoint.Snapshot()
						partialHash, _ := head.Hash()
						if s.directCurrent(self, peer, partialHash) {
							t.Error("partial installed head inherited old freshness")
						}
					}
					<-ctx.Done()
					return nil, ctx.Err()
				}}
				err := s.refreshWithin(context.Background(), 5*time.Second)
				if err == nil || s.directCurrent(self, peer, hash) || s.directFresh.Load() != nil {
					t.Fatal("failed round reported fresh success", err)
				}
				if mode != "unavailable" && time.Since(start) != 5*time.Second {
					t.Fatal("pagination extended whole round", time.Since(start))
				}
				if mode == "partial-pages" && (calls != 2 || s.endpoint.HeadEpoch() != rotation.Core.Epoch) {
					t.Fatal("verified partial page lost")
				}
			})
		})
	}
}

type stalledSTUNPublisher struct {
	remoteagentv1connect.MachineServiceClient
	started chan struct{}
	calls   atomic.Int32
}

func (p *stalledSTUNPublisher) PutMachine(ctx context.Context, _ *connect.Request[pb.PutMachineRequest]) (*connect.Response[pb.PutMachineResponse], error) {
	if p.calls.Add(1) == 1 {
		close(p.started)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestDirectPeriodicSyncDoesNotWaitForSixtySecondPublication(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	synctest.Test(t, func(t *testing.T) {
		c := &conn{r: New(publishBackend{}), lifetime: context.Background()}
		initializeTestSecurityKeys(t, c)
		s := c.security
		publisher := &stalledSTUNPublisher{started: make(chan struct{})}
		c.machine = publisher
		publication := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			publication <- c.ensurePublished(ctx)
		}()
		<-publisher.started
		start := time.Now()
		var starts []time.Duration
		s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			starts = append(starts, time.Since(start))
			return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
		}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.syncLoop(ctx, 15*time.Second, nil) }()
		time.Sleep(61 * time.Second)
		cancel()
		<-done
		if err := <-publication; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("publication did not use its own sixty-second budget", err)
		}
		if len(starts) != 5 {
			t.Fatal("publication blocked security", starts)
		}
		for i, d := range starts {
			if d != time.Duration(i)*15*time.Second {
				t.Fatal("sync was completion-relative", starts)
			}
		}
		self, peer, head := directBinding(t, c)
		if !s.directCurrent(self, peer, head) {
			t.Fatal("slow publication invalidated healthy security")
		}
	})
}
func TestDirectInstallAndLeaseGates(t *testing.T) {
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	s := c.security
	self, peer, hash := directBinding(t, c)
	now := s.now()
	s.directFresh.Store(&directFreshness{head: hash, until: now + 20000, leaseUntil: s.endpoint.LeaseExpiration()})
	if !s.directCurrent(self, peer, hash) {
		t.Fatal("valid binding")
	}
	changed := peer
	changed.XPublic[0] ^= 1
	if s.directCurrent(self, changed, hash) {
		t.Fatal("unverified replacement key accepted")
	}
	_, recovery, _ := ed25519.GenerateKey(nil)
	var public [32]byte
	copy(public[:], recovery.Public().(ed25519.PublicKey))
	next, err := deviceauth.BuildRecoveryKeyRotation(keys.states[len(keys.states)-1], keys.secret, public, keys.phone, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.endpoint.Install([]devicesec.State{next}, []*devicesec.RecoveryStatement{nil}); err != nil {
		t.Fatal(err)
	}
	if s.directCurrent(self, peer, hash) {
		t.Fatal("installed head did not invalidate old direct binding")
	}
	lease, err := deviceauth.BuildLease(next, keys.phone, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.endpoint.InstallLease(lease, now); err != nil {
		t.Fatal(err)
	}
	newHash, _ := next.Hash()
	s.clock = func() uint64 { return lease.NotAfter }
	s.directFresh.Store(&directFreshness{head: newHash, until: lease.NotAfter + 20000, leaseUntil: lease.NotAfter})
	if s.directCurrent(self, peer, newHash) {
		t.Fatal("lease expiration extended by clock skew")
	}
}

func TestDirectCurrentSurvivesConcurrentRenewalAndRejectsInvalidation(t *testing.T) {
	c := &conn{}
	initializeTestSecurityKeys(t, c)
	s := c.security
	self, peer, hash := directBinding(t, c)
	s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
		return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
	}}
	baseNow := uint64(time.Now().UnixMilli())
	var hook atomic.Value
	s.clock = func() uint64 {
		if fn, ok := hook.Load().(func()); ok && fn != nil {
			fn()
		}
		return baseNow
	}
	if err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Simulate syncOnce renewing directFresh with a new pointer while directCurrent is mid-check.
	calls := 0
	hook.Store(func() {
		calls++
		if calls == 2 {
			hook.Store((func())(nil))
			if err := s.syncOnce(context.Background()); err != nil {
				t.Errorf("concurrent renewal failed: %v", err)
			}
		}
	})
	if !s.directCurrent(self, peer, hash) {
		t.Fatal("routine freshness renewal caused directCurrent to return false")
	}

	// Simulate an invalidation followed by a re-sync while directCurrent is mid-check.
	calls = 0
	hook.Store(func() {
		calls++
		if calls == 2 {
			hook.Store((func())(nil))
			s.invalidateDirect()
			if err := s.syncOnce(context.Background()); err != nil {
				t.Errorf("re-sync after invalidation failed: %v", err)
			}
		}
	})
	if s.directCurrent(self, peer, hash) {
		t.Fatal("concurrent invalidation was missed after re-sync")
	}
}

func TestDirectNetworkChangeKeepsFreshnessAndRetriesTransientSyncError(t *testing.T) {
	t.Run("networkChanged preserves directFresh within until window", func(t *testing.T) {
		c := &conn{}
		initializeTestSecurityKeys(t, c)
		s := c.security
		self, peer, hash := directBinding(t, c)
		s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
		}}
		if err := s.syncOnce(context.Background()); err != nil || !s.directCurrent(self, peer, hash) {
			t.Fatalf("initial sync failed: %v", err)
		}
		// Drain initial syncNow if any.
		select {
		case <-s.syncNow:
		default:
		}
		s.networkChanged()
		if !s.directCurrent(self, peer, hash) {
			t.Fatal("normal networkChanged wiped directFresh immediately")
		}
		select {
		case <-s.syncNow:
		default:
			t.Fatal("networkChanged did not request immediate sync")
		}
	})

	t.Run("networkChanged retiring in-flight syncOnce does not wipe directFresh", func(t *testing.T) {
		c := &conn{}
		initializeTestSecurityKeys(t, c)
		s := c.security
		self, peer, hash := directBinding(t, c)
		s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
		}}
		if err := s.syncOnce(context.Background()); err != nil || !s.directCurrent(self, peer, hash) {
			t.Fatalf("initial sync failed: %v", err)
		}
		started := make(chan struct{})
		s.client = ownershipSecurityClient{read: func(ctx context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		flightDone := make(chan struct{})
		go func() {
			defer close(flightDone)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				<-started
				s.networkChanged()
				cancel()
			}()
			_ = s.refreshWithin(ctx, 5*time.Second)
		}()
		<-flightDone
		s.workers.Wait()
		if !s.directCurrent(self, peer, hash) {
			t.Fatal("retired syncOnce wiped directFresh")
		}
	})

	t.Run("wake and setAuthorized(false) clear directFresh immediately with distinct reasons", func(t *testing.T) {
		c := &conn{}
		initializeTestSecurityKeys(t, c)
		s := c.security
		self, peer, hash := directBinding(t, c)
		s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
		}}
		if err := s.syncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		s.wake()
		if s.directCurrent(self, peer, hash) {
			t.Fatal("wake did not clear directFresh")
		}
		if r := s.directCurrentReason(self, peer, hash); r != "wake" {
			t.Fatalf("wake reason = %q, want wake", r)
		}
		if err := s.syncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		s.setAuthorized(false)
		if s.directCurrent(self, peer, hash) {
			t.Fatal("setAuthorized(false) did not clear directFresh")
		}
		if r := s.directCurrentReason(self, peer, hash); r != "auth_blocked" {
			t.Fatalf("setAuthorized(false) reason = %q, want auth_blocked", r)
		}
		// Re-authorize and test freshness_expired / lease_expired / head_changed / sync_failed.
		s.setAuthorized(true)
		baseNow := s.now()
		s.clock = func() uint64 { return baseNow }
		if err := s.syncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		s.clock = func() uint64 { return baseNow + 20_000 }
		if r := s.directCurrentReason(self, peer, hash); r != "freshness_expired" {
			t.Fatalf("expired freshness reason = %q, want freshness_expired", r)
		}
		s.clock = func() uint64 { return s.endpoint.LeaseExpiration() }
		if r := s.directCurrentReason(self, peer, hash); r != "lease_expired" {
			t.Fatalf("expired lease reason = %q, want lease_expired", r)
		}
		s.clock = func() uint64 { return baseNow }
		otherHash := hash
		otherHash[0] ^= 1
		if r := s.directCurrentReason(self, peer, otherHash); r != "head_changed" {
			t.Fatalf("head changed reason = %q, want head_changed", r)
		}
		s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
		}}
		if err := s.syncOnce(context.Background()); err == nil {
			t.Fatal("expected permission denied error")
		}
		s.setAuthorized(true)
		s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
		}}
		if err := s.syncOnce(context.Background()); err == nil {
			t.Fatal("expected unavailable error")
		}
		if r := s.directCurrentReason(self, peer, hash); r != "sync_failed" {
			t.Fatalf("sync failed reason = %q, want sync_failed", r)
		}
	})

	t.Run("syncOnce retries transient transport error once within 5s budget and resets idle conns", func(t *testing.T) {
		c := &conn{}
		initializeTestSecurityKeys(t, c)
		s := c.security
		self, peer, hash := directBinding(t, c)
		var calls, resets atomic.Int32
		s.closeIdleConnections = func() { resets.Add(1) }
		s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			if calls.Add(1) == 1 {
				return nil, connect.NewError(connect.CodeUnavailable, errors.New("unexpected EOF"))
			}
			return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
		}}
		if err := s.syncOnce(context.Background()); err != nil {
			t.Fatalf("syncOnce failed on single transient error: %v", err)
		}
		if calls.Load() != 2 || resets.Load() != 1 {
			t.Fatalf("calls=%d resets=%d, want 2 and 1", calls.Load(), resets.Load())
		}
		if !s.directCurrent(self, peer, hash) {
			t.Fatal("directCurrent false after successful retry")
		}
		// Non-retryable error must not retry.
		calls.Store(0)
		resets.Store(0)
		s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
			calls.Add(1)
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("revoked"))
		}}
		if err := s.syncOnce(context.Background()); err == nil {
			t.Fatal("expected error on permission denied")
		}
		if calls.Load() != 1 || resets.Load() != 0 {
			t.Fatalf("non-retryable error made calls=%d resets=%d, want 1 and 0", calls.Load(), resets.Load())
		}
	})

	t.Run("syncLoop retries in 1-2s after persistent failure instead of waiting 12s", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := &conn{}
			initializeTestSecurityKeys(t, c)
			s := c.security
			self, peer, hash := directBinding(t, c)
			start := time.Now()
			var calls atomic.Int32
			var recoveredAt time.Duration
			s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
				n := calls.Add(1)
				if n == 1 {
					return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
				}
				if n <= 3 {
					// Fail both initial attempt and in-round retry on the second sync.
					return nil, connect.NewError(connect.CodeUnavailable, errors.New("outage"))
				}
				if recoveredAt == 0 {
					recoveredAt = time.Since(start)
				}
				return connect.NewResponse(&pb.GetSecurityStatesResponse{HeadEpoch: s.endpoint.HeadEpoch()}), nil
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.syncLoop(ctx, 12*time.Second, nil) }()
			time.Sleep(16 * time.Second)
			cancel()
			<-done
			if recoveredAt == 0 || recoveredAt < 13*time.Second || recoveredAt > 15*time.Second {
				t.Fatalf("recoveredAt = %v, want within 1-2s after 12s failure", recoveredAt)
			}
			if !s.directCurrent(self, peer, hash) {
				t.Fatal("directCurrent false after fast retry recovery")
			}
		})
	})
}
