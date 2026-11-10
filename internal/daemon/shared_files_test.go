package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// sendHarness is a live session whose project folder, temp folder and home
// directory are separate test folders, so each rule can be hit on its own.
type sendHarness struct {
	*harness
	cwd, temp, home, outside string
}

func newSendHarness(t *testing.T) *sendHarness {
	t.Helper()
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	t.Cleanup(func() { h.s.log.Close() })
	sh := &sendHarness{harness: h, cwd: h.dir, temp: t.TempDir(), home: t.TempDir(), outside: t.TempDir()}
	h.d.uploads.tempRoots = []string{sh.temp}
	h.d.uploads.home = sh.home
	return sh
}

func (h *sendHarness) id() string { return h.s.meta.ID }

func (h *sendHarness) send(path string) (*pb.SharedFile, error) {
	return h.d.sendFile(context.Background(), h.id(), h.id(), path)
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func wantCode(t *testing.T, what string, err error, code connect.Code, text string) {
	t.Helper()
	if connect.CodeOf(err) != code || !strings.Contains(fmt.Sprint(err), text) {
		t.Fatalf("%s: got %v, want %v containing %q", what, err, code, text)
	}
}

// download reads every block the way a client does and returns the file.
func download(t *testing.T, u *uploads, session string, f *pb.SharedFile) []byte {
	t.Helper()
	var out []byte
	for off := int64(0); off == 0 || off < f.SizeBytes; off += sharedChunkBytes {
		res, err := u.sharedChunk(session, f.FileId, off)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Data) != sharedChunkBytes || res.TotalBytes != uint64(f.SizeBytes) {
			t.Fatalf("block at %d: %d bytes, total %d", off, len(res.Data), res.TotalBytes)
		}
		out = append(out, res.Data...)
	}
	if tail := out[f.SizeBytes:]; !bytes.Equal(tail, make([]byte, len(tail))) {
		t.Fatal("the last block is not padded with zeros")
	}
	return out[:f.SizeBytes]
}

func TestSendFileCopiesAndServesBlocks(t *testing.T) {
	h := newSendHarness(t)
	want := randomBytes(t, 2*sharedChunkBytes+100)
	writeTestFile(t, filepath.Join(h.cwd, "out", "chart data.bin"), want)
	f, err := h.send("out/chart data.bin")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(want)
	real, _ := filepath.EvalSymlinks(filepath.Join(h.cwd, "out", "chart data.bin"))
	if !validHexID(f.FileId) || f.Name != "chart_data.bin" || f.SizeBytes != int64(len(want)) || !bytes.Equal(f.Sha256, sum[:]) || f.Path != real {
		t.Fatalf("offer: %+v", f)
	}
	// A snapshot: later changes to the original are not sent.
	writeTestFile(t, filepath.Join(h.cwd, "out", "chart data.bin"), []byte("changed"))
	got := download(t, h.d.uploads, h.id(), f)
	if gotSum := sha256.Sum256(got); !bytes.Equal(got, want) || !bytes.Equal(gotSum[:], f.Sha256) {
		t.Fatal("downloaded bytes differ from the original")
	}
	for _, tc := range []struct {
		offset int64
		code   connect.Code
	}{
		{-sharedChunkBytes, connect.CodeInvalidArgument},
		{1, connect.CodeInvalidArgument},
		{sharedChunkBytes - 1, connect.CodeInvalidArgument},
		{3 * sharedChunkBytes, connect.CodeOutOfRange},
	} {
		if _, err := h.d.uploads.sharedChunk(h.id(), f.FileId, tc.offset); connect.CodeOf(err) != tc.code {
			t.Fatalf("offset %d: %v", tc.offset, err)
		}
	}
	if _, err := h.d.uploads.sharedChunk(h.id(), strings.Repeat("0", 32), 0); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown ID: %v", err)
	}
	if _, err := h.d.uploads.sharedChunk(h.id(), "../x", 0); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad ID: %v", err)
	}
	if _, err := h.d.uploads.sharedChunk("fedcba9876543210", f.FileId, 0); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("another session's ID: %v", err)
	}

	// An exact multiple of the block size has no padding; an empty file is
	// one block of zeros, so even it does not look different on the wire.
	for _, n := range []int{sharedChunkBytes, 0, 1} {
		want := randomBytes(t, n)
		path := filepath.Join(h.temp, fmt.Sprintf("f%d", n))
		writeTestFile(t, path, want)
		f, err := h.send(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := download(t, h.d.uploads, h.id(), f); !bytes.Equal(got, want) {
			t.Fatalf("%d bytes: wrong download", n)
		}
		if _, err := h.d.uploads.sharedChunk(h.id(), f.FileId, sharedChunkBytes); connect.CodeOf(err) != connect.CodeOutOfRange {
			t.Fatalf("%d bytes, second block: %v", n, err)
		}
	}
}

