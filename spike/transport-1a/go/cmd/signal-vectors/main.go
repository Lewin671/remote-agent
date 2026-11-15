package main

import (
	"encoding/hex"
	"example.com/transport-sdk-lab/labwire"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func n(s string) uint64   { v, e := strconv.ParseUint(s, 10, 64); must(e); return v }
func raw(s string) []byte { v, e := hex.DecodeString(s); must(e); return v }
func main() {
	if !strings.HasPrefix(os.Getenv("REMOTE_AGENT_HOME"), "/tmp/") {
		panic("isolation")
	}
	data, e := os.ReadFile(os.Args[1])
	must(e)
	var gates [2]*labwire.Signal
	cases, assertions := 0, 0
	for _, row := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		p := strings.Split(row, "\t")
		switch p[0] {
		case "CASE":
			a, b := labwire.Fixture(p[2], n(p[3])), labwire.Fixture(p[4], n(p[5]))
			conn := labwire.Hash([]byte("signal/" + p[1]))[:16]
			gates = [2]*labwire.Signal{labwire.NewSignal(a, b.Public(), conn, 1), labwire.NewSignal(b, a.Public(), conn, 1)}
			cases++
		case "CHALLENGE":
			gates[n(p[1])].ChallengeHash = raw(p[2])
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
	fmt.Printf("Go signal scenarios: %d, assertions: %d\n", cases, assertions)
}
