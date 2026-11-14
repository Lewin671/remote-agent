package relay_test

import (
	"bufio"
	"context"
	"encoding/json"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/coder/websocket"
)

// This opt-in measurement uses real relay authentication, HTTP Forward,
// signatures, the process Link/endpoint, and a counting backend. Native CLI
// acceptance is separate; its model response time is not a recovery timer.
type recoveryLeg struct {
	down, up  *websocket.Conn
	raw       net.Conn
	blackhole atomic.Bool
}
type hijackedRecovery struct {
	http.ResponseWriter
	raw net.Conn
}

func (w *hijackedRecovery) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, r, e := w.ResponseWriter.(http.Hijacker).Hijack()
	w.raw = c
	return c, r, e
}

type recoveryProxy struct {
	target         string
	http           *httputil.ReverseProxy
	mu             sync.Mutex
	legs           []*recoveryLeg
	loss           atomic.Bool
	packets, drops atomic.Int64
	closed         chan struct{}
	workers        sync.WaitGroup
}

func newRecoveryProxy(target string) *recoveryProxy {
	u, _ := url.Parse(target)
	return &recoveryProxy{target: target, http: httputil.NewSingleHostReverseProxy(u), closed: make(chan struct{})}
}
func (p *recoveryProxy) latest() *recoveryLeg {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.legs) == 0 {
		return nil
	}
	return p.legs[len(p.legs)-1]
}
func (p *recoveryProxy) close() {
	close(p.closed)
	p.mu.Lock()
	legs := append([]*recoveryLeg(nil), p.legs...)
	p.mu.Unlock()
	for _, l := range legs {
		l.down.CloseNow()
		l.up.CloseNow()
	}
	p.workers.Wait()
}
func (p *recoveryProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/stream" {
		p.http.ServeHTTP(w, r)
		return
	}
	capture := &hijackedRecovery{ResponseWriter: w}
	down, err := websocket.Accept(capture, r, nil)
	if err != nil {
		return
	}
	defer down.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	up, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(p.target, "http")+r.URL.Path, nil)
	if err != nil {
		return
	}
	defer up.CloseNow()
	down.SetReadLimit(4 << 20)
	up.SetReadLimit(4 << 20)
	leg := &recoveryLeg{down: down, up: up, raw: capture.raw}
	p.mu.Lock()
	index := len(p.legs)
	p.legs = append(p.legs, leg)
	p.mu.Unlock()
	done := make(chan struct{}, 1)
	p.workers.Go(func() {
		defer func() { done <- struct{}{} }()
		for {
			kind, b, e := down.Read(ctx)
			if e != nil {
				return
			}
			if e = up.Write(ctx, kind, b); e != nil {
				return
			}
		}
	})
	// Seeded independent packet loss in a reliable test transport: each 1460
	// byte unit is retried after 200ms, preserving byte order. This is not a
	// modification of the host TCP stack or evidence about WAN packet loss.
	random := rand.New(rand.NewPCG(7, uint64(index+1)))
	for {
		kind, b, e := up.Read(ctx)
		if e != nil {
			if leg.blackhole.Load() {
				select {
				case <-ctx.Done():
				case <-p.closed:
				case <-done:
				}
			}
			return
		}
		if leg.blackhole.Load() {
			continue
		}
		if p.loss.Load() {
			for offset := 0; offset < len(b); offset += 1460 {
				for {
					p.packets.Add(1)
					if random.Float64() >= .03 {
						break
					}
					p.drops.Add(1)
					timer := time.NewTimer(200 * time.Millisecond)
					select {
					case <-timer.C:
					case <-ctx.Done():
						timer.Stop()
						return
					case <-p.closed:
						timer.Stop()
						return
					}
				}
			}
		}
		if down.Write(ctx, kind, b) != nil {
			return
		}
	}
}