func TestSendFileRefusals(t *testing.T) {
	h := newSendHarness(t)
	writeTestFile(t, filepath.Join(h.outside, "secret.txt"), []byte("x"))
	writeTestFile(t, filepath.Join(h.cwd, "dir", "a.txt"), []byte("x"))
	// The project is the home directory here, the case the deny list is for.
	h.s.mu.Lock()
	h.s.meta.Cwd = h.home
	h.s.mu.Unlock()
	writeTestFile(t, filepath.Join(h.home, ".ssh", "id_ed25519"), []byte("key"))
	writeTestFile(t, filepath.Join(h.home, "Library", "Keychains", "login.keychain-db"), []byte("key"))
	writeTestFile(t, filepath.Join(h.home, "notes.txt"), []byte("ok"))
	if err := os.Symlink(filepath.Join(h.outside, "secret.txt"), filepath.Join(h.home, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h.home, ".ssh"), filepath.Join(h.home, "keys")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(h.home, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(h.home, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(maxSharedFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	big.Close()
	for _, tc := range []struct {
		path string
		code connect.Code
		text string
	}{
		{"", connect.CodeInvalidArgument, "path is required"},
		{"bad\x00name", connect.CodeInvalidArgument, "not a valid file path"},
		{"missing.txt", connect.CodeNotFound, "no file"},
		{filepath.Join(h.outside, "secret.txt"), connect.CodePermissionDenied, "copy the file into the project"},
		{"escape", connect.CodePermissionDenied, "copy the file into the project"},
		{".ssh/id_ed25519", connect.CodePermissionDenied, "credentials"},
		{"keys/id_ed25519", connect.CodePermissionDenied, "credentials"},
		{"Library/Keychains/login.keychain-db", connect.CodePermissionDenied, "credentials"},
		{".ssh", connect.CodePermissionDenied, "credentials"},
		{h.temp, connect.CodeInvalidArgument, "zip archive"},
		{"pipe", connect.CodeInvalidArgument, "regular files"},
		{"big.bin", connect.CodeInvalidArgument, "100 MiB"},
	} {
		_, err := h.send(tc.path)
		wantCode(t, fmt.Sprintf("%q", tc.path), err, tc.code, tc.text)
	}
	if _, err := h.send("notes.txt"); err != nil {
		t.Fatalf("an ordinary file in the project: %v", err)
	}

	// Only the session's own MCP may send, for its own session.
	if _, err := h.d.sendFile(context.Background(), h.id(), "", "notes.txt"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("no scope: %v", err)
	}
	if _, err := h.d.sendFile(context.Background(), h.id(), "fedcba9876543210", "notes.txt"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("another scope: %v", err)
	}
	if _, err := h.d.sendFile(context.Background(), "fedcba9876543210", "fedcba9876543210", "notes.txt"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
	// A client command never reaches send_file, even through the handler.
	ctx := agent.WithOrigin(context.Background(), &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "phone"})
	req := connect.NewRequest(&pb.SendFileRequest{SessionId: h.id(), Path: "notes.txt"})
	if _, err := (&handler{d: h.d}).SendFile(ctx, req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("client origin: %v", err)
	}
	h.s.mu.Lock()
	h.s.meta.Exited = true
	h.s.mu.Unlock()
	if _, err := h.send("notes.txt"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("exited session: %v", err)
	}
}

// Links are judged by where they lead, and the offer shows the real path.
func TestSendFileResolvesSymlinks(t *testing.T) {
	h := newSendHarness(t)
	writeTestFile(t, filepath.Join(h.cwd, "build", "report-v3.pdf"), []byte("pdf"))
	if err := os.Symlink(filepath.Join(h.cwd, "build", "report-v3.pdf"), filepath.Join(h.cwd, "report.pdf")); err != nil {
		t.Fatal(err)
	}
	// A link from outside into the project is fine: the bytes are the project's.
	if err := os.Symlink(filepath.Join(h.cwd, "build"), filepath.Join(h.outside, "build")); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(filepath.Join(h.cwd, "build", "report-v3.pdf"))
	for _, path := range []string{"report.pdf", filepath.Join(h.outside, "build", "report-v3.pdf")} {
		f, err := h.send(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if f.Path != real || f.Name != filepath.Base(path) {
			t.Fatalf("%s: %+v", path, f)
		}
	}
}

// The session's uploaded files sit inside the state root but may be sent;
// the rest of the state root and earlier copies may not.
func TestSendFileStateRoot(t *testing.T) {
	h := newSendHarness(t)
	h.d.uploads.tempRoots = []string{filepath.Dir(h.d.dir), h.temp}
	upload := filepath.Join(h.d.uploads.sessionDir(h.id()), "files", strings.Repeat("1", 32), "notes.txt")
	writeTestFile(t, upload, []byte("from the phone"))
	f, err := h.send(upload)
	if err != nil {
		t.Fatalf("uploaded file: %v", err)
	}
	writeTestFile(t, filepath.Join(h.d.dir, "sessions", h.id(), "key.json"), []byte("secret"))
	_, err = h.send(filepath.Join(h.d.dir, "sessions", h.id(), "key.json"))
	wantCode(t, "state root", err, connect.CodePermissionDenied, "Remote Agent's own state")
	_, err = h.send(filepath.Join(h.d.uploads.sharedDir(h.id()), f.FileId, sharedDataName))
	wantCode(t, "a shared copy", err, connect.CodePermissionDenied, "already a copy")
}

func TestSendFilePerTurnLimit(t *testing.T) {
	h := newSendHarness(t)
	writeTestFile(t, filepath.Join(h.cwd, "a.txt"), []byte("a"))
	setTurn := func(id string) {
		h.s.mu.Lock()
		h.s.turnID = id
		h.s.mu.Unlock()
	}
	setTurn("turn-1")
	for i := 0; i < maxSharedPerTurn; i++ {
		if _, err := h.send("a.txt"); err != nil {
			t.Fatalf("file %d: %v", i+1, err)
		}
	}
	_, err := h.send("a.txt")
	wantCode(t, "seventh file", err, connect.CodeResourceExhausted, "at most 6 files")
	// The count comes from the copies on disk, so a restart does not reset it.
	h.d.uploads = newUploads(h.d.dir)
	h.d.uploads.tempRoots, h.d.uploads.home = []string{h.temp}, h.home
	_, err = h.send("a.txt")
	wantCode(t, "after restart", err, connect.CodeResourceExhausted, "at most 6 files")
	setTurn("turn-2")
	if _, err := h.send("a.txt"); err != nil {
		t.Fatalf("next turn: %v", err)
	}
	if left := stagingDirs(t, h.d.uploads, h.id()); len(left) != 0 {
		t.Fatalf("refused copies left staging: %v", left)
	}
}

func stagingDirs(t *testing.T, u *uploads, session string) []string {
	t.Helper()
	entries, _ := os.ReadDir(u.sharedDir(session))
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			out = append(out, e.Name())
		}
	}
	return out
}

// Copies share the attachment quota with uploads and are protected while
// their session lives.
func TestSendFileQuota(t *testing.T) {
	h := newSendHarness(t)
	h.d.uploads.limits = func() int64 { return 12000 }
	writeTestFile(t, filepath.Join(h.cwd, "a.bin"), randomBytes(t, 5000))
	f, err := h.send("a.bin")
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.send("a.bin")
	wantCode(t, "over quota", err, connect.CodeResourceExhausted, "附件存储")
	if left := stagingDirs(t, h.d.uploads, h.id()); len(left) != 0 {
		t.Fatalf("refused copy left staging: %v", left)
	}
	// A client upload counts the copy too.
	sum := sha256.Sum256(make([]byte, 7000))
	if _, err := h.d.uploads.begin(&pb.BeginImageUploadRequest{TransferId: strings.Repeat("2", 32), FileName: "x.bin", TotalBytes: 7000, Sha256: sum[:]}, "phone"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("upload past the copy: %v", err)
	}
	usage, err := h.d.uploads.usage(false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ProtectedBytes < f.SizeBytes || len(usage.Sessions) != 1 || usage.Sessions[0].SessionId != h.id() || usage.Sessions[0].ProtectedBytes < f.SizeBytes {
		t.Fatalf("usage: %+v", usage)
	}
}

// A copy cut short by a crash is staging only: never offered, swept once
// stale. A committed copy survives a restart and goes when the session ends.
func TestSharedFilesLifecycle(t *testing.T) {
	h := newSendHarness(t)
	writeTestFile(t, filepath.Join(h.cwd, "a.txt"), []byte("hello"))
	f, err := h.send("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(h.d.uploads.sharedDir(h.id()), strings.Repeat("3", 32)+".tmp")
	fresh := filepath.Join(h.d.uploads.sharedDir(h.id()), strings.Repeat("4", 32)+".tmp")
	writeTestFile(t, filepath.Join(stale, sharedDataName), []byte("half"))
	writeTestFile(t, filepath.Join(fresh, sharedDataName), []byte("copying"))
	old := time.Now().Add(-2 * sharedStagingTTL)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.d.uploads.sessionStateDir(h.id()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (&meta{ID: h.id(), Cwd: h.cwd}).save(h.d.uploads.sessionStateDir(h.id())); err != nil {
		t.Fatal(err)
	}
	restarted := newUploads(h.d.dir)
	restarted.reconcile()
	restarted.gc(time.Now())
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale staging survived GC")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("GC removed a copy still in progress")
	}
	if got := download(t, restarted, h.id(), f); string(got) != "hello" {
		t.Fatalf("after restart: %q", got)
	}
	// Exited metadata found by GC (a crash before the exit cleanup) and the
	// exit cleanup itself both remove the copies with the session's uploads.
	if err := (&meta{ID: h.id(), Cwd: h.cwd, Exited: true}).save(h.d.uploads.sessionStateDir(h.id())); err != nil {
		t.Fatal(err)
	}
	restarted.gc(time.Now())
	if _, err := restarted.sharedChunk(h.id(), f.FileId, 0); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("after exit GC: %v", err)
	}
	writeTestFile(t, filepath.Join(h.cwd, "b.txt"), []byte("b"))
	h.d.uploads = restarted
	restarted.tempRoots, restarted.home = []string{h.temp}, h.home
	g, err := h.send("b.txt")
	if err != nil {
		t.Fatal(err)
	}
	restarted.dropSession(h.id())
	if _, err := restarted.sharedChunk(h.id(), g.FileId, 0); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("after session cleanup: %v", err)
	}
}

// The tool call that made the offer carries it, whichever CLI recorded it.
func TestAnnotateSendFileToolCall(t *testing.T) {
	h := newSendHarness(t)
	writeTestFile(t, filepath.Join(h.cwd, "a.txt"), []byte("hello"))
	f, err := h.send("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("5", 32)
	for _, tc := range []struct {
		name, title, output string
		want                bool
	}{
		{"claude/codex", "remote_agent.send_file", `{"file_id":"` + f.FileId + `","name":"a.txt","status":"offered"}`, true},
		{"antigravity", "remote_agent/send_file", `Output: "{\"file_id\": \"` + f.FileId + `\"}"`, true},
		{"another tool", "other.send_file", `{"file_id":"` + f.FileId + `"}`, false},
		{"unknown ID", "remote_agent.send_file", `{"file_id":"` + other + `"}`, false},
		{"refused", "remote_agent.send_file", `at most 6 files can be sent in one turn`, false},
	} {
		tc := tc
		e := &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{Kind: pb.ToolKind_TOOL_KIND_MCP, Title: tc.title, Output: tc.output}}}
		h.s.annotate([]*pb.Event{e})()
		got := e.GetToolCall().SharedFiles
		if tc.want != (len(got) == 1) || tc.want && (got[0].FileId != f.FileId || got[0].SizeBytes != 5 || got[0].Name != "a.txt") {
			t.Fatalf("%s: %+v", tc.name, got)
		}
	}
	// A copy in another session is not this call's, even with a matching ID.
	other2 := newSendHarness(t)
	e := &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{Kind: pb.ToolKind_TOOL_KIND_MCP, Title: "remote_agent.send_file", Output: `{"file_id":"` + f.FileId + `"}`}}}
	other2.s.annotate([]*pb.Event{e})()
	if len(e.GetToolCall().SharedFiles) != 0 {
		t.Fatal("annotated another session's copy")
	}
}

