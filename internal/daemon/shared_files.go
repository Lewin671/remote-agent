package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// Files the agent hands to the user (session-files.md "Agent 给客户端发文件").
// The session MCP's send_file asks the daemon to copy one file; the copy
// lives in uploads/<session>/shared/<file id>/ next to the session's
// uploads, counts toward the same attachment quota and is removed with
// them when the session ends. Clients read it in fixed-size blocks.
const (
	maxSharedFileBytes = maxFileUploadBytes
	maxSharedPerTurn   = 6
	sharedChunkBytes   = maxUploadChunk
	sharedStagingTTL   = partialTTL
	sharedDataName     = "file"
	sharedRecordName   = "record.json"
)

// sharedRecord is uploads/<session>/shared/<file id>/record.json. It is
// written into the staging directory before the directory is renamed into
// place, so a committed copy always has its record.
type sharedRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    []byte `json:"sha256"`
	Path      string `json:"path"`
	Turn      string `json:"turn"`
	CreatedMs int64  `json:"created_ms"`
}

func (r *sharedRecord) proto() *pb.SharedFile {
	return &pb.SharedFile{FileId: r.ID, Name: r.Name, SizeBytes: r.Size, Sha256: r.SHA256, Path: r.Path}
}

func validSessionDir(id string) bool {
	return id != "" && id != "partial" && id != "pending" && !strings.ContainsAny(id, "/.\\")
}

func (u *uploads) sharedDir(session string) string {
	return filepath.Join(u.sessionDir(session), "shared")
}

func (u *uploads) loadShared(session, id string) (*sharedRecord, error) {
	if !validSessionDir(session) || !validHexID(id) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a shared file ID is 32 hex digits"))
	}
	b, err := os.ReadFile(filepath.Join(u.sharedDir(session), id, sharedRecordName))
	if os.IsNotExist(err) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("the file is no longer on the computer; shared files are removed when their session ends"))
	}
	if err != nil {
		return nil, err
	}
	r := &sharedRecord{}
	if err := json.Unmarshal(b, r); err != nil || r.ID != id {
		return nil, connect.NewError(connect.CodeDataLoss, errors.New("the shared file's record on the computer is damaged"))
	}
	return r, nil
}

// sharedInTurn counts the committed copies offered in turn. Callers hold u.mu.
func (u *uploads) sharedInTurn(session, turn string) (int, error) {
	entries, err := os.ReadDir(u.sharedDir(session))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() || !validHexID(e.Name()) {
			continue
		}
		if r, err := u.loadShared(session, e.Name()); err == nil && r.Turn == turn {
			n++
		}
	}
	return n, nil
}

func perTurnError() error {
	return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("at most %d files can be sent in one turn", maxSharedPerTurn))
}

