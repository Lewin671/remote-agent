package main

import (
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

func authorizationCases(results map[string]any) {
	for _, reason := range []string{"lease", "member", "key", "generation", "instance"} {
		for _, final := range []bool{false, true} {
			for _, failure := range []bool{false, true} {
				sec := security()
				s := newStream(sec, "auth-deferred-0001")
				s.now = sec.lease.NotAfter - 1
				s.hold = true
				s.failInput = failure
				must(send(s, "relay", sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("once")), false))
				var status Status
				s.secureStatus("relay", final, func(st Status) { status = st })
				packets, acks := s.controlPackets, s.acks
				switch reason {
				case "lease":
					s.now++
				case "member":
					sec.state.Core.Members = nil
				case "key":
					sec.client.EdPublic[0] ^= 1
				case "generation":
					s.controlGeneration++
				case "instance":
					s.instance = "different-owner"
				}
				s.release()
				s.emitACK()
				check(!status.Available && s.controlPackets == packets && s.acks == acks, "unauthorized deferred reply or ACK")
				check(s.injectedChunks == 1 && (s.processed == 1 || s.retiredCompletion != nil), "completion evidence discarded")
			}
		}
		results["audit_deferred_reply_and_ack_"+reason] = true
	}
	for _, direction := range []deviceauth.Purpose{deviceauth.TerminalInput, deviceauth.TerminalOutput} {
		sec := security()
		s := newStream(sec, "auth-batch-ack-001")
		s.now = sec.lease.NotAfter - 1
		must(send(s, "relay", sec.seal(s.id, 1, s.now, direction, []byte("once")), false))
		if direction == deviceauth.TerminalOutput {
			s.render()
		}
		s.now++
		s.emitACK()
		check(s.acks == 0, "batch ACK after expiry")
	}
	results["audit_ack_both_directions_expiry"] = true
	sec := security()
	s := newStream(sec, "auth-pages-000001")
	for n := uint64(1); n <= 2; n++ {
		must(send(s, "relay", sec.seal(s.id, n, s.now, deviceauth.TerminalInput, []byte("once")), false))
	}
	calls := 0
	s.replyHook = func() {
		calls++
		if calls == 2 {
			s.controlGeneration++
		}
	}
	var status Status
	s.secureStatus("relay", false, func(st Status) { status = st })
	check(!status.Available && calls == 2 && s.controlPackets == 2 && s.processed == 2, "pagination bypassed authorization")
	results["audit_each_status_page_authorization"] = true
	// A refreshed lease for the same current membership remains usable.
	sec = security()
	s = newStream(sec, "auth-renewed-0001")
	s.hold = true
	must(send(s, "relay", sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("once")), false))
	s.secureStatus("relay", false, func(st Status) { status = st })
	var err error
	sec.lease, err = deviceauth.BuildLease(sec.state, sec.clientEd, s.now+1)
	must(err)
	s.now++
	s.release()
	check(status.Available && s.processed == 1, "valid renewal rejected")
	results["audit_authorized_renewal_reply"] = true
}

func controlValidationCases(results map[string]any) {
	s := newStream(security(), "auth-validation-01")
	called := false
	for typ := uint64(1); typ <= 13; typ++ {
		e := s.control("relay", typ, []byte{255}, func([]byte) error { called = true; return nil })
		check(e != nil && !called && s.controlSeq == 0 && s.controlPackets == 0, "malformed control changed state")
	}
	results["audit_terminal_controls_before_effects"] = true
}
