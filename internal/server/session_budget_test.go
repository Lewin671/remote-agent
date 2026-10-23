package server

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
)

func TestSessionResponseBudgetAcrossListingPaths(t *testing.T) {
	ts := newTestServer(t)
	_, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	raw, _ := proto.Marshal(&pb.Envelope{Ciphertext: make([]byte, 1024)})
	for _, id := range []string{"live", "ended", "older"} {
		ended := int64(0)
		if id != "live" {
			ended = 1
		}
		if _, err := ts.putSession(ctx, mid, id, raw, raw, 1, ended, nil); err != nil {
			t.Fatal(err)
		}
	}
	st := ts.tenant()
	budget := &sessionBudget{}
	live, err := st.liveSessions(ctx, budget)
	if err != nil {
		t.Fatal(err)
	}
	if want := proto.Size(&pb.ListRemoteSessionsResponse{Sessions: live}); budget.used != want {
		t.Fatalf("charged %d want %d", budget.used, want)
	}
	one := budget.used
	for _, path := range []string{"live", "ended", "ids", "combined", "cursor"} {
		t.Run(path, func(t *testing.T) {
			// Exercise the production boundary with a small fixture by seeding
			// bytes already consumed by earlier rows of the same response.
			b := &sessionBudget{used: maxSessionResponseBytes - one + 1}
			var err error
			switch path {
			case "live":
				_, err = st.liveSessions(ctx, b)
			case "ended":
				_, _, err = st.endedSessions(ctx, nil, 2, b)
			case "ids":
				_, err = st.sessionsByID(ctx, []string{"live", "live"}, b)
			case "combined":
				b.used = maxSessionResponseBytes - one
				if _, err = st.liveSessions(ctx, b); err != nil {
					t.Fatal(err)
				}
				_, _, err = st.endedSessions(ctx, nil, 1, b)
			case "cursor":
				b.used = maxSessionResponseBytes - one - 2 // ended_ms adds two bytes
				_, _, err = st.endedSessions(ctx, nil, 1, b)
			}
			if !errors.Is(err, errSessionResponseFull) {
				t.Fatalf("budget not enforced: %v", err)
			}
			if connect.CodeOf(sessionListError(err)) != connect.CodeResourceExhausted {
				t.Fatal("capacity error hidden as internal error")
			}
		})
	}
	// The lookahead row is not part of the response; only the page and its
	// cursor are charged, even though a second ended row exists.
	b := &sessionBudget{}
	page, next, err := st.endedSessions(ctx, nil, 1, b)
	if err != nil || next == nil {
		t.Fatalf("ended page: %v %v", next, err)
	}
	if want := proto.Size(&pb.ListRemoteSessionsResponse{Sessions: page, Next: next}); b.used != want {
		t.Fatalf("page charged %d want %d", b.used, want)
	}
	// Duplicate explicit IDs are charged each time they appear in a response.
	b = &sessionBudget{used: maxSessionResponseBytes - one}
	if _, err := st.sessionsByID(ctx, []string{"live", "live"}, b); !errors.Is(err, errSessionResponseFull) {
		t.Fatalf("duplicate IDs bypass budget: %v", err)
	}
}

func TestSessionEnvelopePurposeLimits(t *testing.T) {
	for _, tc := range []struct {
		kind  pb.PayloadKind
		limit int
	}{
		{pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, maxSessionKeyEnvelopeBytes},
		{pb.PayloadKind_PAYLOAD_KIND_SESSION_META, maxSessionMetaEnvelopeBytes},
	} {
		want := &pb.Header{Kind: tc.kind, MachineId: "machine", SessionId: "session"}
		h := proto.CloneOf(want)
		h.Version = deviceauth.Version
		raw, _ := proto.Marshal(h)
		env := &pb.Envelope{Header: raw, Ciphertext: make([]byte, tc.limit-512)}
		if _, err := checkEnvelope(env, want); err != nil {
			t.Fatalf("headroom rejected for %s: %v", tc.kind, err)
		}
		env.Ciphertext = make([]byte, tc.limit+1)
		if _, err := checkEnvelope(env, want); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("oversized %s accepted: %v", tc.kind, err)
		}
	}
}
