package deviceauth

import (
	"encoding/json"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"google.golang.org/protobuf/encoding/protojson"
	"os"
	"testing"
)

func TestSharedOutcomeVectors(t *testing.T) {
	b, err := os.ReadFile("../../testdata/transport/outcomes.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Commands []struct {
			Kind, Operation string
			ReadOnly        bool
		}
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
	seen := map[string]bool{}
	for _, v := range f.Commands {
		raw := map[string]any{}
		if v.Kind != "" {
			body := map[string]any{}
			if v.Kind == "schedule" {
				body["operation"] = v.Operation
			}
			raw[v.Kind] = body
			seen[v.Kind] = true
		}
		b, _ := json.Marshal(raw)
		cmd := &pb.Command{}
		if err := protojson.Unmarshal(b, cmd); err != nil {
			t.Fatal(err)
		}
		readOnly := IsReadOnlyCommand(cmd)
		if readOnly != v.ReadOnly {
			t.Fatalf("%s/%s readOnly=%v", v.Kind, v.Operation, readOnly)
		}
		for _, code := range []string{"canceled", "deadline_exceeded", "aborted"} {
			got := outcome.Classify(outcome.Evidence{Source: "daemon", Signed: true, Code: code, ReadOnly: readOnly})
			if (got == outcome.Executed) != v.ReadOnly {
				t.Fatalf("%s/%s: %s", v.Kind, code, got)
			}
		}
	}
	fields := (&pb.Command{}).ProtoReflect().Descriptor().Oneofs().ByName("kind").Fields()
	if len(seen) != fields.Len() {
		t.Fatalf("command catalogue incomplete: %d/%d", len(seen), fields.Len())
	}

}
