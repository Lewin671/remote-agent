package deviceauth

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func TestTransientRefusalCannotReleaseReplayableCreateIdentity(t *testing.T) {
	for _, reason := range []string{"authorization_unavailable", "clock", "lease", "busy"} {
		t.Run(reason, func(t *testing.T) {
			chain := newCurrentChain(t)
			path := filepath.Join(t.TempDir(), "machine.db")
			machine := chain.machineEndpoint(t, path)
			defer func() { machine.Close() }()
			client := chain.clientEndpoint(t)
			defer client.Close()
			now := uint64(time.Now().UnixMilli())
			h := Header{Kind: 5, Purpose: Command, Machine: chain.machine.ID, Request: "original-create-request", Deadline: now + 60_000}
			if reason == "clock" {
				h.Deadline = now + 11*60_000
			}
			plain, err := proto.Marshal(&pb.Command{ProtocolVersion: 15, Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: "/work/example", Env: map[string]string{"Z": "last", "A": "first"}}}})
			if err != nil {
				t.Fatal(err)
			}
			original, err := client.Prepare(h, nil, plain)
			if err != nil {
				t.Fatal(err)
			}
			allowed := reason != "authorization_unavailable"
			options := AcceptOptions{Evidence: true, AllowNew: func() bool { return allowed }}
			lease := machine.data.Lease
			if reason == "lease" {
				machine.data.Lease = nil
			}
			if reason == "busy" {
				// Fill the bounded admission index, without expensive unrelated body writes.
				for i := range maxAccepted {
					machine.durableExpiry = append(machine.durableExpiry, acceptedExpiry{fmt.Sprint(i), h.Deadline + acceptedGrace})
				}
			}
			calls := 0
			create := func(Header, []byte) (Completion, error) { calls++; return Final([]byte("created")), nil }
			_, refused := machine.AcceptWith(context.Background(), original, nil, now, options, create)
			proof, ok := CommandResult(nil, refused)
			if !ok || proof.Reason != reason || calls != 0 {
				t.Fatalf("refusal: %+v, calls=%d", proof, calls)
			}
			opened, packed, err := client.Open(original, nil, false, now)
			if err != nil {
				t.Fatal(err)
			}
			key, payload, err := RequestIdentity(opened, packed)
			if err != nil || string(proof.RequestKeyHash) != string(key[:]) || string(proof.PayloadHash) != string(payload[:]) {
				t.Fatal("unbound refusal", err)
			}

			// Model the unchanged client rule: not_sent releases the old ID; unknown
			// retries it. A late copy must not create a second session after recovery.
			retry := original
			if proof.Outcome == "not_sent" {
				h.Request = "replacement-create-request"
				retry, err = client.Prepare(h, nil, plain)
				if err != nil {
					t.Fatal(err)
				}
			}
			allowed = true
			machine.data.Lease = lease
			machine.durableExpiry = nil
			if reason == "clock" {
				now += 2 * 60_000
			}
			for _, wire := range []Wire{retry, original} {
				if _, err = machine.AcceptWith(context.Background(), wire, nil, now, options, create); err != nil {
					t.Fatal(err)
				}
			}
			if err = machine.Close(); err != nil {
				t.Fatal(err)
			}
			machine, err = OpenEndpoint(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = machine.AcceptWith(context.Background(), original, nil, now, options, create); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("transient %s released a still-executable ID: %d creates", reason, calls)
			}
			if proof.Outcome != "unknown" {
				t.Fatalf("nonterminal refusal: %s", proof.Outcome)
			}
		})
	}
}
