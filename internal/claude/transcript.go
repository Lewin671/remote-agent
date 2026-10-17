package claude

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MaxOutput bounds tool output copied into events.
const MaxOutput = 16 * 1024

// Parser turns Claude Code transcript lines into unified events
// (docs/claude-code-notes.md §2). It keeps what it needs to pair tool
// results with their calls and to tell turns apart; Replay rebuilds that
// after a restart.
type Parser struct {
	inTurn    bool
	turnID    string
	calls     map[string]call // tool calls without a result yet
	questions map[string]*pb.QuestionAsked
	plans     map[string]bool // ExitPlanMode calls
	// reported are AskUserQuestion and ExitPlanMode calls already turned
	// into events, from the transcript or a PreToolUse hook.
	reported  map[string]bool
	ctx       *pb.TurnContext // last context emitted
	usage     *pb.TokenUsage  // running totals
	lastMsgID string          // assistant message whose usage was counted
	found     []agent.FoundImage
}

// Images returns the images found by the last Parse.
func (p *Parser) Images() []agent.FoundImage {
	f := p.found
	p.found = nil
	return f
}

type call struct {
	name   string
	title  string
	input  string
	kind   pb.ToolKind
	digest string // agent.InputDigest
	path   string // the file a Read call reads
}

// NewParser returns a Parser.
func NewParser() *Parser {
	return &Parser{calls: map[string]call{}, questions: map[string]*pb.QuestionAsked{}, plans: map[string]bool{}, reported: map[string]bool{}}
}

var _ agent.Parser = (*Parser)(nil)

// Replay restores the parser's state from an event logged earlier.
func (p *Parser) Replay(e *pb.Event) {
	switch k := e.Kind.(type) {
	case *pb.Event_TurnStarted:
		p.inTurn, p.turnID = true, e.TurnId
	case *pb.Event_TurnEnded:
		p.inTurn = false
		clear(p.calls)
		clear(p.questions)
	case *pb.Event_PlanProposed:
		if id := k.PlanProposed.CallId; id != "" {
			p.report(id)
			p.plans[id] = true
		}
	case *pb.Event_ToolStarted:
		if t := k.ToolStarted; t.CallId != "" {
			kind := t.Kind
			if kind == pb.ToolKind_TOOL_KIND_UNSPECIFIED {
				kind = pb.ToolKind_TOOL_KIND_OTHER
			}
			c := call{name: t.ItemType, title: t.Title, input: t.Input, kind: kind, digest: t.InputDigest}
			if c.name == "Read" {
				c.path = describe(c.name, json.RawMessage(t.Input)).path
			}
			p.calls[t.CallId] = c
		}
	case *pb.Event_ToolCall:
		delete(p.calls, k.ToolCall.CallId)
	case *pb.Event_QuestionAsked:
		p.report(k.QuestionAsked.QuestionId)
		p.questions[k.QuestionAsked.QuestionId] = k.QuestionAsked
	case *pb.Event_QuestionAnswered:
		delete(p.questions, k.QuestionAnswered.QuestionId)
	case *pb.Event_TurnContext:
		p.ctx = k.TurnContext
	case *pb.Event_TokenUsage:
		p.usage, p.lastMsgID = k.TokenUsage, k.TokenUsage.MessageId
	case *pb.Event_SettingsChanged:
		// A switch made through remote-agent, which the transcript shows
		// only with the next reply, or not at all in Plan mode.
		p.ctx = applySettings(p.ctx, k.SettingsChanged)
	}
}

// Restore has nothing to read again: every record the parser keeps state
// for has an event.
func (p *Parser) Restore(string, int64) error { return nil }

// report records that a question or plan call has its event.
func (p *Parser) report(id string) {
	if len(p.reported) > 1000 {
		clear(p.reported)
	}
	p.reported[id] = true
}

