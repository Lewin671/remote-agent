package peerwire

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func n(s string) uint64   { v, e := strconv.ParseUint(s, 10, 64); must(e); return v }
func raw(s string) []byte { v, e := hex.DecodeString(s); must(e); return v }
func TestFrozenSignalVectors(t *testing.T) {
	codec := fixtureCodec()
	data, e := os.ReadFile("../../testdata/transport/signal/scenarios.tsv")
	must(e)
	var gates [2]*Signal
	var ids [2]identity
	var batch *CandidateBatcher
	var sender *Sender
	var queuedRaw []byte
	cases, assertions := 0, 0
	for _, row := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		p := strings.Split(row, "\t")
		switch p[0] {
		case "BODY":
			_, e := KnownPayload(n(p[1]), n(p[2]), raw(p[3]))
			if (e == nil) != (p[4] == "1") {
				panic("body " + p[1] + "/" + p[2])
			}
			assertions++
		case "SCHEMA":
			v, e := NormalizeMessage(raw(p[2]), p[1])
			got := hex.EncodeToString(v)
			if e != nil {
				got = "!"
			}
			if got != p[3] {
				panic("schema " + p[1])
			}
			assertions++
		case "AUTH":
			gates[n(p[1])].SetAuthorization(1700000000000+n(p[2]), p[3] == "1", n(p[4]))
		case "SEND_AUTH":
			sender.SetAuthorization(n(p[1]), p[2] == "1", n(p[3]))
		case "REPLY":
			if gates[n(p[1])].ReplyAllowed(raw(p[4]), 1700000000000+n(p[2])) != (p[3] == "1") {
				panic("reply auth")
			}
			assertions++
		case "CASE":
			a, b := fixture(p[2], n(p[3])), fixture(p[4], n(p[5]))
			conn := Hash([]byte("signal/" + p[1]))[:16]
			ids = [2]identity{a, b}
			gates = [2]*Signal{newTestSignal(codec, a, b.Public, conn, n(p[6]), 1700000000000), newTestSignal(codec, b, a.Public, conn, n(p[6]), 1700000000000)}
			cases++
		case "BATCH_CASE":
			batch = &CandidateBatcher{Generation: n(p[2]), Deadline: n(p[3]), Binding: n(p[4])}
			cases++
		case "BATCH_ADD":
			if batch.Add(raw(p[3]), n(p[2]), n(p[1]), 1) != (p[4] == "1") {
				panic("batch add")
			}
			assertions++
		case "BATCH_FLUSH":
			groups := batch.Flush(n(p[1]), p[2] == "1", 2, 1)
			var sizes []string
			var flat []byte
			for _, g := range groups {
				sizes = append(sizes, strconv.Itoa(len(g)))
				for _, r := range g {
					flat = append(flat, r...)
				}
			}
			if strings.Join(sizes, ",") != p[3] || hex.EncodeToString(flat) != p[4] {
				panic("batch flush")
			}
			assertions++
		case "DISPATCH":
			if gates[n(p[1])].Dispatch(raw(p[4]), 1700000000000+n(p[2])) != (p[3] == "1") {
				panic("dispatch")
			}
			assertions++
		case "FORGET":
			gates[n(p[1])].ForgetPeer()
		case "QUEUE":
			queuedRaw = raw(p[2])
			_, e := codec.open(queuedRaw, gates[n(p[1])].Peer, ids[n(p[1])], 1700000000000)
			must(e)
		case "DELIVER":
			_, e := gates[n(p[1])].Receive(queuedRaw, 1700000000000+n(p[2]))
			if (e == nil) != (p[3] == "1") {
				panic("queued delivery")
			}
			assertions++
		case "SENDER_CASE":
			sender = NewSender(n(p[2]))
			cases++
		case "SEND_ADD":
			if sender.Enqueue(raw(p[4]), n(p[1]), n(p[2]), n(p[3])) != (p[5] == "1") {
				panic("send add")
			}
			assertions++
		case "SEND_TICK":
			if len(sender.Tick(n(p[1]))) != int(n(p[2])) {
				panic("send tick")
			}
			assertions++
		case "SEND_REBIND":
			sender.Rebind(n(p[1]))
		case "SEND_LIMIT":
			sender.Limited(Hash(raw(p[3])), n(p[1]), n(p[2]))
		case "SEND_ADMIT":
			if sender.Admit(raw(p[2]), n(p[1])) != (p[3] == "1") {
				panic("send admit")
			}
			assertions++
		case "SEND_PENDING":
			if len(sender.Pending) != int(n(p[1])) {
				panic("pending")
			}
			assertions++
		case "BATCH_BOUND_FLUSH":
			if len(batch.Flush(n(p[1]), p[2] == "1", n(p[3]), n(p[4]))) != 0 || len(batch.Pending) != 0 {
				panic("stale batch")
			}
			assertions++
		case "POLICY":
			a, z := ids[0], ids[1]
			request, e := codec.open(raw(p[3]), a.Public, z, 1700000000000)
			must(e)
			reply, e := codec.open(raw(p[4]), z.Public, a, 1700000000000)
			must(e)
			if (ValidatePolicyReply(request, reply, 1700000000000+n(p[5])) == nil) != (p[2] == "1") {
				panic(p[1])
			}
			assertions++
		case "RECORD":
			which := n(p[2])
			gates[which].Tick(1700000000000 + n(p[3]))
			sent, e := codec.open(raw(p[4]), gates[which].Self, ids[1-which], 1700000000000)
			must(e)
			if p[1] == "probe" {
				e = gates[which].RememberProbe(sent)
			} else {
				e = gates[which].RememberChallenge(sent)
			}
			if (e == nil) != (p[5] == "1") {
				panic("record")
			}
			assertions++
		case "PROBE", "CHALLENGE":
			which := n(p[1])
			sent, e := codec.open(raw(p[2]), gates[which].Self, ids[1-which], 1700000000000)
			must(e)
			if p[0] == "PROBE" {
				must(gates[which].RememberProbe(sent))
			} else {
				must(gates[which].RememberChallenge(sent))
			}
		case "RX":
			_, e := gates[n(p[1])].Receive(raw(p[4]), 1700000000000+n(p[2]))
			if (e == nil) != (p[3] == "1") {
				panic(fmt.Sprintf("RX %d: %v", cases, e))
			}
			assertions++
		case "LOCAL":
			e := gates[n(p[1])].SetLocal(raw(p[5]), p[3] == "1", n(p[2]))
			if (e == nil) != (p[4] == "1") {
				panic(fmt.Sprintf("LOCAL %d: %v", cases, e))
			}
			assertions++
		case "DTLS":
			gates[n(p[1])].DTLS = true
		case "NEXT":
			must(gates[n(p[1])].NextGeneration(n(p[2])))
		case "TICK":
			gates[n(p[1])].Tick(1700000000000 + n(p[2]))
		case "EXPECT":
			s := gates[n(p[1])]
			v := map[string]uint64{"sdk": uint64(s.SDKCalls), "duplicates": uint64(s.Duplicates), "conflicts": uint64(s.Conflicts), "unsupported": uint64(s.Unsupported), "pending": uint64(len(s.Pending)), "generation": s.Generation, "next": s.Next}
			v["seen"] = uint64(len(s.Seen))
			v["dispatches"] = uint64(s.Dispatches)
			if len(s.Peer.Boot) == 32 {
				v["peer_known"] = 1
			}
			v["stale"] = uint64(s.StaleChallenges)
			if s.BootReady {
				v["boot"] = 1
			}
			if s.GapDeadline != 0 {
				v["gap_ms"] = s.GapDeadline - 1700000000000
			}
			if s.Ready() {
				v["ready"] = 1
			}
			if s.Closed {
				v["closed"] = 1
			}
			if v[p[2]] != n(p[3]) {
				panic(fmt.Sprintf("%s case %d: got %d", p[2], cases, v[p[2]]))
			}
			assertions++
		default:
			panic(p[0])
		}
	}
	t.Logf("frozen signal scenarios: %d, assertions: %d", cases, assertions)
}
