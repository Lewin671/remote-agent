package remoteclient

import (
	"encoding/json"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"os"
	"testing"
)

func TestSharedOutcomeVectors(t *testing.T) {
	b, err := os.ReadFile("../../testdata/transport/outcomes.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Protocol int
		Vectors  []struct {
			outcome.Evidence
			Outcome outcome.Category
		}
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Protocol != 14 || len(f.Vectors) != 504 {
		t.Fatalf("unexpected vector coverage: %d/%d", f.Protocol, len(f.Vectors))
	}
	for i, v := range f.Vectors {
		if got := outcome.Classify(v.Evidence); got != v.Outcome {
			t.Errorf("vector %d: %+v got %s want %s", i, v.Evidence, got, v.Outcome)
		}
	}
}
