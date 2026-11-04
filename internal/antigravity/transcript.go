package antigravity

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var ansPrefixRE = regexp.MustCompile(`^A(\d+):\s*(.*)$`)

type transcriptRecord struct {
	StepIndex       int64                `json:"step_index"`
	Source          string               `json:"source"`
	Type            string               `json:"type"`
	Status          string               `json:"status"`
	CreatedAt       string               `json:"created_at"`
	Content         string               `json:"content"`
	Thinking        string               `json:"thinking"`
	ToolCalls       []transcriptToolCall `json:"tool_calls"`
	Media           []transcriptMedia    `json:"media"`
	InputTokens     int64                `json:"input_tokens"`
	CacheReadTokens int64                `json:"cache_read_tokens"`
	OutputTokens    int64                `json:"output_tokens"`
	TruncatedFields []string             `json:"truncated_fields"`
}
type transcriptToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}
type transcriptMedia struct {
	MimeType string `json:"mime_type"`
	URI      string `json:"uri"`
}
type pendingCall struct {
	id, name, title, input, digest string
	kind                           pb.ToolKind
	isPlan                         bool
	plan                           *agent.PlanFacts
	artifact                       string
}

// Parser reads append-only transcript records. Tool results occupy their own
// step indices; stepIdx in PreToolUse is that result's index, also used as the
// synthetic call ID so early hook events deduplicate with transcript events.
type Parser struct {
	artifactDecisions     map[string]string
	answeringPlan         string
	answeringChoice       pb.PlanChoice
	planAcknowledgement   *pb.Event
	artifactRoot          string
	artifacts             map[string]bool
	artifactIndexComplete bool
	pendingOutcome        *pb.TurnEnded
	pendingLine           int
	attachLines           int
	pendingTime           string
	plans                 map[string]*agent.PlanFacts
	answeredPlans         map[string]bool
	lastPlanID            string
	planMessages          map[string]bool
	fullOffset            int64
	fullLine              int
	transcriptPath        string
	conversation          string
	lineIndex             int
	inTurn                bool
	turnID                string
	pendingCalls          map[int64]pendingCall
	questions             map[string]*pb.QuestionAsked
	reported              map[string]bool
	ctx                   *pb.TurnContext
	usage                 *pb.TokenUsage
	found                 []agent.FoundImage
	restoring             bool
}

var _ agent.Parser = (*Parser)(nil)

func NewParser() *Parser {
	return &Parser{artifactDecisions: map[string]string{}, artifacts: map[string]bool{}, artifactIndexComplete: true, planMessages: map[string]bool{}, plans: map[string]*agent.PlanFacts{}, answeredPlans: map[string]bool{}, pendingCalls: map[int64]pendingCall{}, questions: map[string]*pb.QuestionAsked{}, reported: map[string]bool{}}
}
func (p *Parser) SetTranscriptPath(path string) {
	p.transcriptPath = path
	if filepath.Base(filepath.Dir(path)) == "logs" && filepath.Base(filepath.Dir(filepath.Dir(path))) == ".system_generated" {
		p.artifactRoot = filepath.Dir(filepath.Dir(filepath.Dir(path)))
		p.conversation = filepath.Base(p.artifactRoot)
	}
}
func (p *Parser) SetConversation(id string) { p.conversation = id }

