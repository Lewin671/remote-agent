// Package codex adapts the native Codex CLI: its launch arguments, hooks,
// rollout files and screen.
package codex

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MaxOutput bounds tool output copied into events.
const MaxOutput = 16 * 1024

// Parser turns rollout lines into unified events. It keeps a little state
// to drop duplicates; a fresh Parser is safe to use after a restart.
type Parser struct {
	questions map[string]bool // call IDs of request_user_input calls
}

// NewParser returns a Parser.
func NewParser() *Parser { return &Parser{questions: map[string]bool{}} }

// RememberQuestion restores, after a restart, that callID is a question
// whose rendering as an agent message must be dropped.
func (p *Parser) RememberQuestion(callID string) { p.questions[callID] = true }

// Reported: Codex has no hook events.
func (p *Parser) Reported(*pb.Event) bool { return false }

// Replay restores what the parser needs from an event logged earlier.
func (p *Parser) Replay(e *pb.Event) {
	if q := e.GetQuestionAsked(); q != nil {
		p.RememberQuestion(q.QuestionId)
	}
}

type line struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type typed struct {
	Type string `json:"type"`
}

// Parse converts one rollout line into zero or more events without seq and
// session ID. Unknown record types yield no events.
func (p *Parser) Parse(raw []byte) ([]*pb.Event, error) {
	var l line
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("rollout line: %w", err)
	}
	var evs []*pb.Event
	var err error
	switch l.Type {
	case "event_msg":
		evs, err = p.eventMsg(l.Payload)
	case "turn_context":
		evs, err = turnContext(l.Payload)
	case "response_item":
		evs, err = p.responseItem(l.Payload)
	}
	if err != nil {
		return nil, err
	}
	for _, e := range evs {
		if !l.Timestamp.IsZero() {
			e.Time = timestamppb.New(l.Timestamp)
		}
	}
	return evs, nil
}

