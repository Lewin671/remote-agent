package peerwire

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestFrozenCryptoVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/transport/wire/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Now              uint64 `json:"now_ms"`
		Sender, Receiver string
		Rows             []struct {
			Name    string
			Packet  string `json:"packet_hex"`
			Accept  bool
			Type    uint64
			Payload string `json:"payload_hex"`
		}
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	a, b := fixture(corpus.Sender, 1), fixture(corpus.Receiver, 2)
	codec := fixtureCodec()
	for _, row := range corpus.Rows {
		t.Run(row.Name, func(t *testing.T) {
			raw, err := hex.DecodeString(row.Packet)
			if err != nil {
				t.Fatal(err)
			}
			opened, err := codec.Open(raw, a.Public(), b, corpus.Now)
			if (err == nil) != row.Accept {
				t.Fatalf("authenticated=%v, expected=%v", err == nil, row.Accept)
			}
			if row.Accept && (opened.Type != row.Type || hex.EncodeToString(opened.Payload) != row.Payload || !bytes.Equal(opened.Raw, raw)) {
				t.Fatal("authenticated original bytes changed")
			}
		})
	}
}
func TestAccountAndHeadAreInstanceBound(t *testing.T) {
	a, b := fixture("client", 1), fixture("machine", 2)
	codec := fixtureCodec()
	now := uint64(1700000000000)
	c := NewContext(a, b.Public(), Hash([]byte("connection"))[:16], 1, 1, 1, now, 1, false)
	raw, err := codec.Seal(a, b.Public(), c, 31337, []byte("unknown"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = codec.Open(raw, a.Public(), b, now); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"account", "domain", "head", "epoch"} {
		t.Run(field, func(t *testing.T) {
			other := fixtureCodec()
			switch field {
			case "account":
				other.account = []byte("1123456789abcdef0123456789abcdef")
			case "domain":
				other.accountDomain = Hash([]byte("another domain"))
			case "head":
				other.head = Hash([]byte("another head"))
			case "epoch":
				other.memberEpoch++
			}
			if _, err := other.Open(raw, a.Public(), b, now); err == nil {
				t.Fatal("different verified scope authenticated")
			}
		})
	}
}
func FuzzPeerPacket(f *testing.F) {
	a, b := fixture("client", 1), fixture("machine", 2)
	codec := fixtureCodec()
	now := uint64(1700000000000)
	c := NewProbeContext(a, b.Public(), Hash([]byte("connection"))[:16], 1, now)
	raw, _ := codec.Seal(a, b.Public(), c, 1, PB(B(1, c.Connection), B(2, a.Instance), B(3, a.Boot)), nil, nil, nil)
	f.Add(raw)
	f.Add([]byte{10, 255, 255, 255, 255, 255})
	f.Fuzz(func(t *testing.T, raw []byte) { _, _ = codec.Open(raw, a.Public(), b, now) })
}

func TestServerWrapperDoesNotStealDeliveryBudget(t *testing.T) {
	body := make([]byte, 65536)
	raw := PB(B(11, body))
	got, err := ServerFrameBody(raw, 11)
	if err != nil || len(got) != 65536 {
		t.Fatal("maximum valid delivery was rejected with outer wrapper")
	}
	for _, bad := range [][]byte{append(raw, 0), PB(B(11, make([]byte, 65537))), PB(B(12, body)), {90, 128, 0}} {
		if _, err = ServerFrameBody(bad, 11); err == nil {
			t.Fatal("invalid outer frame accepted")
		}
	}
}