func (p *Parser) Images() []agent.FoundImage { out := p.found; p.found = nil; return out }
func (p *Parser) Replay(e *pb.Event) {
	switch k := e.Kind.(type) {
	case *pb.Event_TurnStarted:
		p.inTurn, p.turnID = true, e.TurnId
	case *pb.Event_TurnEnded:
		p.inTurn = false
		p.pendingOutcome = nil
		p.pendingTime = ""
		clear(p.pendingCalls)
		clear(p.questions)
	case *pb.Event_TurnContext:
		p.ctx = proto.CloneOf(k.TurnContext)
	case *pb.Event_TokenUsage:
		p.usage = proto.CloneOf(k.TokenUsage)
	case *pb.Event_SettingsChanged:
		p.ctx = applySettings(p.ctx, k.SettingsChanged)
	case *pb.Event_QuestionAsked:
		p.report(k.QuestionAsked.QuestionId)
		p.questions[k.QuestionAsked.QuestionId] = k.QuestionAsked
	case *pb.Event_QuestionAnswered:
		delete(p.questions, k.QuestionAnswered.QuestionId)
	case *pb.Event_PlanProposed:
		p.report(k.PlanProposed.CallId)
		p.lastPlanID = k.PlanProposed.CallId
	case *pb.Event_PlanAnswered:
		if p.lastPlanID != "" {
			p.answeredPlans[p.lastPlanID] = true
		}
		for _, plan := range p.plans {
			p.answeredPlans[plan.CallID] = true
		}
		clear(p.plans)
	}
}