// Reported tells whether the question or plan in e, reported by a hook,
// was already parsed from the transcript.
func (p *Parser) Reported(e *pb.Event) bool {
	switch k := e.Kind.(type) {
	case *pb.Event_QuestionAsked:
		return p.reported[k.QuestionAsked.QuestionId]
	case *pb.Event_PlanProposed:
		return k.PlanProposed.CallId != "" && p.reported[k.PlanProposed.CallId]
	}
	return false
}

type record struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	Timestamp   time.Time       `json:"timestamp"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	IsSummary   bool            `json:"isCompactSummary"`
	PromptID    string          `json:"promptId"`
	Permission  string          `json:"permissionMode"`
	Effort      *string         `json:"perTurnEffort"`
	Origin      *origin         `json:"origin"`
	Message     json.RawMessage `json:"message"`
	ToolResult  json.RawMessage `json:"toolUseResult"`
	Attachment  json.RawMessage `json:"attachment"`
	DurationMS  int64           `json:"durationMs"`
	Compact     *struct {
		PostTokens int64 `json:"postTokens"`
	} `json:"compactMetadata"`
	PasteIDs []int32 `json:"imagePasteIds"`
}

type origin struct {
	Kind string `json:"kind"`
}

type message struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Content    json.RawMessage `json:"content"`
	StopReason *string         `json:"stop_reason"`
	Usage      *struct {
		Input         int64 `json:"input_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
		Output        int64 `json:"output_tokens"`
	} `json:"usage"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

// Parse converts one transcript line into zero or more events without seq
// and session ID. Known metadata is ignored; unknown types are diagnosed.
func (p *Parser) Parse(raw []byte) ([]*pb.Event, error) {
	p.found = nil
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("transcript line: %w", err)
	}
	if r.IsSidechain {
		return nil, nil // a subagent's own conversation
	}
	var evs []*pb.Event
	var err error
	switch r.Type {
	case "user":
		evs, err = p.user(&r)
	case "assistant":
		evs, err = p.assistant(&r)
	case "system":
		switch r.Subtype {
		case "turn_duration", "compact_boundary":
			evs = p.system(&r)
		case "informational", "stop_hook_summary": // terminal notices and hook bookkeeping
		default:
			return nil, agent.ErrUnknownRecord
		}
	case "attachment":
		evs = p.attachment(&r)
	case "ai-title", "api-request", "api-request-blob", "api-request-shape",
		"atis-latch", "cost-state", "file-history-delta", "file-history-snapshot",
		"last-prompt", "mode", "permission-mode", "queue-operation",
		"summary", "progress":
		// Bookkeeping, request tracing and terminal-only progress. Queue
		// acknowledgements are checked independently by Queued below.
	default:
		return nil, agent.ErrUnknownRecord
	}
	if err != nil {
		return nil, err
	}
	for _, e := range evs {
		if !r.Timestamp.IsZero() && e.Time == nil {
			e.Time = timestamppb.New(r.Timestamp)
		}
		if e.TurnId == "" {
			e.TurnId = p.turnID
		}
	}
	return evs, nil
}

const interrupted = "[Request interrupted by user"

func (p *Parser) user(r *record) ([]*pb.Event, error) {
	if r.IsMeta || r.IsSummary {
		return nil, nil
	}
	var m message
	if err := json.Unmarshal(r.Message, &m); err != nil {
		return nil, err
	}
	var text string
	var blocks []block
	if json.Unmarshal(m.Content, &text) != nil {
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			return nil, err
		}
	}
	var evs []*pb.Event
	// Tool results.
	for _, b := range blocks {
		if b.Type == "tool_result" {
			evs = append(evs, p.toolResult(b, r.ToolResult)...)
		}
	}
	if len(evs) > 0 {
		return evs, nil
	}
	// The end of an interrupted turn.
	for _, b := range blocks {
		if b.Type == "text" && strings.HasPrefix(b.Text, interrupted) {
			return p.endTurn(pb.TurnOutcome_TURN_OUTCOME_ABORTED, "interrupted", 0), nil
		}
	}
	// What a person typed starts a turn, and so does a background task's
	// notice, which Claude Code answers by itself; slash commands and their
	// output carry no origin.
	if r.Origin != nil && r.Origin.Kind == "task-notification" {
		return p.startTurn(r), nil
	}
	if r.Origin == nil {
		if c := p.modelSet(text); c != nil {
			return []*pb.Event{c}, nil
		}
		return nil, nil
	}
	if r.Origin.Kind != "human" {
		return nil, nil
	}
	evs = append(evs, p.startTurn(r)...)
	evs = append(evs, &pb.Event{TurnId: r.PromptID, Kind: &pb.Event_UserMessage{UserMessage: p.userMessage(text, blocks, r.PasteIDs)}})
	return evs, nil
}

// userMessage builds a message from content that is either plain text or,
// when images were pasted, text and image blocks.
func (p *Parser) userMessage(text string, blocks []block, pasteIDs []int32) *pb.UserMessage {
	msg := &pb.UserMessage{Text: text}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			ref := &pb.ImageRef{Source: pb.ImageSource_IMAGE_SOURCE_USER}
			// imagePasteIds are the N of each "[Image #N]", in order.
			if n := len(msg.Images); n < len(pasteIDs) {
				ref.CliIndex = pasteIDs[n]
			}
			msg.Images = append(msg.Images, p.image(ref, b))
		}
	}
	if len(parts) > 0 {
		msg.Text = strings.Join(parts, "\n")
	}
	msg.Text = unwrapPasted(msg.Text)
	return msg
}

// pastedOpenRE matches the tag Claude Code opens a collapsed paste with.
var pastedOpenRE = regexp.MustCompile(`\n*<pasted_content id="([^"\n]*)">\n`)

