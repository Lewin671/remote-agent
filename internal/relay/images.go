package relay

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
)

// Display copies go up before the events that show them, so a client that
// sees an event can fetch its images (architecture §17). An upload that
// keeps failing must not hold the session's later events back: after a
// few tries the image is given up on and its events say so.

// maxImageTries bounds the uploads tried for one image, across
// reconnects, before it is given up on.
const maxImageTries = 5

// imageRetry is the wait between tries within one pass.
var imageRetry = 2 * time.Second

// uploadImages uploads the display copies the events refer to. It returns
// an error only for a failure worth reconnecting over (the server seems
// unreachable); images that cannot be stored are recorded as failed.
func (c *conn) uploadImages(ctx context.Context, key []byte, sessionID string, evs []*pb.Event) error {
	for _, e := range evs {
		for _, ref := range agent.EventImages(e) {
			if ref.Sync != pb.ImageSync_IMAGE_SYNC_SYNCED || ref.ImageId == "" {
				continue
			}
			if err := c.uploadImage(ctx, key, sessionID, ref.ImageId); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *conn) uploadImage(ctx context.Context, key []byte, sessionID, imageID string) error {
	b := c.r.backend
	for try := 0; ; try++ {
		u := b.Image(sessionID, imageID)
		if u.Done || u.Failed {
			return nil
		}
		payload := &pb.ImagePayload{}
		if proto.Unmarshal(u.Payload, payload) != nil {
			b.ImageUploaded(sessionID, imageID, false, 0)
			return nil
		}
		env, err := c.security.sealMessage(&pb.Header{
			Kind: pb.PayloadKind_PAYLOAD_KIND_IMAGE, MachineId: c.acct.DeviceID, SessionId: sessionID,
			RequestId: imageID, Sender: c.acct.DeviceID,
		}, deviceauth.Image, key, payload)
		if err != nil || proto.Size(env) > 1<<20 {
			b.ImageUploaded(sessionID, imageID, false, 0)
			return nil
		}
		res, err := c.machine.PutImage(ctx, connect.NewRequest(&pb.PutImageRequest{SessionId: sessionID, ImageId: imageID, Image: env}))
		if err == nil {
			b.ImageUploaded(sessionID, imageID, true, res.Msg.ExpiresMs)
			return nil
		}
		failure := rpcSyncError("upload_image", err)
		var se *syncError
		if errors.As(failure, &se) && se.connection {
			return failure
		}
		switch connect.CodeOf(err) {
		case connect.CodeResourceExhausted, connect.CodeInvalidArgument, connect.CodePermissionDenied, connect.CodeFailedPrecondition:
			// Will not change by trying again: the quota is full, or the
			// server refuses this copy.
			slog.Warn("relay: image not stored", "session", sessionID, "image", imageID, "code", connect.CodeOf(err).String())
			b.ImageUploaded(sessionID, imageID, false, 0)
			return nil
		}
		c.r.mu.Lock()
		if c.r.imageTries == nil {
			c.r.imageTries = map[string]int{}
		}
		c.r.imageTries[imageID]++
		tries := c.r.imageTries[imageID]
		c.r.mu.Unlock()
		if tries >= maxImageTries {
			slog.Warn("relay: giving up on image", "session", sessionID, "image", imageID, "code", connect.CodeOf(err).String())
			b.ImageUploaded(sessionID, imageID, false, 0)
			return nil
		}
		if try >= 2 {
			return failure
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-time.After(imageRetry):
		}
	}
}

// withImageStates returns e, or a copy whose image refs say which display
// copies the server does not have after all.
func (c *conn) withImageStates(sessionID string, e *pb.Event) *pb.Event {
	refs := agent.EventImages(e)
	var failed []int
	for i, ref := range refs {
		if ref.Sync == pb.ImageSync_IMAGE_SYNC_SYNCED && c.r.backend.Image(sessionID, ref.ImageId).Failed {
			failed = append(failed, i)
		}
	}
	if len(failed) == 0 {
		return e
	}
	e = proto.CloneOf(e)
	refs = agent.EventImages(e)
	for _, i := range failed {
		ref := refs[i]
		ref.DisplayWidth, ref.DisplayHeight = 0, 0
		ref.Sync = pb.ImageSync_IMAGE_SYNC_PREVIEW_ONLY
		if len(ref.Preview) == 0 {
			ref.Sync = pb.ImageSync_IMAGE_SYNC_NAME_ONLY
		}
	}
	return e
}
