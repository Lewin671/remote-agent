package daemon

import (
	"context"
	"io"
	"os"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

type budgetDirEntry struct{ onName func() }

func (e budgetDirEntry) Name() string {
	if e.onName != nil {
		e.onName()
	}
	return ".hidden"
}
func (budgetDirEntry) IsDir() bool                { return false }
func (budgetDirEntry) Type() os.FileMode          { return 0 }
func (budgetDirEntry) Info() (os.FileInfo, error) { return nil, os.ErrInvalid }

type budgetDirectory struct {
	t                       *testing.T
	remaining, reads, names int
	cancel                  context.CancelFunc
	closed                  chan struct{}
}

func (d *budgetDirectory) ReadDir(n int) ([]os.DirEntry, error) {
	d.reads++
	if n <= 0 || n > directoryBatchSize {
		d.t.Errorf("unbounded directory read: %d", n)
		return nil, os.ErrInvalid
	}
	if d.remaining == 0 {
		return nil, io.EOF
	}
	count := min(n, d.remaining)
	d.remaining -= count
	entries := make([]os.DirEntry, count)
	for i := range entries {
		entries[i] = budgetDirEntry{onName: func() {
			d.names++
			if d.cancel != nil {
				d.cancel()
			}
		}}
	}
	return entries, nil
}
func (d *budgetDirectory) Close() error { close(d.closed); return nil }

func TestDirectoryScanBudgetIncludesHiddenFiles(t *testing.T) {
	for _, n := range []int{maxDirectoryEntries, maxDirectoryEntries + 1} {
		t.Run(map[bool]string{true: "at-limit", false: "over-limit"}[n == maxDirectoryEntries], func(t *testing.T) {
			dir := &budgetDirectory{t: t, remaining: n, closed: make(chan struct{})}
			rpc := &handler{openDir: func(string) (directoryReader, error) { return dir, nil }}
			result, err := rpc.ListDirectories(context.Background(), connect.NewRequest(&pb.ListDirectoriesRequest{Path: "/synthetic"}))
			<-dir.closed
			if n == maxDirectoryEntries {
				if err != nil || result == nil || len(result.Msg.Dirs) != 0 {
					t.Fatalf("at capacity: %v", err)
				}
			} else if connect.CodeOf(err) != connect.CodeResourceExhausted || result != nil {
				t.Fatalf("over capacity: %v", err)
			}
			if dir.names > maxDirectoryEntries {
				t.Fatalf("processed beyond scan budget: %d", dir.names)
			}
		})
	}
}

func TestDirectoryScanStopsInsideBatchOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := &budgetDirectory{t: t, remaining: 2 * directoryBatchSize, cancel: cancel, closed: make(chan struct{})}
	_, err := listDirectories(ctx, "/synthetic", func(string) (directoryReader, error) { return dir, nil })
	<-dir.closed
	if connect.CodeOf(rpcErr(err)) != connect.CodeCanceled || dir.reads != 1 || dir.names != 1 {
		t.Fatalf("cancellation kept scanning: reads=%d names=%d error=%v", dir.reads, dir.names, err)
	}
}