// Restore reconstructs pending tool/result pairing and the absolute physical
// line number, which events alone cannot recover. Token totals, inferred ends
// and hook-only questions are then retained from the durable event replay.
func (p *Parser) Restore(path string, end int64) error {
	if end == 0 {
		p.SetTranscriptPath(path)
		p.lineIndex = 0
		clear(p.pendingCalls)
		clear(p.questions)
		clear(p.plans)
		clear(p.planMessages)
		p.lastPlanID = ""
		p.pendingOutcome = nil
		p.pendingTime = ""
		clear(p.artifacts)
		p.artifactIndexComplete = true
		p.fullOffset = 0
		p.fullLine = 0
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rebuilt := NewParser()
	rebuilt.SetTranscriptPath(path)
	rebuilt.conversation, rebuilt.restoring = p.conversation, true
	rd := bufio.NewReader(io.LimitReader(f, end))
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			if _, parseErr := rebuilt.Parse(line); errors.Is(parseErr, agent.ErrSourcePending) {
				return parseErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	p.transcriptPath, p.lineIndex = path, rebuilt.lineIndex
	p.fullOffset, p.fullLine = rebuilt.fullOffset, rebuilt.fullLine
	p.plans = rebuilt.plans
	p.artifacts = rebuilt.artifacts
	p.artifactDecisions = rebuilt.artifactDecisions
	p.artifactIndexComplete = rebuilt.artifactIndexComplete
	p.planMessages = rebuilt.planMessages
	for path, plan := range p.plans {
		if p.answeredPlans[plan.CallID] {
			delete(p.plans, path)
		}
	}
	if p.inTurn {
		p.pendingOutcome, p.pendingTime, p.pendingLine = rebuilt.pendingOutcome, rebuilt.pendingTime, rebuilt.pendingLine
		p.pendingCalls = rebuilt.pendingCalls
	} else {
		p.pendingOutcome = nil
		p.pendingTime = ""
		clear(p.pendingCalls)
	}
	return nil
}
func (p *Parser) report(id string) {
	if id != "" {
		p.reported[id] = true
	}
}
func (p *Parser) Reported(e *pb.Event) bool {
	if q := e.GetQuestionAsked(); q != nil {
		return p.reported[q.QuestionId]
	}
	if plan := e.GetPlanProposed(); plan != nil {
		return p.reported[plan.CallId]
	}
	return false
}
func (p *Parser) Parse(raw []byte) ([]*pb.Event, error) {
	line := p.lineIndex
	p.lineIndex++
	var r transcriptRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("agy transcript: %w", err)
	}
	if len(r.TruncatedFields) > 0 {
		if err := p.resolveTruncated(&r, line); err != nil {
			if errors.Is(err, agent.ErrSourcePending) {
				p.lineIndex--
			}
			return nil, err
		}
	}
	if line == 0 && r.StepIndex > 1 {
		p.artifactIndexComplete = false
	}
	p.found = nil
	var evs []*pb.Event
	emit := func(kind any) {
		e := &pb.Event{TurnId: p.turnID}
		switch v := kind.(type) {
		case *pb.TurnStarted:
			e.Kind = &pb.Event_TurnStarted{TurnStarted: v}
		case *pb.TurnEnded:
			e.Kind = &pb.Event_TurnEnded{TurnEnded: v}
		case *pb.UserMessage:
			e.Kind = &pb.Event_UserMessage{UserMessage: v}
		case *pb.AgentMessage:
			e.Kind = &pb.Event_AgentMessage{AgentMessage: v}
		case *pb.Reasoning:
			e.Kind = &pb.Event_Reasoning{Reasoning: v}
		case *pb.QuestionAsked:
			e.Kind = &pb.Event_QuestionAsked{QuestionAsked: v}
		case *pb.QuestionAnswered:
			e.Kind = &pb.Event_QuestionAnswered{QuestionAnswered: v}
		case *pb.PlanProposed:
			e.Kind = &pb.Event_PlanProposed{PlanProposed: v}
		case *pb.ToolStarted:
			e.Kind = &pb.Event_ToolStarted{ToolStarted: v}
		case *pb.ToolCall:
			e.Kind = &pb.Event_ToolCall{ToolCall: v}
		case *pb.SettingsChanged:
			e.Kind = &pb.Event_SettingsChanged{SettingsChanged: v}
		case *pb.TokenUsage:
			e.Kind = &pb.Event_TokenUsage{TokenUsage: v}
		case *pb.TurnContext:
			e.Kind = &pb.Event_TurnContext{TurnContext: v}
		}
		if at, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err == nil {
			e.Time = timestamppb.New(at)
		}
		evs = append(evs, e)
	}
	start := func() {
		p.inTurn = true
		p.turnID = nativeCallID(p.conversation, r.StepIndex)
		emit(&pb.TurnStarted{})
		if p.ctx != nil {
			emit(proto.CloneOf(p.ctx))
		}
	}
	switch r.Type {
	case "USER_INPUT":
		text := stripUserEnvelopes(r.Content)
		planWrapped := strings.HasPrefix(text, "/plan ") && strings.TrimSpace(strings.TrimPrefix(text, "/plan ")) != ""
		if planWrapped {
			text = strings.TrimPrefix(text, "/plan ")
		}
		if !planWrapped && strings.HasPrefix(text, "/") {
			break
		} // slash command, not a model turn
		var reviews []string
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if p.planMessages[line] {
				reviews = append(reviews, line)
			}
		}
		generated := len(reviews) > 0 && strings.HasPrefix(strings.TrimSpace(text), reviews[0])
		if generated {
			for _, review := range reviews {
				delete(p.planMessages, review)
				name := strings.TrimPrefix(strings.TrimPrefix(review, "[Approved] "), "[Rejected] ")
				status := "approved"
				choice := pb.PlanChoice_PLAN_CHOICE_IMPLEMENT
				if strings.HasPrefix(review, "[Rejected]") {
					status = "rejected"
					choice = pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING
				}
				pending := false
				remote := false
				for path := range p.artifacts {
					if filepath.Base(path) == name {
						p.artifactDecisions[path] = status
					}
				}
				for path, plan := range p.plans {
					if filepath.Base(path) != name {
						continue
					}
					pending = true
					if plan.CallID == p.answeringPlan && choice == p.answeringChoice {
						remote = true
					}
					p.answeredPlans[plan.CallID] = true
					delete(p.plans, path)
				}
				if pending && len(reviews) == 1 {
					event := &pb.Event{Origin: &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_LOCAL}, Kind: &pb.Event_PlanAnswered{PlanAnswered: &pb.PlanAnswered{Choice: int32(choice)}}}
					if remote {
						p.planAcknowledgement = event
					} else {
						evs = append(evs, event)
					}
				}
			}
		}

		if p.inTurn {
			if p.pendingOutcome != nil {
				emit(proto.CloneOf(p.pendingOutcome))
				if at, err := time.Parse(time.RFC3339Nano, p.pendingTime); err == nil {
					evs[len(evs)-1].Time = timestamppb.New(at)
				}
			} else {
				emit(&pb.TurnEnded{Outcome: pb.TurnOutcome_TURN_OUTCOME_ABORTED, Reason: "new input after unfinished turn"})
			}
		}
		p.pendingOutcome = nil
		p.pendingTime = ""

		clear(p.pendingCalls)
		clear(p.questions)
		start()
		emit(&pb.UserMessage{Text: text, Images: p.readMedia(r.Media, pb.ImageSource_IMAGE_SOURCE_USER)})
		if generated {
			evs[len(evs)-1].Origin = &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_SYSTEM}
		}
	case "PLANNER_RESPONSE":
		p.pendingOutcome = nil
		p.pendingTime = ""
		if !p.inTurn {
			start()
		} // background continuation after an earlier Stop
		if r.Thinking != "" {
			emit(&pb.Reasoning{Text: r.Thinking})
		}
		if r.Content != "" {
			emit(&pb.AgentMessage{Text: r.Content})
		}
		for i, tc := range r.ToolCalls {
			step := r.StepIndex + int64(i) + 1
			id := nativeCallID(p.conversation, step)
			args := normalizeTranscriptArgs(tc.Args)
			call := pendingCall{id: id, name: tc.Name, title: describeTool(tc.Name, args).title, input: string(args), digest: agent.InputDigest(tc.Name, args), kind: toolKindFor(tc.Name)}
			if tc.Name == "write_file" {
				call.artifact = userFacingArtifactPath(args)
			}
			switch {
			case tc.Name == "ask_question":
				if q, err := parseQuestionsFromRaw(id, args); err == nil {
					if !p.reported[id] {
						emit(q)
						p.report(id)
					}
					p.questions[id] = q
				}
			case tc.Name == "write_file" && isPlanWrite(args):
				call.isPlan = true
				emit(&pb.ToolStarted{CallId: id, Kind: call.kind, ItemType: tc.Name, Title: call.title, Input: call.input, InputDigest: call.digest})
				var fields struct {
					Path string `json:"Path"`
				}
				_ = json.Unmarshal(args, &fields)
				text := extractPlanContent(args)
				sum := sha256.Sum256([]byte(text))
				call.plan = &agent.PlanFacts{Path: fields.Path, Revision: hex.EncodeToString(sum[:]), Text: text, CallID: id, Root: p.artifactRoot}

			default:
				emit(&pb.ToolStarted{CallId: id, Kind: call.kind, ItemType: tc.Name, Title: call.title, Input: call.input, InputDigest: call.digest})
			}
			p.pendingCalls[step] = call
		}
		if r.InputTokens > 0 || r.OutputTokens > 0 || r.CacheReadTokens > 0 {
			u := &pb.TokenUsage{}
			if p.usage != nil {
				u = proto.CloneOf(p.usage)
			}
			u.InputTokens += r.InputTokens + r.CacheReadTokens
			u.CachedInputTokens += r.CacheReadTokens
			u.OutputTokens += r.OutputTokens
			u.TotalTokens = u.InputTokens + u.OutputTokens
			u.LastTotalTokens = r.InputTokens + r.CacheReadTokens + r.OutputTokens
			p.usage = u
			emit(proto.CloneOf(u))
		}
		if len(r.ToolCalls) == 0 && p.inTurn {
			outcome := pb.TurnOutcome_TURN_OUTCOME_COMPLETED
			if r.Status == "ERROR" {
				outcome = pb.TurnOutcome_TURN_OUTCOME_ABORTED
			}
			p.pendingOutcome = &pb.TurnEnded{Outcome: outcome}
			p.pendingTime = r.CreatedAt
			p.pendingLine = line
		}

	case "GENERIC":
		call, ok := p.pendingCalls[r.StepIndex]
		if !ok {
			break
		}
		delete(p.pendingCalls, r.StepIndex)
		if r.Status != "ERROR" && call.artifact != "" {
			p.artifacts[call.artifact] = true
			if call.isPlan {
				p.artifactDecisions[call.artifact] = "new"
			} else if p.artifactDecisions[call.artifact] == "" {
				p.artifactDecisions[call.artifact] = "open"
			}
		}
		switch {
		case call.name == "ask_question":
			if q := p.questions[call.id]; q != nil {
				emit(&pb.QuestionAnswered{QuestionId: call.id, Answers: parseQuestionAnswers(q, r.Content)})
				delete(p.questions, call.id)
			}
		case call.isPlan:
			status := "completed"
			if r.Status == "ERROR" {
				status = "failed"
			}
			emit(&pb.ToolCall{CallId: call.id, Kind: call.kind, ItemType: call.name, Title: call.title, Input: call.input, InputDigest: call.digest, Output: r.Content, Status: status})
			if r.Status != "ERROR" && call.plan != nil {
				p.plans[call.plan.Path] = call.plan
				p.lastPlanID = call.id
				name := filepath.Base(call.plan.Path)
				p.planMessages["[Approved] "+name] = true
				p.planMessages["[Rejected] "+name] = true
				if !p.reported[call.id] {
					emit(&pb.PlanProposed{Text: call.plan.Text, CallId: call.id})
					p.report(call.id)
				}
			}
		default:
			status := "completed"
			if r.Status == "ERROR" {
				status = "failed"
			}
			output := r.Content
			truncated := len(output) > 16*1024
			if truncated {
				cut := 16 * 1024
				for cut > 0 && output[cut]&0xc0 == 0x80 {
					cut--
				}
				output = strings.Clone(output[:cut])
			}
			background := pb.ToolBackground_TOOL_BACKGROUND_UNSPECIFIED
			if strings.Contains(r.Content, "Tool is running as a background task") || call.name == "invoke_subagent" {
				background = pb.ToolBackground_TOOL_BACKGROUND_TIMED_OUT
			}
			emit(&pb.ToolCall{Background: background, CallId: call.id, Kind: call.kind, ItemType: call.name, Title: call.title, Input: call.input, InputDigest: call.digest, Output: output, OutputTruncated: truncated, Status: status, Images: p.readMedia(r.Media, pb.ImageSource_IMAGE_SOURCE_TOOL)})
		}
	case "CHECKPOINT":
		emit(&pb.SettingsChanged{Compacted: true})
	}
	return evs, nil
}

