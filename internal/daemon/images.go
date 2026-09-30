package daemon

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/imageproc"
	"github.com/Lewin671/remote-agent/internal/tail"
	"google.golang.org/protobuf/proto"
)

// Images in a session (architecture §17). When a parser finds an image,
// the daemon makes its preview, which goes into the event, and its display
// copy, which the relay uploads before the event. What each image ID
// stands for is kept in images/index.json in the session directory, so
// that a restart neither uploads twice nor loses what an event refers to.
//
// The same picture appearing again in the session (Codex stores an image
// once per look) reuses its ID and display copy. Pictures are recognized
// by an HMAC under a key of the session's own; the HMAC never leaves the
// computer, and image IDs are random, so the server cannot tell two
// images are the same.

// maxImageData bounds the recorded (base64) size of an image the daemon
// looks at.
const maxImageData = 64 << 20

// maxImageEnvelope keeps a sealed display copy under the server's 1 MiB
// envelope limit, with room for the header.
const maxImageEnvelope = 1<<20 - 2048

// Upload states of a display copy.
const (
	imagePending = "pending" // made, not yet on the server
	imageSynced  = "synced"
	imageFailed  = "failed" // gave up; events show the preview only
)

type storedImage struct {
	ID    string         `json:"id"`
	HMAC  string         `json:"hmac"`
	State string         `json:"state"`
	Ref   *storedRefInfo `json:"ref"`
	// ExpiresMs is when the server deletes the display copy.
	ExpiresMs int64 `json:"expires_ms,omitempty"`
}

// storedRefInfo is what a ref of this image gets filled in with.
type storedRefInfo struct {
	Format        pb.ImageFormat `json:"format"`
	Width         int32          `json:"width"`
	Height        int32          `json:"height"`
	Bytes         int64          `json:"bytes"`
	DisplayWidth  int32          `json:"display_width"`
	DisplayHeight int32          `json:"display_height"`
	DisplayFormat pb.ImageFormat `json:"display_format"`
	PreviewFormat pb.ImageFormat `json:"preview_format"`
	PreviewWidth  int32          `json:"preview_width"`
	PreviewHeight int32          `json:"preview_height"`
}

// imageStore is a session's images.
type imageStore struct {
	dir string // <session>/images

	mu    sync.Mutex
	key   []byte
	byID  map[string]*storedImage
	byMAC map[string]*storedImage
}

func openImageStore(sessionDir string) *imageStore {
	st := &imageStore{dir: filepath.Join(sessionDir, "images"), byID: map[string]*storedImage{}, byMAC: map[string]*storedImage{}}
	if b, err := os.ReadFile(filepath.Join(st.dir, "index.json")); err == nil {
		var list []*storedImage
		if json.Unmarshal(b, &list) == nil {
			for _, im := range list {
				if im == nil || im.Ref == nil || !validHexID(im.ID) {
					continue
				}
				st.byID[im.ID] = im
				st.byMAC[im.HMAC] = im
			}
		}
	}
	return st
}

