package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestExplicitResumeClaimsRejectedImplicitLifetime(t *testing.T) {
	for _, origin := range []*pb.Origin{originLocal, {Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}} {
		for _, fault := range []string{"image", "quota", "queue"} {
			t.Run(origin.Kind.String()+"/"+fault, func(t *testing.T) {
				h := closedOwnedSession(t)
				if fault == "quota" {
					h.d.inputMu.Lock()
					h.d.inputBytes = maxDaemonInputBytes
					h.d.inputMu.Unlock()
				}
				if fault == "queue" {
					if err := os.Mkdir(filepath.Join(h.s.dir, "queue.json"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				take := func(string) (messageUploads, error) {
					if fault == "image" {
						return messageUploads{}, errors.New("expired upload")
					}
					return messageUploads{}, nil
				}
				_, err := h.s.enqueueInputContext(context.Background(), "Reply", "rejected-reply", nil, nil, origin, pb.SendMode_SEND_MODE_QUEUE, nil, take)
				if err == nil || queueAccepted(err) {
					t.Fatalf("input was not rejected: %v", err)
				}
				// Freeze the window before the next scheduler tick. The CLI is live,
				// but no input was accepted and this lifetime is still task-owned.
				h.s.stop()
				<-h.s.done
				closers := h.d.scheduledClosures()
				if len(closers) != 1 || !h.term.Alive() {
					t.Fatal("failed implicit resume did not leave a live cleanup candidate")
				}
				calls := tmuxCalls(t, filepath.Join(h.dir, "tmux.calls"))
				ctx := agent.WithOrigin(context.Background(), origin)
				resp, err := (&handler{d: h.d}).ResumeSession(ctx, connect.NewRequest(&pb.ResumeSessionRequest{SessionId: h.s.meta.ID}))
				if err != nil {
					t.Fatalf("explicit resume failed to claim the running task session: %v", err)
				}
				if resp.Msg.Session.TaskOwned || resp.Msg.Session.Status == pb.SessionStatus_SESSION_STATUS_EXITED {
					t.Fatal("explicit resume did not return a live, human-owned session")
				}
				if tmuxCalls(t, filepath.Join(h.dir, "tmux.calls")) != calls {
					t.Fatal("claiming an already running session issued a terminal command")
				}
				persisted, err := loadMeta(h.s.dir)
				if err != nil {
					t.Fatal(err)
				}
				if persisted.TaskOwned || persisted.ScheduledTaskID == "" || persisted.ScheduledRunID == "" {
					t.Fatal("takeover was not persisted with its creation identity intact")
				}
				if len(h.d.scheduledClosures()) != 0 {
					t.Fatal("claimed lifetime remained eligible for automatic cleanup")
				}
				// Even a closer selected before the explicit resume must recheck.
				if err := closers[0].killIf(context.Background(), true); err != nil {
					t.Fatal(err)
				}
				if !h.term.Alive() || h.s.info().Status == pb.SessionStatus_SESSION_STATUS_EXITED {
					t.Fatal("stale automatic closer killed the explicitly claimed CLI")
				}
			})
		}
	}
}

func TestRunningResumeTakeoverRequiresHumanOwnedTask(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin *pb.Origin
		owned  bool
	}{
		{"system", originSystem, true},
		{"unknown", nil, true},
		{"ordinary", originLocal, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := closedOwnedSession(t)
			h.s.meta.Exited, h.s.meta.TaskOwned = false, tc.owned
			_, err := h.d.resumeContextBy(context.Background(), h.s.meta.ID, tc.origin)
			if !errors.Is(err, errRunning) || h.s.info().TaskOwned != tc.owned {
				t.Fatalf("unrelated resume changed behavior: owned=%v err=%v", h.s.info().TaskOwned, err)
			}
		})
	}
}
