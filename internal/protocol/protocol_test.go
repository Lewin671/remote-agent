package protocol_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/protocol"
)

type daemon struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	calls int
}

func (d *daemon) Schedule(context.Context, *connect.Request[pb.ScheduleRequest]) (*connect.Response[pb.ScheduleResponse], error) {
	d.calls++
	return connect.NewResponse(&pb.ScheduleResponse{}), nil
}

// Either end of the local socket without the version, as a build from
// before a protocol change is, is refused by the other.
func TestLocalRefusesAnotherVersion(t *testing.T) {
	for _, tc := range []struct {
		name           string
		daemon, client []connect.Option
		ok             bool
	}{
		{"same", []connect.Option{connect.WithInterceptors(protocol.Local())}, []connect.Option{connect.WithInterceptors(protocol.Local())}, true},
		{"other command", []connect.Option{connect.WithInterceptors(protocol.Local())}, nil, false},
		{"other daemon", nil, []connect.Option{connect.WithInterceptors(protocol.Local())}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &daemon{}
			mux := http.NewServeMux()
			handlerOpts := make([]connect.HandlerOption, len(tc.daemon))
			for i, o := range tc.daemon {
				handlerOpts[i] = o.(connect.HandlerOption)
			}
			mux.Handle(remoteagentv1connect.NewLocalServiceHandler(d, handlerOpts...))
			srv := httptest.NewServer(mux)
			defer srv.Close()
			clientOpts := make([]connect.ClientOption, len(tc.client))
			for i, o := range tc.client {
				clientOpts[i] = o.(connect.ClientOption)
			}
			c := remoteagentv1connect.NewLocalServiceClient(srv.Client(), srv.URL, clientOpts...)
			_, err := c.Schedule(context.Background(), connect.NewRequest(&pb.ScheduleRequest{}))
			if tc.ok {
				if err != nil || d.calls != 1 {
					t.Fatalf("%v, %d calls", err, d.calls)
				}
				return
			}
			if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "update") {
				t.Fatalf("got %v", err)
			}
			if len(tc.daemon) > 0 && d.calls != 0 {
				t.Fatal("the daemon ran a command of another version")
			}
		})
	}
}
