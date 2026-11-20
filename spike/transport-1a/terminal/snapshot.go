package main

import (
	"bytes"
	"example.com/transport-sdk-lab/labwire"
	"fmt"

	"github.com/Lewin671/remote-agent/shared/devicesec"
)

// Shared expected.json records the observed page sequence, not only a bool.
func snapshotCases(results map[string]any) {
	var observedOutputs []uint64
	for _, final := range []bool{false, true} {
		sec := security()
		s := newStream(sec, "snapshot-duplex-01")
		for seq := uint64(1); seq <= 2; seq++ {
			must(send(s, "relay", sec.seal(s.id, seq, s.now, devicesec.TerminalInput, []byte("once")), false))
		}
		must(send(s, "direct", sec.seal(s.id, 1, s.now, devicesec.TerminalOutput, []byte("render later")), false))
		frozenOutput := uint64(0)
		if final {
			s.render()
			frozenOutput = 1
		}
		input := watermark(s.status(final))
		frozenFlow := labwire.PB(labwire.B(1, input), labwire.B(2, watermark(Status{Processed: frozenOutput})))
		expectedToken := labwire.Hash(frozenFlow)
		var tokens [][]byte
		var outputs []uint64
		var barrier []byte
		s.replyObserver = func(typ uint64, raw []byte) {
			name := "InputStatusReply"
			if typ == 12 {
				name = "BarrierReply"
			}
			f, e := labwire.ParseMessage(raw, name)
			must(e)
			if typ == 12 {
				barrier = append([]byte(nil), f[3].B...)
				return
			}
			flow, e := labwire.ParseMessage(f[5].B, "FlowState")
			must(e)
			tokens = append(tokens, append([]byte(nil), f[4].B...))
			outputs = append(outputs, parseWatermark(flow[2].B).Processed)
		}
		// Advance output after page 1 has been constructed, before it is sealed.
		first := true
		s.replyHook = func() {
			if first && !final {
				first = false
				s.render()
			}
		}
		var result Status
		s.secureStatus("relay", final, func(st Status) { result = st })
		check(result.Available && result.Processed == 2 && s.rendered == 1, fmt.Sprintf("snapshot execution: final=%v available=%v processed=%d rendered=%d recovery=%v", final, result.Available, result.Processed, s.rendered, s.networkRecovery))
		check(len(outputs) == 2 && outputs[0] == frozenOutput && outputs[1] == frozenOutput && bytes.Equal(tokens[0], expectedToken) && bytes.Equal(tokens[1], expectedToken), "snapshot pages mixed directions")
		if final {
			check(bytes.Equal(barrier, frozenFlow), "barrier recaptured output")
		} else {
			observedOutputs = outputs
			// With unchanged input, a later output watermark changes the full token.
			s.replyHook = nil
			s.secureStatus("relay", false, func(st Status) { result = st })
			nextToken := labwire.Hash(labwire.PB(labwire.B(1, input), labwire.B(2, watermark(Status{Processed: 1}))))
			check(len(tokens) == 4 && !bytes.Equal(nextToken, expectedToken) && bytes.Equal(tokens[2], nextToken) && bytes.Equal(tokens[3], nextToken) && outputs[2] == 1 && outputs[3] == 1, "output absent from snapshot token")
		}
	}
	results["frozen_bidirectional_status_snapshot"] = map[string]any{"input_processed": 2, "page_output_watermarks": observedOutputs, "later_output_watermark": 1, "output_changes_token": true, "barrier_output_watermark": 1}
}
