package remoteclient

import (
	"encoding/json"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"os"
	"testing"
	"time"
)

func TestDispatchOwnershipVectors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/transport/dispatch.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string
		Prior bool
		Steps []struct {
			Op, Call, Delivery  string
			Uncertain, HasPrior bool
		}
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, row := range cases {
		if only := os.Getenv("RA_DISPATCH_CASE"); only != "" && only != row.Name {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			client, _, mid := syntheticTerminalSecurity(t)
			env, err := client.Seal(mid, "", NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: "/work/example"}}})
			if err != nil {
				t.Fatal(err)
			}
			calls := map[string]*dispatchCall{}
			deliveries := map[string]*dispatchDelivery{}
			for _, step := range row.Steps {
				switch step.Op {
				case "open":
					var prior []DispatchEvidence
					if row.Prior {
						prior = []DispatchEvidence{MayHaveBeenDispatched}
					}
					calls[step.Call] = client.openCall(env, prior)
				case "delivery":
					deliveries[step.Delivery] = calls[step.Call].delivery()
				case "dispatch":
					deliveries[step.Delivery].dispatch()
				case "unsent":
					deliveries[step.Delivery].notForwarded()
				case "close":
					calls[step.Call].close()
				case "check":
					if calls[step.Call].uncertain() != step.Uncertain || calls[step.Call].hasPrior() != step.HasPrior {
						t.Fatalf("%s: uncertain=%v prior=%v, expected %v/%v", step.Call, calls[step.Call].uncertain(), calls[step.Call].hasPrior(), step.Uncertain, step.HasPrior)
					}
				default:
					t.Fatal(step.Op)
				}
			}
		})
	}
}
