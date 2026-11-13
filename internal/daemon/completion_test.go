package daemon

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"strings"
	"testing"
)

func TestRPCCompletionEvidence(t *testing.T) {
	if err := rpcErr(outcome.FinalError(errNotFound)); connect.CodeOf(err) != connect.CodeNotFound || !outcome.IsFinalError(err) {
		t.Fatalf("completion erased diagnostic: %v", err)
	}

	for _, err := range []error{errNotFound, errForkBusy, errHeldChanged, agent.ErrUnsupported} {
		if !outcome.IsFinalError(rpcErr(err)) {
			t.Errorf("settled validation lost evidence: %v", err)
		}
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, agent.ErrDeliveryUnconfirmed, errors.New("unconfirmed storage failure"), connect.NewError(connect.CodeAborted, errors.New("unconfirmed execution"))} {
		if outcome.IsFinalError(rpcErr(err)) {
			t.Errorf("unconfirmed operation became final: %v", err)
		}
	}
	// The execution owner can retain a final failure independently of its code.
	for _, code := range []connect.Code{connect.CodeUnknown, connect.CodeCanceled, connect.CodeDeadlineExceeded} {
		err := rpcErr(outcome.FinalError(connect.NewError(code, errors.New("settled backend result"))))
		if !outcome.IsFinalError(err) || connect.CodeOf(err) != code {
			t.Fatal(err)
		}
	}
}

func TestBackendValidationAndWriteEvidence(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	d := &Daemon{}
	h := &handler{d: d}
	_, createErr := h.CreateSession(context.Background(), connect.NewRequest(&pb.CreateSessionRequest{}))
	_, settingsErr := d.updateSettings(&pb.DaemonSettings{ImageUploadQuotaBytes: 1})
	_, scheduleErr := h.Schedule(context.Background(), connect.NewRequest(&pb.ScheduleRequest{Operation: "create", SpecJson: []byte("{")}))
	_, uploadErr := newUploads(t.TempDir()).begin(&pb.BeginImageUploadRequest{TransferId: "invalid"}, "local")
	renameErr := (&session{}).renameUsing(strings.Repeat("x", renameMax+1), func(*meta, string) error { t.Fatal("validation reached storage"); return nil })
	for name, err := range map[string]error{"create": createErr, "settings": settingsErr, "schedule": scheduleErr, "upload": uploadErr, "rename": renameErr, "capacity": inputCapacityError()} {
		if err == nil || !outcome.IsFinalError(err) {
			t.Errorf("%s lost settled validation: %v", name, err)
		}
	}
	uncertain := errors.New("unconfirmed write")
	_, err := d.saveSettings(&pb.DaemonSettings{}, func(string, []byte) error { return uncertain })
	if !errors.Is(err, uncertain) || outcome.IsFinalError(err) {
		t.Fatalf("write failure became final: %v", err)
	}
}
