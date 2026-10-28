package daemon

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
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
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-+=,", r) {
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
	ext := utf8Prefix(filepath.Ext(name), 99)
	return utf8Prefix(strings.TrimSuffix(name, filepath.Ext(name)), 100-len(ext)) + ext
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

// Files are hashed as a stream. No decoding, metadata removal or 100 MiB buffer.
func (u *uploads) completeFile(t *transfer) (*pb.ImageUploadStatus, error) {
	f, err := os.Open(u.partial(t.ID) + ".part")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	f.Close()
	if err != nil {
		return nil, err
	}
	if n != t.Total || !bytes.Equal(h.Sum(nil), t.SHA256) {
		u.remove(t.ID)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the bytes do not match the hash"))
	}
	t.FileID, t.CompletedMs = newID(16), time.Now().UnixMilli()
	dst := u.pending(t.relativeName())
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return nil, err
	}
	// A hard link preserves the durable original through the record commit, with
	// no second copy of the bytes. Startup GC removes an interrupted orphan.
	if err := os.Link(u.partial(t.ID)+".part", dst); err != nil {
		return nil, err
	}
	syncDir(filepath.Dir(dst))
	if err := u.save(t); err != nil {
		_ = os.RemoveAll(filepath.Dir(dst))
		return nil, err
	}
	_ = os.Remove(u.partial(t.ID) + ".part")
	return t.status(), nil
}

type queuedFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Path string `json:"path"`
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
	// Persist ownership before each move; reconcile finishes an interrupted move.
	var claimed, moved []*transfer
	undo := func() {
		for _, t := range moved {
			_ = os.MkdirAll(filepath.Dir(u.pending(t.relativeName())), 0o700)
			_ = os.Rename(t.sessionPath(u), u.pending(t.relativeName()))
		}
		for _, t := range claimed {
			t.Session = ""
			delete(t.Refs, request)
			_ = u.save(t)
		}
	}
	for _, t := range ts {
		if t.Session == "" {
			t.Session = session
			if err := u.save(t); err != nil {
				t.Session = ""
				undo()
				return out, err
			}
			claimed = append(claimed, t)
			if err := os.MkdirAll(filepath.Dir(t.sessionPath(u)), 0o700); err != nil {
				undo()
				return out, err
			}
			if err := os.Rename(u.pending(t.relativeName()), t.sessionPath(u)); err != nil {
				undo()
				return out, err
			}
			moved = append(moved, t)
			syncDir(filepath.Dir(t.sessionPath(u)))
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
			out.Files = append(out.Files, queuedFile{t.FileID, t.SafeName, t.Total, t.sessionPath(u)})
		} else {
			out.Images = append(out.Images, queuedImage{t.ImageID, t.sessionPath(u)})
		}
	}
	syncDir(filepath.Join(u.dir, "pending"))
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
		if len(t.Refs) > 0 || t.Exposed {
			_ = u.save(t)
			continue
		}
		dst := u.pending(t.relativeName())
		if os.MkdirAll(filepath.Dir(dst), 0o700) == nil && os.Rename(t.sessionPath(u), dst) == nil {
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
		path := strings.TrimPrefix(line, "@")
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
		line := f.Path
		if kind == pb.Agent_AGENT_CLAUDE {
			line = "@" + line
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
			if line == f.Path || line == "@"+f.Path {
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
