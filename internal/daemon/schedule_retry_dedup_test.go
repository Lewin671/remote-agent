package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestAcceptedInputRetryHasNoLifecycleSideEffects(t *testing.T) {
	for _, origin := range []*pb.Origin{originLocal, {Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}, {Kind: pb.OriginKind_ORIGIN_KIND_SYSTEM}} {
		for _, owned := range []bool{true, false} {
			t.Run(origin.Kind.String()+"/owned="+map[bool]string{true: "yes", false: "no"}[owned], func(t *testing.T) {
				h := closedOwnedSession(t)
				calls := filepath.Join(h.dir, "tmux.calls")
				h.s.meta.TaskOwned = owned
				h.s.queue.remember("accepted")
				if err := h.s.queue.save(h.s.dir); err != nil {
					t.Fatal(err)
				}
				if err := h.s.meta.save(h.s.dir); err != nil {
					t.Fatal(err)
				}
				reloaded, err := loadQueue(h.s.dir)
				if err != nil {
					t.Fatal(err)
				}
				h.s.queue = reloaded
				before := h.s.meta
				last := h.s.log.LastSeq()
				// Concurrent local/relay retries must all be no-ops, including image admission.
				var wg sync.WaitGroup
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						pos, err := h.s.enqueueInputContext(context.Background(), "retry", "accepted", nil, nil, origin, pb.SendMode_SEND_MODE_QUEUE, nil, func(string) (messageUploads, error) { t.Error("retry consumed images"); return messageUploads{}, nil })
						if err != nil || pos != 0 {
							t.Errorf("retry: pos=%d err=%v", pos, err)
						}
					}()
				}
				wg.Wait()
				h.s.mu.Lock()
				after := h.s.meta
				queued := len(h.s.queue.Queue)
				h.s.mu.Unlock()
				if !reflect.DeepEqual(before, after) || queued != 0 || h.s.log.LastSeq() != last {
					t.Fatalf("retry changed lifetime/queue/events: before=%+v after=%+v queued=%d", before, after, queued)
				}
				if raw, err := os.ReadFile(calls); err == nil && len(raw) > 0 {
					t.Fatalf("retry called tmux: %s", raw)
				} else if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			})
		}
	}
}
