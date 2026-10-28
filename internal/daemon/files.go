package daemon

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/imageproc"
	"golang.org/x/text/unicode/norm"
)

// safeFileName never exposes a client-chosen path or whitespace to the CLI.
func safeFileName(name string) string {
	name = norm.NFC.String(filepath.Base(name))
	var b strings.Builder
	underscore := false
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) && !strings.ContainsRune("._-+=,", r) {
			r = '_'
		}
		if r == '_' && underscore {
			continue
		}
		b.WriteRune(r)
		underscore = r == '_'
	}
	name = strings.TrimLeft(b.String(), ".-")
	if name == "" {
		return "file"
	}
	if len(name) <= 100 {
		return name
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	_, firstBytes := utf8.DecodeRuneInString(stem)
	ext := utf8Prefix(filepath.Ext(name), 100-firstBytes)
	return utf8Prefix(stem, 100-len(ext)) + ext
}

func utf8Prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (t *transfer) artifactID() string {
	if t.FileID != "" {
		return t.FileID
	}
	return t.ImageID
}
func (t *transfer) relativeName() string {
	if t.FileID != "" {
		return filepath.Join(t.FileID, t.SafeName)
	}
	return t.ImageID + "." + imageproc.Ext(t.Format)
}
func (t *transfer) sessionPath(u *uploads) string {
	if t.FileID != "" {
		return filepath.Join(u.sessionDir(t.Session), "files", t.relativeName())
	}
	return filepath.Join(u.sessionDir(t.Session), t.relativeName())
}

// syncUploadDirs makes every new directory entry durable, including the
// parent entry that names uploads itself. Tests can fail a specific fsync.
func (u *uploads) syncUploadDirs(dir string) error {
	sync := u.syncDir
	if sync == nil {
		sync = syncDir
	}
	for {
		if err := sync(dir); err != nil {
			return err
		}
		if dir == filepath.Dir(u.dir) {
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return errors.New("upload directory is outside the state root")
		}
		dir = parent
	}
}
func (u *uploads) mkdirUploadDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return u.syncUploadDirs(dir)
}

// Caller holds u.mu. Hash through an open descriptor without holding the
// global lock, then reload ownership/state and verify this is still the same
// partial file (abort + re-begin must never complete the old bytes).
func (u *uploads) completeFile(t *transfer) (*pb.ImageUploadStatus, error) {
	f, err := os.Open(u.partial(t.ID) + ".part")
	if err != nil {
		return nil, err
	}
	before, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	hash := u.hashFile
	if hash == nil {
		hash = io.Copy
	}
	h := sha256.New()
	u.mu.Unlock()
	n, hashErr := hash(h, f)
	f.Close()
	u.mu.Lock()
	current, err := u.load(t.ID)
	if err != nil {
		return nil, err
	}
	if err := u.owned(current, t.Device); err != nil {
		return nil, err
	}
	if current.state() != pb.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING {
		return u.durableFileStatus(current)
	}
	now, err := os.Stat(u.partial(t.ID) + ".part")
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, now) || current.FileName != t.FileName || current.Total != t.Total || !bytes.Equal(current.SHA256, t.SHA256) {
		return nil, connect.NewError(connect.CodeAborted, errors.New("the transfer changed while its bytes were being checked"))
	}
	if hashErr != nil {
		return nil, hashErr
	}
	t = current
	if n != t.Total || t.Persisted != t.Total || !bytes.Equal(h.Sum(nil), t.SHA256) {
		u.remove(t.ID)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the bytes do not match the hash"))
	}
	t.FileID, t.CompletedMs = newID(16), time.Now().UnixMilli()
	dst := u.pending(t.relativeName())
	if err := u.mkdirUploadDir(filepath.Dir(dst)); err != nil {
		_ = os.Remove(filepath.Dir(dst))
		return nil, err
	}
	// Keep the durable original until both the new link and its full ancestry
	// have been synced and the completion record has committed.
	if err := os.Link(u.partial(t.ID)+".part", dst); err != nil {
		_ = os.Remove(filepath.Dir(dst))
		return nil, err
	}
	if err := u.syncUploadDirs(filepath.Dir(dst)); err != nil {
		_ = os.RemoveAll(filepath.Dir(dst))
		return nil, err
	}
	if err := u.save(t); err != nil {
		if !fileReplaced(err) {
			_ = os.RemoveAll(filepath.Dir(dst))
		}
		return nil, err
	}
	_ = os.Remove(u.partial(t.ID) + ".part")
	return t.status(), nil
}

