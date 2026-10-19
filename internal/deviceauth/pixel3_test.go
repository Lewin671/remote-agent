package deviceauth

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func TestUserIOCallbackDoesNotHoldSecurityGate(t *testing.T) {
	for _, readOnly := range []bool{true, false} {
		t.Run(map[bool]string{true: "read-only", false: "accepted-mutation"}[readOnly], func(t *testing.T) {
			chain := newCurrentChain(t)
			path := filepath.Join(t.TempDir(), "endpoint.db")
			endpoint := chain.machineEndpoint(t, path)
			defer endpoint.Close()
			client := chain.clientEndpoint(t)
			defer client.Close()
			body := commandBody(t)
			if readOnly {
				body, _ = proto.Marshal(&pb.Command{Kind: &pb.Command_ListDirectories{ListDirectories: &pb.ListDirectoriesRequest{Path: "/synthetic/blocked"}}})
			}
			now := uint64(time.Now().UnixMilli())
			wire, err := client.Prepare(Header{Purpose: Command, Kind: 5, Machine: chain.machine.ID, Request: "blocked-io-request", Deadline: now + 60000}, nil, body)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			finished := make(chan error, 1)
			go func() {
				_, err := endpoint.Accept(wire, nil, now, func(Header, []byte) ([]byte, error) { close(entered); <-release; return []byte("done"), nil })
				finished <- err
			}()
			<-entered
			installed := make(chan error, 1)
			go func() { installed <- endpoint.Install(chain.states(), chain.proofs()) }()
			select {
			case err := <-installed:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(200 * time.Millisecond):
				t.Error("Install waited for user I/O")
			}
			closed := make(chan error, 1)
			go func() { closed <- endpoint.Close() }()
			select {
			case err := <-closed:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(200 * time.Millisecond):
				t.Error("Close waited for user I/O")
			}
			close(release)
			select {
			case err := <-finished:
				if err != nil && !errors.Is(err, ErrUnknown) {
					t.Error(err)
				}
			case <-time.After(time.Second):
				t.Fatal("callback did not finish")
			}
			if !readOnly {
				reopened, err := OpenEndpoint(path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				_, err = reopened.Accept(wire, nil, now, func(Header, []byte) ([]byte, error) { t.Error("accepted request executed twice"); return nil, nil })
				if err != nil && !errors.Is(err, ErrUnknown) {
					t.Fatal(err)
				}
			}
		})
	}
}
