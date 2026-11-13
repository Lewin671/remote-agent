package relay

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

type blockedOpeningBackend struct {
	Backend
	dir              string
	entered, release chan struct{}
	closed           atomic.Bool
	opened           atomic.Int32
}

func (b *blockedOpeningBackend) SessionDir(string) string { return b.dir }
func (b *blockedOpeningBackend) OpenTerminal(string) (Terminal, error) {
	b.opened.Add(1)
	close(b.entered)
	<-b.release
	return authorizationTerminal{b}, nil
}

type authorizationTerminal struct{ b *blockedOpeningBackend }

func (t authorizationTerminal) Snapshot() (*pb.TerminalSnapshot, error) {
	return &pb.TerminalSnapshot{}, nil
}
func (t authorizationTerminal) History(*pb.TerminalHistoryRequest) (*pb.TerminalHistoryPage, error) {
	return nil, nil
}
func (t authorizationTerminal) Output() <-chan []byte        { return nil }
func (t authorizationTerminal) SizeChanged() <-chan struct{} { return nil }
func (t authorizationTerminal) Done() <-chan struct{}        { return nil }
func (t authorizationTerminal) Reason() string               { return "" }
func (t authorizationTerminal) Input([]byte) error           { return nil }
func (t authorizationTerminal) Resize(int, int, bool) error  { return nil }
func (t authorizationTerminal) Close() error                 { t.b.closed.Store(true); return nil }
func TestOpenTerminalRevokedBeforeOrDuringOpeningCannotSucceed(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "during"}[during], func(t *testing.T) {
			b := &blockedOpeningBackend{dir: t.TempDir(), entered: make(chan struct{}), release: make(chan struct{})}
			c := &conn{r: New(b), lifetime: context.Background()}
			keys := initializeTestSecurityKeys(t, c)
			if err := os.WriteFile(filepath.Join(b.dir, "relay.key"), make([]byte, 32), 0600); err != nil {
				t.Fatal(err)
			}
			peer := keys.states[0].Core.Members[0].ID
			ctx := agent.WithOrigin(context.Background(), &pb.Origin{DeviceId: peer, Kind: pb.OriginKind_ORIGIN_KIND_CLIENT})
			if !during {
				c.security.setAuthorized(false)
				if err := c.openTerminal(ctx, "session", "stream-request-012345"); err == nil {
					t.Fatal("revoked opening returned success")
				}
				if b.opened.Load() != 0 {
					t.Fatal("opened native resource while blocked")
				}
				return
			}
			done := make(chan error, 1)
			go func() { done <- c.openTerminal(ctx, "session", "stream-request-012345") }()
			<-b.entered
			c.security.setAuthorized(false)
			close(b.release)
			if err := <-done; err == nil {
				t.Fatal("revoked opening returned success")
			}
			if !b.closed.Load() {
				t.Fatal("failed opening leaked native resource")
			}
		})
	}
}
