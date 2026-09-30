package daemon

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// G1: a device ID the server vouches for is no proof: images can only be
// uploaded and used by commands signed by the device, while unsigned text
// messages still work.
func TestImagesNeedAVerifiedDevice(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	hd := &handler{d: h.d}
	unverified := agent.WithOrigin(context.Background(), &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_UNVERIFIED_CLIENT, DeviceId: "0123456789abcdef0123456789abcdef"})
	_, err := hd.BeginImageUpload(unverified, connect.NewRequest(&pb.BeginImageUploadRequest{
		TransferId: tid, Format: pb.ImageFormat_IMAGE_FORMAT_PNG, TotalBytes: 10, Sha256: make([]byte, 32),
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("unsigned upload: %v", err)
	}
	_, err = hd.Send(unverified, connect.NewRequest(&pb.SendRequest{SessionId: h.s.meta.ID, Text: "x", ImageIds: []string{tid}}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("unsigned send with images: %v", err)
	}
	if _, err := hd.Send(unverified, connect.NewRequest(&pb.SendRequest{SessionId: h.s.meta.ID, Text: "plain", RequestId: "r9"})); err != nil {
		t.Fatalf("unsigned text: %v", err)
	}
}
