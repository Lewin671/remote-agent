package relay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

// dispatcher owns one endpoint for the entire daemon/account lifetime. Socket
// readers may add work; only process shutdown cancels and drains admitted work.
// Same-ID running coordination and persistent retention are separate later steps.
type dispatcher struct {
	ctx       context.Context
	cancel    context.CancelFunc
	workers   sync.WaitGroup
	terminals sync.WaitGroup
	security  *securitySession
	publisher *conn
	acct      *Account
	wasReady  atomic.Bool
}

func (r *Relay) newDispatcher(ctx context.Context, acct *Account) (*dispatcher, error) {
	ctx, cancel := context.WithCancel(ctx)
	securityCredentials := accountCredentials(acct)
	client := remoteagentv1connect.NewClientServiceClient(httpClient(), acct.Server, connect.WithInterceptors(securityCredentials.bearer()))
	security, err := openSecuritySession(acct, client)
	if err != nil {
		cancel()
		return nil, err
	}
	d := &dispatcher{ctx: ctx, cancel: cancel, security: security, acct: acct}
	publishCredentials := accountCredentials(acct)
	d.publisher = &conn{r: r, acct: acct, security: security, lifetime: ctx, publishNow: make(chan struct{}, 1),
		machine: remoteagentv1connect.NewMachineServiceClient(httpClient(), acct.Server, connect.WithInterceptors(publishCredentials.bearer(), r.limitMachineRPCs()))}
	// Each loop has its own budget and credentials; no network I/O lock or RPC
	// slot used by publication is shared with membership synchronization.
	initialSync := make(chan struct{})
	d.workers.Go(func() { _ = security.syncLoop(ctx, r.securityInterval, initialSync) })
	d.workers.Go(func() { d.publisher.checkpointLoop(ctx) })
	d.workers.Go(func() {
		select {
		case <-ctx.Done():
			return
		case <-initialSync:
		}
		for ctx.Err() == nil {
			attempt, cancel := context.WithTimeout(ctx, 60*time.Second)
			err := d.publisher.ensurePublished(attempt)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Warn("relay: current checkpoint pending; retrying during synchronization", "code", connect.CodeOf(err).String())
			}
			meta := r.backend.MachineMeta()
			err = d.publisher.putMachine(ctx, meta)
			if err == nil {
				err = d.publisher.syncLoop(ctx, meta)
			}
			if ctx.Err() != nil {
				return
			}
			if !errors.Is(err, errPublicationRefresh) {
				slog.Warn("relay: publication paused; retrying", "code", connect.CodeOf(err).String())
				sleep(ctx, r.syncRetryBase)
			}
		}
	})
	return d, nil
}
func (d *dispatcher) close() {
	d.cancel()
	d.workers.Wait() // all command handlers stop before joining terminal additions
	d.terminals.Wait()
	d.publisher.writes.close()
	d.security.Close()
}
func sameAccount(a, b *Account) bool {
	return a.Server == b.Server && a.DeviceID == b.DeviceID && bytes.Equal(a.DeviceSeed, b.DeviceSeed) && a.Security != nil && b.Security != nil && a.Security.Pin == b.Security.Pin && bytes.Equal(a.Security.XPrivate, b.Security.XPrivate)
}
func (c *conn) checkpointLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		attempt, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := c.ensurePublished(attempt)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("relay: republishing current state failed; retrying next sync", "code", connect.CodeOf(err).String())
		}
	}
}

// A reconnect wakes pending publication without owning or canceling it.
func (d *dispatcher) ready() {
	d.security.requestSync()
	if !d.wasReady.Swap(true) {
		return
	}
	select {
	case d.publisher.publishNow <- struct{}{}:
	default:
	}
}

var errPublicationRefresh = errors.New("publication refresh")
