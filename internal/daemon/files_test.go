package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/codex"
	"google.golang.org/protobuf/proto"
)

func TestSafeFileName(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"报告 一.csv", "报告_一.csv"}, {"../x.txt", "x.txt"}, {"../", "file"}, {"a\x00\n\tb.log", "a_b.log"},
		{"---.secret", "secret"}, {"...", "file"}, {"e\u0301.txt", "é.txt"}, {"a___  b+=,.txt", "a_b+=,.txt"},
		{strings.Repeat("中", 80) + ".csv", strings.Repeat("中", 32) + ".csv"},
	} {
		if got := safeFileName(tt.in); got != tt.want {
			t.Errorf("%q: %q want %q", tt.in, got, tt.want)
		}
	}
	for _, name := range []string{strings.Repeat("a", 300) + ".txt", "a." + strings.Repeat("中", 100), "中." + strings.Repeat("a", 150)} {
		got := safeFileName(name)
		if len(got) > 100 || !utf8.ValidString(got) || strings.HasPrefix(got, ".") || strings.HasPrefix(got, "-") {
			t.Fatalf("unsafe cap: %q", got)
		}
	}
}
func uploadFile(t *testing.T, u *uploads, name string, data []byte) string {
	t.Helper()
	id := newID(16)
	sum := sha256.Sum256(data)
	req := &pb.BeginImageUploadRequest{TransferId: id, FileName: name, TotalBytes: int64(len(data)), Sha256: sum[:]}
	if _, err := u.begin(req, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.begin(req, "phone"); err != nil {
		t.Fatal(err)
	}
	for off := 0; off < len(data); off += maxUploadChunk {
		if _, err := u.put(&pb.PutImageChunkRequest{TransferId: id, Offset: int64(off), Data: data[off:min(len(data), off+maxUploadChunk)]}, "phone"); err != nil {
			t.Fatal(err)
		}
	}
	st, err := u.complete(id, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if st.FileId == "" || st.ImageId != "" {
		t.Fatalf("wrong status: %v", st)
	}
	again, err := u.complete(id, "phone")
	if err != nil || again.FileId != st.FileId {
		t.Fatalf("not idempotent: %v %v", again, err)
	}
	return st.FileId
}
func TestFileTransferValidationAndOwnership(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	sum := sha256.Sum256([]byte("raw"))
	base := pb.BeginImageUploadRequest{TransferId: tid, FileName: "raw.txt", TotalBytes: 3, Sha256: sum[:]}
	for _, tt := range []struct {
		name   string
		change func(*pb.BeginImageUploadRequest)
	}{
		{"size", func(r *pb.BeginImageUploadRequest) { r.TotalBytes = maxFileUploadBytes + 1 }},
		{"negative", func(r *pb.BeginImageUploadRequest) { r.TotalBytes = -1 }},
		{"format", func(r *pb.BeginImageUploadRequest) { r.Format = pb.ImageFormat_IMAGE_FORMAT_PNG }},
		{"name", func(r *pb.BeginImageUploadRequest) { r.FileName = strings.Repeat("中", 86) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := proto.Clone(&base).(*pb.BeginImageUploadRequest)
			tt.change(r)
			if _, err := u.begin(r, "phone"); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatal(err)
			}
		})
	}
	if _, err := u.begin(&base, "phone"); err != nil {
		t.Fatal(err)
	}
	other := proto.Clone(&base).(*pb.BeginImageUploadRequest)
	other.FileName = "other.txt"
	if _, err := u.begin(other, "phone"); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatal(err)
	}
	if _, err := u.begin(&base, "other"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: []byte("raw")}, "other"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	if _, err := u.complete(tid, "other"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	if _, err := u.status(context.Background(), tid, "other"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	if err := u.abort(tid, "other"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	if _, err := u.complete(tid, "phone"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal(err)
	}
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: []byte("bad")}, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.complete(tid, "phone"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatal(err)
	}
	if _, err := u.status(context.Background(), tid, "phone"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	uploadFile(t, u, "empty.csv", nil)
	// Exact advertised boundary reserves successfully without allocating 100 MiB.
	base.TransferId = newID(16)
	base.TotalBytes = maxFileUploadBytes
	if _, err := u.begin(&base, "phone"); err != nil {
		t.Fatal(err)
	}
}
func TestFilesAcceptanceLifetimeQuotaAndRecovery(t *testing.T) {
	h := newHarness(t, Config{})
	u := h.d.uploads
	data := []byte("EXIF\x00unchanged\r\n中文")
	id := uploadFile(t, u, "../报告 一.csv", data)
	if err := u.checkUploads(nil, []string{id}, "other"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	image := storageUpload(t, u)
	if _, err := u.takeUploads([]string{image}, []string{id, newID(16)}, "phone", h.s.meta.ID, "bad"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	if err := u.check([]string{image}, "phone"); err != nil {
		t.Fatal("partial take moved image", err)
	}
	all, err := u.takeUploads([]string{image}, []string{id}, "phone", h.s.meta.ID, "r")
	if err != nil {
		t.Fatal(err)
	}
	f := all.Files[0]
	want := filepath.Join(u.dir, h.s.meta.ID, "files", id, "报告_一.csv")
	if f.Path != want {
		t.Fatalf("%s != %s", f.Path, want)
	}
	got, _ := os.ReadFile(f.Path)
	if !bytes.Equal(got, data) {
		t.Fatal("file bytes changed")
	}
	for path, mode := range map[string]os.FileMode{f.Path: 0600, filepath.Dir(f.Path): 0700, filepath.Dir(filepath.Dir(f.Path)): 0700} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != mode {
			t.Fatalf("permissions %s: %v %v", path, fi, err)
		}
	}
	if _, err := u.takeUploads(nil, []string{id}, "phone", "another", "r"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal(err)
	}
	if _, err := h.s.enqueueInput("inspect", "r", all.Images, all.Files, originLocal, pb.SendMode_SEND_MODE_QUEUE, nil, nil); err != nil {
		t.Fatal(err)
	}
	q, err := loadQueue(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Queue) != 1 || len(q.Queue[0].Files) != 1 || q.Queue[0].messageText() != "inspect\n\nFile: "+f.Path {
		t.Fatalf("recovery lost file: %+v", q)
	}
	// Restart reference restoration and cleanup cannot discard session files,
	// even without refs or an embedded image witness and after TTL expires.
	u.restoreRefs(h.s, []*transfer{u.byFile(id)})
	u.releaseFiles(all.Files, h.s.meta.ID, "r")
	tr := u.byFile(id)
	tr.CompletedMs = time.Now().Add(-48 * time.Hour).UnixMilli()
	tr.Embedded = true
	tr.AccessMs = tr.CompletedMs
	u.save(tr)
	usage, err := u.usage(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ProtectedBytes < int64(len(data)) {
		t.Fatal("file not protected", usage)
	}
	if _, err := os.Stat(f.Path); err != nil {
		t.Fatal("cleanup deleted file", err)
	}
	h.d.sessions[h.s.meta.ID] = h.s
	snap, err := h.d.imageStorageSnapshot(context.Background())
	if err != nil || snap.ProtectedBytes < int64(len(data)) {
		t.Fatalf("snapshot: %v %v", snap, err)
	}
	u.limits = nil
	u.quota = 1
	if _, err := u.begin(&pb.BeginImageUploadRequest{TransferId: newID(16), FileName: "new.txt", TotalBytes: 1, Sha256: make([]byte, 32)}, "phone"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.Path); err != nil {
		t.Fatal("LRU deleted file", err)
	}
	u.dropSession(h.s.meta.ID)
	if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
		t.Fatal("session cleanup left file", err)
	}
}
func TestFileCompositionConfirmationAndParsedRecognition(t *testing.T) {
	for _, kind := range []pb.Agent{pb.Agent_AGENT_CODEX, pb.Agent_AGENT_CLAUDE} {
		t.Run(kind.String(), func(t *testing.T) {
			h := newHarness(t, Config{})
			u := h.d.uploads
			id := uploadFile(t, u, "data.csv", []byte("x,y\n1,2"))
			all, err := u.takeUploads(nil, []string{id}, "phone", h.s.meta.ID, "r")
			if err != nil {
				t.Fatal(err)
			}
			prefix := "File: "
			if kind == pb.Agent_AGENT_CLAUDE {
				prefix = "File: @"
			}
			line := prefix + all.Files[0].Path
			if kind == pb.Agent_AGENT_CLAUDE {
				line = `File: @"` + all.Files[0].Path + `" (file)`
			}
			for _, text := range []string{"", "inspect\nplease"} {
				want := line
				if text != "" {
					want = text + "\n\n" + line
				}
				in := input{Text: text, Files: all.Files, Composed: composeFiles(text, all.Files, kind)}
				if in.messageText() != want || !in.matches(want) || in.matches(text) {
					t.Fatalf("bad composition/confirmation: %+v", in)
				}
				in.Images = []queuedImage{{ID: "image", Path: "/tmp/test.png"}}
				in.Indexes = []int32{1}
				if !in.matches("[Image #1] "+want) || in.matches(want) {
					t.Fatal("mixed confirmation lost evidence")
				}
			}
			var parser agent.Parser = codex.NewParser()
			record := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": map[string]any{"type": "UserMessage", "content": []map[string]any{{"type": "text", "text": "inspect\n\n" + line}}}}}
			if kind == pb.Agent_AGENT_CLAUDE {
				parser = claude.NewParser()
				record = map[string]any{"type": "user", "uuid": "u", "promptId": "p", "origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": "inspect\n\n" + line}}
			}
			b, _ := json.Marshal(record)
			evs, err := parser.Parse(b)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range evs {
				if m := e.GetUserMessage(); m != nil {
					found = true
					h.s.annotate([]*pb.Event{e})
					if len(m.Files) != 1 || m.Files[0].FileId != id || m.Files[0].SizeBytes != 7 || fileMessageText(m) != "inspect" {
						t.Fatalf("files not recognized: %v", m)
					}
					h.s.observe(e)
					if h.s.title != "inspect" {
						t.Fatal(h.s.title)
					}
				}
			}
			if !found {
				t.Fatal("parser did not emit user message")
			}
			m := &pb.UserMessage{Text: line, Files: u.recognizeFiles(h.s.meta.ID, line)}
			if titleFrom(fileMessageText(m)) != "" {
				t.Fatal("files-only title")
			}
			os.Remove(all.Files[0].Path)
			if refs := u.recognizeFiles(h.s.meta.ID, line); len(refs) != 1 || refs[0].SizeBytes != 0 {
				t.Fatal("missing file reference lost")
			}
			for _, bad := range []string{line + " extra", " " + line, strings.Replace(line, "/files/", "/other/", 1), line + "/../escape", strings.Replace(line, h.s.meta.ID, "other", 1)} {
				if len(u.recognizeFiles(h.s.meta.ID, bad)) != 0 {
					t.Fatalf("recognized %q", bad)
				}
			}
		})
	}
}
func TestInjectTextWithFilesAndImage(t *testing.T) {
	for _, image := range []bool{false, true} {
		t.Run(map[bool]string{false: "files-only", true: "mixed"}[image], func(t *testing.T) {
			h := newHarness(t, Config{})
			id := uploadFile(t, h.d.uploads, "read.txt", []byte("hello"))
			all, err := h.d.uploads.takeUploads(nil, []string{id}, "phone", h.s.meta.ID, "r")
			if err != nil {
				t.Fatal(err)
			}
			text := ""
			if image {
				text = "read these"
				all.Images = imagesFor(t, h.dir, "a.png")
				fakeCodexImages(t, h, map[string]bool{all.Images[0].Path: true})
			}
			if _, err := h.s.enqueueInput(text, "r", all.Images, all.Files, originLocal, pb.SendMode_SEND_MODE_STEER, nil, nil); err != nil {
				t.Fatal(err)
			}
			runInjector(t, h)
			eventually(t, "file message submitted", func() bool {
				for _, st := range inputStates(h, "r") {
					if st.Status == pb.InputStatus_INPUT_STATUS_SUBMITTED {
						return true
					}
				}
				return false
			})
			writes := strings.Join(h.term.written(), "")
			if !strings.Contains(writes, composeFiles(text, all.Files, pb.Agent_AGENT_CODEX)) {
				t.Fatal("not injected", writes)
			}
		})
	}
}
func TestFileReconcileInterruptedMove(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	id := uploadFile(t, u, "a.zip", []byte("ZIP"))
	tr := u.byFile(id)
	tr.Session = "s"
	tr.Refs = map[string]bool{"r": true}
	if err := u.save(tr); err != nil {
		t.Fatal(err)
	}
	u.reconcile()
	if _, err := os.Stat(tr.sessionPath(u)); err != nil {
		t.Fatal(err)
	}
	u.release("s")
	if _, err := os.Stat(u.pending(tr.relativeName())); err != nil {
		t.Fatal(err)
	}
}

// Keep image decoding helpers exercised with a real image alongside raw files.
func TestFileCapabilityWhitespace(t *testing.T) {
	h := newHarness(t, Config{})
	if !h.d.capabilities().FileUpload {
		t.Fatal("file capability missing")
	}
	h.d.uploads.dir = filepath.Join(t.TempDir(), "has space")
	if h.d.capabilities().FileUpload || h.d.capabilities().ImageUpload {
		t.Fatal("whitespace path advertised")
	}
	_ = testPNG(t, 1, 1, color.RGBA{A: 255})
}

func TestFileExpiryAndMixedAcceptanceRollback(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	fileID := uploadFile(t, u, "unused.csv", []byte("hello"))
	tr := u.byFile(fileID)
	tr.CompletedMs = time.Now().Add(-unusedTTL - time.Second).UnixMilli()
	if err := u.save(tr); err != nil {
		t.Fatal(err)
	}
	if _, err := u.status(context.Background(), tr.ID, "phone"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	u.gc(time.Now())
	if _, err := os.Stat(u.pending(tr.relativeName())); !os.IsNotExist(err) {
		t.Fatal("unused file did not expire", err)
	}
	sum := sha256.Sum256([]byte("abcdef"))
	id := newID(16)
	if _, err := u.begin(&pb.BeginImageUploadRequest{TransferId: id, FileName: "partial.bin", TotalBytes: 6, Sha256: sum[:]}, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: id, Data: []byte("abc")}, "phone"); err != nil {
		t.Fatal(err)
	}
	part, err := u.load(id)
	if err != nil {
		t.Fatal(err)
	}
	updated := part.UpdatedMs
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: id, Data: []byte("abc")}, "phone"); err != nil {
		t.Fatal(err)
	}
	part, _ = u.load(id)
	if part.UpdatedMs != updated {
		t.Fatal("duplicate chunk extended TTL")
	}
	part.UpdatedMs = time.Now().Add(-partialTTL - time.Second).UnixMilli()
	u.save(part)
	u.gc(time.Now())
	if _, err := os.Stat(u.partial(id) + ".part"); !os.IsNotExist(err) {
		t.Fatal("partial file did not expire")
	}
	image := storageUpload(t, u)
	fileID = uploadFile(t, u, "move.bin", []byte("bytes"))
	all, err := u.takeUploads([]string{image}, []string{fileID}, "phone", "session", "r")
	if err != nil {
		t.Fatal(err)
	}
	u.untake(all.Images, "session", "r")
	u.untakeFiles(all.Files, "session", "r")
	if err := u.checkUploads([]string{image}, []string{fileID}, "phone"); err != nil {
		t.Fatal("acceptance rollback did not return all uploads", err)
	}
	os.Remove(u.pending(u.byFile(fileID).relativeName()))
	if err := u.checkUploads(nil, []string{fileID}, "phone"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal("missing bytes passed pre-launch check", err)
	}
}

func TestQueuedFileRestoresThroughDaemonStartup(t *testing.T) {
	r := newRestoreHarness(t)
	sid := "00000000000000bb"
	dir := r.save(meta{ID: sid}, true)
	id := uploadFile(t, r.d.uploads, "saved.csv", []byte("a,b"))
	all, err := r.d.uploads.takeUploads(nil, []string{id}, "phone", sid, "r")
	if err != nil {
		t.Fatal(err)
	}
	in := input{RequestID: "r", Text: "saved", Files: all.Files, Composed: composeFiles("saved", all.Files, pb.Agent_AGENT_CODEX), Paused: true, Mode: pb.SendMode_SEND_MODE_QUEUE, Stage: pb.InputStage_INPUT_STAGE_WAITING}
	if err := (&queueState{Queue: []input{in}, Seen: []string{"r"}}).save(dir); err != nil {
		t.Fatal(err)
	}
	if err := r.restore(); err != nil {
		t.Fatal(err)
	}
	s := r.session(sid)
	if s == nil {
		t.Fatal("session not restored")
	}
	s.mu.Lock()
	got := s.queue.Queue[0]
	s.mu.Unlock()
	if len(got.Files) != 1 || got.Files[0].Path != all.Files[0].Path || !got.matches(in.Composed) || !got.Paused {
		t.Fatalf("restored input: %+v", got)
	}
	if _, err := r.d.uploads.usage(true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(got.Files[0].Path); err != nil {
		t.Fatal("startup/cleanup deleted queued file", err)
	}
}

func TestHeldFilesRequeueAndLateRecoveryKeepComposedText(t *testing.T) {
	h := claudeHarness(t, Config{})
	files := []queuedFile{{ID: strings.Repeat("f", 32), Name: "a.csv", Size: 3, Path: "/uploads/s/files/ffffffffffffffffffffffffffffffff/a.csv"}}
	in := input{RequestID: "r", Text: "original", Files: files, Composed: composeFiles("original", files, pb.Agent_AGENT_CLAUDE)}
	h.s.queue.Held = []input{in}
	moved, err := h.s.requeueHeld([]input{in}, "other")
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || !moved[0].matches(in.Composed) || len(moved[0].Files) != 1 {
		t.Fatalf("held file lost: %+v", moved)
	}
	q, err := loadQueue(h.dir)
	if err != nil || len(q.Queue) != 1 || !q.Queue[0].matches(in.Composed) {
		t.Fatalf("held persistence: %v %v", q, err)
	}
	// A submitting request whose event record arrives only after restart can
	// reconstruct file lines from the first shared queue event.
	l := newLogged(&queueState{}, []string{"late"})
	l.note(&pb.Event{Seq: 1, Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "late", Status: pb.InputStatus_INPUT_STATUS_QUEUED, Text: in.Text, Files: sentFiles(files)}}})
	l.note(&pb.Event{Seq: 2, Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "late", Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: pb.InputStage_INPUT_STAGE_SUBMITTING}}})
	l.note(&pb.Event{Seq: 3, Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "late", Status: pb.InputStatus_INPUT_STATUS_SUBMITTED}}})
	h.s.recent = nil
	h.s.meta.Agent = pb.Agent_AGENT_CLAUDE
	h.s.restoreRecent(l)
	if len(h.s.recent) != 1 || !h.s.recent[0].matches(in.Composed) {
		t.Fatal("late file confirmation lost after restart")
	}
}
