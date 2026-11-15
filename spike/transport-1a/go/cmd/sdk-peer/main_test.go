package main

import (
	"context"
	"github.com/pion/webrtc/v4"
	"sync"
	"testing"
	"time"
)

// Exercise the receiving Pion adapter as well as the native/browser receivers.
func TestUnexpectedChannels(t *testing.T) {
	for _, negotiated := range []bool{false, true} {
		t.Run(map[bool]string{false: "DCEP", true: "undeclared negotiated"}[negotiated], func(t *testing.T) {
			a, e := makePeer(false)
			if e != nil {
				t.Fatal(e)
			}
			b, e := makePeer(false)
			if e != nil {
				t.Fatal(e)
			}
			defer a.pc.Close()
			defer b.pc.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			call := func(p *peer, q request) map[string]any {
				r, e := p.handle(ctx, q)
				if e != nil {
					t.Fatal(e)
				}
				result, _ := r.(map[string]any)
				return result
			}
			offer := call(a, request{Operation: "offer"})
			answer := call(b, request{Operation: "answer-offer", SDP: offer["sdp"].([]byte)})
			call(a, request{Operation: "install-answer", Generation: 1, SDP: answer["sdp"].([]byte)})
			opened := make(chan struct{})
			var once sync.Once
			a.dc[0].OnOpen(func() { once.Do(func() { close(opened) }) })
			echo := make(chan struct{}, 2)
			a.dc[0].OnMessage(func(webrtc.DataChannelMessage) { echo <- struct{}{} })
			for _, pair := range []struct {
				to          *peer
				description map[string]any
			}{{a, answer}, {b, offer}} {
				call(pair.to, request{Operation: "candidates", Generation: 1, Ufrag: pair.description["ufrag"].(string), Candidates: pair.description["candidates"].([]webrtc.ICECandidateInit)})
			}
			select {
			case <-opened:
			case <-ctx.Done():
				t.Fatal("open deadline")
			}
			closed := make(chan struct{})
			var closeOnce sync.Once
			b.pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
				if s == webrtc.PeerConnectionStateClosed {
					closeOnce.Do(func() { close(closed) })
				}
			})
			id := uint16(2)
			rogue, e := a.pc.CreateDataChannel("undeclared", &webrtc.DataChannelInit{ID: &id, Negotiated: &negotiated})
			if e != nil {
				t.Fatal(e)
			}
			rogue.OnOpen(func() { _ = rogue.SendText("forbidden-business") })
			if !negotiated {
				select {
				case <-closed:
				case <-ctx.Done():
					t.Fatal("DCEP close deadline")
				}
				if b.dcep.Load() != 1 || !b.closed.Load() || b.business.Load() != 0 {
					t.Fatal("DCEP reached application")
				}
			} else {
				if e = a.dc[0].SendText("barrier"); e != nil {
					t.Fatal(e)
				}
				select {
				case <-echo:
				case <-ctx.Done():
					t.Fatal("barrier deadline")
				}
				select {
				case <-closed:
					t.Fatal("undeclared stream created an application channel")
				case <-time.After(150 * time.Millisecond):
				}
				if b.dcep.Load() != 0 || b.business.Load() != 1 {
					t.Fatal("undeclared payload delivered")
				}
			}
		})
	}
}