func (st *imageStore) ensure() error {
	if st.key != nil {
		return nil
	}
	if err := os.MkdirAll(st.dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(st.dir, "key")
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		st.key = b
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if err := writeFileSync(path, key); err != nil {
		return err
	}
	st.key = key
	return nil
}

// saveLocked writes the index.
func (st *imageStore) saveLocked() error {
	list := make([]*storedImage, 0, len(st.byID))
	for _, im := range st.byID {
		list = append(list, im)
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return writeFileSync(filepath.Join(st.dir, "index.json"), b)
}

func (st *imageStore) displayPath(id string) string { return filepath.Join(st.dir, id+".display") }
func (st *imageStore) previewPath(id string) string { return filepath.Join(st.dir, id+".preview") }

// decodeImageData turns what a transcript records into bytes: base64,
// possibly as a data: URL, possibly left in the file as a blob marker.
func decodeImageData(data, transcript string) ([]byte, error) {
	if tail.IsBlob(data) {
		s, err := tail.ReadBlob(transcript, data, maxImageData)
		if err != nil {
			return nil, err
		}
		data = s
	}
	if len(data) > maxImageData {
		return nil, imageproc.ErrTooLarge
	}
	if strings.HasPrefix(data, "data:") {
		_, rest, ok := strings.Cut(data, ",")
		if !ok {
			return nil, errors.New("malformed data URL")
		}
		data = rest
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(data, "="))
	}
	return b, err
}

// process fills in the ref of a found image: its ID, sizes and preview,
// making and keeping its display copy for the relay. Whatever fails, the
// ref says what is left (a preview, or only its name).
func (st *imageStore) process(f agent.FoundImage, transcript string) {
	ref := f.Ref
	raw, err := decodeImageData(f.Data, transcript)
	if err != nil {
		ref.Sync = pb.ImageSync_IMAGE_SYNC_NAME_ONLY
		slog.Warn("image in transcript not readable", "err", errClass(err))
		return
	}
	ref.Bytes = int64(len(raw))
	ref.Format = imageproc.Sniff(raw)

	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.ensure(); err != nil {
		ref.Sync = pb.ImageSync_IMAGE_SYNC_NAME_ONLY
		slog.Error("image store", "err", errClass(err))
		return
	}
	mac := hmac.New(sha256.New, st.key)
	mac.Write(raw)
	sum := hex.EncodeToString(mac.Sum(nil))
	if im := st.byMAC[sum]; im != nil && st.reusable(im) && st.hasPreview(im) {
		st.fill(ref, im)
		return
	}

	pr, err := imageproc.Process(raw, imageproc.DefaultLimits, func(e imageproc.Encoded) bool {
		return proto.Size(&pb.ImagePayload{Format: e.Format, Width: int32(e.Width), Height: int32(e.Height), Data: e.Data}) <= maxImageEnvelope
	})
	if err != nil {
		ref.Sync = pb.ImageSync_IMAGE_SYNC_NAME_ONLY
		slog.Warn("image in transcript not decodable", "bytes", len(raw), "err", errClass(err))
		return
	}
	im := &storedImage{ID: newID(16), HMAC: sum, Ref: &storedRefInfo{
		Format: ref.Format, Width: int32(pr.Width), Height: int32(pr.Height), Bytes: int64(len(raw)),
	}}
	if p := pr.Preview; pr.PreviewErr == nil && writeFileSync(st.previewPath(im.ID), p.Data) == nil {
		im.Ref.PreviewFormat, im.Ref.PreviewWidth, im.Ref.PreviewHeight = p.Format, int32(p.Width), int32(p.Height)
	}
	im.State = imageFailed
	d, err := pr.Display, pr.DisplayErr
	if err == nil {
		payload, _ := proto.Marshal(&pb.ImagePayload{Format: d.Format, Width: int32(d.Width), Height: int32(d.Height), Data: d.Data})
		if err = writeFileSync(st.displayPath(im.ID), payload); err == nil {
			im.State = imagePending
			im.Ref.DisplayWidth, im.Ref.DisplayHeight, im.Ref.DisplayFormat = int32(d.Width), int32(d.Height), d.Format
		}
	}
	if err != nil {
		slog.Warn("no display copy for image", "image", im.ID, "err", errClass(err))
	}
	st.byID[im.ID] = im
	st.byMAC[sum] = im
	if err := st.saveLocked(); err != nil {
		slog.Error("save image index", "err", errClass(err))
	}
	st.fill(ref, im)
}

// reusable reports whether a stored image can stand for a new appearance:
// its display copy must not be about to expire on the server (a new
// reference does not extend it).
func (st *imageStore) reusable(im *storedImage) bool {
	return im.State != imageSynced || im.ExpiresMs == 0 || time.Until(time.UnixMilli(im.ExpiresMs)) > 24*time.Hour
}

// fill sets ref from a stored image. Callers hold st.mu.
func (st *imageStore) fill(ref *pb.ImageRef, im *storedImage) {
	r := im.Ref
	ref.ImageId = im.ID
	ref.Format, ref.Width, ref.Height, ref.Bytes = r.Format, r.Width, r.Height, r.Bytes
	if r.PreviewFormat != pb.ImageFormat_IMAGE_FORMAT_UNSPECIFIED {
		if b, err := os.ReadFile(st.previewPath(im.ID)); err == nil {
			_ = os.Chtimes(st.previewPath(im.ID), time.Now(), time.Now())
			ref.Preview, ref.PreviewFormat, ref.PreviewWidth, ref.PreviewHeight = b, r.PreviewFormat, r.PreviewWidth, r.PreviewHeight
		}
	}
	switch {
	case im.State != imageFailed:
		// Synced, or pending: the relay uploads it before the event and
		// downgrades the event's copy if it cannot.
		ref.Sync = pb.ImageSync_IMAGE_SYNC_SYNCED
		ref.DisplayWidth, ref.DisplayHeight = r.DisplayWidth, r.DisplayHeight
	case ref.Preview != nil:
		ref.Sync = pb.ImageSync_IMAGE_SYNC_PREVIEW_ONLY
	default:
		ref.Sync = pb.ImageSync_IMAGE_SYNC_NAME_ONLY
	}
}

// ImageToUpload is a display copy the relay must upload.
type ImageToUpload struct {
	Payload []byte // a serialized ImagePayload
	// Done: already on the server, nothing to do.
	Done bool
	// Failed: given up on; events show the preview only.
	Failed bool
}

// upload returns what the relay needs for image id.
func (st *imageStore) upload(id string) ImageToUpload {
	st.mu.Lock()
	defer st.mu.Unlock()
	im := st.byID[id]
	switch {
	case im == nil || im.State == imageFailed:
		return ImageToUpload{Failed: true}
	case im.State == imageSynced:
		return ImageToUpload{Done: true}
	}
	b, err := os.ReadFile(st.displayPath(id))
	if err != nil {
		return ImageToUpload{Failed: true}
	}
	_ = os.Chtimes(st.displayPath(id), time.Now(), time.Now())
	return ImageToUpload{Payload: b}
}

// uploaded records the outcome of an upload. The display copy is deleted
// either way: the server has it, or it is given up on.
func (st *imageStore) uploaded(id string, ok bool, expiresMs int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	im := st.byID[id]
	if im == nil {
		return
	}
	if ok {
		im.State, im.ExpiresMs = imageSynced, expiresMs
	} else {
		im.State = imageFailed
	}
	if err := st.saveLocked(); err != nil {
		slog.Error("save image index", "err", errClass(err))
		return
	}
	_ = os.Remove(st.displayPath(id))
}

// capabilities says what this daemon offers beyond the base protocol.
func (d *Daemon) capabilities() *pb.MachineCapabilities {
	c := &pb.MachineCapabilities{SessionImages: true, CommandSignatures: true, ImageStorage: true, SendModes: true, ApprovalAlways: true, ScheduledTasks: true}
	if d.uploads.usable() {
		c.ImageUpload = true
		c.MaxImagesPerMessage = maxMessageImages
		c.MaxUploadBytes = maxUploadBytes
		c.MaxChunkBytes = maxUploadChunk
		c.UploadFormats = uploadFormats
	}
	return c
}

func (st *imageStore) hasPreview(im *storedImage) bool {
	if im.Ref.PreviewFormat == pb.ImageFormat_IMAGE_FORMAT_UNSPECIFIED {
		return true
	}
	_, err := os.Stat(st.previewPath(im.ID))
	return err == nil
}
