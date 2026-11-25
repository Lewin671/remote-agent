package relay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/transport"
)

// dispatcher owns one endpoint for the entire daemon/account lifetime. Socket
// readers may add work; only process shutdown cancels and drains admitted work.
// Its endpoint also owns the process running and bounded waiter tables.
type dispatcher struct {
	direct *directManager

	ctx             context.Context
	cancel          context.CancelFunc
	workers         sync.WaitGroup
	link            *transport.Link
	endpoint        *endpoint
	maintenance     context.Context
	stopMaintenance context.CancelFunc
	reloadRequested atomic.Bool
	security        *securitySession
	publisher       *conn
	acct            *Account
	wasReady        atomic.Bool
	credentials     []*credentials
}

func (r *Relay) newDispatcher(ctx context.Context, acct *Account, recovering bool) (*dispatcher, error) {
	ctx, cancel := context.WithCancel(ctx)
	maintenance, stopMaintenance := context.WithCancel(ctx)
	securityCredentials := accountCredentials(maintenance, acct)
	securityCredentials.timeout = 3 * time.Second
	securityHTTP := httpClient()
	var closeIdle func()
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr := dt.Clone()
		securityHTTP.Transport = tr
		closeIdle = tr.CloseIdleConnections
	}
	client := remoteagentv1connect.NewClientServiceClient(securityHTTP, acct.Server, connect.WithInterceptors(securityCredentials.bearer()))
	security, err := openSecuritySession(acct, client)
	if err != nil {
		stopMaintenance()
		cancel()
		return nil, err
	}
	security.lifetime = maintenance
	security.credentials = securityCredentials
	security.closeIdleConnections = closeIdle
	r.security.Store(security)
	d := &dispatcher{ctx: ctx, cancel: cancel, security: security, acct: acct, maintenance: maintenance, stopMaintenance: stopMaintenance}
	d.link = transport.NewLink(ctx)
	if recovering {
		security.setAuthorized(false)
	}
	security.beforeSync = func() error {
		latest, err := LoadAccount()
		if err != nil || !sameAccount(acct, latest) {
			d.requestReload()
			if err != nil {
				return err
			}
			return errAccountReload
		}
		return nil
	}
	security.onAuthLost = d.requestReload
	securityCredentials.now = r.retryTime().now
	publishCredentials := accountCredentials(maintenance, acct)
	d.credentials = []*credentials{securityCredentials, publishCredentials}
	publishCredentials.now = r.retryTime().now
	ctx = maintenance
	d.publisher = &conn{r: r, acct: acct, security: security, lifetime: ctx, diskAlertNow: r.retryTime().now, publishNow: make(chan struct{}, 1),
		machine: remoteagentv1connect.NewMachineServiceClient(httpClient(), acct.Server, connect.WithInterceptors(publishCredentials.bearer(), r.limitMachineRPCs()))}
	d.endpoint = &endpoint{r: r, acct: acct, security: security, lifetime: d.link.Context(), link: d.link, sessionKey: d.publisher.sessionKey}
	d.endpoint.admissionOwner()
	d.direct, _ = newDirectManager(d)
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
		d.publisher.publishLoop(ctx)
	})
	return d, nil
}
func (d *dispatcher) close() {
	d.cancel()
	if d.direct != nil {
		d.direct.close()
	}
	if d.endpoint.admission != nil {
		d.endpoint.admission.close()
	}
	for _, c := range d.credentials {
		c.close()
	}
	d.workers.Wait()
	d.link.Close()
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
			var batchErr *republishBatchError
			if !errors.As(err, &batchErr) {
				slog.Warn("relay: republishing current state failed; retrying next sync", "code", connect.CodeOf(err).String())
			}
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

func (c *conn) publishLoop(ctx context.Context) {
	r := c.r
	timing := r.retryTime()
	backoff := retryBackoff{base: r.syncRetryBase, jitter: timing.jitter}
	var checkpointWarn, publishWarn warningGate
	c.onSyncReady = backoff.reset
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := c.ensurePublished(attempt)
		cancel()
		if err != nil && ctx.Err() == nil && checkpointWarn.allow(timing.now()) {
			slog.Warn("relay: current checkpoint pending; retrying during synchronization", "code", connect.CodeOf(err).String())
		}
		meta := c.r.backend.MachineMeta()
		err = c.putMachine(ctx, meta)
		if err == nil {
			err = c.syncLoop(ctx, meta)
		}
		if ctx.Err() != nil {
			return
		}
		if !errors.Is(err, errPublicationRefresh) {
			if publishWarn.allow(timing.now()) {
				slog.Warn("relay: publication paused; retrying", "code", connect.CodeOf(err).String())
			}
			timing.wait(ctx, backoff.failure())
		}
	}
}

// Retire a failed authentication generation without canceling accepted native
// work. No second endpoint opens until the old generation has drained.
func (d *dispatcher) retire() {
	d.stopMaintenance()
	if d.direct != nil {
		d.direct.close()
	}
	for _, c := range d.credentials {
		c.close()
	}
	d.security.setAuthorized(false)
	d.workers.Wait()
	if d.endpoint.admission != nil {
		d.endpoint.admission.close()
	}
	d.link.DrainCalls()
	d.cancel()
	d.link.Close()
	d.publisher.writes.close()
	d.security.Close()
}
func (d *dispatcher) requestReload() {
	d.reloadRequested.Store(true)
	d.security.setAuthorized(false)
	d.stopMaintenance()
}

var errAccountReload = errors.New("relay_account_reload")