// unwrapPasted takes out the tags Claude Code records around a paste it
// showed as "[Pasted text #N +M lines]" (2.1.287):
//
//	\n\n<pasted_content id="94cd">\n…\n</pasted_content id="94cd">\n
//
// leaving the pasted text, so that a message reads as it was written. An
// opening tag without its closing one is left alone.
func unwrapPasted(text string) string {
	if !strings.Contains(text, "<pasted_content id=") {
		return text
	}
	var out strings.Builder
	rest, found := text, false
	for {
		m := pastedOpenRE.FindStringSubmatchIndex(rest)
		if m == nil {
			break
		}
		end := "\n</pasted_content id=\"" + rest[m[2]:m[3]] + "\">"
		n := strings.Index(rest[m[1]:], end)
		if n < 0 {
			break
		}
		// The line breaks Claude Code adds around the tags separate the
		// paste from what was typed next to it.
		if before := rest[:m[0]]; before != "" {
			out.WriteString(before + "\n")
		}
		out.WriteString(rest[m[1] : m[1]+n])
		rest = strings.TrimLeft(rest[m[1]+n+len(end):], "\n")
		if rest != "" {
			out.WriteString("\n")
		}
		found = true
	}
	if !found {
		return text
	}
	return out.String() + rest
}

// modelSetRE matches /model's output: "Set model to `Sonnet 5.5` for this
// session only with `low` effort", "Set model to `Fable 5.1` and saved as
// your default for new sessions". An effort that did not change is not
// named, and the first builds of 2.1.284 wrote no quotes
// (claude-code-notes §9).
var modelSetRE = regexp.MustCompile("^<local-command-stdout>Set model to `?([^`<]+?)`?(?: for this session only| and saved as your default for new sessions| as the default)?(?: with `?(\\w+)`? effort)?</local-command-stdout>$")

