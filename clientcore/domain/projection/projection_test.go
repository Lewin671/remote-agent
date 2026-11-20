package projection

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	stdcorev1 "github.com/Lewin671/remote-agent/gen/remoteagent/core/v1"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	stdtimestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

func stdEventToLite(t testing.TB, raw json.RawMessage) *v1.Event {
	t.Helper()
	var std stdv1.Event
	if err := protojson.Unmarshal(raw, &std); err != nil {
		t.Fatal(err)
	}
	wire, err := proto.Marshal(&std)
	if err != nil {
		t.Fatal(err)
	}
	var lite v1.Event
	if err := lite.Unmarshal(wire); err != nil {
		t.Fatal(err)
	}
	return &lite
}

func stdLiveToLite(t testing.TB, raw json.RawMessage) *v1.LiveSnapshot {
	t.Helper()
	var std stdv1.LiveSnapshot
	if err := protojson.Unmarshal(raw, &std); err != nil {
		t.Fatal(err)
	}
	wire, err := proto.Marshal(&std)
	if err != nil {
		t.Fatal(err)
	}
	var lite v1.LiveSnapshot
	if err := lite.Unmarshal(wire); err != nil {
		t.Fatal(err)
	}
	return &lite
}

func normalizeForVectors(s *corev1.ProjectionState) map[string]any {
	seqs := func(xs []uint64) []string {
		cp := slices.Clone(xs)
		slices.Sort(cp)
		out := make([]string, len(cp))
		for i, v := range cp {
			out[i] = strconv.FormatUint(v, 10)
		}
		return out
	}

	approvals := make([]string, len(s.Approvals))
	for i, a := range s.Approvals {
		approvals[i] = a.ApprovalId
	}

	resolved := make(map[string]any, len(s.Resolved))
	for id, r := range s.Resolved {
		resolved[id] = float64(r.Decision)
	}

	questions := make([]string, len(s.Questions))
	for i, q := range s.Questions {
		questions[i] = q.QuestionId
	}

	answered := make(map[string]any, len(s.Answered))
	for id, a := range s.Answered {
		ans := make([]any, len(a.Answers))
		for i, v := range a.Answers {
			ans[i] = v
		}
		answered[id] = ans
	}

	inputs := make([]any, len(s.Inputs))
	accepted := make(map[string]any)
	failures := make(map[string]any)
	for i, in := range s.Inputs {
		var stage int32
		if in.Accepted != nil && in.Accepted.Stage != v1.InputStage_INPUT_STAGE_UNSPECIFIED {
			stage = int32(in.Accepted.Stage)
		} else if in.Detail != nil {
			stage = int32(in.Detail.Stage)
		}
		var mode int32
		var paused bool
		if in.Detail != nil {
			mode = int32(in.Detail.Mode)
			paused = in.Detail.Paused
		}
		inputs[i] = map[string]any{
			"id":     in.RequestId,
			"status": float64(in.Status),
			"stage":  float64(stage),
			"mode":   float64(mode),
			"paused": paused,
		}
		if in.Accepted != nil {
			imgs := make([]any, len(in.Accepted.ImageIds))
			for j, img := range in.Accepted.ImageIds {
				imgs[j] = img
			}
			var originKind int32
			if in.Accepted.Origin != nil {
				originKind = int32(in.Accepted.Origin.Kind)
			}
			accepted[in.RequestId] = map[string]any{
				"text":   in.Accepted.Text,
				"images": imgs,
				"origin": float64(originKind),
			}
		}
		if in.FailureReason != nil {
			failures[in.RequestId] = *in.FailureReason
		}
	}

	queue := make([]string, len(s.PendingInputs))
	for i, p := range s.PendingInputs {
		queue[i] = p.RequestId
	}

	var plan any
	if s.Plan != nil {
		plan = s.Plan.Text
	}

	unsent := make([]string, len(s.Unsent))
	for i, u := range s.Unsent {
		unsent[i] = u.RequestId
	}
	slices.Sort(unsent)

	runningToolsSeqs := make([]uint64, len(s.Tools))
	for i, t := range s.Tools {
		runningToolsSeqs[i] = t.Seq
	}

	noResultSet := make(map[uint64]struct{}, len(s.NoResultSeqs))
	for _, seq := range s.NoResultSeqs {
		noResultSet[seq] = struct{}{}
	}
	var interrupted []uint64
	for _, seq := range s.UnfinishedSeqs {
		if _, ok := noResultSet[seq]; !ok {
			interrupted = append(interrupted, seq)
		}
	}

	completed := make(map[string]any, len(s.CompletedAt))
	for start, end := range s.CompletedAt {
		completed[strconv.FormatUint(start, 10)] = strconv.FormatUint(end, 10)
	}

	bgSeqs := make([]uint64, len(s.Background))
	for i, b := range s.Background {
		bgSeqs[i] = b.Tool.Seq
	}

	compactions := make(map[string]any, len(s.Compactions))
	for seq, c := range s.Compactions {
		var before, after any
		if c.BeforePercent != nil {
			before = float64(*c.BeforePercent)
		}
		if c.AfterPercent != nil {
			after = float64(*c.AfterPercent)
		}
		compactions[strconv.FormatUint(seq, 10)] = []any{before, after}
	}

	tcSeqs := make([]uint64, 0, len(s.TerminalChanges))
	for seq := range s.TerminalChanges {
		tcSeqs = append(tcSeqs, seq)
	}

	merged := make([]string, 0, len(s.ApprovalCallIds))
	callDecisions := make([]uint64, 0, len(s.ApprovalCallIds))
	for callSeq, approvalID := range s.ApprovalCallIds {
		callDecisions = append(callDecisions, callSeq)
		merged = append(merged, approvalID)
	}
	slices.Sort(merged)

	toAnySlice := func(xs []string) []any {
		out := make([]any, len(xs))
		for i, x := range xs {
			out[i] = x
		}
		return out
	}

	return map[string]any{
		"running":          s.Running,
		"approvals":        toAnySlice(approvals),
		"resolved":         resolved,
		"questions":        toAnySlice(questions),
		"answered":         answered,
		"inputs":           inputs,
		"accepted":         accepted,
		"failures":         failures,
		"queue":            toAnySlice(queue),
		"plan":             plan,
		"unsent":           toAnySlice(unsent),
		"local":            toAnySlice(seqs(s.TypedLocallySeqs)),
		"runningTools":     toAnySlice(seqs(runningToolsSeqs)),
		"unknownTools":     toAnySlice(seqs(s.UnfinishedSeqs)),
		"interruptedTools": toAnySlice(seqs(interrupted)),
		"completed":        completed,
		"background":       toAnySlice(seqs(bgSeqs)),
		"compactions":      compactions,
		"terminalChanges":  toAnySlice(seqs(tcSeqs)),
		"merged":           toAnySlice(merged),
		"callDecisions":    toAnySlice(seqs(callDecisions)),
	}
}

func TestSharedProjectionVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "projection", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Version int `json:"projectionVersion"`
		Vectors []struct {
			Name         string            `json:"name"`
			Agent        int32             `json:"agent"`
			Events       []json.RawMessage `json:"events"`
			Cuts         []int             `json:"cuts"`
			Observations []struct {
				Through string         `json:"through"`
				State   map[string]any `json:"state"`
				History *struct {
					AsyncQuestions []string `json:"asyncQuestions"`
				} `json:"history"`
			} `json:"observations"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Version != 1 || len(corpus.Vectors) == 0 {
		t.Fatalf("unexpected corpus version %d", corpus.Version)
	}

	for _, v := range corpus.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			events := make([]*v1.Event, len(v.Events))
			for i, raw := range v.Events {
				events[i] = stdEventToLite(t, raw)
			}
			agent := v1.Agent(v.Agent)
			for _, obs := range v.Observations {
				through, err := strconv.Atoi(obs.Through)
				if err != nil {
					t.Fatal(err)
				}
				prefix := events[:through]
				got := normalizeForVectors(Derive(prefix, agent))
				if !reflect.DeepEqual(obs.State, got) {
					t.Fatalf("through %d full replay mismatch:\ngot:  %#v\nwant: %#v", through, got, obs.State)
				}
				if obs.History != nil {
					var asyncSeqs []string
					for _, e := range prefix {
						if qa := e.GetQuestionAsked(); qa != nil && qa.Async {
							asyncSeqs = append(asyncSeqs, strconv.FormatUint(e.Seq, 10))
						}
					}
					if asyncSeqs == nil {
						asyncSeqs = []string{}
					}
					if !slices.Equal(obs.History.AsyncQuestions, asyncSeqs) {
						t.Fatalf("asyncQuestions mismatch: got %v, want %v", asyncSeqs, obs.History.AsyncQuestions)
					}
				}
				for _, cut := range v.Cuts {
					if cut > len(prefix) {
						continue
					}
					r := NewReducer(agent)
					r.Append(prefix[:cut])
					heldSnap := r.Snapshot()
					heldNorm := normalizeForVectors(heldSnap)

					proj := NewProjection()
					proj.Update(prefix[:cut], agent)

					r.Append(prefix[cut:])
					afterNorm := normalizeForVectors(r.Snapshot())
					if !reflect.DeepEqual(obs.State, afterNorm) {
						t.Fatalf("through %d cut %d reducer mismatch:\ngot:  %#v\nwant: %#v", through, cut, afterNorm, obs.State)
					}
					projAfter := normalizeForVectors(proj.Update(prefix, agent))
					if !reflect.DeepEqual(obs.State, projAfter) {
						t.Fatalf("through %d cut %d projection mismatch:\ngot:  %#v\nwant: %#v", through, cut, projAfter, obs.State)
					}
					if !reflect.DeepEqual(heldNorm, normalizeForVectors(heldSnap)) {
						t.Fatalf("cut %d mutated earlier snapshot", cut)
					}
				}
			}
		})
	}
}

func TestSharedLiveInputVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "projection", "live-inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Version int `json:"projectionVersion"`
		Vectors []struct {
			Name     string            `json:"name"`
			History  []json.RawMessage `json:"history"`
			Live     json.RawMessage   `json:"live"`
			Expected []any             `json:"expected"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, v := range corpus.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			events := make([]*v1.Event, len(v.History))
			for i, raw := range v.History {
				events[i] = stdEventToLite(t, raw)
			}
			live := stdLiveToLite(t, v.Live)

			r := NewReducer(DefaultAgent)
			r.Append(events)
			states := []*corev1.ProjectionState{
				CurrentActions(Derive(events, DefaultAgent), live, events),
				r.CurrentActions(live),
			}
			for idx, st := range states {
				if st.Plan != nil {
					t.Fatalf("case %d: expected nil plan, got %v", idx, st.Plan)
				}
				actual := make([]any, len(st.Inputs))
				for i, in := range st.Inputs {
					var stage, mode int32
					var paused bool
					if in.Detail != nil {
						if in.Accepted != nil && in.Accepted.Stage != v1.InputStage_INPUT_STAGE_UNSPECIFIED {
							stage = int32(in.Accepted.Stage)
						} else {
							stage = int32(in.Detail.Stage)
						}
						mode = int32(in.Detail.Mode)
						paused = in.Detail.Paused
					}
					var text string
					imgs := []any{}
					files := []any{}
					var originKind int32
					var originDevice string
					if in.Accepted != nil {
						text = in.Accepted.Text
						imgs = make([]any, len(in.Accepted.ImageIds))
						for j, img := range in.Accepted.ImageIds {
							imgs[j] = img
						}
						files = make([]any, len(in.Accepted.Files))
						for j, f := range in.Accepted.Files {
							files[j] = map[string]any{
								"fileId":    f.FileId,
								"name":      f.Name,
								"sizeBytes": strconv.FormatInt(f.SizeBytes, 10),
							}
						}
						if in.Accepted.Origin != nil {
							originKind = int32(in.Accepted.Origin.Kind)
							originDevice = in.Accepted.Origin.DeviceId
						}
					}
					actual[i] = map[string]any{
						"id":     in.RequestId,
						"status": float64(in.Status),
						"stage":  float64(stage),
						"mode":   float64(mode),
						"paused": paused,
						"text":   text,
						"images": imgs,
						"files":  files,
						"origin": map[string]any{
							"kind":     float64(originKind),
							"deviceId": originDevice,
						},
					}
				}
				if !reflect.DeepEqual(v.Expected, actual) {
					t.Fatalf("case %d mismatch:\ngot:  %#v\nwant: %#v", idx, actual, v.Expected)
				}
			}
		})
	}
}

func TestSharedSkeletonContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "projection", "skeletons.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Note   string            `json:"note"`
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Note != SkeletonNote {
		t.Fatalf("note mismatch: %q", corpus.Note)
	}
	for i, raw := range corpus.Events {
		e := stdEventToLite(t, raw)
		if !IsSkeleton(e) {
			t.Fatalf("event %d (%T) not recognized as skeleton", i, e.Kind)
		}
	}
	history := []*v1.Event{
		{Seq: 1, Kind: &v1.Event_PlanProposed{PlanProposed: &v1.PlanProposed{Text: "large plan" + corpus.Note}}},
	}
	live := &v1.LiveSnapshot{
		Session:        &v1.Session{Id: "s", PlanOpen: true},
		PlanEventSeq:   1,
		PlanScreenHash: bytes.Repeat([]byte{1}, 32),
		PlanScreen:     "screen fallback",
	}
	st := CurrentActions(Derive(history, DefaultAgent), live, history)
	if st.Plan == nil || st.Plan.Text != "screen fallback" {
		t.Fatalf("expected planScreen fallback for skeleton plan, got %v", st.Plan)
	}
}

func TestAdditionalProjectionRules(t *testing.T) {
	hash32 := bytes.Repeat([]byte{1}, 32)
	events := []*v1.Event{
		{Seq: 1, Time: &timestamppb.Timestamp{Seconds: 100}, Kind: &v1.Event_TurnStarted{TurnStarted: &v1.TurnStarted{}}},
		{Seq: 2, Kind: &v1.Event_ToolStarted{ToolStarted: &v1.ToolStarted{Title: "rm -rf build", CallId: "c1", InputDigest: "d1"}}},
		{Seq: 3, Kind: &v1.Event_ApprovalRequested{ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "a1", InputDigest: "d1"}}},
		{Seq: 4, Kind: &v1.Event_PlanProposed{PlanProposed: &v1.PlanProposed{Text: "# real plan"}}},
		{Seq: 5, Kind: &v1.Event_InputState{InputState: &v1.InputState{RequestId: "r1", Status: v1.InputStatus_INPUT_STATUS_QUEUED, Text: "hi"}}},
	}

	t.Run("awaitingApprovalAndProjectionReplace", func(t *testing.T) {
		p := NewProjection()
		s1 := p.Update(events[:3], v1.Agent_AGENT_CLAUDE)
		if len(s1.Tools) != 1 || !s1.Tools[0].AwaitingApproval {
			t.Fatalf("expected tool awaiting approval, got %+v", s1.Tools)
		}
		if s2 := p.Update(events[:3], v1.Agent_AGENT_CLAUDE); s2 != s1 {
			t.Fatal("expected cached snapshot pointer on unchanged prefix and agent")
		}
		replaced := slices.Clone(events[:3])
		replaced[2] = &v1.Event{Seq: 3, Kind: &v1.Event_ApprovalRequested{ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "a2", InputDigest: "other"}}}
		s3 := p.Update(replaced, v1.Agent_AGENT_CLAUDE)
		if len(s3.Tools) != 1 || s3.Tools[0].AwaitingApproval {
			t.Fatalf("expected awaitingApproval=false after digest mismatch, got %+v", s3.Tools)
		}
		codex := Derive([]*v1.Event{
			{Seq: 1, Kind: &v1.Event_ToolStarted{ToolStarted: &v1.ToolStarted{Title: "exec", CallId: "x", Input: "go test ./..."}}},
			{Seq: 2, Kind: &v1.Event_ApprovalRequested{ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "a", Command: "go test ./..."}}},
		}, v1.Agent_AGENT_CODEX)
		if len(codex.Tools) != 1 || !codex.Tools[0].AwaitingApproval {
			t.Fatalf("expected Codex command match awaitingApproval=true")
		}
	})

	t.Run("livePlanAndRunningWatermark", func(t *testing.T) {
		r := NewReducer(DefaultAgent)
		r.Append(events)
		live := &v1.LiveSnapshot{
			Session:        &v1.Session{Id: "s", LastSeq: 5, PlanOpen: true},
			PlanEventSeq:   4,
			PlanScreenHash: hash32,
			PlanScreen:     "screen",
			Running:        false,
		}
		st := r.CurrentActions(live)
		if st.Running {
			t.Fatal("expected live.Running=false to win when runningSeq <= lastSeq")
		}
		if st.Plan == nil || st.Plan.Text != "# real plan" {
			t.Fatalf("expected markdown plan text, got %v", st.Plan)
		}
		liveClosed := &v1.LiveSnapshot{
			Session:        &v1.Session{Id: "s", LastSeq: 0, PlanOpen: false},
			PlanEventSeq:   4,
			PlanScreenHash: hash32,
			PlanScreen:     "screen",
			Running:        true,
		}
		stClosed := r.CurrentActions(liveClosed)
		if stClosed.Plan != nil {
			t.Fatalf("expected nil plan when planOpen=false, got %v", stClosed.Plan)
		}
		if !stClosed.Running {
			t.Fatal("expected historical running=true when runningSeq (1) > lastSeq (0)")
		}
		if stNil := r.CurrentActions(nil); stNil.Plan != nil || len(stNil.Approvals) != 0 || len(stNil.Questions) != 0 {
			t.Fatal("expected nil live to clear pending actions")
		}
	})

	t.Run("pendingInputDefaultSendMode", func(t *testing.T) {
		d := Derive([]*v1.Event{
			{Seq: 1, Kind: &v1.Event_InputState{InputState: &v1.InputState{RequestId: "r", Status: v1.InputStatus_INPUT_STATUS_QUEUED, Text: "go"}}},
		}, DefaultAgent)
		if len(d.PendingInputs) != 1 || d.PendingInputs[0].Mode != v1.SendMode_SEND_MODE_STEER {
			t.Fatalf("expected default STEER mode, got %+v", d.PendingInputs)
		}
	})
}

