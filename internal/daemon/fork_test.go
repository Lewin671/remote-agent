package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The history a fork starts with is the conversation: not the source's
// process and queue, not requests nobody can answer any more, and of the
// images only what the events carry.
func TestForkHistory(t *testing.T) {
	at := timestamppb.New(time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC))
	ev := func(e *pb.Event) *pb.Event { e.Time, e.SessionId, e.TurnId = at, "src", "t1"; return e }
	in := []*pb.Event{
		ev(&pb.Event{Seq: 1, Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: pb.SessionStatus_SESSION_STATUS_STARTING}}}),
		ev(&pb.Event{Seq: 2, Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: "r1", Status: pb.InputStatus_INPUT_STATUS_QUEUED}}}),
		ev(&pb.Event{Seq: 3, Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}),
		ev(&pb.Event{Seq: 4, Origin: &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "phone"}, Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "look", RequestId: "r1", Images: []*pb.ImageRef{
			{ImageId: "i1", Sync: pb.ImageSync_IMAGE_SYNC_SYNCED, DisplayWidth: 800, DisplayHeight: 600, Preview: []byte("p"), UploadId: "up1"},
			{ImageId: "i2", Sync: pb.ImageSync_IMAGE_SYNC_SYNCED, DisplayWidth: 800, DisplayHeight: 600},
			{Sync: pb.ImageSync_IMAGE_SYNC_NAME_ONLY, Path: "/a.png"},
		}}}}),
		ev(&pb.Event{Seq: 5, Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a1"}}}),
		ev(&pb.Event{Seq: 6, Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{ApprovalId: "a1", Decision: pb.ApprovalDecision_APPROVAL_DECISION_ALLOW}}}),
		ev(&pb.Event{Seq: 7, Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a2"}}}),
		ev(&pb.Event{Seq: 8, Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "q1"}}}),
		ev(&pb.Event{Seq: 9, Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "done"}}}),
	}
	out := forkHistory(in)
	var kinds []string
	for _, e := range out {
		if e.Seq != 0 || e.SessionId != "" {
			t.Fatalf("a copy keeps the source's place: %v", e)
		}
		switch e.Kind.(type) {
		case *pb.Event_TurnStarted:
			kinds = append(kinds, "start")
		case *pb.Event_UserMessage:
			kinds = append(kinds, "user")
		case *pb.Event_ApprovalRequested:
			kinds = append(kinds, "ask:"+e.GetApprovalRequested().ApprovalId)
		case *pb.Event_ApprovalResolved:
			kinds = append(kinds, "resolved")
		case *pb.Event_AgentMessage:
			kinds = append(kinds, "agent")
		case *pb.Event_TurnEnded:
			kinds = append(kinds, "end:"+e.GetTurnEnded().Outcome.String())
		default:
			t.Fatalf("copied %v", e)
		}
	}
	want := []string{"start", "user", "ask:a1", "resolved", "agent", "end:TURN_OUTCOME_ABORTED"}
	if len(kinds) != len(want) {
		t.Fatalf("history %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("history %v, want %v", kinds, want)
		}
	}
	m := out[1]
	if m.Origin.GetDeviceId() != "phone" || !m.Time.AsTime().Equal(at.AsTime()) {
		t.Fatalf("who sent it and when are kept: %v", m)
	}
	refs := m.GetUserMessage().Images
	if refs[0].Sync != pb.ImageSync_IMAGE_SYNC_PREVIEW_ONLY || refs[0].DisplayWidth != 0 || refs[0].UploadId != "" || string(refs[0].Preview) != "p" ||
		refs[1].Sync != pb.ImageSync_IMAGE_SYNC_NAME_ONLY || refs[2].Sync != pb.ImageSync_IMAGE_SYNC_NAME_ONLY {
		t.Fatalf("images %v", refs)
	}
	// The source's events are untouched.
	if in[3].GetUserMessage().Images[0].Sync != pb.ImageSync_IMAGE_SYNC_SYNCED || in[3].Seq != 4 {
		t.Fatal("the source's event was changed")
	}
	if end := out[len(out)-1]; end.TurnId != "t1" || !end.Time.AsTime().Equal(at.AsTime()) {
		t.Fatalf("the open turn ends where the history does: %v", end)
	}
}

// A Claude Code fork's transcript starts with the source's records; the
// fork has them as events already and reads only what follows.
func TestForkSkipsCopiedTranscriptRecords(t *testing.T) {
	h := claudeHarness(t, Config{})
	h.s.log.Close()
	os.WriteFile(filepath.Join(h.dir, claude.CopiedFile), []byte("u-1\nu-2\n"), 0o600)
	log, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	copied := []*pb.Event{
		{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Text: "from the source"}}},
		{Kind: &pb.Event_SessionForked{SessionForked: &pb.SessionForked{SourceSessionId: "src"}}},
	}
	if err := log.Append("0123456789abcdef", copied, "", 0); err != nil {
		t.Fatal(err)
	}
	m := h.s.meta
	m.ForkedFrom, m.ForkedSeq = "src", 2
	s, err := newSession(h.d, h.dir, m, h.term, log)
	if err != nil {
		log.Close()
		t.Fatal(err)
	}
	h.s = s
	if info := h.s.info(); info.ForkedFrom != "src" || info.ForkedSeq != 2 || info.Title != "from the source" {
		t.Fatalf("info %v", info)
	}

	path := filepath.Join(h.dir, "rollout.jsonl")
	h.writeRollout(
		`{"type":"permission-mode","permissionMode":"default"}`,
		`{"type":"user","uuid":"u-1","origin":{"kind":"human"},"timestamp":"2026-10-03T10:00:00Z","message":{"role":"user","content":"from the source"}}`,
		`{"type":"assistant","uuid":"u-2","timestamp":"2026-10-03T10:00:01Z","message":{"role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"old answer"}]}}`,
		`{"type":"user","uuid":"u-3","origin":{"kind":"human"},"timestamp":"2026-10-03T10:05:00Z","message":{"role":"user","content":"in the fork"}}`,
	)
	h.s.bindRollout("fork-id", path)
	var texts []string
	for _, e := range h.events() {
		switch k := e.Kind.(type) {
		case *pb.Event_UserMessage:
			texts = append(texts, k.UserMessage.Text)
		case *pb.Event_AgentMessage:
			texts = append(texts, k.AgentMessage.Text)
		}
	}
	if len(texts) != 2 || texts[0] != "from the source" || texts[1] != "in the fork" {
		t.Fatalf("messages %q", texts)
	}
}
