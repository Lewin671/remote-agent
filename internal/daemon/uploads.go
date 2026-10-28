package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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
	maxFileUploadBytes = 100 << 20
	maxMessageFiles    = 6
	maxUploadBytes     = 20 << 20
	maxUploadChunk     = 768 << 10
	maxMessageImages   = 6
	partialTTL         = time.Hour
	unusedTTL          = 24 * time.Hour
	maxUploadStorage   = int64(1 << 30)
)

var uploadFormats = []pb.ImageFormat{
	pb.ImageFormat_IMAGE_FORMAT_PNG, pb.ImageFormat_IMAGE_FORMAT_JPEG,
	pb.ImageFormat_IMAGE_FORMAT_WEBP, pb.ImageFormat_IMAGE_FORMAT_GIF,
}

// transfer is uploads/partial/<id>.json.
type transfer struct {
	ID        string         `json:"id"`
	FileName  string         `json:"file_name,omitempty"`
	SafeName  string         `json:"safe_name,omitempty"`
	FileID    string         `json:"file_id,omitempty"`
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
	Session  string          `json:"session,omitempty"`
	Exposed  bool            `json:"exposed,omitempty"`
	Embedded bool            `json:"embedded,omitempty"`
	Refs     map[string]bool `json:"refs,omitempty"`
	AccessMs int64           `json:"access_ms,omitempty"`
}

func (t *transfer) state() pb.ImageUploadState {
	switch {
	case t.Session != "":
		return pb.ImageUploadState_IMAGE_UPLOAD_STATE_USED
	case t.artifactID() != "":
		return pb.ImageUploadState_IMAGE_UPLOAD_STATE_COMPLETE
	}
	return pb.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING
}

func (t *transfer) status() *pb.ImageUploadStatus {
	st := &pb.ImageUploadStatus{
		TransferId: t.ID, State: t.state(), PersistedBytes: t.Persisted, TotalBytes: t.Total,
		ImageId: t.ImageID, FileId: t.FileID, Width: t.Width, Height: t.Height, MaxChunkBytes: maxUploadChunk,
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

	mu       sync.Mutex
	quota    int64
	limits   func() int64
	pins     func() map[string]bool
	syncDir  func(string) error
	hashFile func(io.Writer, io.Reader) (int64, error)
}

func newUploads() *uploads {
	return &uploads{dir: filepath.Join(paths.Dir(), "uploads"), quota: maxUploadStorage}
}

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
	// Enforce the advertised expiry even between periodic GC passes.
	if t.Session == "" && time.Now().UnixMilli() >= t.expiresMs() {
		u.remove(t.ID)
		return nil, connect.NewError(connect.CodeNotFound, errors.New("the transfer expired; upload it again"))
	}
	return t, nil
}

func (t *transfer) expiresMs() int64 {
	if t.artifactID() == "" {
		return t.UpdatedMs + partialTTL.Milliseconds()
	}
	return t.CompletedMs + unusedTTL.Milliseconds()
}

func (u *uploads) save(t *transfer) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	sync := u.syncDir
	if sync == nil {
		sync = syncDir
	}
	return writeFileSyncDir(u.partial(t.ID)+".json", b, sync)
}

func validHexID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

