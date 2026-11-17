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
)

func directBinding(t *testing.T, c *conn) (deviceauth.Member, deviceauth.Member, deviceauth.Hash) {
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
				var rotation deviceauth.State
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
	if err = s.endpoint.Install([]deviceauth.State{next}, []*deviceauth.RecoveryStatement{nil}); err != nil {
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