func (p *Parser) resolveTruncated(r *transcriptRecord, line int) error {
	if p.transcriptPath == "" {
		return errors.New("truncated transcript has no source path")
	}
	f, err := os.Open(filepath.Join(filepath.Dir(p.transcriptPath), "transcript_full.jsonl"))
	if err != nil {
		return agent.ErrSourcePending
	}
	defer f.Close()
	offset, index := p.fullOffset, p.fullLine
	if line < index {
		offset, index = 0, 0
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	rd := bufio.NewReader(f)
	for index <= line {
		b, err := rd.ReadBytes('\n')
		if err != nil && len(b) == 0 {
			return agent.ErrSourcePending
		}
		if len(b) == 0 || b[len(b)-1] != '\n' {
			return agent.ErrSourcePending
		}
		offset += int64(len(b))
		if index == line {
			var full transcriptRecord
			if json.Unmarshal(b, &full) != nil || full.StepIndex != r.StepIndex || full.Type != r.Type {
				return errors.New("full transcript line does not match")
			}
			*r = full
			p.fullOffset, p.fullLine = offset, index+1
			return nil
		}
		index++
	}
	return nil
}

func (p *Parser) Plan() *agent.PlanFacts {
	if len(p.plans) != 1 || !p.artifactIndexComplete || p.artifactRoot == "" {
		return nil
	}
	for _, plan := range p.plans {
		count := 0
		for path := range p.artifacts {
			if filepath.Base(path) == filepath.Base(plan.Path) {
				count++
			}
		}
		if count != 1 {
			return nil
		}
		copy := *plan
		copy.Decisions = map[string]string{}
		for path := range p.artifacts {
			copy.Decisions[filepath.Base(path)] = p.artifactDecisions[path]
		}
		return &copy
	}
	return nil
}

func userFacingArtifactPath(raw []byte) string {
	var fields struct {
		Path string          `json:"Path"`
		Meta json.RawMessage `json:"ArtifactMetadata"`
	}
	if json.Unmarshal(raw, &fields) != nil || len(fields.Meta) == 0 {
		return ""
	}
	var meta struct {
		Facing *bool `json:"UserFacing"`
	}
	if json.Unmarshal(fields.Meta, &meta) != nil {
		return ""
	}
	if meta.Facing != nil && !*meta.Facing {
		return ""
	}
	return fields.Path
}

func (p *Parser) readMedia(media []transcriptMedia, source pb.ImageSource) []*pb.ImageRef {
	if p.restoring {
		return nil
	}
	var refs []*pb.ImageRef
	base, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(p.transcriptPath), "media"))
	if err != nil {
		return nil
	}
	for i, m := range media {
		if !strings.HasPrefix(m.MimeType, "image/") {
			continue
		}
		u, err := url.Parse(m.URI)
		if err != nil || u.Scheme != "file" || u.Host != "" {
			continue
		}
		path, err := filepath.EvalSymlinks(u.Path)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() > 20<<20 {
			f.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, 20<<20+1))
		f.Close()
		if err != nil || len(data) > 20<<20 {
			continue
		}
		ref := &pb.ImageRef{Source: source, CliIndex: int32(i + 1), Path: path}
		refs = append(refs, ref)
		p.found = append(p.found, agent.FoundImage{Ref: ref, Data: base64.StdEncoding.EncodeToString(data)})
	}
	return refs
}
func stripUserEnvelopes(s string) string {
	if start := strings.Index(s, "<USER_REQUEST>"); start >= 0 {
		start += len("<USER_REQUEST>")
		if end := strings.Index(s[start:], "</USER_REQUEST>"); end >= 0 {
			text := s[start : start+end]
			return strings.TrimSpace(text)
		}
	}
	if meta := strings.Index(s, "<ADDITIONAL_METADATA>"); meta >= 0 {
		s = s[:meta]
	}
	return strings.TrimSpace(s)
}

