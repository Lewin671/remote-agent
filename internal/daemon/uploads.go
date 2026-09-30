package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/imageproc"
	"github.com/Lewin671/remote-agent/internal/paths"
)

// Images sent from clients (architecture §17 "从客户端发图片"). A client
// sends each image to the machine in chunks before the message that uses
// it. Chunks are appended to uploads/partial/<transfer>.part and made
// durable one by one, so a client can ask how far the daemon got and go
// on from there. A complete upload is checked (length, hash, format, that
// it decodes), cleaned of metadata and moved to
// uploads/pending/<image ID>.<ext>; accepting a message that names it
// moves it on to uploads/<session>/, where the CLI reads it. Clients never
// choose a path. What is not used is deleted: unfinished transfers an
// hour after their last chunk, finished images after 24 hours, a
// session's images when it ends.

const (
	maxUploadBytes   = 20 << 20
	maxUploadChunk   = 768 << 10
	maxMessageImages = 6
	partialTTL       = time.Hour
	unusedTTL        = 24 * time.Hour
)

var uploadFormats = []pb.ImageFormat{
	pb.ImageFormat_IMAGE_FORMAT_PNG, pb.ImageFormat_IMAGE_FORMAT_JPEG,
	pb.ImageFormat_IMAGE_FORMAT_WEBP, pb.ImageFormat_IMAGE_FORMAT_GIF,
}

// transfer is uploads/partial/<id>.json.
type transfer struct {
	ID        string         `json:"id"`
	Device    string         `json:"device"`
	Format    pb.ImageFormat `json:"format"`
	Total     int64          `json:"total"`
	SHA256    []byte         `json:"sha256"`
	Persisted int64          `json:"persisted"`
	UpdatedMs int64          `json:"updated_ms"`
	// Set once complete.
	ImageID     string `json:"image_id,omitempty"`
	Width       int32  `json:"width,omitempty"`
	Height      int32  `json:"height,omitempty"`
	CompletedMs int64  `json:"completed_ms,omitempty"`
	// Set once a message used it.
	Session string `json:"session,omitempty"`
}

func (t *transfer) state() pb.ImageUploadState {
	switch {
	case t.Session != "":
		return pb.ImageUploadState_IMAGE_UPLOAD_STATE_USED
	case t.ImageID != "":
		return pb.ImageUploadState_IMAGE_UPLOAD_STATE_COMPLETE
	}
	return pb.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING
}

func (t *transfer) status() *pb.ImageUploadStatus {
	st := &pb.ImageUploadStatus{
		TransferId: t.ID, State: t.state(), PersistedBytes: t.Persisted, TotalBytes: t.Total,
		ImageId: t.ImageID, Width: t.Width, Height: t.Height, MaxChunkBytes: maxUploadChunk,
	}
	switch t.state() {
	case pb.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING:
		st.ExpiresMs = t.UpdatedMs + partialTTL.Milliseconds()
	case pb.ImageUploadState_IMAGE_UPLOAD_STATE_COMPLETE:
		st.ExpiresMs = t.CompletedMs + unusedTTL.Milliseconds()
	}
	return st
}

// uploads keeps the transfers. One lock covers them all: chunks are small
// and clients send one image at a time.
type uploads struct {
	dir string

	mu sync.Mutex
}

func newUploads() *uploads { return &uploads{dir: filepath.Join(paths.Dir(), "uploads")} }

func (u *uploads) partial(id string) string   { return filepath.Join(u.dir, "partial", id) }
func (u *uploads) pending(name string) string { return filepath.Join(u.dir, "pending", name) }
func (u *uploads) sessionDir(id string) string {
	return filepath.Join(u.dir, id)
}

// usable reports whether image files can live under dir: both CLIs only
// turn a pasted path into an image when it has no spaces.
func (u *uploads) usable() bool {
	return !strings.ContainsFunc(u.dir, unicode.IsSpace)
}

