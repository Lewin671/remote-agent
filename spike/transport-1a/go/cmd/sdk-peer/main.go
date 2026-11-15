// A local Pion counterpart for cross-SDK interoperability. This process is the
// peer (entitled to see its own SDP), NOT a relay or any product server.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"example.com/transport-sdk-lab/labwire"
	"flag"
	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type round struct {
	gen           uint64
	done          chan struct{}
	complete      bool
	candidates    []webrtc.ICECandidateInit
	local, remote []byte
}
type peer struct {
	pc                                      *webrtc.PeerConnection
	dc                                      [2]*webrtc.DataChannel
	op, mu                                  sync.Mutex
	r                                       *round
	dcep, business, probeRejected, addCalls atomic.Int32
	closed                                  atomic.Bool
}

func makePeer(android bool) (*peer, error) {
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	if android {
		se.SetNAT1To1IPs([]string{"10.0.2.2"}, webrtc.ICECandidateTypeHost)
	}
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelDisabled
	se.LoggerFactory = lf
	pc, e := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if e != nil {
		return nil, e
	}
	p := &peer{pc: pc, r: &round{gen: 1, done: make(chan struct{})}}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if c != nil {
			if len(p.r.candidates) < 64 {
				p.r.candidates = append(p.r.candidates, c.ToJSON())
			}
		} else if !p.r.complete {
			p.r.complete = true
			close(p.r.done)
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) { p.dcep.Add(1); _ = dc.Close(); p.closed.Store(true); go pc.Close() })
	for id := uint16(0); id < 2; id++ {
		neg, ordered, zero := true, id == 0, uint16(0)
		init := &webrtc.DataChannelInit{ID: &id, Negotiated: &neg, Ordered: &ordered}
		if id == 1 {
			init.MaxRetransmits = &zero
		}
		dc, e := pc.CreateDataChannel("lab", init)
		if e != nil {
			pc.Close()
			return nil, e
		}
		p.dc[id] = dc
		index := id
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if p.closed.Load() {
				return
			}
			if index == 1 {
				allowed := bytes.HasPrefix(m.Data, []byte("probe:"))
				if bytes.HasPrefix(m.Data, []byte("probe-gen:")) {
					parts := strings.SplitN(string(m.Data), ":", 3)
					if len(parts) == 3 {
						gen, e := strconv.ParseUint(parts[1], 10, 64)
						p.mu.Lock()
						allowed = e == nil && gen == p.r.gen && len(p.r.local) > 0 && len(p.r.remote) > 0
						p.mu.Unlock()
					}
				}
				if !allowed {
					p.probeRejected.Add(1)
					return
				}
			}
			if index == 0 {
				p.business.Add(1)
			}
			if m.IsString {
				_ = dc.SendText(string(m.Data))
			} else {
				_ = dc.Send(m.Data)
			}
		})
	}
	return p, nil
}
func (p *peer) await(ctx context.Context) error {
	p.mu.Lock()
	done := p.r.done
	p.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Second):
		return context.DeadlineExceeded
	}
}
func (p *peer) description(offer, restart bool) (webrtc.SessionDescription, error) {
	if offer {
		return p.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: restart})
	}
	return p.pc.CreateAnswer(nil)
}

type request struct {
	ID                   string                    `json:"id"`
	Android              bool                      `json:"android"`
	Operation            string                    `json:"operation"`
	SDP                  []byte                    `json:"sdp"`
	Generation           uint64                    `json:"generation"`
	Ufrag                string                    `json:"ufrag"`
	Candidates           []webrtc.ICECandidateInit `json:"candidates"`
	SkipRemoteCandidates bool                      `json:"skip_remote_candidates"`
}

