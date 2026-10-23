package daemon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/tail"
	"google.golang.org/protobuf/proto"
)

type observingParser struct {
	agent.Parser
	before func()
}

func (p *observingParser) Parse(line []byte) ([]*pb.Event, error) {
	p.before()
	return p.Parser.Parse(line)
}

func groupMessage(i int) string {
	return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"m-%d","content":[{"type":"Text","text":"message %d"}]}}}`, i, i)
}

func TestReadBatchGroupsOnlyReadyLines(t *testing.T) {
	h := newHarness(t, Config{})
	defer func() { h.s.log.Close() }()
	h.writeRollout(taskStarted)
	h.s.bindRollout("cli", filepath.Join(h.dir, "rollout.jsonl"))
	initial := h.s.log.LastSeq()
	for i := range 19 {
		h.writeRollout(groupMessage(i))
	}
	parsed := 0
	h.s.parser = &observingParser{Parser: h.s.parser, before: func() {
		want := initial + uint64(parsed/eventlog.MaxGroupBatches*eventlog.MaxGroupBatches)
		if got := h.s.log.LastSeq(); got != want {
			t.Fatalf("before parsing line %d: seq=%d want=%d", parsed, got, want)
		}
		parsed++
	}}
	h.s.readMu.Lock()
	more, ok := h.s.readBatch()
	h.s.readMu.Unlock()
	if !ok || !more || parsed != 19 || h.s.log.LastSeq() != initial+19 {
		t.Fatalf("ready batch: more=%v ok=%v parsed=%d seq=%d", more, ok, parsed, h.s.log.LastSeq())
	}
	// The last three ready records were synced without waiting for eight.
	// A later single record is likewise visible when this read returns.
	h.s.parser = h.s.parser.(*observingParser).Parser
	h.writeRollout(groupMessage(19))
	if !h.s.readRollout() || h.s.log.LastSeq() != initial+20 {
		t.Fatal("single ready record did not commit immediately")
	}
}

func TestTranscriptGroupFailurePreservesAnnotationsAndRetries(t *testing.T) {
	h := newHarness(t, Config{})
	defer func() { h.s.log.Close() }()
	h.writeRollout(taskStarted)
	h.s.bindRollout("cli", filepath.Join(h.dir, "rollout.jsonl"))
	initial := h.s.log.LastSeq()
	h.s.recent = []input{{Text: "hello", RequestID: "r1", Origin: storeOrigin(&pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "synthetic-client"})}}
	h.s.unproven["r1"] = true
	h.writeRollout(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","content":[{"type":"text","text":"hello"}]}}}`, groupMessage(1), taskComplete)
	before, err := os.ReadFile(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.s.log.Close()
	if h.s.readRollout() {
		t.Fatal("append to a closed log succeeded")
	}
	if h.s.log.LastSeq() != initial || !h.s.running || h.s.preview != "" || len(h.s.recent) != 1 || !h.s.unproven["r1"] || !h.s.parserReplayPending {
		t.Fatal("failed group changed projection or consumed annotations")
	}
	if h.s.storageFault == "" {
		t.Fatal("failed group did not expose storage fault")
	}
	after, err := os.ReadFile(filepath.Join(h.dir, "events.jsonl"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed group changed disk")
	}
	h.s.log, err = eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !h.s.readRollout() || h.s.running || h.s.turnEnds != 1 || len(h.s.recent) != 0 || h.s.unproven["r1"] || h.s.storageFault != "" {
		t.Fatal("retry did not rebuild parser and commit once")
	}
	messages, confirmations := 0, 0
	for _, e := range h.events() {
		if m := e.GetUserMessage(); m != nil {
			messages++
			if m.RequestId != "r1" || e.Origin.GetDeviceId() != "synthetic-client" {
				t.Fatal("retry lost source annotation")
			}
		}
		if e.GetInputState().GetRequestId() == "r1" {
			confirmations++
		}
	}
	if messages != 1 || confirmations != 1 {
		t.Fatalf("messages=%d confirmations=%d", messages, confirmations)
	}
}

func TestTranscriptGroupConsumesEachMatchingInputOnce(t *testing.T) {
	h := newHarness(t, Config{})
	defer func() { h.s.log.Close() }()
	for _, id := range []string{"r1", "r2"} {
		h.s.recent = append(h.s.recent, input{Text: "same", RequestID: id, Origin: storeOrigin(originLocal)})
		h.s.unproven[id] = true
	}
	h.writeRollout(taskStarted,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","content":[{"type":"text","text":"same"}]}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","content":[{"type":"text","text":"same"}]}}}`)
	h.s.bindRollout("cli", filepath.Join(h.dir, "rollout.jsonl"))
	var ids []string
	for _, e := range h.events() {
		if m := e.GetUserMessage(); m != nil {
			ids = append(ids, m.RequestId)
		}
	}
	if len(ids) != 2 || ids[0] != "r1" || ids[1] != "r2" || len(h.s.recent) != 0 || len(h.s.unproven) != 0 {
		t.Fatalf("annotation matches reused: ids=%v recent=%v unproven=%v", ids, h.s.recent, h.s.unproven)
	}
}

func TestTranscriptGroupCrashPrefixResumesWithoutDuplicates(t *testing.T) {
	h := newHarness(t, Config{})
	defer func() { h.s.log.Close() }()
	h.writeRollout(taskStarted)
	rollout := filepath.Join(h.dir, "rollout.jsonl")
	h.s.bindRollout("cli", rollout)
	for i := range 5 {
		h.writeRollout(groupMessage(i))
	}
	h.writeRollout(taskComplete)
	if !h.s.readRollout() {
		t.Fatal("prepare transcript records")
	}
	want := h.events()
	logPath := filepath.Join(h.dir, "events.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	h.s.log.Close()
	lines := bytes.SplitAfter(data, []byte("\n"))
	// Reproduce the bytes surviving a crash in the second group: two
	// complete source lines after taskStarted, then a torn third line.
	end := len(lines[0]) + len(lines[1]) + len(lines[2]) + 10
	if err := os.WriteFile(logPath, data[:end], 0600); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	path, checkpoint := h.s.log.Checkpoint()
	if path != rollout || h.s.log.LastSeq() != 3 || h.s.reader.Offset() != checkpoint {
		t.Fatalf("restart prefix: path=%q offset=%d seq=%d", path, checkpoint, h.s.log.LastSeq())
	}
	if !h.s.readRollout() {
		t.Fatal("resume transcript")
	}
	got := h.events()
	if len(got) != len(want) {
		t.Fatalf("duplicate or missing events: got=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if !proto.Equal(got[i], want[i]) {
			t.Fatalf("replayed event %d differs: got=%v want=%v", i+1, got[i], want[i])
		}
	}
	if h.s.running || h.s.turnEnds != 1 {
		t.Fatal("resumed projection differs")
	}
}

func TestTranscriptGroupCommitsBeforeDiagnosticFailure(t *testing.T) {
	h := newHarness(t, Config{})
	defer func() { h.s.log.Close() }()
	h.writeRollout(taskStarted, groupMessage(1), `{broken`, groupMessage(2), taskComplete)
	path := filepath.Join(h.dir, "rollout.jsonl")
	h.s.meta.RolloutPath = path
	h.s.reader = tail.NewReader(path, 0)
	diagnostic := filepath.Join(h.dir, "parse-health.json")
	if err := os.Mkdir(diagnostic, 0700); err != nil {
		t.Fatal(err)
	}
	if h.s.readRollout() {
		t.Fatal("diagnostic failure did not stop read")
	}
	_, offset := h.s.log.Checkpoint()
	if h.s.log.LastSeq() != 2 || h.s.reader.Offset() != offset || !h.s.running {
		t.Fatal("valid preceding group was lost")
	}
	if err := os.Remove(diagnostic); err != nil {
		t.Fatal(err)
	}
	if !h.s.readRollout() || h.s.running || h.s.log.LastSeq() != 4 {
		t.Fatal("diagnostic retry duplicated or lost valid events")
	}
}