func (u *uploads) load(id string) (*transfer, error) {
	if !validHexID(id) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a transfer ID is 32 hex digits"))
	}
	b, err := os.ReadFile(u.partial(id) + ".json")
	if os.IsNotExist(err) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such transfer; it may have expired"))
	}
	if err != nil {
		return nil, err
	}
	t := &transfer{}
	if err := json.Unmarshal(b, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (u *uploads) save(t *transfer) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return writeFileSync(u.partial(t.ID)+".json", b)
}

func validHexID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

// imageOwner names who uploads, so that only they can continue and send
// it: the device that signed the command, or this computer. A device the
// server names for an unsigned command is no proof and may not handle
// images at all; unsigned text messages still work.
func imageOwner(o *pb.Origin) (string, error) {
	switch {
	case o == nil || o.Kind == pb.OriginKind_ORIGIN_KIND_LOCAL:
		return "local", nil
	case o.Kind == pb.OriginKind_ORIGIN_KIND_CLIENT && o.DeviceId != "":
		return o.DeviceId, nil
	}
	return "", connect.NewError(connect.CodePermissionDenied, errors.New("images need commands signed by the device; update the client"))
}

func (u *uploads) owned(t *transfer, device string) error {
	if t.Device != device {
		return connect.NewError(connect.CodePermissionDenied, errors.New("the transfer belongs to another device"))
	}
	return nil
}