func (p *Parser) eventMsg(raw json.RawMessage) ([]*pb.Event, error) {
	var t typed
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	switch t.Type {
	case "task_started":
		var m struct {
			TurnID string `json:"turn_id"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}}), nil
	case "task_complete":
		var m struct {
			TurnID     string `json:"turn_id"`
			DurationMS int64  `json:"duration_ms"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
			Outcome: pb.TurnOutcome_TURN_OUTCOME_COMPLETED, DurationMs: m.DurationMS,
		}}}), nil
	case "turn_aborted":
		var m struct {
			TurnID     string `json:"turn_id"`
			Reason     string `json:"reason"`
			DurationMS int64  `json:"duration_ms"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{
			Outcome: pb.TurnOutcome_TURN_OUTCOME_ABORTED, Reason: m.Reason, DurationMs: m.DurationMS,
		}}}), nil
	case "token_count":
		return tokenCount(raw)
	case "item_completed":
		return p.itemCompleted(raw)
	}
	return nil, nil
}

type usage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type rateLimit struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int64   `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

func (r *rateLimit) pb() *pb.RateLimit {
	if r == nil {
		return nil
	}
	return &pb.RateLimit{UsedPercent: r.UsedPercent, WindowMinutes: r.WindowMinutes, ResetsAt: r.ResetsAt}
}

func tokenCount(raw json.RawMessage) ([]*pb.Event, error) {
	var m struct {
		Info *struct {
			Total   usage `json:"total_token_usage"`
			Last    usage `json:"last_token_usage"`
			Context int64 `json:"model_context_window"`
		} `json:"info"`
		RateLimits *struct {
			Primary   *rateLimit `json:"primary"`
			Secondary *rateLimit `json:"secondary"`
			PlanType  string     `json:"plan_type"`
		} `json:"rate_limits"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.Info == nil {
		return nil, nil
	}
	u := &pb.TokenUsage{
		InputTokens:           m.Info.Total.InputTokens,
		CachedInputTokens:     m.Info.Total.CachedInputTokens,
		OutputTokens:          m.Info.Total.OutputTokens,
		ReasoningOutputTokens: m.Info.Total.ReasoningOutputTokens,
		TotalTokens:           m.Info.Total.TotalTokens,
		LastTotalTokens:       m.Info.Last.TotalTokens,
		ContextWindow:         m.Info.Context,
	}
	if r := m.RateLimits; r != nil {
		u.PrimaryLimit, u.SecondaryLimit, u.PlanType = r.Primary.pb(), r.Secondary.pb(), r.PlanType
	}
	return one("", &pb.Event{Kind: &pb.Event_TokenUsage{TokenUsage: u}}), nil
}

func turnContext(raw json.RawMessage) ([]*pb.Event, error) {
	var m struct {
		TurnID            string `json:"turn_id"`
		Model             string `json:"model"`
		Effort            string `json:"effort"`
		ApprovalPolicy    string `json:"approval_policy"`
		ApprovalsReviewer string `json:"approvals_reviewer"`
		SandboxPolicy     struct {
			Type string `json:"type"`
		} `json:"sandbox_policy"`
		CollaborationMode struct {
			Mode string `json:"mode"`
		} `json:"collaboration_mode"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return one(m.TurnID, &pb.Event{Kind: &pb.Event_TurnContext{TurnContext: &pb.TurnContext{
		Model:             m.Model,
		Effort:            m.Effort,
		ApprovalPolicy:    m.ApprovalPolicy,
		ApprovalsReviewer: m.ApprovalsReviewer,
		Sandbox:           m.SandboxPolicy.Type,
		CollaborationMode: m.CollaborationMode.Mode,
	}}}), nil
}

type duration struct {
	Secs  int64 `json:"secs"`
	Nanos int64 `json:"nanos"`
}

func (d duration) ms() int64 { return d.Secs*1000 + d.Nanos/1e6 }

func (p *Parser) itemCompleted(raw json.RawMessage) ([]*pb.Event, error) {
	var m struct {
		TurnID string          `json:"turn_id"`
		Item   json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	var head struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(m.Item, &head); err != nil {
		return nil, err
	}
	switch head.Type {
	case "UserMessage":
		var it struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(m.Item, &it); err != nil {
			return nil, err
		}
		msg := &pb.UserMessage{}
		var parts []string
		for _, c := range it.Content {
			switch c.Type {
			case "text":
				parts = append(parts, c.Text)
			case "image", "local_image", "input_image":
				msg.ImageCount++
			}
		}
		msg.Text = strings.Join(parts, "\n")
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_UserMessage{UserMessage: msg}}), nil

	case "AgentMessage":
		// request_user_input_async is also rendered as an agent message
		// whose ID is the tool call ID; the question event already has it.
		if p.questions[head.ID] {
			return nil, nil
		}
		var it struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Phase string `json:"phase"`
		}
		if err := json.Unmarshal(m.Item, &it); err != nil {
			return nil, err
		}
		var parts []string
		for _, c := range it.Content {
			parts = append(parts, c.Text)
		}
		text := strings.Join(parts, "\n")
		// In Plan mode the plan comes twice: as a Plan item, which becomes
		// the event, and as this message wrapped in <proposed_plan>.
		if strings.HasPrefix(strings.TrimSpace(text), "<proposed_plan>") {
			return nil, nil
		}
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{
			Text: text, Phase: it.Phase,
		}}}), nil

	case "Plan":
		var it struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(m.Item, &it); err != nil {
			return nil, err
		}
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_PlanProposed{PlanProposed: &pb.PlanProposed{Text: it.Text}}}), nil

	case "Reasoning":
		var it struct {
			Summary []string `json:"summary_text"`
		}
		if err := json.Unmarshal(m.Item, &it); err != nil {
			return nil, err
		}
		if len(it.Summary) == 0 {
			return nil, nil
		}
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_Reasoning{Reasoning: &pb.Reasoning{
			Text: strings.Join(it.Summary, "\n\n"),
		}}}), nil

	case "CommandExecution":
		var it struct {
			ID       string          `json:"id"`
			Command  json.RawMessage `json:"command"`
			Status   string          `json:"status"`
			Output   string          `json:"aggregated_output"`
			ExitCode *int32          `json:"exit_code"`
			Duration duration        `json:"duration"`
		}
		if err := json.Unmarshal(m.Item, &it); err != nil {
			return nil, err
		}
		title := commandTitle(it.Command)
		tc := &pb.ToolCall{
			CallId: it.ID, Kind: pb.ToolKind_TOOL_KIND_COMMAND, Title: title, Input: title,
			Status: it.Status, ExitCode: it.ExitCode, DurationMs: it.Duration.ms(), ItemType: head.Type,
		}
		tc.Output, tc.OutputTruncated = truncate(it.Output)
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: tc}}), nil

	case "FileChange":
		var it struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Changes map[string]struct {
				Type string `json:"type"`
				Diff string `json:"unified_diff"`
			} `json:"changes"`
		}
		if err := json.Unmarshal(m.Item, &it); err != nil {
			return nil, err
		}
		paths := make([]string, 0, len(it.Changes))
		for path := range it.Changes {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		var b strings.Builder
		for _, path := range paths {
			c := it.Changes[path]
			fmt.Fprintf(&b, "%s %s\n%s\n", c.Type, path, c.Diff)
		}
		tc := &pb.ToolCall{
			CallId: it.ID, Kind: pb.ToolKind_TOOL_KIND_FILE_CHANGE, Title: strings.Join(paths, ", "),
			Status: it.Status, ItemType: head.Type,
		}
		tc.Output, tc.OutputTruncated = truncate(b.String())
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: tc}}), nil

	case "McpToolCall":
		var it struct {
			ID        string          `json:"id"`
			Server    string          `json:"server"`
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
			Status    string          `json:"status"`
			Result    *struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
			Error    json.RawMessage `json:"error"`
			Duration duration        `json:"duration"`
		}
		if err := json.Unmarshal(m.Item, &it); err != nil {
			return nil, err
		}
		var out []string
		if it.Result != nil {
			for _, c := range it.Result.Content {
				if c.Text != "" {
					out = append(out, c.Text)
				} else {
					out = append(out, "["+c.Type+"]")
				}
			}
		} else if len(it.Error) > 0 && string(it.Error) != "null" {
			out = append(out, string(it.Error))
		}
		tc := &pb.ToolCall{
			CallId: it.ID, Kind: pb.ToolKind_TOOL_KIND_MCP, Title: it.Server + "." + it.Tool,
			Input: compactJSON(it.Arguments), Status: it.Status, DurationMs: it.Duration.ms(), ItemType: head.Type,
		}
		tc.Output, tc.OutputTruncated = truncate(strings.Join(out, "\n"))
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: tc}}), nil

	case "Extension":
		var it struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Query  string `json:"query"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(m.Item, &it); err != nil {
			return nil, err
		}
		kind := pb.ToolKind_TOOL_KIND_OTHER
		if it.Kind == "web.search" {
			kind = pb.ToolKind_TOOL_KIND_WEB_SEARCH
		}
		title := it.Query
		if title == "" {
			title = it.Kind
		}
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{
			CallId: it.ID, Kind: kind, Title: title, Status: it.Status, ItemType: head.Type + ":" + it.Kind,
		}}}), nil

	case "ImageView":
		var it struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal(m.Item, &it); err != nil {
			return nil, err
		}
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{
			CallId: it.ID, Kind: pb.ToolKind_TOOL_KIND_OTHER, Title: "view image " + strings.TrimPrefix(it.Path, "file://"),
			Status: "completed", ItemType: head.Type,
		}}}), nil

	case "ContextCompaction":
		// Recorded however it was started: /compact typed in the
		// terminal, or sent through remote-agent.
		return one(m.TurnID, &pb.Event{Kind: &pb.Event_SettingsChanged{SettingsChanged: &pb.SettingsChanged{Compacted: true}}}), nil
	}

	// Other item types (sub-agents, compaction, ...) are kept as generic
	// tool calls so the stream stays complete.
	tc := &pb.ToolCall{CallId: head.ID, Kind: pb.ToolKind_TOOL_KIND_OTHER, Title: head.Type, ItemType: head.Type}
	tc.Input, _ = truncate(compactJSON(m.Item))
	return one(m.TurnID, &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: tc}}), nil
}

func (p *Parser) responseItem(raw json.RawMessage) ([]*pb.Event, error) {
	var m struct {
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     string          `json:"input"`
		CallID    string          `json:"call_id"`
		Output    json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	switch m.Type {
	case "custom_tool_call":
		// Written when the call starts; the item_completed record comes
		// when it ends.
		return one("", &pb.Event{Kind: &pb.Event_ToolStarted{ToolStarted: &pb.ToolStarted{Title: toolTitle(m.Name, m.Input)}}}), nil
	case "function_call":
		if m.Name != "request_user_input" && m.Name != "request_user_input_async" {
			return one("", &pb.Event{Kind: &pb.Event_ToolStarted{ToolStarted: &pb.ToolStarted{Title: toolTitle(m.Name, m.Arguments)}}}), nil
		}
		q, err := ParseQuestions(m.CallID, m.Name, []byte(m.Arguments))
		if err != nil {
			return nil, err
		}
		p.questions[m.CallID] = true
		return one("", &pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: q}}), nil
	case "function_call_output":
		answers, ok := parseAnswers(m.Output)
		if !ok {
			return nil, nil
		}
		return one("", &pb.Event{Kind: &pb.Event_QuestionAnswered{QuestionAnswered: &pb.QuestionAnswered{
			QuestionId: m.CallID, Answers: answers,
		}}}), nil
	}
	return nil, nil
}

// ParseQuestions converts request_user_input arguments into a QuestionAsked.
// The same JSON arrives as the PreToolUse hook's tool_input.
func ParseQuestions(callID, tool string, args []byte) (*pb.QuestionAsked, error) {
	var a struct {
		Questions []struct {
			ID       string          `json:"id"`
			Header   string          `json:"header"`
			Question string          `json:"question"`
			Title    string          `json:"title"`
			Options  json.RawMessage `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("%s arguments: %w", tool, err)
	}
	q := &pb.QuestionAsked{QuestionId: callID, Async: tool == "request_user_input_async"}
	for _, x := range a.Questions {
		text := x.Question
		if text == "" {
			text = x.Title
		}
		item := &pb.Question{Id: x.ID, Header: x.Header, Text: text}
		// Options are objects for request_user_input and plain strings for
		// request_user_input_async.
		var objs []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		}
		var strs []string
		if json.Unmarshal(x.Options, &objs) == nil {
			for _, o := range objs {
				item.Options = append(item.Options, &pb.QuestionOption{Label: o.Label, Description: o.Description})
			}
		} else if json.Unmarshal(x.Options, &strs) == nil {
			for _, s := range strs {
				item.Options = append(item.Options, &pb.QuestionOption{Label: s})
			}
		}
		q.Questions = append(q.Questions, item)
	}
	return q, nil
}