// imageOwner names who uploads, so that only they can continue and send
// it: the device that signed the command, or this computer. The relay
// rejects unsigned commands before they get here, so any other origin is
// not a device and may not handle images.
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
	case req.FileName != "" && (!utf8.ValidString(req.FileName) || len(req.FileName) > 255):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("file_name must be at most 255 UTF-8 bytes"))
	case req.FileName != "" && req.Format != pb.ImageFormat_IMAGE_FORMAT_UNSPECIFIED:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a file transfer must use UNSPECIFIED format"))
	case req.FileName == "" && imageproc.Ext(req.Format) == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the format must be PNG, JPEG, WebP or GIF"))
	case req.FileName != "" && (req.TotalBytes < 0 || req.TotalBytes > maxFileUploadBytes):
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a file must be 0 to %d bytes", maxFileUploadBytes))
	case req.FileName == "" && (req.TotalBytes <= 0 || req.TotalBytes > maxUploadBytes):
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("an image must be 1 to %d bytes", maxUploadBytes))
	case len(req.Sha256) != sha256.Size:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("sha256 must be 32 bytes"))
	}
	pins := u.protectedSessions()
	u.mu.Lock()
	defer u.mu.Unlock()
	if t, err := u.load(req.TransferId); err == nil {
		if err := u.owned(t, device); err != nil {
			return nil, err
		}
		if t.FileName != req.FileName || t.Format != req.Format || t.Total != req.TotalBytes || !bytes.Equal(t.SHA256, req.Sha256) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("the transfer exists with other parameters"))
		}
		return t.status(), nil
	} else if connect.CodeOf(err) != connect.CodeNotFound {
		return nil, err
	}
	u.gcLocked(time.Now())
	if err := u.makeRoom(req.TotalBytes+1024, pins); err != nil {
		return nil, err
	}
	for _, d := range []string{filepath.Join(u.dir, "partial"), filepath.Join(u.dir, "pending")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	t := &transfer{FileName: req.FileName, ID: req.TransferId, Device: device, Format: req.Format, Total: req.TotalBytes, SHA256: req.Sha256, UpdatedMs: time.Now().UnixMilli()}
	if req.FileName != "" {
		t.SafeName = safeFileName(req.FileName)
	}
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
	case n == 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a chunk must not be empty"))
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
		t.UpdatedMs = time.Now().UnixMilli()
	}
	if err := u.save(t); err != nil {
		return nil, err
	}
	return t.status(), nil
}

func (u *uploads) complete(id, device string) (*pb.ImageUploadStatus, error) {
	pins := u.protectedSessions()
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
		if t.FileID != "" {
			return u.durableFileStatus(t)
		}
		return t.status(), nil
	}
	if t.Persisted != t.Total {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("only %d of %d bytes arrived", t.Persisted, t.Total))
	}
	if t.FileName != "" {
		return u.completeFile(t)
	}
	permit := imageproc.Acquire()
	defer permit.Release()
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
	img, _, err := permit.Decode(data, imageproc.DefaultLimits)
	if err != nil {
		return nil, bad("the image does not decode")
	}
	// Clients remove metadata already; this makes sure no location reaches
	// the CLI's record if one did not.
	clean, err := permit.StripMetadata(data)
	if err != nil {
		return nil, bad("the image's metadata cannot be read")
	}
	if len(clean) > maxUploadBytes {
		return nil, bad("the cleaned image exceeds the image size limit")
	}
	// The original .part goes away after commit; allow only this one
	// bounded extra copy while the cleaned file and its record are saved.
	if err := u.makeRoom(int64(len(clean)-len(data))+1024, pins); err != nil {
		return nil, err
	}
	format := imageproc.Sniff(clean)
	imageID := newID(16)
	if err := writeFileSync(u.pending(imageID+"."+imageproc.Ext(format)), clean); err != nil {
		return nil, err
	}
	b := img.Bounds()
	t.ImageID, t.Width, t.Height, t.CompletedMs, t.Format = imageID, int32(b.Dx()), int32(b.Dy()), time.Now().UnixMilli(), format
	if err := u.save(t); err != nil {
		_ = os.Remove(u.pending(imageID + "." + imageproc.Ext(format)))
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
		if json.Unmarshal(b, t) == nil && t.artifactID() != "" && t.Session == "" {
			path := u.pending(t.relativeName())
			if t.FileID != "" {
				path = filepath.Dir(path)
			}
			if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
				slog.Error("remove unused image", "image", t.ImageID, "err", errClass(err))
				return // keep its record so GC can retry
			}
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
	// Only this take call may undo a new claim. Never persisted in queue.json.
	NewClaim bool   `json:"-"`
	ID       string `json:"id"`
	Path     string `json:"path"`
}

// take moves the images of a message being accepted into the session's
// folder and returns them in order. They must be complete uploads of the
// same device; an image this session already used (a message resent after
// it failed) may be named again. Nothing moves unless all can.
func (u *uploads) take(ids []string, device, sessionID, request string) ([]queuedImage, error) {
	all, err := u.takeUploads(ids, nil, device, sessionID, request)
	return all.Images, err
}

func (u *uploads) check(ids []string, device string) error {
	return u.checkUploads(ids, nil, device)
}

