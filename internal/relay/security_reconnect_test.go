package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

type failedSecurityPull struct {
	remoteagentv1connect.UnimplementedClientServiceHandler
	code connect.Code
}

func (s failedSecurityPull) GetSecurityStates(context.Context, *connect.Request[pb.GetSecurityStatesRequest]) (*connect.Response[pb.GetSecurityStatesResponse], error) {
	return nil, connect.NewError(s.code, errors.New("synthetic pull failure"))
}
func TestSecuritySyncRequestsReloadOnAuthenticationLoss(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeUnavailable} {
		t.Run(code.String(), func(t *testing.T) {
			c := &conn{r: New(publishBackend{})}
			initializeTestSecurityKeys(t, c)
			path, h := remoteagentv1connect.NewClientServiceHandler(failedSecurityPull{code: code})
			mux := http.NewServeMux()
			mux.Handle(path, h)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			c.security.client = remoteagentv1connect.NewClientServiceClient(http.DefaultClient, srv.URL)
			c.security.requestSync()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			reloads := 0
			c.security.onAuthLost = func() { reloads++; cancel() }
			err := c.security.syncLoop(ctx, 20*time.Millisecond, nil)
			if code == connect.CodeUnavailable {
				if reloads != 0 || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err, reloads)
				}
			} else {
				if reloads != 1 || !errors.Is(err, context.Canceled) || !c.security.blocked.Load() {
					t.Fatal(err, reloads)
				}
			}
		})
	}
}