func (p *peer) handle(ctx context.Context, q request) (any, error) {
	p.op.Lock()
	defer p.op.Unlock()
	switch q.Operation {
	case "offer", "restart-offer", "answer-offer":
		restart := q.Operation == "restart-offer"
		if restart {
			p.mu.Lock()
			if !p.r.complete {
				p.mu.Unlock()
				return nil, context.DeadlineExceeded
			}
			p.r = &round{gen: p.r.gen + 1, done: make(chan struct{})}
			p.mu.Unlock()
		}
		offer := q.Operation != "answer-offer"
		if !offer {
			if e := labwire.ValidateSDP(q.SDP); e != nil {
				return nil, e
			}
			if e := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: string(q.SDP)}); e != nil {
				return nil, e
			}
			p.mu.Lock()
			p.r.remote = append([]byte(nil), q.SDP...)
			p.mu.Unlock()
		}
		d, e := p.description(offer, restart)
		if e != nil {
			return nil, e
		}
		if e = labwire.ValidateSDP([]byte(d.SDP)); e != nil {
			return nil, e
		}
		if e = p.pc.SetLocalDescription(d); e != nil {
			return nil, e
		}
		p.mu.Lock()
		p.r.local = []byte(d.SDP)
		p.mu.Unlock()
		if e = p.await(ctx); e != nil {
			return nil, e
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		info, e := labwire.InspectSDP(p.r.local)
		if e != nil {
			return nil, e
		}
		return map[string]any{"sdp": p.r.local, "generation": p.r.gen, "ufrag": string(info.Ufrag), "candidates": p.r.candidates, "type": d.Type.String()}, nil
	case "install-answer":
		if q.Generation != p.r.gen {
			return nil, labwire.ErrSignal
		}
		if e := labwire.ValidateSDP(q.SDP); e != nil {
			return nil, e
		}
		if e := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(q.SDP)}); e != nil {
			return nil, e
		}
		p.mu.Lock()
		p.r.remote = append([]byte(nil), q.SDP...)
		p.mu.Unlock()
		return map[string]bool{"installed": true}, nil
	case "candidates":
		info, e := labwire.InspectSDP(p.r.remote)
		if e != nil {
			return nil, e
		}
		if q.Generation != p.r.gen || q.Ufrag != string(info.Ufrag) || len(q.Candidates) > 64 {
			return nil, labwire.ErrSignal
		}
		for _, c := range q.Candidates {
			mid := string(info.Mid)
			index := uint16(0)
			if c.SDPMid != nil {
				mid = *c.SDPMid
			}
			if c.SDPMLineIndex != nil {
				index = *c.SDPMLineIndex
			}
			if mid != string(info.Mid) || index != 0 || c.UsernameFragment != nil && *c.UsernameFragment != q.Ufrag {
				return nil, labwire.ErrSignal
			}
			candidate := labwire.Candidate{Original: []byte(c.Candidate), Ufrag: info.Ufrag, Mid: info.Mid, Index: 0, Generation: q.Generation}
			if _, _, e := labwire.ReadCandidates(labwire.CandidateBody([]labwire.Candidate{candidate}, 0)); e != nil {
				return nil, e
			}
		}
		// An Android emulator initiates towards its built-in host gateway. Pion
		// learns its loopback peer-reflexive mapping; never probe guest-private IPs
		// through the host's physical routes in this isolated interoperability test.
		if !q.SkipRemoteCandidates {
			for _, c := range q.Candidates {
				if e := p.pc.AddICECandidate(c); e != nil {
					return nil, e
				}
				p.addCalls.Add(1)
			}
		}
		if e := p.pc.AddICECandidate(webrtc.ICECandidateInit{}); e != nil {
			return nil, e
		}
		return map[string]int32{"add_calls": p.addCalls.Load()}, nil
	case "stats":
		return map[string]any{"generation": p.r.gen, "dcep_rejected": p.dcep.Load(), "business_received": p.business.Load(), "probe_business_rejected": p.probeRejected.Load(), "add_calls": p.addCalls.Load(), "closed": p.closed.Load(), "ice": p.pc.ICEConnectionState().String()}, nil
	case "rogue-dcep", "rogue-negotiated":
		id := uint16(2)
		neg := q.Operation == "rogue-negotiated"
		dc, e := p.pc.CreateDataChannel("undeclared", &webrtc.DataChannelInit{ID: &id, Negotiated: &neg})
		if e != nil {
			return nil, e
		}
		dc.OnOpen(func() { _ = dc.SendText("forbidden-business") })
		return map[string]bool{"sent": true}, nil
	case "close":
		p.closed.Store(true)
		return nil, p.pc.Close()
	}
	return nil, labwire.ErrPacket
}
func main() {
	if !strings.HasPrefix(os.Getenv("REMOTE_AGENT_HOME"), "/tmp/") {
		panic("isolation")
	}
	info := flag.String("info", "", "private output file")
	flag.Parse()
	if !strings.HasPrefix(*info, "/tmp/") {
		panic("private info path")
	}
	raw := make([]byte, 32)
	if _, e := rand.Read(raw); e != nil {
		panic(e)
	}
	token := hex.EncodeToString(raw)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		panic(e)
	}
	defer listener.Close()
	var mu sync.Mutex
	peers := map[string]*peer{}
	validID := regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8>"))
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		var q request
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&q) != nil || !validID.MatchString(q.ID) {
			http.Error(w, "request", 400)
			return
		}
		mu.Lock()
		p := peers[q.ID]
		if p == nil {
			if len(peers) >= 32 {
				mu.Unlock()
				http.Error(w, "capacity", 429)
				return
			}
			if q.Operation != "offer" && q.Operation != "answer-offer" {
				mu.Unlock()
				http.Error(w, "missing", 404)
				return
			}
			p, e = makePeer(q.Android)
			if e != nil {
				mu.Unlock()
				http.Error(w, "sdk", 500)
				return
			}
			peers[q.ID] = p
		}
		mu.Unlock()
		result, e := p.handle(r.Context(), q)
		if e != nil {
			http.Error(w, "rejected", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
		if q.Operation == "close" {
			mu.Lock()
			delete(peers, q.ID)
			mu.Unlock()
		}
	})
	data, _ := json.Marshal(map[string]any{"url": "http://" + listener.Addr().String(), "token": token, "pid": os.Getpid()})
	if e = os.WriteFile(*info, data, 0600); e != nil {
		panic(e)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	_ = server.Serve(listener)
}
