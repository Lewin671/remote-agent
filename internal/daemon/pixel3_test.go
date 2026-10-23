package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestDirectoryRPCDeadlineDoesNotWaitForBlockedReader(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	h := &handler{openDir: func(string) (directoryReader, error) { close(entered); <-release; return nil, os.ErrNotExist }}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := h.ListDirectories(ctx, connect.NewRequest(&pb.ListDirectoriesRequest{Path: "/synthetic/blocked"}))
		result <- err
	}()
	<-entered
	select {
	case err := <-result:
		if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("request deadline waited for blocked directory reader")
	}
}

func TestReadOnlySettingsAndUploadQueriesDoNotRunFilesystemCleanup(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.d.uploads
	if err := os.MkdirAll(filepath.Join(u.dir, "partial"), 0700); err != nil {
		t.Fatal(err)
	}
	record := &transfer{ID: "0123456789abcdef0123456789abcdef", Device: "local", Total: 1024, UpdatedMs: time.Now().Add(-partialTTL - time.Second).UnixMilli()}
	if err := u.save(record); err != nil {
		t.Fatal(err)
	}
	part := u.partial(record.ID) + ".part"
	if err := os.WriteFile(part, []byte("bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := (&handler{d: h.d}).GetSettings(context.Background(), connect.NewRequest(&pb.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); err != nil {
		t.Error("read-only settings query deleted an upload", err)
	}
	if _, err := u.status(context.Background(), record.ID, "local"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expired transfer status=%v", err)
	}
	if _, err := os.Stat(u.partial(record.ID) + ".json"); err != nil {
		t.Error("read-only status query deleted a transfer", err)
	}
}