// reconcile finishes moves a crash cut short: an image recorded as a
// session's but still in pending moves into the session's folder. It runs
// when the daemon starts.
func (u *uploads) reconcile() {
	u.mu.Lock()
	defer u.mu.Unlock()
	entries, _ := os.ReadDir(filepath.Join(u.dir, "partial"))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t, err := u.load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil || t.Session == "" || t.artifactID() == "" {
			continue
		}
		name := t.relativeName()
		if _, err := os.Stat(u.pending(name)); err != nil {
			continue
		}
		if err := u.mkdirUploadDir(filepath.Dir(t.sessionPath(u))); err == nil {
			if err := os.Rename(u.pending(name), t.sessionPath(u)); err != nil {
				slog.Error("finish image move", "image", t.ImageID, "err", errClass(err))
			} else if err := u.syncUploadDirs(filepath.Dir(t.sessionPath(u))); err != nil {
				slog.Error("sync upload move", "err", errClass(err))
			} else if t.FileID != "" {
				_ = os.Remove(filepath.Dir(u.pending(name)))
			}
		}
	}
}

// release gives the images a session was sent back to pending, for a
// session whose creation did not finish.
func (u *uploads) release(sessionID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	entries, _ := os.ReadDir(filepath.Join(u.dir, "partial"))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t, err := u.load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil || t.Session != sessionID || t.artifactID() == "" {
			continue
		}
		name := t.relativeName()
		src := t.sessionPath(u)
		if _, err := os.Stat(src); err == nil {
			if err := u.mkdirUploadDir(filepath.Dir(u.pending(name))); err != nil {
				continue
			}
			if err := os.Rename(src, u.pending(name)); err != nil {
				slog.Error("return image", "image", t.ImageID, "err", errClass(err))
				continue
			}
		}
		if err := u.syncUploadDirs(filepath.Dir(u.pending(name))); err != nil {
			slog.Error("sync returned upload", "err", errClass(err))
			continue
		}
		if t.FileID != "" {
			_ = os.Remove(filepath.Dir(src))
		}
		t.Session = ""
		t.Refs = nil
		if err := u.save(t); err != nil {
			slog.Error("save transfer", "transfer", t.ID, "err", errClass(err))
		}
	}
	_ = os.Remove(filepath.Join(u.sessionDir(sessionID), "files"))
	_ = os.Remove(u.sessionDir(sessionID))
}

// untake returns images of a message that was not accepted after all to
// the pending folder.
func (u *uploads) untake(imgs []queuedImage, sessionID, request string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, im := range imgs {
		t := u.byImage(im.ID)
		if t == nil || t.Session != sessionID {
			continue
		}
		delete(t.Refs, request)
		if !im.NewClaim || t.Exposed || len(t.Refs) > 0 {
			_ = u.save(t)
			continue
		}
		if os.Rename(im.Path, u.pending(filepath.Base(im.Path))) == nil {
			t.Session = ""
			t.Refs = nil
			_ = u.save(t)
		}
	}
}

// dropSession deletes files and ownership records together. Failed deletes
// retain the records, and periodic GC retries using the exited session's meta.
func (u *uploads) dropSession(sessionID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.dropSessionLocked(sessionID)
}

