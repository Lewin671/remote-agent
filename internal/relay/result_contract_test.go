package relay_test

import (
	"bytes"
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWireIdentityConflictPreservesOriginalAndBodyEviction(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "body", true: "summary"}[missing], func(t *testing.T) {
			e := setup(t)
			e.backend.add(t, "session")
			if missing {
				e.backend.sendResult = func(context.Context) error {
					return outcome.FinalError(connect.NewError(connect.CodeInternal, errors.New(strings.Repeat("x", deviceauth.MaxCipherBytes))))
				}
			}
			e.startRelay()
			s := e.session(t, "session", 0)
			e.online(t, true)
			id, deadline := remoteclient.NewRequestID(), time.Now().Add(time.Minute)
			seal := func(text string) *pb.Envelope {
				env, err := e.client.Seal(s.MachineID, s.ID, id, deadline, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: text, RequestId: id}}})
				if err != nil {
					t.Fatal(err)
				}
				return env
			}
			original, other := seal("original"), seal("different")
			result, err := e.client.Forward(context.Background(), s.MachineID, original)
			if result == nil || result.Outcome != "executed" || result.ResultAvailable == missing {
				t.Fatalf("first result: %v", err)
			}
			if missing && !remoteclient.Uncertain(err) || !missing && err != nil {
				t.Fatal(err)
			}
			conflict, err := e.client.Forward(context.Background(), s.MachineID, other)
			if conflict == nil || conflict.Outcome != "conflict" || conflict.ConflictKind != "identity" || conflict.ResultAvailable || conflict.Kind != nil || err == nil {
				t.Fatalf("identity leaked or failed: %v", err)
			}
			again, err := e.client.Forward(context.Background(), s.MachineID, original)
			if again == nil || again.Outcome != "executed" || again.ResultAvailable == missing || e.backend.sendCount() != 1 {
				t.Fatalf("original changed/repeated: %v", err)
			}
		})
	}
}
func TestOfflineAttemptCannotOverrideEarlierSignedUnknown(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "session")
	e.backend.sendResult = func(context.Context) error { return errors.New("no completion evidence") }
	e.startRelay()
	s := e.session(t, "session", 0)
	e.online(t, true)
	seal := func() *pb.Envelope {
		w, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: "fixture"}}})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	accepted := seal()
	_, err := e.client.Forward(context.Background(), s.MachineID, accepted)
	var ce *remoteclient.CommandError
	if !errors.As(err, &ce) || ce.Outcome != outcome.Unknown || !ce.FromDaemon {
		t.Fatal("unknown lacks proof", err)
	}
	e.stopRelay()
	e.online(t, false)
	for _, prior := range []bool{false, true} {
		env := accepted
		if !prior {
			env = seal()
		}
		_, err = e.client.Forward(context.Background(), s.MachineID, env)
		if !errors.As(err, &ce) {
			t.Fatal(err)
		}
		want := outcome.NotSent
		if prior {
			want = outcome.Unknown
		}
		if ce.Outcome != want {
			t.Fatalf("prior %v: %s", prior, ce.Outcome)
		}
	}
	if e.backend.sendCount() != 1 {
		t.Fatal("repeated")
	}
}

func TestExpiredPreviousProtocolQueryWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		age    time.Duration
		daemon bool
	}{
		{"within-retention", time.Second, true},
		{"past-query-window", deviceauth.ResultRetention + time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			e.backend.add(t, "session")
			e.startRelay()
			s := e.session(t, "session", 0)
			e.online(t, true)
			// Migration may have removed the old accepted row. Within the
			// recovery window the daemon still supplies signed unknown.
			env, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(-tc.age), &pb.Command{ProtocolVersion: 14, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: "old authorized payload"}}})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				proof, err := e.client.Forward(context.Background(), s.MachineID, env)
				var failure *remoteclient.CommandError
				if !errors.As(err, &failure) || failure.FromDaemon != tc.daemon || failure.Outcome != outcome.Unknown {
					t.Fatal("wrong expiry boundary", err)
				}
				if tc.daemon && (proof == nil || proof.Outcome != "unknown") || !tc.daemon && proof != nil {
					t.Fatal("wrong expiry proof", proof)
				}
			}
			if e.backend.sendCount() != 0 {
				t.Fatal("old authorization executed again")
			}
		})
	}
}

func TestLostReplyIsRecoveredAfterDeadlineWithoutReexecution(t *testing.T) {
	var lost atomic.Bool
	requests := make(chan *pb.ForwardRequest, 4)
	firstStatus := make(chan int, 1)
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/Forward") {
				next.ServeHTTP(w, r)
				return
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			request := &pb.ForwardRequest{}
			if err = proto.Unmarshal(raw, request); err != nil {
				t.Error(err)
				return
			}
			requests <- request
			if lost.CompareAndSwap(false, true) {
				response := httptest.NewRecorder()
				next.ServeHTTP(response, r)
				firstStatus <- response.Code
				// The daemon finished and produced its signed result, but no
				// response bytes reach the client on this first connection.
				panic(http.ErrAbortHandler)
			}
			next.ServeHTTP(w, r)
		})
	})
	e.backend.add(t, "session")
	e.startRelay()
	s := e.session(t, "session", 0)
	e.online(t, true)
	deadline := time.Now().Add(4 * time.Second)
	id := remoteclient.NewRequestID()
	env, err := e.client.Seal(s.MachineID, s.ID, id, deadline, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, RequestId: id, Text: "retain this result"}}})
	if err != nil {
		t.Fatal(err)
	}
	original := proto.Clone(env).(*pb.Envelope)
	if _, err = e.client.Forward(context.Background(), s.MachineID, env); !remoteclient.Uncertain(err) {
		t.Fatal("lost result became definite", err)
	}
	first := <-requests
	if status := <-firstStatus; status != http.StatusOK || e.backend.sendCount() != 1 {
		t.Fatal("first command did not finish before response loss", status)
	}
	timer := time.NewTimer(time.Until(deadline) + 20*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	previousAttempt := first.AttemptId
	for range 2 {
		result, err := e.client.Forward(context.Background(), s.MachineID, env)
		if err != nil || result == nil || result.Outcome != "executed" || !result.ResultAvailable || result.GetSend().GetQueuePosition() != 1 {
			t.Fatal("retained result inaccessible after deadline", result, err)
		}
		request := <-requests
		if request.AttemptId == previousAttempt || !proto.Equal(request.Command, original) || !proto.Equal(env, original) {
			t.Fatal("query changed original authorization or reused attempt")
		}
		previousAttempt = request.AttemptId
		if e.backend.sendCount() != 1 {
			t.Fatal("query reexecuted the command")
		}
	}
	// The same path may query, but it cannot start an expired, previously
	// unaccepted authorization. This uses the current command protocol.
	never, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), deadline, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: "must not execute"}}})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := e.client.Forward(context.Background(), s.MachineID, never)
	var failure *remoteclient.CommandError
	if !errors.As(err, &failure) || !failure.FromDaemon || failure.Outcome != outcome.Unknown || proof == nil || proof.Outcome != "unknown" || e.backend.sendCount() != 1 {
		t.Fatal("query window extended execution authorization", proof, err)
	}
}
