package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"example.com/transport-sdk-lab/labwire"
	"fmt"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/encoding/protowire"
)

// Encode one immutable status page at a time. Every page carries the same
// authenticated snapshot token; queries cannot assemble advancing watermarks.
func watermark(st Status) []byte {
	out := labwire.PB(labwire.U(1, st.Processed), labwire.U(2, st.InputSuccess), labwire.U(3, st.Failed))
	codes := map[string]uint64{"applied": 1, "non_input_failed": 2, "partial_or_unknown": 3, "blocked": 4, "not_applied": 5, "expired_not_applied": 6}
	for _, d := range st.Details {
		h, e := hex.DecodeString(d.Hash)
		must(e)
		detail := labwire.PB(labwire.U(1, d.Seq), labwire.B(2, h), labwire.U(3, codes[d.Outcome]), labwire.U(4, uint64(d.Known)), labwire.U(5, uint64(d.Possible)), labwire.U(6, uint64(d.Total)), labwire.U(7, 0))
		out = append(out, labwire.PB(labwire.B(4, detail))...)
	}
	return out
}
func parseWatermark(raw []byte) Status {
	var st Status
	st.Available = true
	last := protowire.Number(0)
	for len(raw) > 0 {
		n, t, k := protowire.ConsumeTag(raw)
		check(k > 0 && n >= last && (n != last || n == 4), "watermark ordering")
		last = n
		raw = raw[k:]
		if n <= 3 {
			v, k := protowire.ConsumeVarint(raw)
			check(k > 0 && t == protowire.VarintType, "watermark integer")
			raw = raw[k:]
			switch n {
			case 1:
				st.Processed = v
			case 2:
				st.InputSuccess = v
			case 3:
				st.Failed = v
			}
			continue
		}
		check(n == 4 && t == protowire.BytesType && len(st.Details) < 128, "detail bound")
		b, k := protowire.ConsumeBytes(raw)
		check(k > 0, "detail size")
		raw = raw[k:]
		f, e := labwire.Parse(b, 7)
		must(e)
		check(len(f) == 7 && len(f[2].B) == 32 && f[3].U >= 1 && f[3].U <= 6, "detail fields")
		names := []string{"", "applied", "non_input_failed", "partial_or_unknown", "blocked", "not_applied", "expired_not_applied"}
		st.Details = append(st.Details, Detail{Seq: f[1].U, Hash: hex.EncodeToString(f[2].B), Outcome: names[f[3].U], Known: int(f[4].U), Possible: int(f[5].U), Total: int(f[6].U)})
	}
	return st
}
func (s *Stream) reply(typ uint64, body []byte) []byte {
	s.controlSeq++
	conn := labwire.Hash([]byte(s.id))[:16]
	ctx := labwire.NewContext(s.sec.machineWire, s.sec.clientWire.Public(), conn, 0, 1, s.controlSeq, s.now, 2, false)
	packet, e := labwire.Seal(s.sec.machineWire, s.sec.clientWire.Public(), ctx, typ, body, nil, nil, nil)
	must(e)
	check(len(packet) <= 16384, "control budget")
	s.controlPackets++
	s.controlBytes += len(packet)
	opened, e := labwire.Open(packet, s.sec.machineWire.Public(), s.sec.clientWire, s.now)
	must(e)
	check(opened.Type == typ && bytes.Equal(opened.Context.Connection, conn), "reply binding")
	return opened.Payload
}
func (s *Stream) secureStatus(path string, final bool, reply func(Status)) {
	id := labwire.Hash([]byte(fmt.Sprintf("query/%s/%d", s.id, s.controlSeq)))[:16]
	typ := uint64(3)
	body := labwire.PB(labwire.B(1, []byte(s.id)), labwire.B(2, id), labwire.U(3, 1), labwire.U(4, 128), labwire.B(5, nil))
	if final {
		typ = 6
		body = labwire.PB(labwire.B(1, []byte(s.id)), labwire.B(2, id), labwire.U(4, 1))
	}
	must(s.control(path, typ, body, func(raw []byte) error {
		f, e := labwire.Parse(raw, 5)
		if e != nil || !bytes.Equal(f[1].B, []byte(s.id)) || !bytes.Equal(f[2].B, id) {
			return errors.New("query binding")
		}
		deliver := func(st Status) {
			full := watermark(st)
			token := labwire.Hash(full)
			result := Status{Available: st.Available, Final: st.Final, Processed: st.Processed, InputSuccess: st.InputSuccess, Failed: st.Failed}
			pages := max(1, len(st.Details))
			for n := 0; n < pages; n++ {
				page := st
				page.Details = nil
				if len(st.Details) > 0 {
					page.Details = []Detail{st.Details[n]}
				}
				flow := labwire.PB(labwire.B(1, watermark(page)), labwire.B(2, watermark(Status{Processed: s.rendered})))
				response := labwire.PB(labwire.B(1, []byte(s.id)), labwire.B(2, id), labwire.U(3, 1), labwire.B(4, token), labwire.B(5, flow), labwire.U(6, uint64(n+1)), labwire.U(7, uint64(boolInt(n == pages-1))))
				raw := s.reply(11, response)
				r, e := labwire.Parse(raw, 7)
				must(e)
				check(bytes.Equal(r[1].B, []byte(s.id)) && bytes.Equal(r[2].B, id) && bytes.Equal(r[4].B, token), "snapshot binding")
				flows, e := labwire.Parse(r[5].B, 2)
				must(e)
				decoded := parseWatermark(flows[1].B)
				result.Details = append(result.Details, decoded.Details...)
			}
			if final {
				raw := s.reply(12, labwire.PB(labwire.B(1, []byte(s.id)), labwire.B(2, id), labwire.B(3, labwire.PB(labwire.B(1, full))), labwire.U(4, 1), labwire.U(5, 1)))
				f, e := labwire.Parse(raw, 5)
				must(e)
				check(f[4].U == 1 && s.stopped, "nonfinal barrier")
			}
			reply(result)
		}
		if final {
			if s.inflight != nil {
				s.barrierWaiting = true
				s.barrierReply = deliver
			} else {
				deliver(s.barrier())
			}
		} else {
			s.query(deliver)
		}
		return nil
	}))
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

type registry struct{ active, retired map[string]*Stream }

func (r *registry) add(s *Stream) error {
	if len(r.active) >= 16 {
		return errors.New("active budget")
	}
	r.active[s.id] = s
	return nil
}
func (r *registry) retire(s *Stream) error {
	if len(r.retired) >= 16 {
		return errors.New("retired budget")
	}
	check(s.stopped, "retire without barrier")
	delete(r.active, s.id)
	r.retired[s.id] = s
	return nil
}
func (r *registry) prune(now uint64) {
	for id, s := range r.retired {
		if now-s.retiredAt >= 30000 {
			delete(r.retired, id)
		}
	}
}

func recoveryCases(sec *Security, results map[string]any) {
	s := newStream(sec, "stream-recovery-01")
	first := sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("applied"))
	second := sec.seal(s.id, 2, s.now, deviceauth.TerminalInput, []byte("lost before admission"))
	must(s.cacheInput(first))
	must(s.cacheInput(second))
	must(send(s, "direct", first, false))
	var st Status
	s.secureStatus("relay", false, func(x Status) { st = x })
	s.acknowledge(st)
	check(len(s.inputCache) == 1 && s.inputCache[2].identity == second.identity, "status retransmitted applied input")
	must(s.attach("relay", s.instance, 2))
	must(send(s, "relay", s.inputCache[2], false))
	s.secureStatus("relay", false, func(x Status) { st = x })
	s.acknowledge(st)
	check(len(s.inputCache) == 0 && s.injectedChunks == 2 && s.openCalls == 1, "old stream recovery")
	results["status_reply_and_old_stream_resend"] = map[string]int{"control_packets": s.controlPackets, "control_bytes": s.controlBytes}
	s = newStream(sec, "stream-recovery-02")
	s.hold = true
	one := sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("inflight"))
	two := sec.seal(s.id, 2, s.now, deviceauth.TerminalInput, []byte("never sent"))
	must(send(s, "direct", one, false))
	var final *Status
	s.secureStatus("relay", true, func(x Status) { final = &x })
	check(final == nil, "barrier reply overtook execution")
	s.release()
	check(final != nil && final.Final && final.Processed == 1 && s.stopped, "barrier final reply")
	check(send(s, "relay", two, false) != nil, "old authorized late copy admitted")
	fresh := newStream(sec, "stream-recovery-03")
	reauthorized := sec.seal(fresh.id, 1, fresh.now, deviceauth.TerminalInput, two.plainInput())
	must(send(fresh, "relay", reauthorized, false))
	check(fresh.injectedChunks == 1 && s.injectedChunks == 1, "new stream duplicated known input")
	results["final_barrier_before_new_authorization"] = true
	s = newStream(sec, "stream-recovery-04")
	for n := uint64(1); n <= 17; n++ {
		must(send(s, "direct", sec.seal(s.id, n, s.now, deviceauth.TerminalOutput, []byte("render")), false))
		s.render()
	}
	check(s.acks == 1, "16-frame ACK")
	s.advance(20)
	check(s.acks == 2, "20ms ACK")
	ack := s.reply(4, labwire.PB(labwire.B(1, []byte(s.id)), labwire.U(2, 2), labwire.B(3, watermark(Status{Processed: s.rendered}))))
	f, e := labwire.Parse(ack, 3)
	must(e)
	check(parseWatermark(f[3].B).Processed == 17, "authenticated rendered watermark")
	results["authenticated_ack_batching"] = true
	r := registry{map[string]*Stream{}, map[string]*Stream{}}
	for n := 0; n < 16; n++ {
		must(r.add(newStream(sec, fmt.Sprintf("stream-registry-%02d", n))))
	}
	check(r.add(newStream(sec, "stream-registry-full")) != nil, "active overflow")
	for _, s := range r.active {
		s.barrier()
		must(r.retire(s))
	}
	extra := newStream(sec, "stream-registry-extra")
	extra.barrier()
	check(r.retire(extra) != nil, "retired overflow")
	r.prune(base + 29999)
	check(len(r.retired) == 16, "early status eviction")
	r.prune(base + 30000)
	check(len(r.retired) == 0, "retired leak")
	results["active_retired_query_budgets"] = true
}
func (e Envelope) plainInput() []byte { // The original business bytes are retained, never guessed from display output.
	var value []byte
	for raw := e.plain; len(raw) > 0; {
		n, t, k := protowire.ConsumeTag(raw)
		check(k > 0, "terminal proto")
		raw = raw[k:]
		if n == 10 && t == protowire.BytesType {
			b, k := protowire.ConsumeBytes(raw)
			check(k > 0, "input bytes")
			value = append([]byte(nil), b...)
			break
		}
		k = protowire.ConsumeFieldValue(n, t, raw)
		check(k > 0, "terminal field")
		raw = raw[k:]
	}
	return value
}
