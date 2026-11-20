package main

import (
	"bytes"

	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func auditCases(sec *Security, results map[string]any) {
	for _, direction := range []devicesec.Purpose{devicesec.TerminalInput, devicesec.TerminalOutput} {
		name := "input"
		if direction == devicesec.TerminalOutput {
			name = "output"
		}
		s := newStream(sec, "audit-expiry-"+name+"-001")
		late := sec.seal(s.id, 2, base, direction, []byte("late"))
		s.advance(59000)
		must(send(s, "relay", late, false))
		deadline := s.gap
		if direction == devicesec.TerminalOutput {
			deadline = s.outputGapDeadline
		}
		check(deadline == base+60000, "gap outlived authorization")
		s.advance(59999)
		check(!s.networkRecovery, "early expiry recovery")
		s.advance(60000)
		check(s.networkRecovery && s.failed == 0, "deadline boundary not recovery")
		s.advance(60001)
		first := sec.seal(s.id, 1, s.now, direction, []byte("first"))
		must(send(s, "relay", first, false))
		if direction == devicesec.TerminalInput {
			check(s.processed == 1 && s.injectedChunks == 1, "expired buffered input applied")
		} else {
			s.render()
			s.render()
			check(s.rendered == 1, "expired buffered output rendered")
		}
		results["audit_"+name+"_original_deadline"] = true

		for _, split := range []bool{false, true} {
			s = newStream(sec, "audit-gap-"+name+"-002")
			if split {
				must(send(s, "relay", sec.seal(s.id, 4, s.now, direction, []byte("four")), false))
			} else {
				must(send(s, "relay", sec.seal(s.id, 2, s.now, direction, []byte("two")), false))
			}
			s.advance(2000)
			if split {
				must(send(s, "direct", sec.seal(s.id, 2, s.now, direction, []byte("two")), false))
			} else {
				must(send(s, "direct", sec.seal(s.id, 4, s.now, direction, []byte("four")), false))
			}
			s.advance(9000)
			must(send(s, "relay", sec.seal(s.id, 1, s.now, direction, []byte("one")), false))
			want := base + 12000
			if split {
				want = base + 10000
			}
			deadline = s.gap
			if direction == devicesec.TerminalOutput {
				deadline = s.outputGapDeadline
			}
			check(deadline == want, "successive/split gap age")
		}
		results["audit_"+name+"_successive_and_split_gaps"] = true

		s = newStream(sec, "audit-identity-"+name+"-003")
		must(send(s, "relay", sec.seal(s.id, 2, s.now, direction, []byte("old")), false))
		s.instance = "daemon-instance-new"
		first = sec.seal(s.id, 1, s.now, direction, []byte("new"))
		must(send(s, "direct", first, false))
		if direction == devicesec.TerminalInput {
			check(s.processed == 1 && s.injectedChunks == 1, "old instance input delivered")
		} else {
			s.render()
			s.render()
			check(s.rendered == 1, "old instance output rendered")
		}
		check(s.networkRecovery && s.failed == 0, "stale identity not recovery")
		results["audit_"+name+"_buffer_identity"] = true
	}
	s := newStream(sec, "audit-assembly-0001")
	large := sec.seal(s.id, 1, s.now, devicesec.TerminalOutput, bytes.Repeat([]byte{'f'}, 32<<10))
	parts := fragments(large, 1)
	must(s.receive("relay", s.instance, parts[0]))
	s.now = base + 5000
	check(s.receive("relay", s.instance, parts[1]) != nil && s.assemblyBytes == 0 && len(s.accepted) == 0, "assembly expiry bypassed by late tick")
	s = newStream(sec, "audit-assembly-0002")
	large = sec.seal(s.id, 1, base, devicesec.TerminalOutput, bytes.Repeat([]byte{'f'}, 32<<10))
	s.advance(59000)
	parts = fragments(large, 1)
	must(s.receive("relay", s.instance, parts[0]))
	s.advance(60000)
	for _, f := range parts[1:] {
		_ = s.receive("relay", s.instance, f)
	}
	check(s.rendered == 0 && s.renderReady == nil, "late assembled envelope applied")
	s = newStream(sec, "audit-leg-00000001")
	large = sec.seal(s.id, 1, base, devicesec.TerminalOutput, bytes.Repeat([]byte{'f'}, 32<<10))
	parts = fragments(large, 1)
	must(s.receive("relay", s.instance, parts[0]))
	must(s.attach("relay", s.instance, 2))
	check(s.receive("relay", s.instance, parts[1]) != nil, "old leg fragment accepted")
	results["audit_assembly_expiry_identity_and_leg"] = true

	s = newStream(sec, "audit-render-00001")
	must(send(s, "relay", sec.seal(s.id, 1, base, devicesec.TerminalOutput, []byte("render later")), false))
	s.advance(60000)
	s.render()
	check(s.rendered == 0 && s.networkRecovery, "late UI consumed expired frame")
	results["audit_contiguous_render_deadline"] = true

	s = newStream(sec, "audit-replay-00001")
	e := sec.seal(s.id, 1, base, devicesec.TerminalOutput, []byte("cached"))
	s.advance(59000)
	s.rememberOutput(e)
	s.advance(60000)
	_, err := s.replayFrom(1)
	check(err != nil, "replay extended original expiry")
	s = newStream(sec, "audit-replay-00002")
	e = sec.seal(s.id, 1, base, devicesec.TerminalOutput, []byte("cached"))
	s.rememberOutput(e)
	s.instance = "daemon-instance-new"
	_, err = s.replayFrom(1)
	check(err != nil, "replay crossed instance")
	s = newStream(sec, "audit-input-cache1")
	e = sec.seal(s.id, 1, base, devicesec.TerminalInput, []byte("cached"))
	must(s.cacheInput(e))
	s.instance = "daemon-instance-new"
	check(send(s, "relay", s.inputCache[1], false) != nil && s.injectedChunks == 0, "input cache crossed instance")
	results["audit_replay_and_send_cache_bounds"] = true

	s = newStream(sec, "audit-membership-01")
	e = sec.seal(s.id, 2, base, devicesec.TerminalInput, []byte("queued"))
	must(send(s, "relay", e, false))
	other := *sec
	other.client.ID = "different-client-member"
	s.sec = &other
	check(s.fresh(s.pending[2]) != nil, "deferred sender binding bypassed")
	check(len(s.accepted) == 0, "unauthorized queued Accept")
	results["audit_current_membership_before_admission"] = true

	s = newStream(sec, "audit-completion-01")
	s.hold = true
	must(send(s, "relay", sec.seal(s.id, 1, s.now, devicesec.TerminalInput, []byte("once")), false))
	check(s.injectedChunks == 1, "backend not started at admission")
	var reply Status
	s.secureStatus("relay", false, func(st Status) { reply = st })
	s.advance(10000)
	s.release()
	check(!reply.Available && s.processed == 1 && s.injectedChunks == 1, "late query lost execution evidence or redelivered")
	s = newStream(sec, "audit-completion-02")
	s.hold = true
	must(send(s, "relay", sec.seal(s.id, 1, s.now, devicesec.TerminalInput, []byte("once")), false))
	s.instance = "daemon-instance-new"
	s.release()
	check(s.processed == 0 && s.retiredCompletion != nil && s.injectedChunks == 1, "completion crossed owner instance")
	results["audit_completion_vs_new_admission"] = true
	// A known, already admitted completion is retained even after the caller's
	// deadline; it cannot authorize a second backend call or a new-owner reply.
}
