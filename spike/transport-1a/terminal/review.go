package main

import (
	"bytes"
	"fmt"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

// These named cases join the language-independent expected.json contract.
// Envelopes are real authenticated business frames; paths and renderer are fake.
func reviewCases(sec *Security, results map[string]any) {
	s := newStream(sec, "stream-review-001")
	bad := Fragment{id: "tiny-declaration", hash: "opaque", count: 128, total: 1, pathGen: 1, body: bytes.Repeat([]byte{1}, 16<<10)}
	check(s.receive("relay", s.instance, bad) != nil && len(s.assemblies) == 0 && s.assemblyBytes == 0, "oversize first fragment retained")
	first := Fragment{id: "cumulative", hash: "opaque", count: 3, total: 8, pathGen: 1, body: []byte{1, 2, 3, 4}}
	must(s.receive("relay", s.instance, first))
	first.body[0] = 99
	key := "relay/1/cumulative"
	check(s.assemblies[key].parts[0][0] == 1, "retained caller backing array")
	excessive := first
	excessive.index = 1
	excessive.body = bytes.Repeat([]byte{2}, 5)
	check(s.receive("relay", s.instance, excessive) != nil && s.assemblyBytes == 4 && s.assemblies[key].retained == 4 && len(s.assemblies[key].parts) == 1, "cumulative total overflow retained")
	duplicate := first
	duplicate.body = []byte{1, 2, 3, 4}
	must(s.receive("relay", s.instance, duplicate))
	check(s.assemblyBytes == 4, "duplicate fragment charged twice")
	s.advance(5000)
	check(s.assemblyBytes == 0 && len(s.assemblies) == 0, "actual byte expiry accounting")
	results["review_fragment_declared_total_and_ownership"] = true

	s = newStream(sec, "stream-review-002")
	for assembly := 0; assembly < 2; assembly++ {
		for index := 0; index < 64; index++ {
			f := Fragment{id: fmt.Sprintf("full-%d", assembly), hash: "opaque", index: index, count: 128, total: 1 << 20, pathGen: 1, body: bytes.Repeat([]byte{3}, 16<<10)}
			must(s.receive("relay", s.instance, f))
		}
	}
	check(s.assemblyBytes == 2<<20 && len(s.assemblies) == 2, "actual peer budget not charged")
	extra := Fragment{id: "third", hash: "opaque", count: 2, total: 2, pathGen: 1, body: []byte{4}}
	check(s.receive("direct", s.instance, extra) != nil && s.assemblyBytes == 2<<20 && len(s.assemblies) == 2, "peer retained budget bypass")
	more := Fragment{id: "full-0", hash: "opaque", index: 64, count: 128, total: 1 << 20, pathGen: 1, body: []byte{4}}
	check(s.receive("relay", s.instance, more) != nil && s.assemblyBytes == 2<<20, "completed declared total bypass")
	s.advance(5000)
	check(s.assemblyBytes == 0 && len(s.assemblies) == 0, "full peer budget not released")
	results["review_fragment_actual_peer_budget"] = map[string]int{"peak_retained_bytes": 2 << 20, "after_expiry": s.assemblyBytes}

	s = newStream(sec, "stream-review-003")
	second := sec.seal(s.id, 2, s.now, deviceauth.TerminalOutput, []byte("second"))
	conflict := sec.seal(s.id, 2, s.now, deviceauth.TerminalOutput, []byte("changed"))
	must(send(s, "direct", second, false))
	check(send(s, "relay", conflict, false) != nil && s.rendered == 0 && s.dirtyACK == 0, "pending output conflict accepted")
	same := sec.seal(s.id, 2, s.now, deviceauth.TerminalOutput, []byte("second"))
	check(same.identity == second.identity && !bytes.Equal(same.raw, second.raw), "output fixture not reencrypted")
	must(send(s, "relay", same, false))
	check(len(s.output) == 1, "pending output repeated")
	firstOutput := sec.seal(s.id, 1, s.now, deviceauth.TerminalOutput, []byte("first"))
	must(send(s, "relay", firstOutput, false))
	s.render()
	s.render()
	check(s.rendered == 2 && len(s.output) == 0 && len(s.renderedIdentity) == 2, "render identity lost")
	dirty := s.dirtyACK
	check(send(s, "relay", conflict, false) != nil && s.dirtyACK == dirty && s.rendered == 2, "rendered output conflict ACKed")
	must(send(s, "direct", same, false))
	check(s.rendered == 2 && s.renderReady == nil, "duplicate output rerendered")
	s.rememberOutput(firstOutput)
	s.rememberOutput(second)
	s.advance(30000)
	check(len(s.replay) == 0 && len(s.renderedIdentity) == 2, "output replay evicted conflict identities")
	check(send(s, "direct", conflict, false) != nil, "conflict accepted after replay eviction")
	must(send(s, "relay", same, false))
	check(len(s.details) == 0 && s.processed == 0 && s.failed == 0, "output evidence changed input state")
	results["review_output_conflicts_before_after_render_and_replay"] = true

	s = newStream(sec, "stream-review-004")
	for seq := uint64(1); seq <= 512; seq++ {
		must(send(s, "relay", sec.seal(s.id, seq, s.now, deviceauth.TerminalOutput, []byte{1}), false))
		s.render()
	}
	overflow := sec.seal(s.id, 513, s.now, deviceauth.TerminalOutput, []byte{2})
	check(send(s, "direct", overflow, false) != nil && s.rendered == 512 && len(s.renderedIdentity) == 512, "output identity budget silently evicted")
	s.advance(60000)
	check(len(s.renderedIdentity) == 512, "identity expired before original deadline")
	s.advance(60001)
	check(len(s.renderedIdentity) == 0, "expired identity leak")
	late := sec.seal(s.id, 1, s.now, deviceauth.TerminalOutput, []byte("new authorization for old sequence"))
	dirty = s.dirtyACK
	check(send(s, "relay", late, false) != nil && s.dirtyACK == dirty, "unknown old output ACKed after identity expiry")
	must(send(s, "relay", sec.seal(s.id, 513, s.now, deviceauth.TerminalOutput, []byte{3}), false))
	s.render()
	check(s.rendered == 513 && len(s.renderedIdentity) == 1, "output identity capacity not recovered")
	results["review_output_identity_retention_and_backpressure"] = true
}