func isPlanWrite(raw json.RawMessage) bool {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	metaVal, ok := m["ArtifactMetadata"]
	if !ok {
		return false
	}
	switch v := metaVal.(type) {
	case map[string]any:
		rf, _ := v["RequestFeedback"].(bool)
		return rf
	case string:
		var inner map[string]any
		if json.Unmarshal([]byte(v), &inner) == nil {
			rf, _ := inner["RequestFeedback"].(bool)
			return rf
		}
	}
	return false
}

func extractPlanContent(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	contentVal, ok := m["Content"]
	if !ok {
		return ""
	}
	switch v := contentVal.(type) {
	case string:
		if strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"") {
			var unquoted string
			if json.Unmarshal([]byte(v), &unquoted) == nil {
				return unquoted
			}
		}
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

type toolDesc struct {
	title string
}

func describeTool(name string, raw json.RawMessage) toolDesc {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)

	switch name {
	case "run_command":
		if summary, ok := m["toolSummary"].(string); ok && summary != "" {
			return toolDesc{title: unquoteString(summary)}
		}
		if cmd, ok := m["CommandLine"].(string); ok && cmd != "" {
			return toolDesc{title: unquoteString(cmd)}
		}
	case "write_file", "edit_file", "read_file":
		if path, ok := m["Path"].(string); ok && path != "" {
			return toolDesc{title: unquoteString(path)}
		}
	case "call_mcp_tool":
		srv, _ := m["server_name"].(string)
		tl, _ := m["tool_name"].(string)
		if srv != "" || tl != "" {
			return toolDesc{title: fmt.Sprintf("%s/%s", srv, tl)}
		}
	}
	return toolDesc{title: name}
}

