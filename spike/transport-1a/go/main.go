// Isolated SDK experiment; no product imports or configuration.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func await(done <-chan struct{}) {
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		panic("SDK deadline")
	}
}

type peer struct {
	pc         *webrtc.PeerConnection
	dc         [2]*webrtc.DataChannel
	mu         sync.Mutex
	candidates []webrtc.ICECandidateInit
	unexpected atomic.Int32
}

func makePeer() *peer {
	s := webrtc.SettingEngine{}
	s.SetIncludeLoopbackCandidate(true)
	s.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	s.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	l := logging.NewDefaultLoggerFactory()
	l.DefaultLogLevel = logging.LogLevelDisabled
	s.LoggerFactory = l
	api := webrtc.NewAPI(webrtc.WithSettingEngine(s))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	must(err)
	p := &peer{pc: pc}
	pc.OnDataChannel(func(dc *webrtc.DataChannel) { p.unexpected.Add(1); _ = dc.Close() })
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			p.mu.Lock()
			p.candidates = append(p.candidates, c.ToJSON())
			p.mu.Unlock()
		}
	})
	for id := uint16(0); id < 2; id++ {
		negotiated, ordered, zero := true, id == 0, uint16(0)
		init := &webrtc.DataChannelInit{ID: &id, Negotiated: &negotiated, Ordered: &ordered}
		if id == 1 {
			init.MaxRetransmits = &zero
		}
		p.dc[id], err = pc.CreateDataChannel(fmt.Sprintf("lab-%d", id), init)
		must(err)
	}
	return p
}

func copyCandidates(from, to *peer) int {
	from.mu.Lock()
	defer from.mu.Unlock()
	for _, c := range from.candidates {
		must(to.pc.AddICECandidate(c))
	}
	must(to.pc.AddICECandidate(webrtc.ICECandidateInit{}))
	return len(from.candidates)
}

func main() {
	if !strings.HasPrefix(os.Getenv("REMOTE_AGENT_HOME"), "/tmp/") {
		panic("isolated state required")
	}
	if len(os.Args) == 3 && os.Args[1] == "vectors" {
		runSDPVectors(os.Args[2])
		return
	}
	if len(os.Args) != 2 {
		panic("output directory required")
	}
	out := os.Args[1]
	must(os.MkdirAll(out, 0700))
	started := time.Now()
	a, b := makePeer(), makePeer()
	defer a.pc.Close()
	defer b.pc.Close()
	open := make(chan struct{})
	b.dc[0].OnOpen(func() { close(open) })
	message := make(chan struct{})
	b.dc[0].OnMessage(func(m webrtc.DataChannelMessage) {
		if string(m.Data) == "lab-message" {
			close(message)
		}
	})
	offer, err := a.pc.CreateOffer(nil)
	must(err)
	// Persist SDK-produced pre-gather SDP byte-for-byte, without host candidates.
	must(os.WriteFile(filepath.Join(out, "pion-offer.sdp"), []byte(offer.SDP), 0600))
	must(validateSDP([]byte(offer.SDP)))
	ga := webrtc.GatheringCompletePromise(a.pc)
	must(a.pc.SetLocalDescription(offer))
	await(ga)
	must(b.pc.SetRemoteDescription(offer))
	answer, err := b.pc.CreateAnswer(nil)
	must(err)
	must(os.WriteFile(filepath.Join(out, "pion-answer.sdp"), []byte(answer.SDP), 0600))
	must(validateSDP([]byte(answer.SDP)))
	gb := webrtc.GatheringCompletePromise(b.pc)
	must(b.pc.SetLocalDescription(answer))
	await(gb)
	must(a.pc.SetRemoteDescription(answer))
	ca, cb := copyCandidates(a, b), copyCandidates(b, a)
	await(open)
	must(a.dc[0].Send([]byte("lab-message")))
	await(message)
	restart, err := a.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	must(err)
	must(os.WriteFile(filepath.Join(out, "pion-restart.sdp"), []byte(restart.SDP), 0600))
	if restart.SDP == offer.SDP {
		panic("unchanged restart")
	}
	result := map[string]any{"sdk": "pion/webrtc/v4 v4.2.26", "elapsed_ms": float64(time.Since(started).Microseconds()) / 1000, "candidates": []int{ca, cb}, "dcep_callbacks": a.unexpected.Load() + b.unexpected.Load(), "negotiated_ids": []int{0, 1}, "message": true, "raw_remote_description_matches": a.pc.RemoteDescription().SDP == answer.SDP && b.pc.RemoteDescription().SDP == offer.SDP}
	encoded, err := json.MarshalIndent(result, "", "  ")
	must(err)
	fmt.Println(string(encoded))
}
