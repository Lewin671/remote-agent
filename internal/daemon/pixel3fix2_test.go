package daemon

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/paths"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestTerminalInputFramesDoNotInterleaveAcrossStreams(t *testing.T) {
	h := newHarness(t, Config{})
	a, b := &TermStream{owner: h.s}, &TermStream{owner: h.s}
	entered, release := make(chan struct{}), make(chan struct{})
	aDone, bDone := make(chan error, 1), make(chan error, 1)
	var mu sync.Mutex
	var chunks []string
	write := func(args []string) error {
		marker := args[len(args)-1]
		mu.Lock()
		chunks = append(chunks, marker)
		first := len(chunks) == 1
		mu.Unlock()
		if first {
			close(entered)
			<-release
		}
		return nil
	}
	go func() { aDone <- a.inputUsing(bytes.Repeat([]byte("a"), 512), write) }()
	<-entered
	go func() { bDone <- b.inputUsing([]byte("b"), write) }()
	var bFinished bool
	select {
	case err := <-bDone:
		bFinished = true
		t.Errorf("second stream interleaved an unfinished input frame: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	if !bFinished {
		if err := <-bDone; err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(chunks, []string{"61", "61", "62"}) {
		t.Fatalf("terminal chunk order=%v", chunks)
	}
}

func TestTerminalResizeCommitsFitStateWithCommand(t *testing.T) {
	stream := &TermStream{}
	entered, release := make(chan struct{}), make(chan struct{})
	a, b := make(chan error, 1), make(chan error, 1)
	var physicalFit atomic.Bool
	go func() {
		a <- stream.resizeUsing(80, 24, true, func(...string) error {
			close(entered)
			<-release
			physicalFit.Store(true)
			return nil
		})
	}()
	<-entered
	go func() {
		b <- stream.resizeUsing(0, 0, false, func(...string) error { physicalFit.Store(false); return nil })
	}()
	var bFinished bool
	select {
	case err := <-b:
		bFinished = true
		t.Errorf("later resize overtook an unfinished terminal command: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-a; err != nil {
		t.Fatal(err)
	}
	if !bFinished {
		if err := <-b; err != nil {
			t.Fatal(err)
		}
	}
	if physicalFit.Load() != stream.fitted {
		t.Fatalf("terminal fit=%v cache=%v", physicalFit.Load(), stream.fitted)
	}
}

func TestSettingsCommitSerializesDiskAndCache(t *testing.T) {
	h := newHarness(t, Config{})
	written, release := make(chan struct{}), make(chan struct{})
	aDone, bDone := make(chan error, 1), make(chan error, 1)
	a, b := &pb.DaemonSettings{DefaultModel: "first"}, &pb.DaemonSettings{DefaultModel: "second"}
	go func() {
		_, err := h.d.saveSettings(a, func(path string, data []byte) error {
			if err := writeFileAtomic(path, data); err != nil {
				return err
			}
			close(written)
			<-release
			return nil
		})
		aDone <- err
	}()
	<-written
	go func() { _, err := h.d.updateSettings(b); bDone <- err }()
	var bFinished bool
	select {
	case err := <-bDone:
		bFinished = true
		t.Errorf("second settings commit overtook unfinished first commit: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	if !bFinished {
		if err := <-bDone; err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	disk := &pb.DaemonSettings{}
	if err := protojson.Unmarshal(raw, disk); err != nil {
		t.Fatal(err)
	}
	if got := h.d.settings().DefaultModel; got != disk.DefaultModel {
		t.Errorf("cache=%q disk=%q", got, disk.DefaultModel)
	}
	if disk.DefaultModel != b.DefaultModel {
		t.Errorf("final settings=%v", disk)
	}
}

type inspectedEntry struct {
	os.DirEntry
	inspect func() (fs.FileInfo, error)
}

type blockedStopTerminal struct {
	terminal
	entered, release chan struct{}
	calls            atomic.Int32
}

func (t *blockedStopTerminal) Kill() error {
	if t.calls.Add(1) == 1 {
		close(t.entered)
	}
	<-t.release
	return nil
}
func (*blockedStopTerminal) Alive() bool                             { return false }
func (*blockedStopTerminal) HostAlive(context.Context) (bool, error) { return false, nil }

func TestConcurrentStopsDoNotOverlapSessionLifecycle(t *testing.T) {
	h := newHarness(t, Config{})
	term := &blockedStopTerminal{terminal: h.s.term, entered: make(chan struct{}), release: make(chan struct{})}
	h.s.term = term
	a, b := make(chan error, 1), make(chan error, 1)
	go func() { a <- h.s.kill() }()
	<-term.entered
	go func() { b <- h.s.kill() }()
	time.Sleep(100 * time.Millisecond)
	if n := term.calls.Load(); n != 1 {
		t.Errorf("%d stop callbacks entered the lifecycle concurrently", n)
	}
	close(term.release)
	if err := <-a; err != nil {
		t.Fatal(err)
	}
	if err := <-b; err != nil {
		t.Fatal(err)
	}
	if n := term.calls.Load(); n != 1 {
		t.Errorf("second stop repeated an already completed stop: %d calls", n)
	}
}

func (e inspectedEntry) Info() (fs.FileInfo, error) { return e.inspect() }

func TestStorageSnapshotToleratesConcurrentRemoval(t *testing.T) {
	for _, cache := range []bool{false, true} {
		for _, disappears := range []bool{true, false} {
			name := map[bool]string{false: "upload", true: "cache"}[cache] + "/" + map[bool]string{true: "removed", false: "io-error"}[disappears]
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, Config{})
				path := filepath.Join(h.d.uploads.dir, "partial", "gone.part")
				if cache {
					path = filepath.Join(paths.SessionsDir(), "synthetic", "images", "gone.display")
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				wantErr := errors.New("synthetic metadata I/O error")
				var inspected atomic.Bool
				wrap := func(e os.DirEntry) os.DirEntry {
					return inspectedEntry{e, func() (fs.FileInfo, error) {
						inspected.Store(true)
						if !disappears {
							return nil, wantErr
						}
						if err := os.Remove(path); err != nil {
							return nil, err
						}
						return e.Info()
					}}
				}
				readDir := func(dir string) ([]os.DirEntry, error) {
					es, err := os.ReadDir(dir)
					if cache && dir == filepath.Dir(path) {
						for i, e := range es {
							if e.Name() == filepath.Base(path) {
								es[i] = wrap(e)
							}
						}
					}
					return es, err
				}
				walkDir := func(root string, visit fs.WalkDirFunc) error {
					return filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
						if !cache && p == path && err == nil {
							e = wrap(e)
						}
						return visit(p, e, err)
					})
				}
				_, err := h.d.imageStorageSnapshotUsing(context.Background(), readDir, walkDir)
				if !inspected.Load() {
					t.Fatal("metadata inspection was not exercised")
				}
				if disappears && err != nil {
					t.Fatalf("concurrent removal failed snapshot: %v", err)
				}
				if !disappears && !errors.Is(err, wantErr) {
					t.Fatalf("metadata error lost: %v", err)
				}
			})
		}
	}
}