func unquoteString(s string) string {
	if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") {
		var u string
		if json.Unmarshal([]byte(s), &u) == nil {
			return u
		}
	}
	return s
}

func toolKindFor(name string) pb.ToolKind {
	switch name {
	case "run_command":
		return pb.ToolKind_TOOL_KIND_COMMAND
	case "write_file", "edit_file", "read_file":
		return pb.ToolKind_TOOL_KIND_FILE_CHANGE
	case "call_mcp_tool":
		return pb.ToolKind_TOOL_KIND_MCP
	default:
		return pb.ToolKind_TOOL_KIND_OTHER
	}
}

func parseQuestionsFromRaw(callID string, raw json.RawMessage) (*pb.QuestionAsked, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	qVal, ok := m["questions"]
	if !ok {
		return nil, errors.New("no questions key in args")
	}
	var items []struct {
		Question      string          `json:"question"`
		Options       json.RawMessage `json:"options"`
		IsMultiSelect bool            `json:"is_multi_select"`
	}
	switch v := qVal.(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &items); err != nil {
			return nil, err
		}
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &items); err != nil {
			return nil, err
		}
	}
	if len(items) == 0 {
		return nil, errors.New("no questions in list")
	}
	qa := &pb.QuestionAsked{QuestionId: callID}
	for _, item := range items {
		q := &pb.Question{
			Id:          item.Question,
			Text:        item.Question,
			MultiSelect: item.IsMultiSelect,
		}
		var strOpts []string
		if json.Unmarshal(item.Options, &strOpts) == nil {
			for _, opt := range strOpts {
				q.Options = append(q.Options, &pb.QuestionOption{Label: opt})
			}
		} else {
			var objOpts []struct {
				Label  string `json:"label"`
				Option string `json:"option"`
			}
			if json.Unmarshal(item.Options, &objOpts) == nil {
				for _, opt := range objOpts {
					lbl := opt.Label
					if lbl == "" {
						lbl = opt.Option
					}
					q.Options = append(q.Options, &pb.QuestionOption{Label: lbl})
				}
			}
		}
		qa.Questions = append(qa.Questions, q)
	}
	return qa, nil
}

