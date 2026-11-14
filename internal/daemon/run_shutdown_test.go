package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/updater"
)

func newRunHarness(t *testing.T) *restoreHarness {
	r := newRestoreHarness(t)
	dir, err := os.MkdirTemp("/tmp", "ra-run-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	r.d.dir = dir
	r.d.uploads = newUploads(dir)
	r.d.uploads.limits = func() int64 { return r.d.settings().ImageUploadQuotaBytes }
	r.d.uploads.pins = r.d.protectedImageSessions
	if err := updater.EnsureState(dir); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunWaitsForStartedBeforeUnlock(t *testing.T) {
	// Run holds daemon.lock and executes a freshly written tmux script. Keep
	// this in the serial phase: concurrent forks can inherit those descriptors.
	r := newRunHarness(t)
	ready, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r.d.cfg.Started = func(ctx context.Context) {
		close(ready)
		<-ctx.Done()
		close(canceled)
		<-release
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.d.Run(ctx) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	}
	cancel()
	<-canceled
	select {
	case err := <-done:
		close(release)
		t.Fatalf("Run returned before Started finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	lock, err := os.OpenFile(filepath.Join(r.d.dir, "daemon.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		close(release)
		t.Fatal("daemon lock released before workers finished")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Run has closed its descriptor. A child forked by a concurrent test may
	// briefly hold an inherited copy until its exec closes it (CLOEXEC), so
	// the lock must become free promptly rather than at this exact instant.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunStartupFailureCancelsRestoredWorkers(t *testing.T) {
	// Run holds daemon.lock and executes a freshly written tmux script. Keep
	// this in the serial phase: concurrent forks can inherit those descriptors.
	r := newRunHarness(t)
	r.save(meta{ID: "00000000000000ab"}, true)
	if err := os.WriteFile(filepath.Join(r.d.dir, "schedules.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.d.Run(ctx); err == nil {
		t.Fatal("accepted broken schedules")
	}
	if r.d.ctx.Err() == nil {
		t.Fatal("startup failure left daemon context alive")
	}
	s := r.session("00000000000000ab")
	select {
	case <-s.done:
	default:
		t.Fatal("Run returned before restored workers finished")
	}
	if s.meta.Exited {
		t.Fatal("shutdown marked native CLI exited")
	}
}

type shutdownPollTerm struct {
	terminal
	entered, release chan struct{}
}

func (f *shutdownPollTerm) Poll(ctx context.Context) (string, bool, error) {
	close(f.entered)
	<-f.release // stand in for an event commit that is already in progress
	return "", true, ctx.Err()
}

func TestRunWaitsForSessionWorkers(t *testing.T) {
	// Run holds daemon.lock and executes a freshly written tmux script. Keep
	// this in the serial phase: concurrent forks can inherit those descriptors.
	r := newRunHarness(t)
	h := newHarness(t, Config{})
	h.s.d = r.d
	term := &shutdownPollTerm{terminal: h.term, entered: make(chan struct{}), release: make(chan struct{})}
	h.s.term = term
	r.d.cfg.Started = func(ctx context.Context) {
		r.d.mu.Lock()
		r.d.sessions[h.s.meta.ID] = h.s
		r.d.mu.Unlock()
		h.s.start(ctx)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.d.Run(ctx) }()
	<-term.entered
	cancel()
	select {
	case err := <-done:
		close(term.release)
		t.Fatalf("Run returned before poll worker finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(term.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !h.term.Alive() {
		t.Fatal("shutdown killed native CLI")
	}
}

func TestServeLocalWaitsForHTTPDrain(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveLocal(ctx, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			<-release
			w.WriteHeader(http.StatusNoContent)
		}))
	}()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := http.Get("http://" + ln.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	cancel()
	<-canceled
	select {
	case err := <-done:
		close(release)
		t.Fatalf("Serve returned before HTTP drain: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-clientDone
}
