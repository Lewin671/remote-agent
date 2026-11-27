package security

import (
	"encoding/json"
	"os"
	"testing"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestIsReadOnlyCommandMatchesOutcomeVectors(t *testing.T) {
	rawFile, err := os.ReadFile("../../testdata/transport/outcomes.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Commands []struct {
			Kind      string `json:"kind"`
			Operation string `json:"operation"`
			ReadOnly  bool   `json:"readOnly"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(rawFile, &f); err != nil {
		t.Fatal(err)
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
		jb, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		stdCmd := &stdv1.Command{}
		if err := protojson.Unmarshal(jb, stdCmd); err != nil {
			t.Fatalf("protojson unmarshal %s/%s: %v", v.Kind, v.Operation, err)
		}
		wire, err := proto.Marshal(stdCmd)
		if err != nil {
			t.Fatalf("proto marshal %s/%s: %v", v.Kind, v.Operation, err)
		}
		var liteCmd v1.Command
		if err := liteCmd.Unmarshal(wire); err != nil {
			t.Fatalf("lite unmarshal %s/%s: %v", v.Kind, v.Operation, err)
		}
		if got := IsReadOnlyCommand(&liteCmd); got != v.ReadOnly {
			t.Fatalf("%s/%s: IsReadOnlyCommand=%v want %v", v.Kind, v.Operation, got, v.ReadOnly)
		}
	}
	fields := (&stdv1.Command{}).ProtoReflect().Descriptor().Oneofs().ByName("kind").Fields()
	if len(seen) != fields.Len() {
		t.Fatalf("command catalogue incomplete: %d/%d", len(seen), fields.Len())
	}
}
