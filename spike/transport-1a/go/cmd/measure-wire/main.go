package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"example.com/transport-sdk-lab/labwire"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}

type metrics struct{ InitialFrames, InitialBytes, RetryFrames, RetryBytes, AckFrames, AckBytes, MaxFrame, MaxAck int }

func main() {
	if !strings.HasPrefix(os.Getenv("REMOTE_AGENT_HOME"), "/tmp/") {
		panic("isolation")
	}
	root := os.Args[1]
	results := map[string]any{}
	now := uint64(1700000000000)
	var corpus *os.File
	if len(os.Args) > 2 {
		var e error
		corpus, e = os.Create(os.Args[2])
		must(e)
		defer corpus.Close()
	}
	for _, sdk := range []string{"pion", "chrome", "webkit", "android", "ios"} {
		started := time.Now()
		stats := map[string]*metrics{"client": {}, "daemon": {}}
		for peer := 0; peer < 8; peer++ {
			client := labwire.Fixture(fmt.Sprintf("client-%02d", peer), 1)
			daemon := labwire.Fixture("daemon-lab", 2)
			for use := uint64(1); use <= 2; use++ {
				conn := labwire.Hash([]byte(fmt.Sprintf("capacity/%d/%d", peer, use)))[:16]
				var local [2]labwire.SDPInfo
				var hashes [2][]byte
				ids := []labwire.Identity{client, daemon}
				for i, id := range ids {
					kind := "answer"
					if bytes.Compare(id.ID, ids[1-i].ID) < 0 {
						kind = "offer"
					}
					name := sdk
					if i == 1 {
						name = "pion"
					}
					raw, e := os.ReadFile(filepath.Join(root, name+"-"+kind+".sdp"))
					must(e)
					local[i], e = labwire.InspectSDP(raw)
					must(e)
					hashes[i] = labwire.Hash(raw)
				}
				for side, from := range ids {
					to := ids[1-side]
					name := []string{"client", "daemon"}[side]
					m := stats[name]
					ackSeq := uint64(0)
					emit := func(typ, seq, generation uint64, payload []byte, boot bool) {
						ctx := labwire.NewContext(from, to.Public(), conn, use, generation, seq, now, 1, boot)
						packet, e := labwire.Seal(from, to.Public(), ctx, typ, payload, nil, nil, nil)
						must(e)
						opened, e := labwire.Open(packet, from.Public(), to, now)
						must(e)
						if !bytes.Equal(opened.Payload, payload) {
							panic("wire round trip")
						}
						if corpus != nil && sdk == "android" {
							label := fmt.Sprintf("client-%02d", peer)
							other := "daemon-lab"
							if side == 1 {
								label, other = other, label
							}
							fmt.Fprintf(corpus, "%s\t%d\t%s\t%d\t%s\n", label, from.Role, other, to.Role, hex.EncodeToString(packet))
						}
						m.InitialFrames++
						m.InitialBytes += len(packet)
						m.MaxFrame = max(m.MaxFrame, len(packet))
						frozen := append([]byte(nil), packet...)
						// Capacity stress: all three copies are actual frozen encrypted frames.
						for attempt := 0; attempt < 3; attempt++ {
							if !bytes.Equal(packet, frozen) {
								panic("changed retry")
							}
							m.RetryFrames++
							m.RetryBytes += len(packet)
							ackSeq++
							ackCtx := labwire.NewContext(to, from.Public(), conn, use, max(uint64(1), generation), ackSeq, now, 2, false)
							ackBody := labwire.PB(labwire.U(4, 1), labwire.U(5, seq), labwire.B(6, labwire.Hash(packet)), labwire.U(7, generation), labwire.U(8, use))
							ack, e := labwire.Seal(to, from.Public(), ackCtx, 4, ackBody, nil, nil, nil)
							must(e)
							_, e = labwire.Open(ack, to.Public(), from, now)
							must(e)
							m.AckFrames++
							m.AckBytes += len(ack)
							m.MaxAck = max(m.MaxAck, len(ack))
						}
					}
					if side == 0 {
						emit(1, 1, 0, labwire.PB(labwire.B(1, conn), labwire.B(2, from.Instance), labwire.B(3, from.Boot)), true)
					} else {
						emit(2, 1, 0, labwire.PB(labwire.B(1, conn), labwire.B(2, from.Instance), labwire.B(3, from.Boot), labwire.B(4, to.Boot)), true)
					}
					typ := uint64(4)
					if bytes.Compare(from.ID, to.ID) < 0 {
						typ = 3
					}
					emit(typ, 1, 1, labwire.SDPBody(local[side].Raw), false)
					for batch := 0; batch < 4; batch++ {
						var cs []labwire.Candidate
						for n := 0; n < 16; n++ {
							c := labwire.Candidate{Original: []byte(fmt.Sprintf("candidate:%d 1 UDP 2122260223 192.0.2.1 5000 typ host ufrag %s x-lab %s", batch*16+n+1, local[side].Ufrag, strings.Repeat("x", 800))), Ufrag: local[side].Ufrag, Mid: local[side].Mid, Generation: 1}
							c.Original = append(c.Original, bytes.Repeat([]byte{'x'}, 1024-len(c.Encode()))...)
							if len(c.Encode()) != 1024 {
								panic("candidate size")
							}
							cs = append(cs, c)
						}
						body := labwire.CandidateBody(cs, uint64(batch))
						_, _, e := labwire.ReadCandidates(body)
						must(e)
						emit(5, uint64(batch+2), 1, body, false)
					}
					emit(6, 6, 1, labwire.EndBody(local[side].Ufrag, 64, 4), false)
					offerSide := 0
					if bytes.Compare(client.ID, daemon.ID) > 0 {
						offerSide = 1
					}
					answerSide := 1 - offerSide
					emit(7, 7, 1, labwire.PB(labwire.B(1, hashes[offerSide]), labwire.B(2, hashes[answerSide]), labwire.B(3, local[offerSide].Fingerprint), labwire.B(4, local[answerSide].Fingerprint)), false)
				}
			}
		}
		for _, m := range stats {
			if m.InitialFrames != 128 || m.InitialBytes > 3<<20 || m.RetryFrames != 384 || m.RetryBytes > 9<<20 || m.MaxAck > 1024 || m.AckBytes > 384<<10 {
				panic("wire budget")
			}
		}
		results[sdk] = map[string]any{"directions": stats, "codec_wall_ms": float64(time.Since(started).Microseconds()) / 1000}
	}
	encoded, e := json.MarshalIndent(map[string]any{"evidence": "real protobuf framing, X25519/HKDF/AES-GCM/Ed25519; synthetic maximum candidate capacity, not a success timing claim", "results": results}, "", "  ")
	must(e)
	fmt.Println(string(encoded))
}