// modelSet reads a model switch from the output of /model, however it was
// run: typed in the terminal, it is recorded nowhere else until the next
// reply, and in Plan mode not even there. The model becomes its alias, as
// for a switch made through remote-agent; one that is none of the aliases
// keeps the name Claude Code prints.
func (p *Parser) modelSet(text string) *pb.Event {
	m := modelSetRE.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	next := &pb.TurnContext{}
	if p.ctx != nil {
		next = proto.CloneOf(p.ctx)
	}
	next.Model = m[1]
	for _, info := range models {
		if strings.EqualFold(strings.Fields(m[1])[0], info.DisplayName) {
			next.Model = info.Slug
			if len(info.Efforts) == 0 {
				next.Effort = ""
			}
		}
	}
	if m[2] != "" {
		next.Effort = m[2]
	}
	if p.ctx != nil && proto.Equal(next, p.ctx) {
		return nil
	}
	p.ctx = next
	return &pb.Event{Kind: &pb.Event_TurnContext{TurnContext: proto.CloneOf(next)}}
}

// startTurn opens the turn of a user record.
func (p *Parser) startTurn(r *record) []*pb.Event {
	var evs []*pb.Event
	if p.inTurn {
		// The previous turn left no end record, as when it failed.
		evs = append(evs, p.endTurn(pb.TurnOutcome_TURN_OUTCOME_ABORTED, "no end recorded", 0)...)
	}
	p.inTurn, p.turnID = true, r.PromptID
	evs = append(evs, &pb.Event{TurnId: r.PromptID, Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	if c := p.context(r.Permission, "", nil); c != nil {
		evs = append(evs, c)
	}
	return evs
}

func (p *Parser) endTurn(outcome pb.TurnOutcome, reason string, ms int64) []*pb.Event {
	if !p.inTurn {
		return nil
	}
	p.inTurn = false
	clear(p.calls)
	e := &pb.Event{TurnId: p.turnID, Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{Outcome: outcome, Reason: reason, DurationMs: ms}}}
	// Questions of an ended turn can no longer be answered.
	clear(p.questions)
	return []*pb.Event{e}
}

// context emits the turn context when the model, effort or permission
// mode differs from the last one emitted.
func (p *Parser) context(mode, model string, effort *string) *pb.Event {
	next := &pb.TurnContext{}
	if p.ctx != nil {
		next = proto.CloneOf(p.ctx)
	}
	if mode != "" {
		next.PermissionMode = mode
	}
	if model != "" {
		next.Model = model
		next.Effort = ""
		if effort != nil {
			next.Effort = *effort
		}
	}
	if p.ctx != nil && proto.Equal(next, p.ctx) {
		return nil
	}
	p.ctx = next
	return &pb.Event{Kind: &pb.Event_TurnContext{TurnContext: proto.CloneOf(next)}}
}

func (p *Parser) assistant(r *record) ([]*pb.Event, error) {
	var m message
	if err := json.Unmarshal(r.Message, &m); err != nil {
		return nil, err
	}
	var blocks []block
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, err
	}
	var evs []*pb.Event
	// In Plan mode Claude Code answers with another model for the time
	// being (claude-code-notes §2); the session's model is the one it goes
	// back to, which resuming must keep.
	planning := p.ctx != nil && p.ctx.PermissionMode == "plan"
	if m.Model != "" && m.Model != "<synthetic>" && !planning {
		if c := p.context("", m.Model, r.Effort); c != nil {
			evs = append(evs, c)
		}
	}
	phase := "commentary"
	if m.StopReason != nil && *m.StopReason == "end_turn" {
		phase = "final_answer"
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				evs = append(evs, &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: b.Text, Phase: phase}}})
			}
		case "thinking":
			if strings.TrimSpace(b.Thinking) == "" {
				continue
			}
			// Claude Code prints a narration as a message of its own, not
			// as a folded thought (claude-code-notes §2).
			if narration(b.Signature) {
				evs = append(evs, &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: strings.TrimSpace(b.Thinking), Phase: "commentary"}}})
			} else {
				evs = append(evs, &pb.Event{Kind: &pb.Event_Reasoning{Reasoning: &pb.Reasoning{Text: b.Thinking}}})
			}
		case "tool_use":
			evs = append(evs, p.toolUse(b)...)
		}
	}
	// Every content block of a reply is its own line with the same usage;
	// count it once.
	if m.Usage != nil && m.ID != "" && m.ID != p.lastMsgID {
		p.lastMsgID = m.ID
		u := &pb.TokenUsage{}
		if p.usage != nil {
			u = proto.CloneOf(p.usage)
		}
		in := m.Usage.Input + m.Usage.CacheCreation + m.Usage.CacheRead
		u.InputTokens += in
		u.CachedInputTokens += m.Usage.CacheRead
		u.OutputTokens += m.Usage.Output
		u.TotalTokens = u.InputTokens + u.OutputTokens
		u.LastTotalTokens = in + m.Usage.Output
		u.ContextWindow = ContextWindow(m.Model)
		u.MessageId = m.ID
		p.usage = u
		evs = append(evs, &pb.Event{Kind: &pb.Event_TokenUsage{TokenUsage: proto.CloneOf(u)}})
	}
	return evs, nil
}