// share copies src (already validated and opened) for session. The space
// is reserved under the upload lock by sizing the staging file, the copy
// and hash run without it, and the rename into place commits the copy.
func (u *uploads) share(session, turn string, drops uint64, src *os.File, before os.FileInfo, name, path string) (*sharedRecord, error) {
	if !validSessionDir(session) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid session"))
	}
	pins := u.protectedSessions()
	id := newID(16)
	staging := filepath.Join(u.sharedDir(session), id+".tmp")
	size := before.Size()
	u.mu.Lock()
	if u.drops[session] != drops {
		u.mu.Unlock()
		return nil, errEnded()
	}
	if n, err := u.sharedInTurn(session, turn); err != nil {
		u.mu.Unlock()
		return nil, err
	} else if n >= maxSharedPerTurn {
		u.mu.Unlock()
		return nil, perTurnError()
	}
	if err := u.makeRoom(size+4096, pins); err != nil {
		u.mu.Unlock()
		return nil, err
	}
	if err := u.mkdirUploadDir(u.sharedDir(session)); err != nil {
		u.mu.Unlock()
		return nil, err
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		u.mu.Unlock()
		return nil, err
	}
	dst, err := os.OpenFile(filepath.Join(staging, sharedDataName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		// The full length counts toward the quota while the bytes arrive.
		err = dst.Truncate(size)
	}
	u.mu.Unlock()
	cleanup := func() {
		if dst != nil {
			dst.Close()
		}
		_ = os.RemoveAll(staging)
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, size))
	if err == nil && n != size {
		err = errChanged
	}
	if err == nil {
		// More bytes than the size taken before the copy: it is still being written.
		var one [1]byte
		if k, _ := src.Read(one[:]); k > 0 {
			err = errChanged
		}
	}
	if err == nil {
		// Rewritten in place at the same length: the copy may mix old and new bytes.
		after, serr := src.Stat()
		if serr != nil {
			err = serr
		} else if after.Size() != size || !after.ModTime().Equal(before.ModTime()) {
			err = errChanged
		}
	}
	if err == nil {
		err = dst.Sync()
	}
	if err == nil {
		err = dst.Close()
		dst = nil
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	r := &sharedRecord{ID: id, Name: name, Size: size, SHA256: h.Sum(nil), Path: path, Turn: turn, CreatedMs: time.Now().UnixMilli()}
	b, _ := json.Marshal(r)
	if err := writeFileSync(filepath.Join(staging, sharedRecordName), b); err != nil {
		cleanup()
		return nil, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	// The session's files went while the copy ran; renaming into a folder
	// made again by this call would leave a copy nothing removes.
	if u.drops[session] != drops {
		cleanup()
		return nil, errEnded()
	}
	if n, err := u.sharedInTurn(session, turn); err != nil || n >= maxSharedPerTurn {
		cleanup()
		if err == nil {
			err = perTurnError()
		}
		return nil, err
	}
	if err := os.Rename(staging, filepath.Join(u.sharedDir(session), id)); err != nil {
		cleanup()
		return nil, err
	}
	if err := u.syncUploadDirs(u.sharedDir(session)); err != nil {
		// The rename may not survive a crash; withdraw the offer rather than report one.
		_ = os.RemoveAll(filepath.Join(u.sharedDir(session), id))
		return nil, err
	}
	return r, nil
}

func errEnded() error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("the session ended while the file was being copied"))
}

// sessionDrops counts how often session's files were removed. send_file
// takes it before checking that the session is alive: a removal after that
// check changes the count, and the copy is not offered.
func (u *uploads) sessionDrops(session string) uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.drops[session]
}

var errChanged = connect.NewError(connect.CodeAborted, errors.New("the file changed while it was being copied; send it again once it is complete"))

// sweepShared removes staging directories a crash or failed copy left
// behind. A copy in progress is far younger than the limit. Callers hold u.mu.
func (u *uploads) sweepShared(session string, now time.Time) {
	entries, _ := os.ReadDir(u.sharedDir(session))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) >= sharedStagingTTL {
			if err := os.RemoveAll(filepath.Join(u.sharedDir(session), e.Name())); err != nil {
				slog.Error("remove shared file staging", "err", errClass(err))
			}
		}
	}
}

// sharedChunk reads the block at offset, padded with zeros to the fixed
// block length, so every result has the same size.
func (u *uploads) sharedChunk(session, id string, offset int64) (*pb.GetSharedFileChunkResponse, error) {
	r, err := u.loadShared(session, id)
	if err != nil {
		return nil, err
	}
	switch {
	case offset < 0 || offset%sharedChunkBytes != 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("offset must be a multiple of %d", sharedChunkBytes))
	case offset >= r.Size && !(offset == 0 && r.Size == 0):
		return nil, connect.NewError(connect.CodeOutOfRange, fmt.Errorf("the file has %d bytes", r.Size))
	}
	f, err := os.Open(filepath.Join(u.sharedDir(session), id, sharedDataName))
	if os.IsNotExist(err) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("the file is no longer on the computer; shared files are removed when their session ends"))
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size() != r.Size {
		return nil, connect.NewError(connect.CodeDataLoss, errors.New("the shared copy on the computer is damaged"))
	}
	data := make([]byte, sharedChunkBytes)
	want := min(int64(sharedChunkBytes), r.Size-offset)
	if want > 0 {
		if _, err := f.ReadAt(data[:want], offset); err != nil {
			return nil, err
		}
	}
	return &pb.GetSharedFileChunkResponse{Data: data, TotalBytes: proto.Uint64(uint64(r.Size))}, nil
}

