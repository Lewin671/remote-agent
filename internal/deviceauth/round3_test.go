package deviceauth

import (
	"bytes"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

func TestInstallWaitsForExecution(t *testing.T) {
	c := newCurrentChain(t)
	machine := c.machineEndpoint(t, "")
	defer machine.Close()
	client := c.clientEndpoint(t)
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := client.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "gate-request-123456", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := machine.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { close(entered); <-release; return []byte("done"), nil })
		finished <- err
	}()
	<-entered
	phone, _ := c.genesis.Core.Member(c.genesis.Core.AuthorityID)
	next := mustBuildCurrent(t, c.withClient, []Member{phone, c.machine}, []string{c.client.ID}, bytes.Repeat([]byte{9}, 32), bytes.Repeat([]byte{0x77}, 32), c.phoneEd, now)
	installed := make(chan error, 1)
	go func() { installed <- machine.Install([]State{next}, []*RecoveryStatement{nil}) }()
	select {
	case err := <-installed:
		close(release)
		<-finished
		t.Fatalf("installation crossed running callback: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-installed; err != nil {
		t.Fatal(err)
	}
	if _, err := machine.Accept(w, nil, now+1, func(Header, []byte) ([]byte, error) { t.Fatal("removed sender executed"); return nil, nil }); err != nil {
		t.Fatalf("authenticated recorded result: %v", err)
	}
}
func TestRecordedResultAfterLeaseExpires(t *testing.T) {
	c := newCurrentChain(t)
	m := c.machineEndpoint(t, "")
	defer m.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "lease-request-12345", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { return []byte("done"), nil }); err != nil {
		t.Fatal(err)
	}
	if out, err := m.Accept(w, nil, c.withClient.Core.NotAfter+1, func(Header, []byte) ([]byte, error) { t.Fatal("ran twice"); return nil, nil }); err != nil || string(out) != "done" {
		t.Fatalf("recorded retry: %q %v", out, err)
	}
}
func TestReadOnlyResultsDoNotFillAcceptedLog(t *testing.T) {
	c := newCurrentChain(t)
	m := c.machineEndpoint(t, "")
	defer m.Close()
	cl := c.clientEndpoint(t)
	defer cl.Close()
	now := uint64(time.Now().UnixMilli())
	body, _ := proto.Marshal(&pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
	w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "readonly-request-123", Deadline: now + 60000}, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := m.Accept(w, nil, now, func(Header, []byte) ([]byte, error) { return bytes.Repeat([]byte{1}, 4<<20), nil }); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.data.Accepted) != 0 {
		t.Fatal("read-only result entered accepted log")
	}
}

func TestRePairChainMustExtendEveryLocalEpoch(t *testing.T) {
	c := newCurrentChain(t)
	ep := c.machineEndpoint(t, "")
	defer ep.Close()
	if err := ep.CheckExtension(c.states()); err != nil {
		t.Fatal(err)
	}
	if err := ep.CheckExtension(c.states()[:2]); err == nil {
		t.Fatal("truncated chain replaced local head")
	}
	phone, _ := c.genesis.Core.Member(c.genesis.Core.AuthorityID)
	fork := mustBuildCurrent(t, c.added, []Member{phone, c.machine, c.client}, nil, bytes.Repeat([]byte{0x88}, 32), bytes.Repeat([]byte{0x55}, 32), c.phoneEd, c.withClient.Core.IssuedAt)
	if err := ep.CheckExtension([]State{c.genesis, c.added, fork}); err == nil {
		t.Fatal("same-pin fork replaced local chain")
	}
}

func TestScheduleQueriesDoNotPersistResultsButMutationsDo(t *testing.T) {
	for _, operation := range []string{"list", "get", "create", "unknown"} {
		t.Run(operation, func(t *testing.T) {
			c := newCurrentChain(t)
			m := c.machineEndpoint(t, "")
			defer m.Close()
			cl := c.clientEndpoint(t)
			defer cl.Close()
			now := uint64(time.Now().UnixMilli())
			body, _ := proto.Marshal(&pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: operation}}})
			w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "schedule-query-12345", Deadline: now + 60000}, nil, body)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			fn := func(Header, []byte) ([]byte, error) { calls++; return []byte("result"), nil }
			for i := 0; i < 2; i++ {
				if _, err := m.Accept(w, nil, now, fn); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "list" || operation == "get" {
				if calls != 2 || len(m.data.Accepted) != 0 {
					t.Fatal("query result was retained in the acceptance log")
				}
			} else if calls != 1 || len(m.data.Accepted) != 1 {
				t.Fatal("mutating or unknown schedule operation bypassed at-most-once")
			}
		})
	}
}