// narration reports whether a thinking block is one the model wrote for the
// user to read. The block looks like any other thought; only the label
// inside its signature (field 2 > 1 > 8) says "narration" instead of
// "thinking". A signature that does not parse is a thought.
func narration(signature string) bool {
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	header, ok := field(raw, 2)
	if !ok {
		return false
	}
	if header, ok = field(header, 1); !ok {
		return false
	}
	label, ok := field(header, 8)
	return ok && string(label) == "narration"
}

// field returns the first length-delimited field num of a protobuf message.
func field(msg []byte, num protowire.Number) ([]byte, bool) {
	for len(msg) > 0 {
		n, typ, size := protowire.ConsumeTag(msg)
		if size < 0 {
			return nil, false
		}
		msg = msg[size:]
		if n == num && typ == protowire.BytesType {
			v, size := protowire.ConsumeBytes(msg)
			return v, size >= 0
		}
		if size = protowire.ConsumeFieldValue(n, typ, msg); size < 0 {
			return nil, false
		}
		msg = msg[size:]
	}
	return nil, false
}

func (p *Parser) toolUse(b block) []*pb.Event {
	switch b.Name {
	case "AskUserQuestion":
		if p.reported[b.ID] {
			return nil // reported by the PreToolUse hook
		}
		q, err := ParseQuestions(b.ID, b.Input)
		if err != nil {
			break
		}
		p.report(b.ID)
		p.questions[b.ID] = q
		return []*pb.Event{{Kind: &pb.Event_QuestionAsked{QuestionAsked: q}}}
	case "ExitPlanMode":
		p.plans[b.ID] = true
		if p.reported[b.ID] {
			return nil
		}
		p.report(b.ID)
		return []*pb.Event{{Kind: &pb.Event_PlanProposed{PlanProposed: PlanFromInput(b.ID, b.Input)}}}
	}
	c := describe(b.Name, b.Input)
	p.calls[b.ID] = c
	return []*pb.Event{{Kind: &pb.Event_ToolStarted{ToolStarted: &pb.ToolStarted{
		Title: c.title, CallId: b.ID, Kind: c.kind, Input: c.input, ItemType: c.name, InputDigest: c.digest,
	}}}}
}

// PlanFromInput is the plan in ExitPlanMode's input.
func PlanFromInput(callID string, input []byte) *pb.PlanProposed {
	var in struct {
		Plan string `json:"plan"`
	}
	_ = json.Unmarshal(input, &in)
	return &pb.PlanProposed{Text: in.Plan, CallId: callID}
}