func (u *uploads) dropSessionLocked(sessionID string) {
	if sessionID == "" || sessionID == "partial" || sessionID == "pending" || strings.ContainsAny(sessionID, "/.\\") {
		return
	}
	if err := os.RemoveAll(u.sessionDir(sessionID)); err != nil {
		slog.Error("remove session images", "session", sessionID, "err", errClass(err))
		return
	}
	entries, _ := os.ReadDir(filepath.Join(u.dir, "partial"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t, err := u.load(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil && t.Session == sessionID {
				u.remove(t.ID)
			}
		}
	}
}

// gc keeps used records while the session runs (including retries after a
// day), expires unused uploads, and sweeps files left by interrupted writes.
func (u *uploads) gc(now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.gcLocked(now)
}

func (u *uploads) gcLocked(now time.Time) {
	entries, err := os.ReadDir(filepath.Join(u.dir, "partial"))
	if err != nil && !os.IsNotExist(err) {
		slog.Error("scan uploads", "err", errClass(err))
		return
	}
	referenced := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			if strings.HasSuffix(name, ".tmp") {
				_ = os.Remove(u.partial(name))
			} else if strings.HasSuffix(name, ".part") {
				if _, err := os.Stat(u.partial(strings.TrimSuffix(name, ".part")) + ".json"); os.IsNotExist(err) {
					_ = os.Remove(u.partial(name))
				}
			}
			continue
		}
		t, err := u.load(strings.TrimSuffix(name, ".json"))
		if err != nil {
			// Corrupt records cannot protect bytes indefinitely. Leave a
			// day to recover them manually before treating them as orphaned.
			if fi, e := e.Info(); e == nil && now.Sub(fi.ModTime()) >= unusedTTL {
				u.remove(strings.TrimSuffix(name, ".json"))
			}
			continue
		}
		if t.FileID == "" && t.Session != "" && len(t.Refs) == 0 && !t.Exposed {
			if err := u.removeOriginal(t); err == nil {
				continue
			}
		}
		if t.Session != "" {
			if m, err := loadMeta(paths.SessionDir(t.Session)); err == nil && m.Exited {
				// Retry record deletion even if the image directory was
				// already deleted before a crash or a failed unlink.
				u.dropSessionLocked(t.Session)
				continue
			}
		}
		if t.Session == "" && now.UnixMilli() >= t.expiresMs() {
			u.remove(t.ID)
			continue
		}
		if t.artifactID() != "" {
			referenced[strings.Split(t.relativeName(), string(filepath.Separator))[0]] = true
			// Complete uploads no longer need the received original.
			_ = os.Remove(u.partial(t.ID) + ".part")
		}
	}
	pending, _ := os.ReadDir(filepath.Join(u.dir, "pending"))
	for _, e := range pending {
		if referenced[e.Name()] {
			continue
		}
		fi, err := e.Info()
		if err == nil && (strings.HasSuffix(e.Name(), ".tmp") || now.Sub(fi.ModTime()) >= unusedTTL) {
			if err := os.RemoveAll(u.pending(e.Name())); err != nil {
				slog.Error("remove orphan image", "err", errClass(err))
			}
		}
	}
	// Covers a crash after Exited was saved but before dropSession.
	folders, _ := os.ReadDir(u.dir)
	for _, e := range folders {
		if !e.IsDir() || e.Name() == "partial" || e.Name() == "pending" {
			continue
		}
		m, err := loadMeta(paths.SessionDir(e.Name()))
		if err == nil && m.Exited {
			u.dropSessionLocked(e.Name())
		} else if os.IsNotExist(err) {
			// A failed session creation may have returned files to pending
			// and removed its meta, leaving an empty/orphan directory.
			if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) >= unusedTTL {
				u.dropSessionLocked(e.Name())
			}
		}
	}
}

// room counts disk bytes plus the yet-to-arrive bytes reserved by partial
// uploads. Never evict a live session's originals: Codex may read them again.
// Callers hold u.mu. Counting reservations prevents concurrent uploads from
// all passing a free-space check before their chunks arrive.
func (u *uploads) room(add int64) error {
	var used int64
	err := filepath.WalkDir(u.dir, func(path string, e fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !e.Type().IsRegular() {
			return nil
		}
		fi, err := e.Info()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		used += fi.Size()
		if filepath.Dir(path) == filepath.Join(u.dir, "partial") && strings.HasSuffix(e.Name(), ".json") {
			b, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			var t transfer
			if json.Unmarshal(b, &t) == nil && t.artifactID() == "" && validHexID(t.ID) {
				var received int64
				if fi, err := os.Stat(u.partial(t.ID) + ".part"); err == nil {
					received = fi.Size()
				} else if !os.IsNotExist(err) {
					return err
				}
				used += max(int64(0), t.Total-received)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	quota := u.quota
	if u.limits != nil {
		quota = u.limits()
	}
	if used+add > quota {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("这台电脑的附件存储已满；请结束不用的会话、等待未使用附件过期或提高限额"))
	}
	return nil
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

// errClass describes an error for the log without the file paths that
// os errors carry.
func errClass(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Op + ": " + le.Err.Error()
	}
	return err.Error()
}
