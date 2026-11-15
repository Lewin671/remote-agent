package main

import (
	"bytes"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

func reviewR2Cases(sec *Security, results map[string]any) {
	s := newStream(sec, "stream-r2-output-001")
	large := sec.seal(s.id, 2, s.now, deviceauth.TerminalOutput, bytes.Repeat([]byte{'x'}, 512<<10))
	check(len(large.raw) > 512<<10, "budget fixture omitted encoding")
	must(send(s, "direct", large, true))
	retained := len(large.raw)
	for seq := uint64(3); seq <= 4; seq++ {
		more := sec.seal(s.id, seq, s.now, deviceauth.TerminalOutput, bytes.Repeat([]byte{'y'}, 512<<10))
		check(send(s, "relay", more, false) != nil, "encoded output budget bypass")
		check(s.outputBytes == retained && len(s.output) == 1 && s.rendered == 0 && s.assemblyBytes == 0, "rejected output changed retention")
	}
	must(send(s, "relay", large, false))
	check(s.outputBytes == retained, "duplicate output charged twice")
	first := sec.seal(s.id, 1, s.now, deviceauth.TerminalOutput, []byte("prefix"))
	must(send(s, "relay", first, false))
	check(s.outputBytes == retained+len(first.raw), "output prefix charge")
	s.render()
	check(s.outputBytes == retained, "render did not release exact encoded size")
	s.render()
	check(s.outputBytes == 0 && len(s.outputReceivedAt) == 0, "render byte leak")
	must(send(s, "relay", sec.seal(s.id, 3, s.now, deviceauth.TerminalOutput, bytes.Repeat([]byte{'z'}, 512<<10)), false))
	s.render()
	check(s.outputBytes == 0, "output budget did not recover")
	results["r2_output_encoded_byte_budget"] = map[string]int{"one_512k_payload_encoded_bytes": retained, "after_render": s.outputBytes}

	s = newStream(sec, "stream-r2-output-002")
	second := sec.seal(s.id, 2, s.now, deviceauth.TerminalOutput, []byte("gap"))
	must(send(s, "relay", second, false))
	check(s.outputGapDeadline == base+10000 && s.gap == 0, "output timer not independent")
	s.advance(6000)
	must(send(s, "direct", second, false))
	must(send(s, "relay", sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("input continues")), false))
	check(s.outputGapDeadline == base+10000 && s.gap == 0 && s.inputSuccess == 1, "input/duplicate reset output deadline")
	s.advance(9999)
	check(!s.networkRecovery, "early output recovery")
	s.advance(10000)
	check(s.networkRecovery && s.failed == 0 && s.rendered == 0 && s.inputSuccess == 1, "output gap not network recovery")
	results["r2_output_independent_ten_second_deadline"] = true

	s = newStream(sec, "stream-r2-output-003")
	must(send(s, "relay", sec.seal(s.id, 2, s.now, deviceauth.TerminalOutput, []byte("second")), false))
	s.advance(9000)
	must(send(s, "direct", sec.seal(s.id, 1, s.now, deviceauth.TerminalOutput, []byte("first")), false))
	check(s.outputGapDeadline == 0 && s.rendered == 0, "received prefix confused with slow renderer")
	s.advance(10000)
	check(!s.networkRecovery, "closed output gap timed out")
	s.render()
	s.render()
	check(s.outputBytes == 0 && s.rendered == 2, "resolved output not rendered")
	results["r2_output_gap_resolution_before_render"] = true

	for _, fill := range []bool{false, true} {
		id := "stream-r2-output-004"
		if fill {
			id = "stream-r2-output-005"
		}
		s = newStream(sec, id)
		must(send(s, "relay", sec.seal(s.id, 2, s.now, deviceauth.TerminalOutput, []byte("second")), false))
		s.advance(2000)
		fourth := sec.seal(s.id, 4, s.now, deviceauth.TerminalOutput, []byte("fourth"))
		must(send(s, "direct", fourth, false))
		s.advance(9000)
		must(send(s, "relay", sec.seal(s.id, 1, s.now, deviceauth.TerminalOutput, []byte("first")), false))
		check(s.outputGapDeadline == base+12000, "successive output gap inherited/reset deadline")
		s.advance(10000)
		must(send(s, "relay", fourth, false))
		check(!s.networkRecovery && s.outputGapDeadline == base+12000, "output duplicate refreshed deadline")
		s.advance(11999)
		check(!s.networkRecovery, "successive output gap early recovery")
		if fill {
			must(send(s, "direct", sec.seal(s.id, 3, s.now, deviceauth.TerminalOutput, []byte("third")), false))
			for range 4 {
				s.render()
			}
			check(s.outputGapDeadline == 0 && s.outputBytes == 0, "successive gap did not drain")
		}
		s.advance(12000)
		check(s.networkRecovery != fill && s.failed == 0, "successive output gap recovery boundary")
	}
	results["r2_successive_output_gaps_keep_own_deadlines"] = true
}