func TestConcurrentRenameCommitsMetadataInOrder(t *testing.T) {
	h := newHarness(t, Config{})
	written, release := make(chan struct{}), make(chan struct{})
	aDone, bDone := make(chan error, 1), make(chan error, 1)
	go func() {
		aDone <- h.s.renameUsing("first", func(m *meta, dir string) error {
			if err := m.save(dir); err != nil {
				return err
			}
			close(written)
			<-release
			return nil
		})
	}()
	<-written
	go func() { bDone <- h.s.rename("second") }()
	var bFinished bool
	select {
	case err := <-bDone:
		bFinished = true
		t.Errorf("second rename overtook unfinished first commit: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	if !bFinished {
		if err := <-bDone; err != nil {
			t.Fatal(err)
		}
	}
	m, err := loadMeta(h.s.dir)
	if err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	live := h.s.meta
	h.s.mu.Unlock()
	if m.Title != live.Title || live.Title != "second" {
		t.Fatalf("disk=%q live=%q", m.Title, live.Title)
	}
}

type serializedControlAdapter struct {
	agent.Adapter
	apply func()
}

func (a serializedControlAdapter) SetModel(agent.Term, string, string) error { a.apply(); return nil }
func (a serializedControlAdapter) SetMode(agent.Term, pb.ApprovalMode, bool, agent.ModeFacts) error {
	a.apply()
	return nil
}

func TestControlCommitRetainsTerminalLockUntilEventRecorded(t *testing.T) {
	for _, mode := range []bool{false, true} {
		t.Run(map[bool]string{false: "model", true: "mode"}[mode], func(t *testing.T) {
			h, _ := controlHarness(t)
			applied := make(chan struct{})
			h.s.ag = serializedControlAdapter{h.s.ag, func() { h.s.readMu.Lock(); close(applied) }}
			done := make(chan error, 1)
			go func() {
				if mode {
					done <- h.s.setApprovalMode(pb.ApprovalMode_APPROVAL_MODE_AUTO)
				} else {
					done <- h.s.setModel("gpt-6-sol", "medium")
				}
			}()
			<-applied
			deadline := time.Now().Add(100 * time.Millisecond)
			for time.Now().Before(deadline) {
				if h.s.ctlMu.TryLock() {
					h.s.ctlMu.Unlock()
					t.Error("terminal lock released before settings event was recorded")
					break
				}
				time.Sleep(time.Millisecond)
			}
			h.s.readMu.Unlock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			want := &pb.SettingsChanged{Model: "gpt-6-sol", Effort: "medium"}
			if mode {
				want = &pb.SettingsChanged{Mode: pb.ApprovalMode_APPROVAL_MODE_AUTO}
			}
			if !proto.Equal(lastSettings(h), want) {
				t.Fatal("missing settings event")
			}
		})
	}
}
