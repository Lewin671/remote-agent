package antigravity

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
)

// Every committed physical line is a possible daemon restart point. In
// particular, restart between a planner call and its GENERIC result must keep
// the call ID, input digest, question and full-log physical line alignment.
func TestRestoreAtEveryTranscriptBoundary(t *testing.T) {
	path := filepath.Join("testdata", "transcript", "transcript.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	live := NewParser()
	live.SetTranscriptPath(path)
	var events [][]*pb.Event
	for _, line := range lines {
		evs, err := live.Parse(line)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, evs)
	}
	offset := int64(0)
	for split := 0; split <= len(lines); split++ {
		p := NewParser()
		for _, group := range events[:split] {
			for _, e := range group {
				p.Replay(e)
			}
		}
		if err := p.Restore(path, offset); err != nil {
			t.Fatal(err)
		}
		for j := split; j < len(lines); j++ {
			got, err := p.Parse(lines[j])
			if err != nil {
				t.Fatalf("split %d line %d: %v", split, j, err)
			}
			if len(got) != len(events[j]) {
				t.Fatalf("split %d line %d: %d events want %d", split, j, len(got), len(events[j]))
			}
			for k := range got {
				if !proto.Equal(got[k], events[j][k]) {
					t.Fatalf("split %d line %d event %d differs: %v / %v", split, j, k, got[k], events[j][k])
				}
			}
		}
		if split < len(lines) {
			offset += int64(len(lines[split]))
		}
	}
}
func TestTruncatedLineUsesFullRecordAndChecksIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	full := `{"step_index":4,"type":"PLANNER_RESPONSE","content":"complete answer","thinking":"complete reasoning"}` + "\n"
	mustWrite(t, filepath.Join(dir, "transcript_full.jsonl"), []byte(full))
	p := NewParser()
	p.SetTranscriptPath(path)
	evs, err := p.Parse([]byte(`{"step_index":4,"type":"PLANNER_RESPONSE","content":"cut","truncated_fields":["content","thinking"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var text, thinking string
	for _, e := range evs {
		if m := e.GetAgentMessage(); m != nil {
			text = m.Text
		}
		if r := e.GetReasoning(); r != nil {
			thinking = r.Text
		}
	}
	if text != "complete answer" || thinking != "complete reasoning" {
		t.Fatalf("full fields %q %q", text, thinking)
	}
	p = NewParser()
	p.SetTranscriptPath(path)
	if _, err = p.Parse([]byte(`{"step_index":5,"type":"PLANNER_RESPONSE","truncated_fields":["content"]}`)); err == nil {
		t.Fatal("accepted mismatched full-log step")
	}
}
func TestHookAndTranscriptQuestionDeduplicate(t *testing.T) {
	a := New("agy", t.TempDir())
	p := NewParser()
	p.SetConversation("id")
	_, err := p.Parse([]byte(`{"step_index":3,"type":"USER_INPUT","content":"<USER_REQUEST>question</USER_REQUEST>"}`))
	if err != nil {
		t.Fatal(err)
	}
	h, err := a.DecodeHook([]byte(`{"hook_event_name":"PreToolUse","conversationId":"id","stepIdx":5,"toolCall":{"name":"ask_question","args":{"questions":[{"question":"Choose","options":["A","B"]}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range a.HookEvents(h) {
		p.Replay(e)
	}
	evs, err := p.Parse([]byte(`{"step_index":4,"type":"PLANNER_RESPONSE","tool_calls":[{"name":"ask_question","args":{"questions":"[{\"question\":\"Choose\",\"options\":[\"A\",\"B\"]}]"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.GetQuestionAsked() != nil {
			t.Fatal("duplicate question")
		}
	}
	evs, err = p.Parse([]byte(`{"step_index":5,"type":"GENERIC","content":"A1: B"}`))
	if err != nil || len(evs) != 1 || evs[0].GetQuestionAnswered().QuestionId != "id/step_5" {
		t.Fatalf("result %v %v", evs, err)
	}
}
func TestMediaRestrictedToTranscriptMedia(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "secret.png"), []byte("secret"))
	mustWrite(t, filepath.Join(dir, "outside.png"), []byte("secret"))
	if err := os.Mkdir(filepath.Join(dir, "media"), 0700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "media", "image with space.png"), []byte("image"))
	if err := os.Symlink(filepath.Join(dir, "secret.png"), filepath.Join(dir, "media", "link.png")); err != nil {
		t.Fatal(err)
	}
	p := NewParser()
	p.SetTranscriptPath(filepath.Join(dir, "transcript.jsonl"))
	refs := p.readMedia([]transcriptMedia{{MimeType: "image/png", URI: "file://" + filepath.Join(dir, "secret.png")}, {MimeType: "image/png", URI: "file://" + filepath.Join(dir, "media", "link.png")}, {MimeType: "image/png", URI: "file://" + filepath.Join(dir, "media", "image%20with%20space.png")}}, pb.ImageSource_IMAGE_SOURCE_TOOL)
	if len(refs) != 1 || !strings.HasSuffix(refs[0].Path, "image with space.png") || len(p.Images()) != 1 {
		t.Fatal("media confinement or URL decoding failed")
	}
}
func TestMalformedRecordDoesNotConsumePendingCall(t *testing.T) {
	p := NewParser()
	_, _ = p.Parse([]byte(`{"step_index":1,"type":"PLANNER_RESPONSE","tool_calls":[{"name":"run_command","args":{"CommandLine":"echo ok"}}]}`))
	if _, err := p.Parse([]byte(`bad`)); err == nil {
		t.Fatal("invalid record accepted")
	}
	evs, err := p.Parse([]byte(`{"step_index":2,"type":"GENERIC","status":"ERROR","content":"denied"}`))
	if err != nil || len(evs) != 1 {
		t.Fatalf("pending call lost %v %v", evs, err)
	}
	call := evs[0].GetToolCall()
	if call.InputDigest == "" || call.Status != "failed" {
		t.Fatal("missing result metadata")
	}
}
func TestNativeApprovalRejectsHistoryPrefixesAndChangedPrompt(t *testing.T) {
	a := New("agy", t.TempDir())
	screen := readScreenFixture(t, "permission-prompt.txt")
	hook := &agent.Hook{ToolName: "run_command", ToolInput: json.RawMessage(`{"CommandLine":"echo hello_perm_test"}`)}
	for _, wrong := range []string{"echo", "git push", "hello_perm_test"} {
		h := *hook
		h.ToolInput, _ = json.Marshal(map[string]string{"CommandLine": wrong})
		if _, ok := a.MatchApproval(wrong+"\n"+screen, &h); ok {
			t.Fatalf("matched history or prefix %q", wrong)
		}
	}
	term := &fakeTerm{screen: strings.ReplaceAll(screen, "echo hello_perm_test", "echo changed")}
	if err := a.AnswerApproval(term, hook, true, nil); err == nil || len(term.written) > 0 {
		t.Fatal("answered changed native prompt")
	}
}

func TestUploadedUserMediaRejectsPathsOutsideConversation(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "conv", ".system_generated", "logs")
	media := filepath.Join(root, "conv", ".user_uploaded")
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(media, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.png")
	mustWrite(t, secret, []byte("private"))
	if err := os.Symlink(secret, filepath.Join(media, "link.png")); err != nil {
		t.Fatal(err)
	}
	p := NewParser()
	p.SetTranscriptPath(filepath.Join(logs, "transcript.jsonl"))
	for _, uri := range []string{secret, "file://" + secret, filepath.Join(media, "link.png"), "relative.png", "https://example.com/image.png"} {
		if refs := p.readMedia([]transcriptMedia{{MimeType: "image/png", URI: uri}}, pb.ImageSource_IMAGE_SOURCE_USER); len(refs) != 0 {
			t.Fatalf("outside media accepted: %s", uri)
		}
	}
	if err := os.RemoveAll(media); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(secret), media); err != nil {
		t.Fatal(err)
	}
	if refs := p.readMedia([]transcriptMedia{{MimeType: "image/png", URI: filepath.Join(media, "secret.png")}}, pb.ImageSource_IMAGE_SOURCE_USER); len(refs) != 0 {
		t.Fatal("symlink media root escaped conversation")
	}
}