// describe names a tool call for display.
func describe(name string, raw json.RawMessage) call {
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	c := call{name: name, title: name, kind: pb.ToolKind_TOOL_KIND_OTHER, input: compactJSON(raw), digest: agent.InputDigest(name, raw)}
	switch name {
	case "Bash":
		c.kind, c.title, c.input = pb.ToolKind_TOOL_KIND_COMMAND, str("command"), str("command")
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		c.kind = pb.ToolKind_TOOL_KIND_FILE_CHANGE
		if f := str("file_path"); f != "" {
			c.title = f
		} else if f := str("notebook_path"); f != "" {
			c.title = f
		}
	case "Read":
		c.title = "Read " + str("file_path")
		c.path = str("file_path")
	case "Glob", "Grep":
		c.title = name + " " + str("pattern")
	case "WebSearch":
		c.kind, c.title = pb.ToolKind_TOOL_KIND_WEB_SEARCH, str("query")
	case "WebFetch":
		c.kind, c.title = pb.ToolKind_TOOL_KIND_WEB_SEARCH, str("url")
	case "Task", "Agent":
		if d := str("description"); d != "" {
			c.title = name + ": " + d
		}
	default:
		if strings.HasPrefix(name, "mcp__") {
			c.kind = pb.ToolKind_TOOL_KIND_MCP
			c.title = strings.Replace(strings.TrimPrefix(name, "mcp__"), "__", ".", 1)
		}
	}
	if strings.TrimSpace(c.title) == "" {
		c.title = name
	}
	return c
}

func (p *Parser) toolResult(b block, structured json.RawMessage) []*pb.Event {
	if q, ok := p.questions[b.ToolUseID]; ok {
		delete(p.questions, b.ToolUseID)
		answers := parseAnswers(q, structured)
		if b.IsError || answers == nil {
			return nil // declined; the turn's end follows
		}
		return []*pb.Event{{Kind: &pb.Event_QuestionAnswered{QuestionAnswered: &pb.QuestionAnswered{QuestionId: b.ToolUseID, Answers: answers}}}}
	}
	if p.plans[b.ToolUseID] {
		delete(p.plans, b.ToolUseID)
		return nil // approved or not, the plan card closes with the prompt
	}
	c, ok := p.calls[b.ToolUseID]
	if !ok {
		c = call{title: "tool", kind: pb.ToolKind_TOOL_KIND_OTHER}
	}
	delete(p.calls, b.ToolUseID)
	tc := &pb.ToolCall{CallId: b.ToolUseID, Kind: c.kind, Title: c.title, Input: c.input, Status: "completed", ItemType: c.name, InputDigest: c.digest}
	if b.IsError {
		tc.Status = "failed"
	}
	tc.Background, tc.BackgroundId = background(structured)
	tc.Output, tc.OutputTruncated = truncate(resultText(b.Content))
	var inner []block
	if json.Unmarshal(b.Content, &inner) == nil {
		for _, ib := range inner {
			if ib.Type != "image" {
				continue
			}
			ref := &pb.ImageRef{Source: pb.ImageSource_IMAGE_SOURCE_TOOL, Path: c.path}
			if d := readDimensions(structured); d != nil && len(tc.Images) == 0 {
				// Read shrinks images over 2000px; the original size is
				// what the file has.
				if d.OriginalWidth != d.DisplayWidth || d.OriginalHeight != d.DisplayHeight {
					ref.OriginalWidth, ref.OriginalHeight = d.OriginalWidth, d.OriginalHeight
				}
			}
			tc.Images = append(tc.Images, p.image(ref, ib))
		}
	}
	return []*pb.Event{{Kind: &pb.Event_ToolCall{ToolCall: tc}}}
}

// image records an image block for ref.
func (p *Parser) image(ref *pb.ImageRef, b block) *pb.ImageRef {
	if b.Source == nil || b.Source.Type != "base64" || b.Source.Data == "" {
		ref.Sync = pb.ImageSync_IMAGE_SYNC_NAME_ONLY
		return ref
	}
	p.found = append(p.found, agent.FoundImage{Ref: ref, Data: b.Source.Data})
	return ref
}

