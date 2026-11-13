package daemon

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

func TestDiskErrorPreservesCompletionInsteadOfInferringFromSpace(t *testing.T) {
	for _, full := range []bool{false, true} {
		d := New(t.TempDir(), Config{})
		d.diskVolume = func() (*pb.DiskStatus, error) {
			if full {
				return &pb.DiskStatus{AvailableBytes: 1}, nil
			}
			return &pb.DiskStatus{AvailableBytes: 50e9}, nil
		}
		for _, c := range []struct {
			name    string
			err     error
			final   bool
			aborted bool
		}{
			{"unconfirmed write", syscall.ENOSPC, false, false},
			{"known precheck", outcome.FinalError(syscall.EDQUOT), true, false},
			{"unknown masks precheck", outcome.UndeterminedError(outcome.FinalError(syscall.ENOSPC)), false, false},
			{"file replaced", &fileReplacedError{outcome.FinalError(syscall.ENOSPC)}, false, false},
			{"queue accepted", errors.Join(errQueueAccepted, outcome.FinalError(syscall.ENOSPC)), false, true},
		} {
			t.Run(c.name, func(t *testing.T) {
				e := d.diskError(c.err)
				code := protocol.StorageExhausted
				if full {
					code = protocol.DiskFull
				}
				if protocol.MachineErrorCode(e) != code || outcome.IsFinalError(e) != c.final {
					t.Fatal(e)
				}
				if c.aborted && connect.CodeOf(e) != connect.CodeAborted {
					t.Fatal(e)
				}
			})
		}
	}
}

func TestAcceptedDiskFailuresRemainUnknownWithoutRepeating(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "storage_exhausted", true: "disk_full"}[full], func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			h, _ := controlHarness(t)
			h.d.sessions[h.s.meta.ID] = h.s
			h.d.diskVolume = func() (*pb.DiskStatus, error) {
				if full {
					return &pb.DiskStatus{AvailableBytes: 1}, nil
				}
				return &pb.DiskStatus{AvailableBytes: 50e9}, nil
			}
			h.d.syncStateDir = func(string) error { return syscall.ENOSPC }
			client, machine := startCancellationTestRelay(t, outcomeAuditBackend{interruptedModelBackend{Daemon: h.d}})
			cmd := &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: h.s.meta.ID, RequestId: "accepted-disk-input", Text: "fixture"}}}
			env, err := client.Seal(machine, h.s.meta.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), cmd)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err = client.Forward(context.Background(), machine, env); !remoteclient.Uncertain(err) {
					t.Fatalf("accepted failure finalized: %v", err)
				}
			}
			h.s.mu.Lock()
			n := len(h.s.queue.Queue)
			h.s.mu.Unlock()
			if n != 1 {
				t.Fatalf("queue has %d entries", n)
			}
		})
	}
}