type queuedFile struct {
	// Ephemeral receipt for acceptance rollback, not session ownership state.
	NewClaim bool   `json:"-"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Path     string `json:"path"`
}
type messageUploads struct {
	Images []queuedImage
	Files  []queuedFile
}

func sentFiles(files []queuedFile) []*pb.SentFile {
	out := make([]*pb.SentFile, 0, len(files))
	for _, f := range files {
		out = append(out, &pb.SentFile{FileId: f.ID, Name: f.Name, SizeBytes: f.Size, Path: f.Path})
	}
	return out
}

func (u *uploads) byFile(id string) *transfer {
	entries, _ := os.ReadDir(filepath.Join(u.dir, "partial"))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t, err := u.load(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil && t.FileID == id {
			return t
		}
	}
	return nil
}

// Callers hold u.mu. Validate both lists before claiming or moving anything.
func (u *uploads) selectUploads(imageIDs, fileIDs []string, device, session string) ([]*transfer, error) {
	if len(imageIDs) > maxMessageImages || len(fileIDs) > maxMessageFiles {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a message has at most 6 images and 6 files"))
	}
	var out []*transfer
	seen := map[string]bool{}
	for kind, ids := range [][]string{imageIDs, fileIDs} {
		label := "image"
		if kind == 1 {
			label = "file"
		}
		for _, id := range ids {
			var t *transfer
			if validHexID(id) {
				if kind == 0 {
					t = u.byImage(id)
				} else {
					t = u.byFile(id)
				}
			}
			// A rejected claim may still be returning after an earlier I/O error.
			// Finish that intent before any new acceptance can reuse its session path.
			if t != nil && t.Returning && t.Device == device {
				if err := u.returnPending(t); err != nil {
					return nil, err
				}
				var err error
				t, err = u.load(t.ID)
				if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
					return nil, err
				}
			}
			switch {
			case t == nil || session == "" && t.Session != "":
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s %s is not on this machine; upload it again", label, id))
			case seen[id]:
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s %s is named twice", label, id))
			case t.Device != device:
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s %s was sent by another device", label, id))
			case t.Session != "" && t.Session != session:
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s %s belongs to another session", label, id))
			}
			path := u.pending(t.relativeName())
			if t.Session != "" {
				path = t.sessionPath(u)
			}
			if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s %s is not on this machine; upload it again", label, id))
			}
			seen[id] = true
			out = append(out, t)
		}
	}
	return out, nil
}
func (u *uploads) checkUploads(images, files []string, device string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, err := u.selectUploads(images, files, device, "")
	return err
}
func (u *uploads) takeUploads(images, files []string, device, session, request string) (messageUploads, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := messageUploads{}
	ts, err := u.selectUploads(images, files, device, session)
	if err != nil {
		return out, err
	}
	// Preserve every pre-call reference, including transfers already owned by
	// this session. A partial failure undoes all refs, not only new claims.
	before := map[*transfer]transfer{}
	for _, t := range ts {
		copy := *t
		copy.Refs = maps.Clone(t.Refs)
		before[t] = copy
	}
	var moved []*transfer
	undo := func() {
		// A failed reverse move must keep the session owner: startup can then
		// locate/reconcile either copy. Never describe session bytes as pending.
		retained := map[*transfer]bool{}
		for _, t := range moved {
			t.Returning = true
			if err := u.save(t); err != nil {
				retained[t] = true
				continue
			}
			if err := u.returnPending(t); err != nil {
				retained[t] = true
			}
		}
		for t, old := range before {
			owner := t.Session
			*t = old
			if retained[t] {
				t.Session = owner
				t.Returning = true
			}
			_ = u.save(t)
		}
	}
	for _, t := range ts {
		newClaim := t.Session == ""
		if newClaim {
			t.Session = session
			if err := u.mkdirUploadDir(filepath.Dir(t.sessionPath(u))); err != nil {
				undo()
				return out, err
			}
			// Persist ownership first so startup can finish a crash-cut move.
			if err := u.save(t); err != nil {
				undo()
				return out, err
			}
			src := u.pending(t.relativeName())
			if err := os.Rename(src, t.sessionPath(u)); err != nil {
				undo()
				return out, err
			}
			moved = append(moved, t)
			if err := u.syncUploadDirs(filepath.Dir(t.sessionPath(u))); err != nil {
				undo()
				return out, err
			}
			if err := u.syncUploadDirs(filepath.Dir(src)); err != nil {
				undo()
				return out, err
			}
			if t.FileID != "" {
				_ = os.Remove(filepath.Dir(src))
			}
		}
		if t.Refs == nil {
			t.Refs = map[string]bool{}
		}
		t.Refs[request] = true
		t.AccessMs = time.Now().UnixMilli()
		if err := u.save(t); err != nil {
			undo()
			return out, err
		}
		if t.FileID != "" {
			out.Files = append(out.Files, queuedFile{ID: t.FileID, Name: t.SafeName, Size: t.Total, Path: t.sessionPath(u), NewClaim: newClaim})
		} else {
			out.Images = append(out.Images, queuedImage{ID: t.ImageID, Path: t.sessionPath(u), NewClaim: newClaim})
		}
	}
	return out, nil
}

func (u *uploads) untakeFiles(files []queuedFile, session, request string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, f := range files {
		t := u.byFile(f.ID)
		if t == nil || t.Session != session {
			continue
		}
		delete(t.Refs, request)
		if !f.NewClaim || len(t.Refs) > 0 || t.Exposed {
			_ = u.save(t)
			continue
		}
		dst := u.pending(t.relativeName())
		src := t.sessionPath(u)
		if u.mkdirUploadDir(filepath.Dir(dst)) == nil && os.Rename(src, dst) == nil {
			_ = u.syncUploadDirs(filepath.Dir(dst))
			_ = os.Remove(filepath.Dir(src))
			t.Session = ""
			_ = u.save(t)
		}
	}
}

// Recognition is stateless: history, native terminal input and restarted queues
// all use the same exact path rule. Never read arbitrary agent-mentioned files.
func (u *uploads) recognizeFiles(session, text string) []*pb.SentFile {
	root := filepath.Join(u.sessionDir(session), "files") + string(filepath.Separator)
	var out []*pb.SentFile
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		path := fileLinePath(line)
		if !strings.HasPrefix(path, root) || filepath.Clean(path) != path {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(path, root), string(filepath.Separator))
		if len(parts) != 2 || !validHexID(parts[0]) || parts[1] != safeFileName(parts[1]) || seen[path] {
			continue
		}
		size := int64(0)
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
			size = fi.Size()
		}
		out = append(out, &pb.SentFile{FileId: parts[0], Name: parts[1], SizeBytes: size, Path: path})
		seen[path] = true
	}
	return out
}

func uploadDataPath(root, path, ext string) bool {
	rel, _ := filepath.Rel(root, path)
	parts := strings.Split(rel, string(filepath.Separator))
	return (len(parts) == 4 && parts[1] == "files") || (len(parts) == 3 && parts[0] == "pending") || ext == ".part" || ext == ".png" || ext == ".jpg" || ext == ".webp" || ext == ".gif"
}
func (in input) messageText() string {
	if len(in.Files) > 0 {
		return in.Composed
	}
	return in.Text
}
func composeFiles(text string, files []queuedFile, kind pb.Agent) string {
	if len(files) == 0 {
		return text
	}
	lines := make([]string, 0, len(files))
	for _, f := range files {
		line := "File: " + f.Path
		if kind == pb.Agent_AGENT_CLAUDE {
			line = `File: @"` + f.Path + `" (file)`
		}
		lines = append(lines, line)
	}
	if text != "" {
		return text + "\n\n" + strings.Join(lines, "\n")
	}
	return strings.Join(lines, "\n")
}
func fileMessageText(m *pb.UserMessage) string {
	lines := strings.Split(m.Text, "\n")
	for _, f := range m.Files {
		for i, line := range lines {
			if fileLinePath(line) == f.Path {
				lines[i] = ""
			}
		}
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}
func (u *uploads) releaseFiles(files []queuedFile, session, request string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, f := range files {
		t := u.byFile(f.ID)
		if t != nil && t.Session == session {
			delete(t.Refs, request)
			_ = u.save(t)
		}
	}
}

// Exact forms from native input/history and the current upload composer.
func fileLinePath(line string) string {
	if strings.HasPrefix(line, `File: @"`) && strings.HasSuffix(line, `" (file)`) {
		return strings.TrimSuffix(strings.TrimPrefix(line, `File: @"`), `" (file)`)
	}
	return strings.TrimPrefix(strings.TrimPrefix(line, "File: "), "@")
}

// A lost/failed metadata fsync may have left the new completion record visible.
// A retry must actually sync it before acknowledging durability and unlinking
// the retained partial, rather than just returning the visible status.
func (u *uploads) durableFileStatus(t *transfer) (*pb.ImageUploadStatus, error) {
	if err := u.save(t); err != nil {
		return nil, err
	}
	_ = os.Remove(u.partial(t.ID) + ".part")
	return t.status(), nil
}

// Complete a rejected claim's return after an interrupted/failed reverse move.
// Session stays authoritative until pending bytes and both directory entries
// are durable. This also handles a crash after rename but before metadata save.
func (u *uploads) returnPending(t *transfer) error {
	src := t.sessionPath(u)
	dst := u.pending(t.relativeName())
	if _, err := os.Stat(src); err == nil {
		if err := u.mkdirUploadDir(filepath.Dir(dst)); err != nil {
			return err
		}
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		if err := u.syncUploadDirs(filepath.Dir(dst)); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Dir(src)); err == nil {
			if err := u.syncUploadDirs(filepath.Dir(src)); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// If both copies vanished there is no directory entry left to sync.
	// Still release the provisional owner so the stale record can expire.
	owner, refs := t.Session, t.Refs
	t.Session, t.Returning = "", false
	t.Refs = nil
	if err := u.save(t); err != nil {
		t.Session, t.Returning = owner, true
		t.Refs = refs
		return err
	}
	if t.FileID != "" {
		_ = os.Remove(filepath.Dir(src))
	}
	return nil
}