// sharedFiles resolves the IDs a send_file call returned to this session's
// copies. Unknown IDs are dropped: the output is the CLI's record.
func (u *uploads) sharedFiles(session string, ids []string) []*pb.SharedFile {
	var out []*pb.SharedFile
	for _, id := range ids {
		if r, err := u.loadShared(session, id); err == nil {
			out = append(out, r.proto())
		}
	}
	return out
}

// sendFileTool reports whether a parsed tool call is the session MCP's
// send_file: Claude Code and Codex title it server.tool, Antigravity
// server/tool.
func sendFileTool(c *pb.ToolCall) bool {
	return c.Kind == pb.ToolKind_TOOL_KIND_MCP && (c.Title == "remote_agent.send_file" || c.Title == "remote_agent/send_file")
}

// The tool's result is {"file_id":"<32 hex>",...}; CLIs may record it as
// text, escaped inside another JSON string, or with spacing.
var sharedIDPattern = regexp.MustCompile(`file_id\W{1,8}([0-9a-f]{32})\b`)

func sharedFileIDs(output string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, m := range sharedIDPattern.FindAllStringSubmatch(output, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			ids = append(ids, m[1])
		}
	}
	return ids
}

// sendTarget is where a session may send files from.
type sendTarget struct {
	allowed []string // real paths of the project, upload and temp folders
	uploads string   // this session's upload folder, inside the state root
	shared  string   // its shared copies, never re-sent
	denied  []string // credentials and Remote Agent state
}

// homeDenied are folders under the home directory that hold credentials,
// keys or browser profiles. The list guards against mistakes; an agent can
// copy any readable file into the project first (scope.md §4).
var homeDenied = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".netrc", ".git-credentials",
	".config/gcloud", ".config/gh", ".config/remote-agent", ".mozilla", ".config/google-chrome", ".config/chromium",
	"Library/Keychains", "Library/Cookies", "Library/Safari",
	"Library/Application Support/Google/Chrome", "Library/Application Support/Firefox",
}

func realPath(p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func within(path, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (d *Daemon) sendTarget(session, cwd string) sendTarget {
	u := d.uploads
	t := sendTarget{uploads: realPath(u.sessionDir(session)), shared: realPath(u.sharedDir(session))}
	temps := u.tempRoots
	if temps == nil {
		temps = []string{os.TempDir(), "/tmp"}
	}
	for _, p := range append([]string{cwd}, temps...) {
		if r := realPath(p); r != "" && filepath.IsAbs(r) {
			t.allowed = append(t.allowed, r)
		}
	}
	home := u.home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	t.denied = append(t.denied, realPath(d.dir))
	if home != "" {
		for _, p := range homeDenied {
			t.denied = append(t.denied, realPath(filepath.Join(home, p)))
		}
	}
	return t
}

// check decides on a real (symlink-free) path.
func (t sendTarget) check(path string) error {
	if within(path, t.shared) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("that is already a copy sent to the user"))
	}
	if within(path, t.uploads) {
		return nil
	}
	inside := false
	for _, root := range t.allowed {
		inside = inside || within(path, root)
	}
	if !inside {
		return connect.NewError(connect.CodePermissionDenied, errors.New("only files inside the project directory, this session's uploaded files or the system temporary directory can be sent; copy the file into the project directory first if the user wants it"))
	}
	for _, root := range t.denied {
		if within(path, root) {
			return connect.NewError(connect.CodePermissionDenied, errors.New("this location holds credentials, keys or Remote Agent's own state and is never sent"))
		}
	}
	return nil
}