type recoverySample struct {
	Scenario    string  `json:"scenario"`
	DiscoveryMS float64 `json:"discovery_ms"`
	RecoveryMS  float64 `json:"recovery_ms"`
	TotalMS     float64 `json:"total_ms"`
	Ready       bool    `json:"ready"`
	Call        bool    `json:"call"`
	Attempts    int     `json:"attempts"`
}
type recoveryPause struct{ entered, release chan struct{} }
type recoveryObservation struct {
	pause             atomic.Pointer[recoveryPause]
	mu                sync.Mutex
	start, discovered time.Time
	ready             chan time.Time
	attempts          int
	loss              bool
	lossProbes        int
}

func (o *recoveryObservation) event(event string) {
	if event == "hint" {
		if pause := o.pause.Swap(nil); pause != nil {
			close(pause.entered)
			<-pause.release
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if event == "dial" && o.loss {
		o.lossProbes++
	}
	if o.start.IsZero() {
		return
	}
	if (event == "hint" || event == "suspect" || event == "lost") && o.discovered.IsZero() {
		o.discovered = time.Now()
	}
	if event == "dial" {
		o.attempts++
	}
	if event == "ready" {
		select {
		case o.ready <- time.Now():
		default:
		}
	}
}

func TestRecoveryMeasurements(t *testing.T) {
	output := os.Getenv("RA_RECOVERY_METRICS")
	if output == "" {
		t.Skip("opt-in recovery measurement")
	}
	if home := os.Getenv("REMOTE_AGENT_HOME"); !strings.HasPrefix(home, "/tmp/") && !strings.HasPrefix(home, "/private/tmp/") {
		t.Fatal("isolated home required")
	}
	samples, _ := strconv.Atoi(os.Getenv("RA_RECOVERY_SAMPLES"))
	if samples < 1 {
		samples = 20
	}
	lossSeconds, _ := strconv.Atoi(os.Getenv("RA_RECOVERY_LOSS_SECONDS"))
	if lossSeconds < 1 {
		lossSeconds = 120
	}
	var rows []recoverySample
	var noise map[string]any
	for _, scenario := range []string{"rst", "blackhole", "network_hint", "same_interface_address", "notification_storm", "scheduler_pause", "bulk_six", "bulk_eight"} {
		t.Run(scenario, func(t *testing.T) {
			e := setup(t)
			e.backend.add(t, "measurement")
			proxy := newRecoveryProxy(e.acct.Server)
			front := httptest.NewServer(proxy)
			defer front.Close()
			defer proxy.close()
			e.acct.Server = front.URL
			if err := e.acct.Save(); err != nil {
				t.Fatal(err)
			}
			changes := make(chan uint64, 1)
			observation := &recoveryObservation{}
			r := e.startRelayBackend(e.backend, func(r *relay.Relay) {
				relay.SetNetworkChangesForTest(r, changes)
				relay.ObserveRecoveryForTest(r, observation.event)
			})
			defer e.stopRelay()
			e.online(t, true)
			session := e.session(t, "measurement", 0)
			// HTTP command success is checked through the production encrypted
			// request/result path, rather than treating Ready as recovery.
			call := func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, err := e.client.Do(ctx, session, &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: session.ID, Title: "measured"}}})
				return err == nil
			}
			if !call() {
				t.Fatal("initial Link call failed")
			}
			for sample := 0; sample < samples; sample++ {
				var release chan struct{}
				var calls sync.WaitGroup
				if scenario == "bulk_six" || scenario == "bulk_eight" {
					count := 6
					if scenario == "bulk_eight" {
						count = 8
					}
					release = make(chan struct{})
					entered := make(chan struct{}, count)
					e.backend.sendResult = func(ctx context.Context) error {
						entered <- struct{}{}
						select {
						case <-release:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					for range count {
						calls.Go(func() {
							ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
							defer cancel()
							_, _ = e.client.Do(ctx, session, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: session.ID, Text: "load"}}})
						})
					}
					for range count {
						select {
						case <-entered:
						case <-time.After(5 * time.Second):
							t.Fatal("load was not admitted")
						}
					}
				}
				ready := make(chan time.Time, 1)
				started := time.Now()
				observation.mu.Lock()
				observation.start = started
				observation.discovered = time.Time{}
				observation.ready = ready
				observation.attempts = 0
				observation.mu.Unlock()
				leg := proxy.latest()
				if leg == nil {
					t.Fatal("missing authenticated path")
				}
				switch scenario {
				case "rst":
					if tcp, ok := leg.raw.(*net.TCPConn); ok {
						_ = tcp.SetLinger(0)
					}
					_ = leg.raw.Close()
				case "blackhole":
					leg.blackhole.Store(true)
				case "scheduler_pause":
					// Pause only the recovery scheduler. The existing socket
					// readers and execution owner remain independently alive.
					pause := &recoveryPause{make(chan struct{}), make(chan struct{})}
					observation.pause.Store(pause)
					changes <- uint64(sample + 1)
					<-pause.entered
					timer := time.NewTimer(3500 * time.Millisecond)
					<-timer.C
					started = time.Now()
					observation.mu.Lock()
					observation.start = started
					observation.discovered = time.Time{}
					observation.mu.Unlock()
					close(pause.release)
				default:
					iterations := 1
					if scenario == "notification_storm" {
						iterations = 1000
					}
					for n := 0; n < iterations; n++ {
						select {
						case <-changes:
						default:
						}
						changes <- uint64(sample*1000 + n + 1)
					}
				}
				row := recoverySample{Scenario: scenario}
				select {
				case <-ready:
					row.Ready = true
				case <-time.After(8 * time.Second):
				}
				if row.Ready {
					row.Call = call()
				}
				finished := time.Now()
				observation.mu.Lock()
				discovered := observation.discovered
				if discovered.IsZero() {
					discovered = started
				}
				row.Attempts = observation.attempts
				observation.start = time.Time{}
				observation.mu.Unlock()
				row.DiscoveryMS = float64(discovered.Sub(started)) / float64(time.Millisecond)
				row.RecoveryMS = float64(finished.Sub(discovered)) / float64(time.Millisecond)
				row.TotalMS = float64(finished.Sub(started)) / float64(time.Millisecond)
				rows = append(rows, row)
				if release != nil {
					close(release)
					calls.Wait()
					waitFor(t, "load execution finished", func() bool { return relay.ActiveCallsForTest(r) == 0 })
					e.backend.sendResult = nil
				}
				if !row.Ready {
					t.Fatal("path did not recover; no later samples taken")
				}
			}
			if scenario == "network_hint" {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				frames, err := e.client.Stream(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var offline atomic.Int64
				var observe atomic.Bool
				readerDone := make(chan struct{})
				go func() {
					defer close(readerDone)
					for f := range frames {
						if m := f.GetMachineUpdated().GetMachine(); m != nil && m.Id == e.acct.DeviceID && !m.Online && observe.Load() {
							offline.Add(1)
						}
					}
				}()
				proxy.packets.Store(0)
				proxy.drops.Store(0)
				proxy.loss.Store(true)
				observe.Store(true)
				observation.mu.Lock()
				observation.loss = true
				observation.lossProbes = 0
				observation.mu.Unlock()
				start := time.Now()
				timer := time.NewTimer(time.Duration(lossSeconds) * time.Second)
				<-timer.C
				duration := time.Since(start).Seconds()
				proxy.loss.Store(false)
				observe.Store(false)
				observation.mu.Lock()
				probes := observation.lossProbes
				observation.loss = false
				observation.mu.Unlock()
				noise = map[string]any{"seconds": duration, "configured_loss": .03, "packets": proxy.packets.Load(), "lost_packets": proxy.drops.Load(), "false_offline": offline.Load(), "extra_probes": probes, "extra_probes_per_hour": float64(probes) * 3600 / duration, "final_link_call": call(), "ready_generations": relay.ReadyCountForTest(r)}
				cancel()
				<-readerDone
			}
		})
	}
	bytes, err := json.MarshalIndent(map[string]any{"samples": rows, "loss": noise, "model": "local reliable test transport; real relay, Link, authenticated RPC; counting backend"}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(output, bytes, 0600); err != nil {
		t.Fatal(err)
	}
}