// parseAnswers recognizes request_user_input's output:
// {"answers":{"<id>":{"answers":["..."]}}}. The output field is a JSON
// string in the rollout.
func parseAnswers(raw json.RawMessage) ([]*pb.QuestionAnswer, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	var out struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil || out.Answers == nil {
		return nil, false
	}
	ids := make([]string, 0, len(out.Answers))
	for id := range out.Answers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var answers []*pb.QuestionAnswer
	for _, id := range ids {
		answers = append(answers, &pb.QuestionAnswer{QuestionId: id, Answers: out.Answers[id].Answers})
	}
	return answers, true
}

func one(turnID string, e *pb.Event) []*pb.Event {
	e.TurnId = turnID
	return []*pb.Event{e}
}

// commandTitle extracts the script from ["/bin/zsh", "-lc", "<script>"].
func commandTitle(raw json.RawMessage) string {
	var argv []string
	if json.Unmarshal(raw, &argv) == nil {
		if len(argv) == 3 && (argv[1] == "-lc" || argv[1] == "-c") {
			return argv[2]
		}
		return strings.Join(argv, " ")
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
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
	return string(b)
}

func truncate(s string) (string, bool) {
	if len(s) <= MaxOutput {
		return s, false
	}
	cut := MaxOutput
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// execCmd finds the shell command in Codex's exec tool call, which is a
// small program like `tools.exec_command({cmd:"go test ./..."})`.
var execCmd = regexp.MustCompile(`\bcmd"?\s*:\s*("(?:[^"\\]|\\.)*")`)

// toolTitle names a started tool call: its command for shell calls.
func toolTitle(name, input string) string {
	if m := execCmd.FindStringSubmatch(input); m != nil {
		var cmd string
		if json.Unmarshal([]byte(m[1]), &cmd) == nil && cmd != "" {
			return cmd
		}
	}
	return name
}