func parseQuestionAnswers(q *pb.QuestionAsked, content string) []*pb.QuestionAnswer {
	ansMap := map[int]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if m := ansPrefixRE.FindStringSubmatch(line); len(m) == 3 {
			var idx int
			fmt.Sscanf(m[1], "%d", &idx)
			ansMap[idx] = strings.TrimSpace(m[2])
		}
	}

	var out []*pb.QuestionAnswer
	for i, item := range q.Questions {
		ansStr, ok := ansMap[i+1]
		if !ok && i == 0 && len(ansMap) == 0 {
			// Single question without A1 prefix
			ansStr = strings.TrimSpace(content)
		}
		var answers []string
		if item.MultiSelect {
			parts := strings.Split(ansStr, ",")
			for _, p := range parts {
				if t := strings.TrimSpace(p); t != "" {
					answers = append(answers, t)
				}
			}
		} else if ansStr != "" {
			answers = []string{ansStr}
		}
		out = append(out, &pb.QuestionAnswer{
			QuestionId: item.Id,
			Answers:    answers,
		})
	}
	return out
}

func (a *Adapter) NewParser() agent.Parser   { return NewParser() }
func (a *Adapter) SubmitLog(_ string) string { return a.history }
func (a *Adapter) Submitted(line []byte, match func(string) bool, id string) bool {
	var in struct {
		Text         string `json:"display"`
		Conversation string `json:"conversationId"`
		Type         string `json:"type"`
	}
	if json.Unmarshal(line, &in) != nil || in.Type == "slash_command" && !strings.HasPrefix(in.Text, "/plan ") {
		return false
	}
	if id == "" || in.Conversation == "" || in.Conversation != id {
		return false
	}
	return match(a.NormalizeInput(in.Text))
}
func (a *Adapter) Copied(_ string) func([]byte) bool { return nil }
func (a *Adapter) HookEvents(h *agent.Hook) []*pb.Event {
	switch h.HookEventName {
	case "PreToolUse":
		if h.ToolName == "ask_question" {
			if q, err := parseQuestionsFromRaw(h.ToolUseID, h.ToolInput); err == nil {
				return []*pb.Event{{Kind: &pb.Event_QuestionAsked{QuestionAsked: q}}}
			}
		}

	case "Stop":
		outcome := pb.TurnOutcome_TURN_OUTCOME_COMPLETED
		switch h.TerminationReason {
		case "USER_CANCELED", "ERROR", "MAX_INVOCATIONS", "MAX_FORCED_INVOCATIONS", "MAX_TOKEN_BUDGET_EXCEEDED":
			outcome = pb.TurnOutcome_TURN_OUTCOME_ABORTED
		case "NO_TOOL_CALL", "TERMINAL_STEP_TYPE", "TERMINAL_CUSTOM_HOOK", "INJECTED_RESPONSE":
		default:
			return nil
		}
		return []*pb.Event{{Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: outcome, Reason: h.TerminationReason}}}}
	}
	return nil
}
func (a *Adapter) ModeLocked(ctx *pb.TurnContext) bool {
	return ctx != nil && ctx.ApprovalPolicy == "always-proceed"
}