type dimensions struct {
	OriginalWidth  int32 `json:"originalWidth"`
	OriginalHeight int32 `json:"originalHeight"`
	DisplayWidth   int32 `json:"displayWidth"`
	DisplayHeight  int32 `json:"displayHeight"`
}

// readDimensions reads the sizes Read reports for an image file.
func readDimensions(structured json.RawMessage) *dimensions {
	var r struct {
		Type string `json:"type"`
		File *struct {
			Dimensions *dimensions `json:"dimensions"`
		} `json:"file"`
	}
	if json.Unmarshal(structured, &r) != nil || r.Type != "image" || r.File == nil {
		return nil
	}
	return r.File.Dimensions
}

// resultText is a tool result's content: a string, or blocks of which the
// text ones are kept.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ToolName string `json:"tool_name"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch {
		case b.Text != "":
			parts = append(parts, b.Text)
		case b.ToolName != "":
			parts = append(parts, b.ToolName)
		default:
			parts = append(parts, "["+b.Type+"]")
		}
	}
	return strings.Join(parts, "\n")
}

func (p *Parser) system(r *record) []*pb.Event {
	switch r.Subtype {
	case "turn_duration":
		return p.endTurn(pb.TurnOutcome_TURN_OUTCOME_COMPLETED, "", r.DurationMS)
	case "compact_boundary":
		// Recorded however it was started: /compact typed in the terminal,
		// sent through remote-agent, or automatic.
		evs := []*pb.Event{{Kind: &pb.Event_SettingsChanged{SettingsChanged: &pb.SettingsChanged{Compacted: true}}}}
		if r.Compact != nil && p.usage != nil {
			u := proto.CloneOf(p.usage)
			u.LastTotalTokens = r.Compact.PostTokens
			p.usage = u
			evs = append(evs, &pb.Event{Kind: &pb.Event_TokenUsage{TokenUsage: proto.CloneOf(u)}})
		}
		return evs
	}
	return nil
}

func (p *Parser) attachment(r *record) []*pb.Event {
	var a struct {
		Type     string          `json:"type"`
		Prompt   json.RawMessage `json:"prompt"`
		Origin   *origin         `json:"origin"`
		PasteIDs []int32         `json:"imagePasteIds"`
	}
	if json.Unmarshal(r.Attachment, &a) != nil {
		return nil
	}
	// A message sent while the turn ran, taken into it. With pasted images
	// the prompt is a list of blocks instead of a string.
	if a.Type != "queued_command" || a.Origin == nil || a.Origin.Kind != "human" {
		return nil
	}
	var text string
	var blocks []block
	if json.Unmarshal(a.Prompt, &text) != nil && json.Unmarshal(a.Prompt, &blocks) != nil {
		return nil
	}
	msg := p.userMessage(text, blocks, a.PasteIDs)
	if msg.Text == "" && len(msg.Images) == 0 {
		return nil
	}
	return []*pb.Event{{Kind: &pb.Event_UserMessage{UserMessage: msg}}}
}

// ParseQuestions converts AskUserQuestion's input into a QuestionAsked.
// Claude Code gives questions no ID; the question text is used, as it keys
// the answers.
func ParseQuestions(callID string, input []byte) (*pb.QuestionAsked, error) {
	var in struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("AskUserQuestion input: %w", err)
	}
	q := &pb.QuestionAsked{QuestionId: callID}
	for _, x := range in.Questions {
		item := &pb.Question{Id: x.Question, Header: x.Header, Text: x.Question, MultiSelect: x.MultiSelect}
		for _, o := range x.Options {
			item.Options = append(item.Options, &pb.QuestionOption{Label: o.Label, Description: o.Description})
		}
		q.Questions = append(q.Questions, item)
	}
	return q, nil
}

