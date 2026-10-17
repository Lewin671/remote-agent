package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type diagnosticListHandler struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
}

func (*diagnosticListHandler) ListSessions(context.Context, *connect.Request[pb.ListSessionsRequest]) (*connect.Response[pb.ListSessionsResponse], error) {
	return connect.NewResponse(&pb.ListSessionsResponse{Sessions: []*pb.Session{{Id: "0123456789abcdef", Status: pb.SessionStatus_SESSION_STATUS_EXITED, StatusDetail: "stopped", CreatedAt: timestamppb.Now()}}}), nil
}

func TestListKeepsLocalDiagnosticsSeparateFromCLIStatus(t *testing.T) {
	dir, err := os.MkdirTemp("", "ra-diag-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("REMOTE_AGENT_HOME", dir)
	mux := http.NewServeMux()
	path, handler := remoteagentv1connect.NewLocalServiceHandler(&diagnosticListHandler{}, connect.WithInterceptors(protocol.Local()))
	mux.Handle(path, handler)
	ln, err := net.Listen("unix", filepath.Join(dir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()
	sessionDir := filepath.Join(dir, "sessions", "0123456789abcdef")
	os.MkdirAll(sessionDir, 0700)
	os.WriteFile(filepath.Join(sessionDir, "parse-health.json"), []byte(`{"failures":1,"unknown":2,"start":10,"end":20,"type":"bad","category":"json_type"}`), 0600)
	os.MkdirAll(filepath.Join(dir, "diagnostics"), 0700)
	os.WriteFile(filepath.Join(dir, "diagnostics", "0123456789abcdef.json"), []byte(`{"observed_at":"2026-10-16T00:00:00Z"}`), 0600)
	for _, all := range []bool{false, true} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stdout
		os.Stdout = w
		var args []string
		if all {
			args = []string{"-a"}
		}
		err = runList(args)
		w.Close()
		os.Stdout = saved
		b, readErr := io.ReadAll(r)
		r.Close()
		if err != nil || readErr != nil {
			t.Fatalf("ls error %v %v", err, readErr)
		}
		out := string(b)
		if !strings.Contains(out, "exited (stopped)") {
			t.Fatalf("CLI reason changed: %q", out)
		}
		if all {
			if !strings.Contains(out, "LOCAL DIAGNOSTICS") || !strings.Contains(out, "parse failures=1 unknown=2") || !strings.Contains(out, "storage failure") {
				t.Fatalf("ls -a hides local files: %q", out)
			}
		} else if strings.Contains(out, "diagnostic") || strings.Contains(out, "failure") {
			t.Fatalf("normal listing warns about diagnostics: %q", out)
		}
	}
}
