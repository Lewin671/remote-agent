package daemon

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	// Abort may have committed its intent while hashing, then failed to
	// unlink or sync. Deleting overrides both receiving and completed state.
	if current.Deleting {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("the transfer is being deleted"))
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
	return u.commitUpload(t, func(dst string) error {
		return os.Link(u.partial(t.ID)+".part", dst)
	})
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
			if t != nil && !t.Deleting && t.Returning && t.Device == device {
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
			case t == nil || t.Deleting || session == "" && t.Session != "":
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
	undo := func() {
		for t, old := range before {
			if old.Session == "" && t.Session != "" {
				t.Refs = nil
				// Includes an interrupted claim save or a move not yet renamed.
				// Never overwrite a failed return with the old unowned snapshot.
				if err := u.startReturn(t); err != nil {
					slog.Error("undo upload claim", "err", errClass(err))
				}
			} else {
				*t = old
				_ = u.save(t)
			}
		}
	}
	for _, t := range ts {
		newClaim := t.Session == ""
		if newClaim {
			t.Session = session
			t.Accepted = false
			if err := u.moveUpload(t); err != nil {
				undo()
				return out, err
			}
		}
		if t.Refs == nil {
			t.Refs = map[string]bool{}
		}
		t.Refs[request] = true
		t.AccessMs = time.Now().UnixMilli()
		if err := u.step("claim-reference", func() error { return u.save(t) }); err != nil {
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
		u.untakeClaim(t, f.NewClaim, session, request)
	}
}

// Callers hold u.mu. Only this call's newly claimed, unexposed upload can
// return to pending. Persist that intent before mkdir/rename, so a failure
// leaves recovery able to distinguish rejection from an accepted attachment.
func (u *uploads) untakeClaim(t *transfer, newClaim bool, session, request string) {
	if t == nil || t.Session != session {
		return
	}
	delete(t.Refs, request)
	returning := newClaim && !t.Accepted && len(t.Refs) == 0 && !t.Exposed
	var err error
	if returning {
		err = u.startReturn(t)
	} else {
		err = u.save(t)
	}
	if err != nil {
		slog.Error("return rejected upload", "err", errClass(err))
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
		if t != nil && !t.Returning && !t.Deleting && t.Session == session {
			delete(t.Refs, request)
			t.Accepted = true // Only durable accepted messages release references.
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
