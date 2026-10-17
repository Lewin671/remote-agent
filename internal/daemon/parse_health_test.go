package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/internal/tail"
)

func TestParseFailureIsDurableAndDoesNotBlockFollowingRecords(t *testing.T) {
	h := newHarness(t, Config{})
	path := filepath.Join(h.dir, "rollout.jsonl")
	good := `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t"}}` + "\n"
	bad := `{"type":"event_msg","payload":{"type":"task_complete","duration_ms":"PRIVATE_BODY"}}` + "\n"
	unknown := `{"type":"future_schema","text":"PRIVATE_BODY"}` + "\n"
	end := `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t"}}` + "\n"
	os.WriteFile(path, []byte(good+bad+unknown+end), 0o600)
	h.s.meta.RolloutPath = path
	h.s.reader = tail.NewReader(path, 0)
	if !h.s.readRollout() {
		t.Fatal("permanent bad line blocked session")
	}
	if h.s.running {
		t.Fatal("following end not processed")
	}
	want := h.s.parseHealth
	if want.Failures != 1 || want.Unknown != 1 || want.Start != int64(len(good+bad)) || want.End != int64(len(good+bad+unknown)) {
		t.Fatalf("health: %+v", want)
	}
	data, err := os.ReadFile(filepath.Join(h.dir, "parse-health.json"))
	if err != nil || strings.Contains(string(data), "PRIVATE_BODY") {
		t.Fatalf("unsafe/missing diagnostic %s %v", data, err)
	}
	if !strings.Contains(h.s.info().StatusDetail, "failures=1 unknown=1") {
		t.Fatal("query hides parse health")
	}
	m := h.s.meta
	h.s.log.Close()
	h.open(m)
	if h.s.parseHealth != want {
		t.Fatal("diagnostics lost on restart")
	}
	if !h.s.readRollout() || h.s.parseHealth != want {
		t.Fatal("restart double counted failures")
	}
}

// No later event means the event log has only an in-memory checkpoint;
// replay after restart must still not count the same failed line twice.
func TestParseFailureReplayDeduplicatesWithoutFollowingEvent(t *testing.T) {
	h := newHarness(t, Config{})
	path := filepath.Join(h.dir, "rollout.jsonl")
	os.WriteFile(path, []byte("{bad secret\n{bad secret2\n"), 0o600)
	h.s.meta.RolloutPath = path
	h.s.reader = tail.NewReader(path, 0)
	if !h.s.readRollout() || h.s.parseHealth.Failures != 2 {
		t.Fatal("failures not counted")
	}
	m := h.s.meta
	h.s.log.Close()
	h.open(m)
	if !h.s.readRollout() || h.s.parseHealth.Failures != 2 {
		t.Fatalf("replay: %+v", h.s.parseHealth)
	}
}

func TestParseDiagnosticWriteFailureRetriesBeforeCheckpoint(t *testing.T) {
	h := newHarness(t, Config{})
	path := filepath.Join(h.dir, "rollout.jsonl")
	os.WriteFile(path, []byte("{bad\n"+`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t"}}`+"\n"), 0o600)
	h.s.meta.RolloutPath = path
	h.s.reader = tail.NewReader(path, 0)
	diagnostic := filepath.Join(h.dir, "parse-health.json")
	os.Mkdir(diagnostic, 0o700) // atomic rename must fail, even when run as root
	if h.s.readRollout() {
		t.Fatal("skipped without a durable diagnostic")
	}
	_, off := h.s.log.Checkpoint()
	if off != 0 || h.s.running || h.s.parseHealth.Failures != 0 {
		t.Fatal("failed diagnostic advanced state")
	}
	if !strings.Contains(h.s.info().StatusDetail, "storage failure") {
		t.Fatal("diagnostic storage failure invisible")
	}
	os.Remove(diagnostic)
	if !h.s.readRollout() || !h.s.running || h.s.parseHealth.Failures != 1 {
		t.Fatal("did not recover after storage repair")
	}
}

func TestParseFailureDiscardsParserPartialMutation(t *testing.T) {
	h := newHarness(t, Config{})
	path := filepath.Join(h.dir, "rollout.jsonl")
	lines := []string{
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c","input":"tools.exec_command({cmd:\"true\"})"}}`,
		`{"type":"event_msg","payload":{"type":"task_complete","duration_ms":"bad"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c","output":"Script completed\nOutput:\nok"}}`,
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	h.s.meta.RolloutPath = path
	h.s.reader = tail.NewReader(path, 0)
	if !h.s.readRollout() {
		t.Fatal("read failed")
	}
	n := 0
	for _, e := range h.events() {
		if c := e.GetToolCall(); c != nil && c.CallId == "c" {
			n++
		}
	}
	if h.s.parseHealth.Failures != 1 || n != 1 {
		t.Fatalf("health=%+v completions=%d", h.s.parseHealth, n)
	}
}
