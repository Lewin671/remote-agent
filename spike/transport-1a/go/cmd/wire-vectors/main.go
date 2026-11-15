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
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func main() {
	if !strings.HasPrefix(os.Getenv("REMOTE_AGENT_HOME"), "/tmp/") {
		panic("isolation")
	}
	out := os.Args[1]
	must(os.MkdirAll(out, 0700))
	a := labwire.Fixture("c000000000000001", 1)
	b := labwire.Fixture("m000000000000001", 2)
	now := uint64(1700000000000)
	conn := labwire.Hash([]byte("lab/connection"))[:16]
	type Row struct {
		Name    string `json:"name"`
		Packet  string `json:"packet_hex"`
		Accept  bool   `json:"accept"`
		Type    uint64 `json:"type"`
		Payload string `json:"payload_hex"`
	}
	var rows []Row
	add := func(name string, ctx labwire.Context, typ uint64, payload []byte, accept bool) {
		id := labwire.Hash([]byte("frame/" + name))[:16]
		nc := labwire.Hash([]byte("context/" + name))[:12]
		nb := labwire.Hash([]byte("body/" + name))[:12]
		packet, e := labwire.Seal(a, b.Public(), ctx, typ, payload, id, nc, nb)
		must(e)
		opened, e := labwire.Open(packet, a.Public(), b, now)
		if (e == nil) != accept {
			panic(name)
		}
		if accept && (!bytes.Equal(opened.Payload, payload) || opened.Type != typ) {
			panic(name)
		}
		rows = append(rows, Row{name, hex.EncodeToString(packet), accept, typ, hex.EncodeToString(payload)})
	}
	c := labwire.NewContext(a, b.Public(), conn, 1, 1, 1, now, 1, false)
	add("offer", c, 3, []byte("raw SDP remains byte-exact\r\n"), true)
	add("authenticated-unknown-type", c, 31337, []byte{0, 255, 1}, true)
	ack := labwire.NewContext(a, b.Public(), conn, 1, 1, 1, now, 2, false)
	add("ack", ack, 4, labwire.PB(labwire.U(5, 1), labwire.B(6, labwire.Hash([]byte("original")))), true)
	boot := labwire.NewProbeContext(a, b.Public().Member(), conn, 1, now)
	add("probe", boot, 1, labwire.PB(labwire.B(1, conn), labwire.B(2, a.Instance), labwire.B(3, a.Boot)), true)
	badUnknown := boot
	badUnknown.RecipientUnknown = 0
	add("empty-recipient-without-marker", badUnknown, 1, nil, false)
	badUnknown = boot
	badUnknown.RecipientUnknown = 2
	add("invalid-unknown-marker", badUnknown, 1, nil, false)
	badUnknown = boot
	badUnknown.ReceiverInstance = b.Instance
	add("unknown-with-instance", badUnknown, 1, nil, false)
	badUnknown = boot
	badUnknown.ReceiverBoot = b.Boot
	add("unknown-with-nonce", badUnknown, 1, nil, false)
	add("unknown-challenge", boot, 2, nil, false)
	add("unknown-control", boot, 4, nil, false)
	badUnknown = boot
	badUnknown.SenderBoot = nil
	add("unknown-sender", badUnknown, 1, nil, false)
	exact := c
	exact.Deadline = now
	exact.Issued = now - 1
	add("deadline-boundary", exact, 3, nil, false)
	exact = c
	exact.Deadline = now + 1
	add("deadline-one-ms-left", exact, 3, nil, true)
	exact = c
	exact.Issued = now + 100
	exact.Deadline = now + 100
	add("zero-validity-interval", exact, 3, nil, false)
	exact = c
	exact.Domain = append([]byte{239, 187, 191}, []byte("signal")...)
	add("domain-bom", exact, 3, nil, false)
	bad := c
	bad.ReceiverBoot = labwire.Hash([]byte("stale receiver boot"))
	add("stale-boot", bad, 3, nil, false)
	bad = c
	bad.SenderInstance = labwire.Hash([]byte("stale sender"))[:16]
	add("stale-sender-instance", bad, 3, nil, false)
	bad = c
	bad.ReceiverInstance = labwire.Hash([]byte("stale receiver"))[:16]
	add("stale-receiver-instance", bad, 3, nil, false)
	bad = c
	bad.FromRole = 2
	add("wrong-role", bad, 3, nil, false)
	bad = c
	bad.Machine = []byte("wrong-machine-id")
	add("wrong-machine", bad, 3, nil, false)
	bad = c
	bad.Deadline = now - 1
	bad.Issued = now - 10000
	add("expired", bad, 3, nil, false)
	bad = c
	bad.Deadline = now + 30001
	add("overlong-deadline", bad, 3, nil, false)
	bad = c
	bad.Domain = []byte("other-purpose")
	add("wrong-purpose", bad, 3, nil, false)
	add("sdp-in-bootstrap", boot, 3, []byte("forbidden"), false)
	raw, e := hex.DecodeString(rows[0].Packet)
	must(e)
	mutated := append([]byte(nil), raw...)
	mutated[len(mutated)-1] ^= 1
	rows = append(rows, Row{"tampered-signature", hex.EncodeToString(mutated), false, 0, ""})
	rows = append(rows, Row{"duplicate-outer-field", hex.EncodeToString(append(append([]byte(nil), raw...), 0x08, 0x01)), false, 0, ""})
	rows = append(rows, Row{"truncated", hex.EncodeToString(raw[:len(raw)-1]), false, 0, ""})
	for _, r := range rows {
		packet, e := hex.DecodeString(r.Packet)
		must(e)
		_, e = labwire.Open(packet, a.Public(), b, now)
		if (e == nil) != r.Accept {
			panic(r.Name)
		}
	}
	result := map[string]any{"now_ms": now, "sender": "c000000000000001", "receiver": "m000000000000001", "rows": rows}
	encoded, e := json.MarshalIndent(result, "", "  ")
	must(e)
	must(os.WriteFile(filepath.Join(out, "vectors.json"), encoded, 0600))
	var table strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&table, "%s\t%t\t%d\t%s\t%s\n", row.Name, row.Accept, row.Type, row.Payload, row.Packet)
	}
	must(os.WriteFile(filepath.Join(out, "vectors.tsv"), []byte(table.String()), 0600))
	fmt.Printf("Go real-crypto vectors: %d passed\n", len(rows))
}