func (a *Adapter) EventConversation(e *pb.Event) string {
	conversation, _, ok := strings.Cut(e.TurnId, "/")
	if !ok {
		return ""
	}
	return conversation
}

// Transcript args encode values as JSON strings, while hooks carry native
// values. Normalize both before computing digests or parsing questions.
func normalizeTranscriptArgs(raw json.RawMessage) json.RawMessage {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return raw
	}
	for key, v := range values {
		var s string
		if json.Unmarshal(v, &s) == nil && json.Valid([]byte(s)) {
			values[key] = json.RawMessage(s)
		}
	}
	out, _ := json.Marshal(values)
	return out
}

// A no-tool response is a completion candidate, not a boundary: PostInvocation
// hooks may still force continuation or fail. Stop is authoritative; a stable
// idle native screen can recover a Stop missed while the daemon was offline.
func (p *Parser) IdleCompletion() *pb.Event {
	if !p.inTurn || p.pendingOutcome == nil || len(p.pendingCalls) > 0 || p.pendingLine >= p.attachLines {
		return nil
	}
	e := &pb.Event{TurnId: p.turnID, Kind: &pb.Event_TurnEnded{TurnEnded: proto.CloneOf(p.pendingOutcome)}}
	if at, err := time.Parse(time.RFC3339Nano, p.pendingTime); err == nil {
		e.Time = timestamppb.New(at)
	}
	return e
}

// CaptureAttachSnapshot records complete physical lines already present when
// the daemon attaches. CreatedAt can precede writing by an entire inference,
// so timestamps cannot establish that a Stop was missed while offline.
func (p *Parser) CaptureAttachSnapshot(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	rd := bufio.NewReader(io.LimitReader(f, st.Size()))
	count := 0
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			count++
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	p.attachLines = count
	return nil
}

func (p *Parser) BeginPlanAnswer(facts *agent.PlanFacts, choice pb.PlanChoice) {
	p.answeringPlan = ""
	p.planAcknowledgement = nil
	p.answeringChoice = choice
	if facts != nil {
		p.answeringPlan = facts.CallID
	}
}
func (p *Parser) PlanAnswerObserved() bool { return p.planAcknowledgement != nil }
func (p *Parser) EndPlanAnswer(success bool) []*pb.Event {
	var events []*pb.Event
	if !success && p.planAcknowledgement != nil {
		events = append(events, p.planAcknowledgement)
	}
	p.answeringPlan = ""
	p.planAcknowledgement = nil
	return events
}

// Plan-mode input is recorded by agy as /plan followed by the actual prompt.
// This is a native control envelope, not a second human message or a slash-only
// command; normalize both recorded input and a caller's explicit /plan prompt.
func (a *Adapter) NormalizeInput(text string) string {
	if strings.HasPrefix(text, "/plan ") {
		return strings.TrimPrefix(text, "/plan ")
	}
	return text
}