// parseAnswers reads toolUseResult.answers: {"<question>":"<label>"}, with
// the labels of a multi-select question joined by ", ".
func parseAnswers(q *pb.QuestionAsked, raw json.RawMessage) []*pb.QuestionAnswer {
	var res struct {
		Answers map[string]string `json:"answers"`
	}
	if json.Unmarshal(raw, &res) != nil || res.Answers == nil {
		return nil
	}
	var out []*pb.QuestionAnswer
	seen := map[string]bool{}
	for _, item := range q.Questions {
		a, ok := res.Answers[item.Text]
		if !ok {
			continue
		}
		seen[item.Text] = true
		out = append(out, &pb.QuestionAnswer{QuestionId: item.Id, Answers: splitAnswer(item, a)})
	}
	// Answers to questions the call did not list, in a stable order.
	var rest []string
	for k := range res.Answers {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		out = append(out, &pb.QuestionAnswer{QuestionId: k, Answers: []string{res.Answers[k]}})
	}
	return out
}

// splitAnswer splits a multi-select answer into labels when every part is
// one of the options; a typed answer stays whole.
func splitAnswer(item *pb.Question, a string) []string {
	if !item.MultiSelect {
		return []string{a}
	}
	labels := map[string]bool{}
	for _, o := range item.Options {
		labels[o.Label] = true
	}
	parts := strings.Split(a, ", ")
	for _, p := range parts {
		if !labels[p] {
			return []string{a}
		}
	}
	return parts
}

// ContextWindow is the context size of a model. Claude Code does not write
// it to the transcript; haiku 4.5 has 200k and sonnet 5.5 1M
// (claude-code-notes §2). Other current models are assumed to have 1M.
func ContextWindow(model string) int64 {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "[1m]"):
		return 1_000_000
	case strings.Contains(m, "haiku"):
		return 200_000
	case strings.Contains(m, "claude-3"), strings.Contains(m, "-4-"), strings.HasSuffix(m, "-4"):
		return 200_000
	case strings.HasPrefix(m, "claude-"):
		return 1_000_000
	}
	return 0
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	s, _ := truncate(string(b))
	return s
}

func truncate(s string) (string, bool) {
	if len(s) <= MaxOutput {
		return s, false
	}
	cut := MaxOutput
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return strings.Clone(s[:cut]), true
}

// background tells from a tool's structured result whether it went on in
// the background: a Bash call with a background task (asked for, moved
// after its timeout, or by the user), or a subagent launched async.
func background(structured json.RawMessage) (pb.ToolBackground, string) {
	var r struct {
		BackgroundTaskID   string `json:"backgroundTaskId"`
		BackgroundedByUser bool   `json:"backgroundedByUser"`
		TimedOutAfterMs    int64  `json:"timedOutAfterMs"`
		IsAsync            bool   `json:"isAsync"`
		Status             string `json:"status"`
		AgentID            string `json:"agentId"`
	}
	if json.Unmarshal(structured, &r) != nil {
		return pb.ToolBackground_TOOL_BACKGROUND_UNSPECIFIED, ""
	}
	switch {
	case r.BackgroundTaskID != "" && r.BackgroundedByUser:
		return pb.ToolBackground_TOOL_BACKGROUND_BY_USER, r.BackgroundTaskID
	case r.BackgroundTaskID != "" && r.TimedOutAfterMs > 0:
		return pb.ToolBackground_TOOL_BACKGROUND_TIMED_OUT, r.BackgroundTaskID
	case r.BackgroundTaskID != "":
		return pb.ToolBackground_TOOL_BACKGROUND_REQUESTED, r.BackgroundTaskID
	case r.IsAsync || r.Status == "async_launched":
		return pb.ToolBackground_TOOL_BACKGROUND_REQUESTED, r.AgentID
	}
	return pb.ToolBackground_TOOL_BACKGROUND_UNSPECIFIED, ""
}