// openSendFile resolves and opens what the agent named. The final open
// does not follow links and must reach the object that was checked.
func openSendFile(t sendTarget, cwd, raw string) (f *os.File, real string, size int64, err error) {
	switch {
	case strings.TrimSpace(raw) == "":
		return nil, "", 0, connect.NewError(connect.CodeInvalidArgument, errors.New("path is required"))
	case len(raw) > 4096 || !utf8.ValidString(raw) || strings.ContainsRune(raw, 0):
		return nil, "", 0, connect.NewError(connect.CodeInvalidArgument, errors.New("path is not a valid file path"))
	}
	abs := raw
	if !filepath.IsAbs(abs) {
		if cwd == "" {
			return nil, "", 0, connect.NewError(connect.CodeInvalidArgument, errors.New("give an absolute path"))
		}
		abs = filepath.Join(cwd, abs)
	}
	real, err = filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", 0, connect.NewError(connect.CodeNotFound, errors.New("no file at that path"))
		}
		return nil, "", 0, connect.NewError(connect.CodeFailedPrecondition, errors.New("the path cannot be read"))
	}
	if err := t.check(real); err != nil {
		return nil, "", 0, err
	}
	before, err := os.Lstat(real)
	if err != nil {
		return nil, "", 0, connect.NewError(connect.CodeNotFound, errors.New("no file at that path"))
	}
	if err := sendableMode(before.Mode()); err != nil {
		return nil, "", 0, err
	}
	// O_NONBLOCK: a FIFO swapped in after the check must not hang the open.
	f, err = os.OpenFile(real, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, "", 0, connect.NewError(connect.CodePermissionDenied, errors.New("the file cannot be read"))
		}
		return nil, "", 0, connect.NewError(connect.CodeAborted, errors.New("the file changed while it was being checked; try again"))
	}
	fi, err := f.Stat()
	if err == nil {
		err = sendableMode(fi.Mode())
	}
	if err == nil && !os.SameFile(before, fi) {
		err = connect.NewError(connect.CodeAborted, errors.New("the file changed while it was being checked; try again"))
	}
	if err == nil {
		// A parent directory swapped for a link after the check would lead
		// elsewhere; the path must still resolve to the checked place.
		if again, e := filepath.EvalSymlinks(abs); e != nil || again != real {
			err = connect.NewError(connect.CodeAborted, errors.New("the file changed while it was being checked; try again"))
		}
	}
	if err == nil && fi.Size() > maxSharedFileBytes {
		err = connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the file has %d bytes; at most %d bytes (100 MiB) can be sent", fi.Size(), maxSharedFileBytes))
	}
	if err != nil {
		f.Close()
		return nil, "", 0, err
	}
	return f, real, fi.Size(), nil
}

func sendableMode(m os.FileMode) error {
	switch {
	case m.IsRegular():
		return nil
	case m.IsDir():
		return connect.NewError(connect.CodeInvalidArgument, errors.New("that is a directory; put it in a zip archive and send the archive"))
	}
	return connect.NewError(connect.CodeInvalidArgument, errors.New("only regular files can be sent"))
}

// sendFile serves the bound session MCP's send_file. scope is the MCP's
// session; the request must name the same one.
func (d *Daemon) sendFile(ctx context.Context, sessionID, scope, raw string) (*pb.SharedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope == "" || scope != sessionID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("send_file requires this session's Remote Agent MCP"))
	}
	s, err := d.exact(scope)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such session"))
	}
	drops := d.uploads.sessionDrops(sessionID)
	var cwd, turn string
	var exited bool
	// The turn decides the per-turn limit; read the transcript first so the
	// call counts toward the turn that made it.
	_ = s.withLatestOrCachedContext("send_file", func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		cwd, turn, exited = s.meta.Cwd, s.turnID, s.meta.Exited || s.closing
		return nil
	})
	if exited {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the session has ended"))
	}
	f, real, _, err := openSendFile(d.sendTarget(sessionID, cwd), cwd, raw)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	name := safeFileName(filepath.Base(raw))
	r, err := d.uploads.share(sessionID, turn, drops, f, fi, name, real)
	if err != nil {
		return nil, err
	}
	return r.proto(), nil
}
