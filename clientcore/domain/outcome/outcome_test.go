package outcome

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestOutcomeVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "transport", "outcomes.json"))
	if err != nil {
		t.Fatalf("read outcomes.json: %v", err)
	}
	var fixture struct {
		Protocol int `json:"protocol"`
		Vectors  []struct {
			Name         string `json:"name"`
			Source       string `json:"source"`
			Code         string `json:"code"`
			Signed       bool   `json:"signed"`
			Prior        bool   `json:"prior"`
			ReadOnly     bool   `json:"readOnly"`
			Declared     string `json:"declared"`
			Reason       string `json:"reason"`
			ConflictKind string `json:"conflictKind"`
			Dispatched   bool   `json:"dispatched"`
			NotForwarded bool   `json:"notForwarded"`
			AttemptBound bool   `json:"attemptBound"`
			Admitted     bool   `json:"admitted"`
			Outcome      string `json:"outcome"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse outcomes.json: %v", err)
	}
	if len(fixture.Vectors) == 0 {
		t.Fatal("no vectors in outcomes.json")
	}
	for i, v := range fixture.Vectors {
		got := Classify(Evidence{
			Source:       v.Source,
			Code:         v.Code,
			Signed:       v.Signed,
			Prior:        v.Prior,
			ReadOnly:     v.ReadOnly,
			Declared:     Category(v.Declared),
			Reason:       v.Reason,
			ConflictKind: v.ConflictKind,
			Dispatched:   v.Dispatched,
			NotForwarded: v.NotForwarded,
			AttemptBound: v.AttemptBound,
			Admitted:     v.Admitted,
		})
		if string(got) != v.Outcome {
			t.Fatalf("vector %d (%s): got %q, want %q", i, v.Name, got, v.Outcome)
		}
	}
}

func TestVerifyOutcome(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	payload := bytes.Repeat([]byte{2}, 32)
	res := &v1.CommandResult{
		RequestKeyHash:  key,
		PayloadHash:     payload,
		Outcome:         "executed",
		Reason:          "ok",
		ResultAvailable: true,
		Kind:            &v1.CommandResult_StopSession{StopSession: &v1.StopSessionResponse{}},
	}
	if got := Verify(res, key, payload, "executed", "ok", false); got != Executed {
		t.Fatalf("got %q, want executed", got)
	}
	if got := Verify(res, key[:31], payload, "executed", "ok", false); got != Unknown {
		t.Fatalf("short key: got %q, want unknown", got)
	}
	if got := Verify(res, key, payload, "rejected", "ok", false); got != Unknown {
		t.Fatalf("outer mismatch: got %q, want unknown", got)
	}
	if got := Verify(nil, key, payload, "executed", "ok", false); got != Unknown {
		t.Fatalf("nil result: got %q, want unknown", got)
	}
}
