package deviceauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAuthenticatedCapabilityKeepsOriginalBytesAndOwner(t *testing.T) {
	c := newCurrentChain(t)
	e := c.machineEndpoint(t, "")
	defer e.Close()
	other := c.machineEndpoint(t, "")
	defer other.Close()
	client := c.clientEndpoint(t)
	defer client.Close()
	// Repeated known protobuf fields are valid but would be collapsed by
	// decoding and reserializing. The wire authorization binds all these bytes.
	original := bytes.Repeat(commandBody(t), 512)
	now := uint64(time.Now().UnixMilli())
	wire, err := client.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "authenticated-byte-identity", Deadline: now + 60000}, nil, original)
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Authenticate(wire, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	copy := a.Body()
	copy[0] ^= 1
	if !bytes.Equal(a.Body(), original) {
		t.Fatal("caller mutated authenticated bytes")
	}
	calls := 0
	fn := func(h Header, plain []byte) (Completion, error) {
		calls++
		decoded, err := Unpack(h, plain)
		if err != nil || !bytes.Equal(decoded, original) {
			t.Error("original protobuf bytes changed")
		}
		return Final([]byte("done")), nil
	}
	if _, err := other.AcceptAuthenticated(context.Background(), a, now, AcceptOptions{}, fn); !errors.Is(err, ErrInvalid) {
		t.Fatal("capability crossed endpoint ownership")
	}
	if calls != 0 {
		t.Fatal("foreign endpoint executed capability")
	}
	if _, err := e.AcceptAuthenticated(context.Background(), a, now, AcceptOptions{}, fn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("callbacks=%d", calls)
	}
}
func TestAbandonedCallbackCannotPublishLateFinalResult(t *testing.T) {
	c := newCurrentChain(t)
	e := c.machineEndpoint(t, "")
	defer e.Close()
	client := c.clientEndpoint(t)
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	wire, err := client.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: "overdue-mutation-identity", Deadline: now + 60000}, nil, commandBody(t))
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Authenticate(wire, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	abandon := make(chan func(), 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := e.AcceptAuthenticated(context.Background(), a, now, AcceptOptions{Running: func(fn func()) { abandon <- fn }}, func(Header, []byte) (Completion, error) { <-release; return Final([]byte("late success")), nil })
		done <- err
	}()
	mark := <-abandon
	mark()
	_, err, wait, found := e.Lookup(context.Background(), a, now)
	if !found || wait != nil || !errors.Is(err, ErrOutcomeUnknown) {
		close(release)
		<-done
		t.Fatal("abandoned execution remained waitable or final")
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("late completion=%v", err)
	}
	body, err, wait, found := e.Lookup(context.Background(), a, now)
	if !found || wait != nil || len(body) != 0 || !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal("late callback replaced unknown with a final body")
	}
}

func TestReadOnlyDuplicatesShareInflightResultWithoutAcceptanceLog(t *testing.T) {
	c := newCurrentChain(t)
	e := c.machineEndpoint(t, filepath.Join(t.TempDir(), "security.db"))
	defer e.Close()
	client := c.clientEndpoint(t)
	defer client.Close()
	body, err := proto.Marshal(&pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: "session", FileId: "fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := uint64(time.Now().UnixMilli())
	wire, err := client.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Session: "session", Request: "readonly-inflight-request", Deadline: now + 60000}, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.Authenticate(wire, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	result := make(chan error, 1)
	calls := 0
	go func() {
		_, err := e.AcceptWith(context.Background(), wire, nil, now, AcceptOptions{}, func(Header, []byte) (Completion, error) {
			calls++
			close(entered)
			<-release
			return Final([]byte("block")), nil
		})
		result <- err
	}()
	<-entered
	_, err, wait, found := e.Lookup(context.Background(), prepared, now)
	if !found || err != nil || wait == nil {
		t.Fatal("read-only duplicate did not join the running call")
	}
	e.mu.Lock()
	rows := len(e.data.Accepted)
	runners := len(e.running)
	e.mu.Unlock()
	if rows != 0 || runners != 1 {
		t.Fatalf("read-only state: accepted=%d running=%d", rows, runners)
	}
	var persisted int
	if err = e.db.QueryRow("SELECT COUNT(*) FROM accepted").Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("read-only call persisted: rows=%d err=%v", persisted, err)
	}
	once.Do(func() { close(release) })
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	got, err := wait.Wait()
	if err != nil || string(got) != "block" {
		t.Fatalf("duplicate result=%q err=%v", got, err)
	}
	if _, err = e.Accept(wire, nil, now, func(Header, []byte) (Completion, error) { calls++; return Final([]byte("fresh")), nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("completed read-only result was cached: calls=%d", calls)
	}
	if err = e.db.QueryRow("SELECT COUNT(*) FROM accepted").Scan(&persisted); err != nil || persisted != 0 {
		t.Fatal("read-only completion wrote acceptance log")
	}
}

func TestWaiterGlobalBudgetAcrossIndependentKeys(t *testing.T) {
	c := newCurrentChain(t)
	e := c.machineEndpoint(t, "")
	defer e.Close()
	client := c.clientEndpoint(t)
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	release := make(chan struct{})
	done := make(chan error, 9)
	entered := make(chan struct{}, 9)
	var waiters []*ResultWaiter
	defer func() {
		close(release)
		for _, w := range waiters {
			w.Cancel()
			_, _ = w.Wait()
		}
		for range 9 {
			<-done
		}
	}()
	prepared := make([]*Authenticated, 9)
	for i := range 9 {
		wire, err := client.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: fmt.Sprintf("global-wait-key-%04d", i), Deadline: now + 60000}, nil, commandBody(t))
		if err != nil {
			t.Fatal(err)
		}
		prepared[i], err = e.Authenticate(wire, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, err := e.Accept(wire, nil, now, func(Header, []byte) (Completion, error) { entered <- struct{}{}; <-release; return Final(nil), nil })
			done <- err
		}()
	}
	for range 9 {
		<-entered
	}
	for i, p := range prepared {
		count := 31
		if i == 8 {
			count = 8
		}
		for range count {
			_, err, w, found := e.Lookup(context.Background(), p, now)
			if err != nil || w == nil || !found {
				t.Fatal("premature global waiter rejection")
			}
			waiters = append(waiters, w)
		}
	}
	_, err, w, found := e.Lookup(context.Background(), prepared[8], now)
	if w != nil {
		waiters = append(waiters, w)
	}
	if !found || w != nil || !errors.Is(err, ErrOutcomeUnknown) {
		t.Error("257th duplicate bypassed the global limit while its key still had room")
	}
}
