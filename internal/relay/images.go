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
func (c *conn) uploadImages(ctx context.Context, key []byte, sessionID string, evs []*pb.Event, beforeUpload func() error) error {
	for _, e := range evs {
		for _, ref := range agent.EventImages(e) {
			if ref.Sync != pb.ImageSync_IMAGE_SYNC_SYNCED || ref.ImageId == "" {
				continue
			}
			if err := c.uploadImage(ctx, key, sessionID, ref.ImageId, beforeUpload); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *conn) uploadImage(ctx context.Context, key []byte, sessionID, imageID string, beforeUpload func() error) error {
	if err := c.security.endpoint.CheckLease(uint64(time.Now().UnixMilli())); err != nil {
		return err
	}
	for try := 0; ; try++ {
		u := c.imageState(sessionID, imageID)
		if u.Done || u.Failed {
			return nil
		}
		// Earlier events must not wait behind this image's RPC or retries.
		if err := beforeUpload(); err != nil {
			return err
		}
		payload := &pb.ImagePayload{}
		if proto.Unmarshal(u.Payload, payload) != nil {
			return c.imageUploaded(sessionID, imageID, false, 0)
		}
		env, err := c.security.sealMessage(&pb.Header{
			Kind: pb.PayloadKind_PAYLOAD_KIND_IMAGE, MachineId: c.acct.DeviceID, SessionId: sessionID,
			RequestId: imageID, Sender: c.acct.DeviceID,
		}, deviceauth.Image, key, payload)
		if errors.Is(err, deviceauth.ErrStale) || errors.Is(err, deviceauth.ErrClock) {
			return err
		}
		if err != nil || proto.Size(env) > 1<<20 {
			return c.imageUploaded(sessionID, imageID, false, 0)
		}
		res, err := c.machine.PutImage(ctx, connect.NewRequest(&pb.PutImageRequest{SessionId: sessionID, ImageId: imageID, Image: env}))
		if err == nil {
			return c.imageUploaded(sessionID, imageID, true, res.Msg.ExpiresMs)
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
			return c.imageUploaded(sessionID, imageID, false, 0)
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
			return c.imageUploaded(sessionID, imageID, false, 0)
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

// Image access may also touch the daemon's LRU metadata. Own that mutation
// through its completion, just like the upload-result index update.
func (c *conn) imageState(sessionID, imageID string) ImageUpload {
	var state ImageUpload
	if err := c.writeDisk(func() error {
		state = c.r.backend.Image(sessionID, imageID)
		return nil
	}); err != nil {
		return ImageUpload{Failed: true}
	}
	return state
}

// imageUploaded mutates the daemon's cache/index on behalf of this publisher.
// A canceled publisher must not start another such mutation after shutdown.
func (c *conn) imageUploaded(sessionID, imageID string, ok bool, expiresMs int64) error {
	return c.writeDisk(func() error {
		c.r.backend.ImageUploaded(sessionID, imageID, ok, expiresMs)
		return nil
	})
}

// withImageStates returns e, or a copy whose image refs say which display
// copies the server does not have after all.
func (c *conn) withImageStates(sessionID string, e *pb.Event) *pb.Event {
	refs := agent.EventImages(e)
	var failed []int
	for i, ref := range refs {
		if ref.Sync == pb.ImageSync_IMAGE_SYNC_SYNCED && c.imageState(sessionID, ref.ImageId).Failed {
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