func (u *uploads) begin(req *pb.BeginImageUploadRequest, device string) (*pb.ImageUploadStatus, error) {
	if !u.usable() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the remote-agent folder's path has spaces, which the CLIs do not take in image paths"))
	}
	switch {
	case !validHexID(req.TransferId):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a transfer ID is 32 hex digits"))
	case imageproc.Ext(req.Format) == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the format must be PNG, JPEG, WebP or GIF"))
	case req.TotalBytes <= 0 || req.TotalBytes > maxUploadBytes:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("an image must be 1 to %d bytes", maxUploadBytes))
	case len(req.Sha256) != sha256.Size:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("sha256 must be 32 bytes"))
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if t, err := u.load(req.TransferId); err == nil {
		if err := u.owned(t, device); err != nil {
			return nil, err
		}
		if t.Format != req.Format || t.Total != req.TotalBytes || !bytes.Equal(t.SHA256, req.Sha256) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("the transfer exists with other parameters"))
		}
		return t.status(), nil
	} else if connect.CodeOf(err) != connect.CodeNotFound {
		return nil, err
	}
	for _, d := range []string{filepath.Join(u.dir, "partial"), filepath.Join(u.dir, "pending")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	t := &transfer{ID: req.TransferId, Device: device, Format: req.Format, Total: req.TotalBytes, SHA256: req.Sha256, UpdatedMs: time.Now().UnixMilli()}
	f, err := os.OpenFile(u.partial(t.ID)+".part", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := u.save(t); err != nil {
		return nil, err
	}
	return t.status(), nil
}

func (u *uploads) put(req *pb.PutImageChunkRequest, device string) (*pb.ImageUploadStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	t, err := u.load(req.TransferId)
	if err != nil {
		return nil, err
	}
	if err := u.owned(t, device); err != nil {
		return nil, err
	}
	n := int64(len(req.Data))
	switch {
	case t.state() != pb.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING:
		if req.Offset+n <= t.Total {
			return t.status(), nil // a late retry of a chunk of a finished upload
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the transfer is complete"))
	case n > maxUploadChunk:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a chunk carries at most %d bytes", maxUploadChunk))
	case req.Offset < 0 || req.Offset > t.Persisted:
		return nil, connect.NewError(connect.CodeOutOfRange, fmt.Errorf("the next chunk starts at %d", t.Persisted))
	case req.Offset+n > t.Total:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the chunk goes past the image's length"))
	}
	f, err := os.OpenFile(u.partial(t.ID)+".part", os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Bytes the daemon has already must be the same: a retried chunk.
	if overlap := min(req.Offset+n, t.Persisted) - req.Offset; overlap > 0 {
		have := make([]byte, overlap)
		if _, err := f.ReadAt(have, req.Offset); err != nil {
			return nil, err
		}
		if !bytes.Equal(have, req.Data[:overlap]) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the chunk differs from the bytes already received there"))
		}
	}
	if req.Offset+n > t.Persisted {
		if _, err := f.WriteAt(req.Data[t.Persisted-req.Offset:], t.Persisted); err != nil {
			return nil, err
		}
		if err := f.Sync(); err != nil {
			return nil, err
		}
		t.Persisted = req.Offset + n
	}
	t.UpdatedMs = time.Now().UnixMilli()
	if err := u.save(t); err != nil {
		return nil, err
	}
	return t.status(), nil
}

func (u *uploads) status(id, device string) (*pb.ImageUploadStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	t, err := u.load(id)
	if err != nil {
		return nil, err
	}
	if err := u.owned(t, device); err != nil {
		return nil, err
	}
	return t.status(), nil
}

func (u *uploads) complete(id, device string) (*pb.ImageUploadStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	t, err := u.load(id)
	if err != nil {
		return nil, err
	}
	if err := u.owned(t, device); err != nil {
		return nil, err
	}
	if t.state() != pb.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING {
		return t.status(), nil
	}
	if t.Persisted != t.Total {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("only %d of %d bytes arrived", t.Persisted, t.Total))
	}
	data, err := os.ReadFile(u.partial(t.ID) + ".part")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	bad := func(msg string) error {
		// A transfer that cannot complete is dropped; the client starts
		// over with a new one.
		u.remove(t.ID)
		return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
	}
	if int64(len(data)) != t.Total || !bytes.Equal(sum[:], t.SHA256) {
		return nil, bad("the bytes do not match the hash")
	}
	if imageproc.Sniff(data) != t.Format {
		return nil, bad("the bytes are not the format given")
	}
	img, _, err := imageproc.Decode(data, imageproc.DefaultLimits)
	if err != nil {
		return nil, bad("the image does not decode")
	}
	// Clients remove metadata already; this makes sure no location reaches
	// the CLI's record if one did not.
	clean, err := imageproc.StripMetadata(data)
	if err != nil {
		return nil, bad("the image's metadata cannot be read")
	}
	format := imageproc.Sniff(clean)
	imageID := newID(16)
	if err := writeFileSync(u.pending(imageID+"."+imageproc.Ext(format)), clean); err != nil {
		return nil, err
	}
	b := img.Bounds()
	t.ImageID, t.Width, t.Height, t.CompletedMs, t.Format = imageID, int32(b.Dx()), int32(b.Dy()), time.Now().UnixMilli(), format
	if err := u.save(t); err != nil {
		return nil, err
	}
	_ = os.Remove(u.partial(t.ID) + ".part")
	return t.status(), nil
}

func (u *uploads) abort(id, device string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	t, err := u.load(id)
	if connect.CodeOf(err) == connect.CodeNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if err := u.owned(t, device); err != nil {
		return err
	}
	if t.Session != "" {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("a message already uses the image"))
	}
	u.remove(t.ID)
	return nil
}

// remove deletes a transfer and its files. Callers hold u.mu.
func (u *uploads) remove(id string) {
	b, err := os.ReadFile(u.partial(id) + ".json")
	if err == nil {
		t := &transfer{}
		if json.Unmarshal(b, t) == nil && t.ImageID != "" && t.Session == "" {
			_ = os.Remove(u.pending(t.ImageID + "." + imageproc.Ext(t.Format)))
		}
	}
	_ = os.Remove(u.partial(id) + ".part")
	_ = os.Remove(u.partial(id) + ".json")
}

// byImage finds the transfer that produced an image. Callers hold u.mu.
func (u *uploads) byImage(imageID string) *transfer {
	entries, _ := os.ReadDir(filepath.Join(u.dir, "partial"))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t, err := u.load(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil && t.ImageID == imageID {
			return t
		}
	}
	return nil
}

