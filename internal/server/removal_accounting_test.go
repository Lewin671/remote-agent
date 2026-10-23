package server

import (
	"context"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestDeviceRemovalDoesNotRewriteEventUsage(t *testing.T) {
	for _, kind := range []pb.DeviceKind{pb.DeviceKind_DEVICE_KIND_CLIENT, pb.DeviceKind_DEVICE_KIND_MACHINE} {
		t.Run(kind.String(), func(t *testing.T) {
			ts := newTestServer(t)
			_, id, err := ts.register(t, ts.keys, kind, "setup")
			if err != nil {
				t.Fatal(err)
			}
			// Any rewrite, even to the same value, proves removal still recounts
			// history. Actual archive deletion owns that accounting instead.
			if _, err := ts.srv.store.db.Exec(`CREATE TRIGGER forbid_usage_rewrite BEFORE UPDATE OF event_bytes ON accounts BEGIN SELECT RAISE(ABORT, 'unexpected recount'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := ts.tenant().removeDevice(context.Background(), id); err != nil {
				t.Fatal(err)
			}
		})
	}
}
