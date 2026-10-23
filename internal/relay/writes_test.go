package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestCanceledConnectionCannotAdmitAnotherWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &conn{lifetime: ctx}
	if err := c.writeDisk(func() error { t.Error("canceled connection admitted a writer"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write=%v", err)
	}
}

func TestConnectionCloseDrainsStartedDiskMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &conn{lifetime: ctx}
	path := filepath.Join(t.TempDir(), "owned-state")
	syncing, release := make(chan struct{}), make(chan struct{})
	written, closed := make(chan error, 1), make(chan struct{})
	go func() {
		written <- c.writeDisk(func() error {
			return writeFileSyncWithDirectorySync(path, []byte("committed"), func(string) error {
				close(syncing)
				<-release
				return nil
			})
		})
	}()
	<-syncing // atomic replacement happened; durability still belongs to this connection
	cancel()
	// The writer has reached its durability barrier and must still own the
	// same mutex close uses to drain it, regardless of closer scheduling.
	if c.writes.mu.TryLock() {
		c.writes.mu.Unlock()
		t.Error("disk mutation released connection ownership before durability completed")
	}
	go func() { c.writes.close(); close(closed) }()
	close(release)
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	<-closed
	if err := c.writeDisk(func() error { t.Error("late mutation ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("late mutation=%v", err)
	}
}

type delayedCheckpointPath struct {
	checkpointBackend
	entered, release chan struct{}
}

func (b delayedCheckpointPath) SessionDir(string) string {
	close(b.entered)
	<-b.release
	return b.dir
}

func TestLateCheckpointCannotRecreateDirectoryAfterClose(t *testing.T) {
	dir := t.TempDir()
	backend := delayedCheckpointPath{checkpointBackend: checkpointBackend{dir: dir}, entered: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &conn{lifetime: ctx, r: New(backend)}
	done := make(chan error, 1)
	go func() {
		done <- c.persistCheckpoint("synthetic-session", &pb.PutSessionRequest{SessionId: "synthetic-session"})
	}()
	<-backend.entered
	cancel()
	closed := make(chan struct{})
	go func() { c.writes.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		close(backend.release)
		<-done
		t.Fatal("shutdown waited for a backend callback preceding disk admission")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	close(backend.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("late checkpoint=%v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("late checkpoint recreated cleaned state directory: %v", err)
	}
}

type imageWriterBackend struct {
	checkpointBackend
	images atomic.Int32
	reads  atomic.Int32
}

func (b *imageWriterBackend) Image(string, string) ImageUpload {
	b.reads.Add(1)
	return ImageUpload{Done: true}
}

func (b *imageWriterBackend) ImageUploaded(string, string, bool, int64) { b.images.Add(1) }

func TestClosedConnectionRejectsAllPublicationMutations(t *testing.T) {
	dir := t.TempDir()
	b := &imageWriterBackend{checkpointBackend: checkpointBackend{dir: dir}}
	c := &conn{lifetime: context.Background(), r: New(b), acct: &Account{Server: "https://example.com"}}
	mark := filepath.Join(dir, "relay.sent")
	if err := os.WriteFile(mark, []byte("previous-owner"), 0600); err != nil {
		t.Fatal(err)
	}
	c.writes.close()
	if _, err := c.sessionKey("synthetic-session", false); !errors.Is(err, context.Canceled) {
		t.Errorf("late key creation=%v", err)
	}
	if err := c.persistCheckpoint("synthetic-session", &pb.PutSessionRequest{}); !errors.Is(err, context.Canceled) {
		t.Errorf("late checkpoint=%v", err)
	}
	for _, status := range []pb.SessionStatus{pb.SessionStatus_SESSION_STATUS_EXITED, pb.SessionStatus_SESSION_STATUS_IDLE} {
		c.noteSentMeta("synthetic-session", []byte("key"), &pb.Session{Id: "synthetic-session", Status: status}, 1)
		if raw, err := os.ReadFile(mark); err != nil || string(raw) != "previous-owner" {
			t.Errorf("late sent-marker mutation: %q %v", raw, err)
		}
	}
	if err := c.imageUploaded("synthetic-session", "synthetic-image", true, 123); !errors.Is(err, context.Canceled) {
		t.Errorf("late image state=%v", err)
	}
	if state := c.imageState("synthetic-session", "synthetic-image"); !state.Failed {
		t.Error("closed image lookup was not canceled")
	}
	if n := b.reads.Load(); n != 0 {
		t.Errorf("late image access callbacks=%d", n)
	}
	if n := b.images.Load(); n != 0 {
		t.Errorf("late image callbacks=%d", n)
	}
	if len(c.r.sentMeta) != 0 {
		t.Error("dead connection changed shared metadata cache")
	}
	for _, name := range []string{"relay.key", "current-checkpoint.pb"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("late file %s: %v", name, err)
		}
	}
}