// TestLiteWireCompatibility verifies standard protobuf <-> lite protobuf wire compatibility
// across representative features: Timestamp, oneof, optional scalars, bytes, enum, repeated, and map.
// Direction-only types are tested in their generated direction (Unmarshal for inbound, Marshal/Size for outbound).
func TestLiteWireCompatibility(t *testing.T) {
	exitCode := int32(0)
	usedPct := float64(42.5)
	totalBytes := uint64(4096)

	roundTripCases := []struct {
		name string
		std  proto.Message
		lite interface {
			Size() int
			Marshal() ([]byte, error)
			Unmarshal([]byte) error
		}
		freshStd func() proto.Message
	}{
		{
			name: "event-tool-call-oneof-optional-timestamp-bytes",
			std: &stdv1.Event{
				Seq:       42,
				SessionId: "s-wire",
				Time:      &stdtimestamppb.Timestamp{Seconds: 1700000123, Nanos: 456000000},
				TurnId:    "t-1",
				Origin: &stdv1.Origin{
					Kind:               stdv1.OriginKind_ORIGIN_KIND_CLIENT,
					DeviceId:           "dev-1",
					RequestId:          "req-1",
					ObservedEventSeq:   41,
					ObservedScreenHash: []byte{1, 2, 3, 4},
				},
				Kind: &stdv1.Event_ToolCall{
					ToolCall: &stdv1.ToolCall{
						CallId:          "call-1",
						Kind:            stdv1.ToolKind_TOOL_KIND_COMMAND,
						Title:           "go test ./...",
						ExitCode:        &exitCode,
						DurationMs:      1250,
						Background:      stdv1.ToolBackground_TOOL_BACKGROUND_TIMED_OUT,
						OutputTruncated: true,
						SharedFiles: []*stdv1.SharedFile{
							{FileId: "f1", Name: "out.log", SizeBytes: 128, Sha256: []byte{0xaa, 0xbb}},
						},
					},
				},
			},
			lite:     &v1.Event{},
			freshStd: func() proto.Message { return &stdv1.Event{} },
		},
		{
			name: "token-usage-double-nested",
			std: &stdv1.Event{
				Seq: 9,
				Kind: &stdv1.Event_TokenUsage{
					TokenUsage: &stdv1.TokenUsage{
						InputTokens:     1000,
						OutputTokens:    200,
						LastTotalTokens: 1200,
						ContextWindow:   200000,
						PrimaryLimit:    &stdv1.RateLimit{UsedPercent: 37.5, WindowMinutes: 300, ResetsAt: 1700009999},
					},
				},
			},
			lite:     &v1.Event{},
			freshStd: func() proto.Message { return &stdv1.Event{} },
		},
		{
			name: "machine-capabilities-packed-enum",
			std: &stdv1.MachineCapabilities{
				ImageUpload:    true,
				FileUpload:     true,
				MaxUploadBytes: 10485760,
				MaxChunkBytes:  65536,
				UploadFormats: []stdv1.ImageFormat{
					stdv1.ImageFormat_IMAGE_FORMAT_PNG,
					stdv1.ImageFormat_IMAGE_FORMAT_WEBP,
				},
			},
			lite:     &v1.MachineCapabilities{},
			freshStd: func() proto.Message { return &stdv1.MachineCapabilities{} },
		},
		{
			name: "answer-question-packed-int32-optional",
			std: &stdv1.AnswerQuestionRequest{
				SessionId:        "s1",
				QuestionId:       "q1",
				Option:           -1,
				QuestionIndex:    &exitCode,
				Options:          []int32{0, 1, -2, 100},
				ObservedEventSeq: 77,
			},
			lite:     &v1.AnswerQuestionRequest{},
			freshStd: func() proto.Message { return &stdv1.AnswerQuestionRequest{} },
		},
		{
			name:     "maps-string-scalar",
			std:      &stdv1.CreateSessionRequest{Cwd: "/w", Env: map[string]string{"A": "1", "B": ""}},
			lite:     &v1.CreateSessionRequest{},
			freshStd: func() proto.Message { return &stdv1.CreateSessionRequest{} },
		},
		{
			name: "nested-project-prefs",
			std: &stdv1.ProjectPrefs{
				Schema:    1,
				Pinned:    []*stdv1.ProjectPref{{Path: "/repo", SinceMs: 123456}},
				RecentOps: []string{"op-1", "op-2"},
				Settings: []*stdv1.ProjectSettings{
					{
						Path:      "/repo",
						Agent:     stdv1.Agent_AGENT_CLAUDE,
						MachineId: "m-1",
						UpdatedMs: 999,
						Agents: []*stdv1.AgentSettings{
							{Agent: stdv1.Agent_AGENT_CLAUDE, Model: "opus", Effort: "high", Mode: stdv1.ApprovalMode_APPROVAL_MODE_AUTO, StandardContext: true},
						},
					},
				},
			},
			lite:     &v1.ProjectPrefs{},
			freshStd: func() proto.Message { return &stdv1.ProjectPrefs{} },
		},
	}

	for _, tc := range roundTripCases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := proto.Marshal(tc.std)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.lite.Unmarshal(wire); err != nil {
				t.Fatal(err)
			}
			wantSize := tc.lite.Size()
			roundWire, err := tc.lite.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if len(roundWire) != wantSize {
				t.Fatalf("Size() = %d, len(Marshal()) = %d", wantSize, len(roundWire))
			}
			gotStd := tc.freshStd()
			if err := proto.Unmarshal(roundWire, gotStd); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(tc.std, gotStd) {
				t.Fatalf("round-trip mismatch:\ngot:  %v\nwant: %v", gotStd, tc.std)
			}
		})
	}

	t.Run("shared-file-chunk-bytes-optional-fixed64", func(t *testing.T) {
		std := &stdv1.GetSharedFileChunkResponse{
			Data:       []byte("hello"),
			TotalBytes: &totalBytes,
		}
		wire, err := proto.Marshal(std)
		if err != nil {
			t.Fatal(err)
		}
		var lite v1.GetSharedFileChunkResponse
		if err := lite.Unmarshal(wire); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(lite.Data, std.Data) || lite.TotalBytes == nil || *lite.TotalBytes != totalBytes {
			t.Fatalf("decoded GetSharedFileChunkResponse mismatch: %+v", lite)
		}
	})

	t.Run("quota-window-optional-double", func(t *testing.T) {
		std := &stdv1.QuotaWindow{
			Name:          "five_hour",
			UsedPercent:   &usedPct,
			ResetsAtMs:    1700005000000,
			WindowMinutes: 300,
		}
		wire, err := proto.Marshal(std)
		if err != nil {
			t.Fatal(err)
		}
		var lite v1.QuotaWindow
		if err := lite.Unmarshal(wire); err != nil {
			t.Fatal(err)
		}
		if lite.Name != "five_hour" || lite.UsedPercent == nil || *lite.UsedPercent != usedPct ||
			lite.ResetsAtMs != 1700005000000 || lite.WindowMinutes != 300 {
			t.Fatalf("decoded QuotaWindow mismatch: %+v", lite)
		}
	})

	t.Run("entropy-packed-double", func(t *testing.T) {
		std := &stdcorev1.EntropyProvided{
			RandomIds:  []string{"id1", "id2"},
			UnitFloats: []float64{0.0, 0.25, 0.75, 1.0},
		}
		wire, err := proto.Marshal(std)
		if err != nil {
			t.Fatal(err)
		}
		var lite corev1.EntropyProvided
		if err := lite.Unmarshal(wire); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(lite.RandomIds, std.RandomIds) || !reflect.DeepEqual(lite.UnitFloats, std.UnitFloats) {
			t.Fatalf("decoded EntropyProvided mismatch: %+v", lite)
		}
	})

	marshalCases := []struct {
		name string
		lite interface {
			Size() int
			Marshal() ([]byte, error)
		}
		want     proto.Message
		freshStd func() proto.Message
	}{
		{
			name: "maps-uint64-and-message-values-and-packed-uint64",
			lite: &corev1.ProjectionState{
				Resolved: map[string]*corev1.ResolvedApproval{
					"a1": {
						Decision: v1.ApprovalDecision(1),
						Message:  "ok",
						Origin:   &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "d1", RequestId: "r1"},
					},
					"a2": {Decision: v1.ApprovalDecision(2), Message: ""},
				},
				CompletedAt:      map[uint64]uint64{3: 7, 0: 1},
				ApprovalCallIds:  map[uint64]string{5: "call-5"},
				UnfinishedSeqs:   []uint64{10, 200, 300000},
				NoResultSeqs:     []uint64{11},
				TypedLocallySeqs: []uint64{12, 13},
			},
			want: &stdcorev1.ProjectionState{
				Resolved: map[string]*stdcorev1.ResolvedApproval{
					"a1": {
						Decision: stdv1.ApprovalDecision(1),
						Message:  "ok",
						Origin:   &stdv1.Origin{Kind: stdv1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "d1", RequestId: "r1"},
					},
					"a2": {Decision: stdv1.ApprovalDecision(2), Message: ""},
				},
				CompletedAt:      map[uint64]uint64{3: 7, 0: 1},
				ApprovalCallIds:  map[uint64]string{5: "call-5"},
				UnfinishedSeqs:   []uint64{10, 200, 300000},
				NoResultSeqs:     []uint64{11},
				TypedLocallySeqs: []uint64{12, 13},
			},
			freshStd: func() proto.Message { return &stdcorev1.ProjectionState{} },
		},
		{
			name: "deep-nested-viewmodel-home",
			lite: &corev1.ViewModel{
				View: &corev1.ViewModel_Home{
					Home: &corev1.HomeViewModel{
						Headline: &corev1.HomeHeadlineView{
							Title: &corev1.Text{
								Key: "home.running",
								Args: []*corev1.TextArg{
									{Name: "count", Value: &corev1.TextArg_IntValue{IntValue: 3}},
									{Name: "sub", Value: &corev1.TextArg_TextValue{TextValue: &corev1.Text{Key: "common.ok"}}},
								},
							},
						},
						Running: []*corev1.SessionRowView{
							{
								SessionId:  "s-1",
								Title:      &corev1.Text{Key: "common.plain", Args: []*corev1.TextArg{{Name: "value", Value: &corev1.TextArg_StrValue{StrValue: "Fix bug"}}}},
								SecondLine: &corev1.Text{Key: "status.running"},
								Agent:      v1.Agent_AGENT_CODEX,
							},
						},
					},
				},
			},
			want: &stdcorev1.ViewModel{
				View: &stdcorev1.ViewModel_Home{
					Home: &stdcorev1.HomeViewModel{
						Headline: &stdcorev1.HomeHeadlineView{
							Title: &stdcorev1.Text{
								Key: "home.running",
								Args: []*stdcorev1.TextArg{
									{Name: "count", Value: &stdcorev1.TextArg_IntValue{IntValue: 3}},
									{Name: "sub", Value: &stdcorev1.TextArg_TextValue{TextValue: &stdcorev1.Text{Key: "common.ok"}}},
								},
							},
						},
						Running: []*stdcorev1.SessionRowView{
							{
								SessionId:  "s-1",
								Title:      &stdcorev1.Text{Key: "common.plain", Args: []*stdcorev1.TextArg{{Name: "value", Value: &stdcorev1.TextArg_StrValue{StrValue: "Fix bug"}}}},
								SecondLine: &stdcorev1.Text{Key: "status.running"},
								Agent:      stdv1.Agent_AGENT_CODEX,
							},
						},
					},
				},
			},
			freshStd: func() proto.Message { return &stdcorev1.ViewModel{} },
		},
	}

	for _, tc := range marshalCases {
		t.Run(tc.name, func(t *testing.T) {
			wantSize := tc.lite.Size()
			wire, err := tc.lite.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if len(wire) != wantSize {
				t.Fatalf("Size() = %d, len(Marshal()) = %d", wantSize, len(wire))
			}
			gotStd := tc.freshStd()
			if err := proto.Unmarshal(wire, gotStd); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(tc.want, gotStd) {
				t.Fatalf("marshal mismatch:\ngot:  %v\nwant: %v", gotStd, tc.want)
			}
		})
	}
}

func BenchmarkDerive10K(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "projection", "vectors.json"))
	if err != nil {
		b.Fatal(err)
	}
	var corpus struct {
		Vectors []struct {
			Agent  int32             `json:"agent"`
			Events []json.RawMessage `json:"events"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		b.Fatal(err)
	}
	var longest []*v1.Event
	var agent v1.Agent
	for _, v := range corpus.Vectors {
		if len(v.Events) > len(longest) {
			agent = v1.Agent(v.Agent)
			longest = make([]*v1.Event, len(v.Events))
			for i, raw := range v.Events {
				longest[i] = stdEventToLite(b, raw)
			}
		}
	}
	const target = 10000
	events := make([]*v1.Event, target)
	for i := 0; i < target; i++ {
		src := longest[i%len(longest)]
		cp := *src
		cp.Seq = uint64(i + 1)
		events[i] = &cp
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Derive(events, agent)
	}
}