// Blocks are for the account's clients and local callers only.
func TestGetSharedFileChunkOrigins(t *testing.T) {
	h := newSendHarness(t)
	writeTestFile(t, filepath.Join(h.cwd, "a.txt"), []byte("hello"))
	f, err := h.send("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	req := func() *connect.Request[pb.GetSharedFileChunkRequest] {
		return connect.NewRequest(&pb.GetSharedFileChunkRequest{SessionId: h.id(), FileId: f.FileId})
	}
	hd := &handler{d: h.d}
	for _, o := range []*pb.Origin{nil, {Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "phone"}} {
		ctx := context.Background()
		if o != nil {
			ctx = agent.WithOrigin(ctx, o)
		}
		res, err := hd.GetSharedFileChunk(ctx, req())
		if err != nil || !bytes.HasPrefix(res.Msg.Data, []byte("hello")) {
			t.Fatalf("origin %v: %v", o, err)
		}
	}
	for _, o := range []*pb.Origin{{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}, {Kind: pb.OriginKind_ORIGIN_KIND_SYSTEM}, {Kind: pb.OriginKind_ORIGIN_KIND_UNSPECIFIED}} {
		if _, err := hd.GetSharedFileChunk(agent.WithOrigin(context.Background(), o), req()); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("origin %v: %v", o, err)
		}
	}
}

func TestCapabilitiesOfferSharedFiles(t *testing.T) {
	c := New(t.TempDir(), Config{}).capabilities()
	if !c.SharedFiles || c.MaxSharedFileBytes != 100<<20 || c.SharedFileChunkBytes != 768<<10 {
		t.Fatalf("capabilities: %+v", c)
	}
}