// queuedImage is an image of a queued message: its upload ID and file.
type queuedImage struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// take moves the images of a message being accepted into the session's
// folder and returns them in order. They must be complete uploads of the
// same device; an image this session already used (a message resent after
// it failed) may be named again. Nothing moves unless all can.
func (u *uploads) take(ids []string, device, sessionID string) ([]queuedImage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > maxMessageImages {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a message has at most %d images", maxMessageImages))
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	var ts []*transfer
	seen := map[string]bool{}
	for _, id := range ids {
		t := u.byImage(id)
		switch {
		case !validHexID(id) || t == nil:
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("image %s is not on this machine; upload it again", id))
		case seen[id]:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image %s is named twice", id))
		case t.Device != device:
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("image %s was sent by another device", id))
		case t.Session != "" && t.Session != sessionID:
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("image %s belongs to another session", id))
		}
		seen[id] = true
		ts = append(ts, t)
	}
	if err := os.MkdirAll(u.sessionDir(sessionID), 0o700); err != nil {
		return nil, err
	}
	var out []queuedImage
	var moved []*transfer
	undo := func() {
		for _, t := range moved {
			name := t.ImageID + "." + imageproc.Ext(t.Format)
			_ = os.Rename(filepath.Join(u.sessionDir(sessionID), name), u.pending(name))
		}
	}
	for _, t := range ts {
		name := t.ImageID + "." + imageproc.Ext(t.Format)
		dst := filepath.Join(u.sessionDir(sessionID), name)
		if t.Session == "" {
			if err := os.Rename(u.pending(name), dst); err != nil {
				undo()
				return nil, err
			}
			moved = append(moved, t)
		} else if _, err := os.Stat(dst); err != nil {
			undo()
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("image %s is gone; upload it again", t.ImageID))
		}
		out = append(out, queuedImage{ID: t.ImageID, Path: dst})
	}
	syncDir(u.sessionDir(sessionID))
	for _, t := range moved {
		t.Session = sessionID
		if err := u.save(t); err != nil {
			slog.Error("save transfer", "transfer", t.ID, "err", err)
		}
	}
	return out, nil
}

// check tells whether take would accept the images, without moving them.
func (u *uploads) check(ids []string, device string) error {
	if len(ids) > maxMessageImages {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a message has at most %d images", maxMessageImages))
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, id := range ids {
		t := u.byImage(id)
		switch {
		case !validHexID(id) || t == nil || t.Session != "":
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("image %s is not on this machine; upload it again", id))
		case t.Device != device:
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("image %s was sent by another device", id))
		}
	}
	return nil
}

// untake returns images of a message that was not accepted after all to
// the pending folder.
func (u *uploads) untake(imgs []queuedImage, sessionID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, im := range imgs {
		t := u.byImage(im.ID)
		if t == nil || t.Session != sessionID {
			continue
		}
		if os.Rename(im.Path, u.pending(filepath.Base(im.Path))) == nil {
			t.Session = ""
			_ = u.save(t)
		}
	}
}

// dropSession deletes the images a session was sent, once it has ended.
func (u *uploads) dropSession(sessionID string) {
	if sessionID == "" || strings.ContainsAny(sessionID, "/.") {
		return
	}
	_ = os.RemoveAll(u.sessionDir(sessionID))
}

// gc deletes unfinished transfers and unused images past their time, and
// the records of used ones after a day.
func (u *uploads) gc(now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	entries, _ := os.ReadDir(filepath.Join(u.dir, "partial"))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			if strings.HasSuffix(name, ".part") {
				if _, err := os.Stat(u.partial(strings.TrimSuffix(name, ".part")) + ".json"); os.IsNotExist(err) {
					_ = os.Remove(u.partial(name))
				}
			}
			continue
		}
		t, err := u.load(strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		var at int64
		switch t.state() {
		case pb.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING:
			at = t.UpdatedMs + partialTTL.Milliseconds()
		default:
			at = t.CompletedMs + unusedTTL.Milliseconds()
		}
		if now.UnixMilli() >= at {
			u.remove(t.ID)
		}
	}
}

// gcLoop runs gc every ten minutes until stop is closed.
func (u *uploads) gcLoop(stop <-chan struct{}) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		u.gc(time.Now())
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
