package remoteclient

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// remoteclient has no domain reducer. Validate the common input contract here
// using the actual Go protobuf decoder rather than inventing a fourth reducer.
func TestProjectionVectorContract(t *testing.T) {
	data, err := os.ReadFile("../../testdata/projection/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Version int `json:"projectionVersion"`
		Vectors []struct {
			Name         string            `json:"name"`
			Events       []json.RawMessage `json:"events"`
			Cuts         []int             `json:"cuts"`
			Observations []struct {
				Through string          `json:"through"`
				State   json.RawMessage `json:"state"`
			} `json:"observations"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Version != 1 || len(corpus.Vectors) == 0 {
		t.Fatal("invalid projection corpus")
	}
	names := map[string]bool{}
	for _, v := range corpus.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			if names[v.Name] {
				t.Fatal("duplicate vector name")
			}
			names[v.Name] = true
			for i, raw := range v.Events {
				var e pb.Event
				if err := protojson.Unmarshal(raw, &e); err != nil {
					t.Fatal(err)
				}
				if e.Seq != uint64(i+1) || e.SessionId != "projection-test" || e.Kind == nil {
					t.Fatalf("invalid event %d", i+1)
				}
			}
			if len(v.Cuts) != len(v.Events)+1 {
				t.Fatal("must cover every replay cut")
			}
			for i, cut := range v.Cuts {
				if i != cut {
					t.Fatal("invalid cut")
				}
			}
			for _, o := range v.Observations {
				through, err := strconv.Atoi(o.Through)
				if err != nil || through < 0 || through > len(v.Events) || len(o.State) == 0 {
					t.Fatal("invalid observation")
				}
			}
		})
	}
}
