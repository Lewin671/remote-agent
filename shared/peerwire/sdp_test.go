package peerwire

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestFrozenSDPVectors(t *testing.T) {
	root := "../../testdata/transport/sdp/"
	b, err := os.ReadFile(root + "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		File   string
		Accept bool
		SHA256 string
		Bytes  int
	}
	if err = json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Run(r.File, func(t *testing.T) {
			raw, err := os.ReadFile(root + r.File)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) != r.Bytes || hex.EncodeToString(Hash(raw)) != r.SHA256 {
				t.Fatal("frozen SDK bytes changed")
			}
			if err = ValidateSDP(raw); (err == nil) != r.Accept {
				t.Fatalf("accept=%v want %v", err == nil, r.Accept)
			}
		})
	}
}
func FuzzSDP(f *testing.F) {
	raw, _ := os.ReadFile("../../testdata/transport/sdp/pion-offer.sdp")
	f.Add(raw)
	f.Fuzz(func(t *testing.T, raw []byte) { _ = ValidateSDP(raw) })
}
