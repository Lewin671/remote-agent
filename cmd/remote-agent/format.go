package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// formatEvent renders an event as one or more human-readable lines.
func formatEvent(e *pb.Event) string {
	ts := ""
	if e.Time != nil {
		ts = e.Time.AsTime().Local().Format("15:04:05")
	}
	head := fmt.Sprintf("#%-4d %s ", e.Seq, ts)
	var body string
	switch k := e.Kind.(type) {
	case *pb.Event_UserMessage:
		body = "user: " + k.UserMessage.Text
		if n := k.UserMessage.ImageCount; n > 0 {
			body += fmt.Sprintf(" [%d image(s)]", n)
		}
	case *pb.Event_AgentMessage:
		label := "codex"
		if k.AgentMessage.Phase == "commentary" {
			label = "codex (progress)"
		}
		body = label + ": " + k.AgentMessage.Text
	case *pb.Event_Reasoning:
		body = "reasoning: " + k.Reasoning.Text
	case *pb.Event_ToolCall:
		t := k.ToolCall
		kind := strings.ToLower(strings.TrimPrefix(t.Kind.String(), "TOOL_KIND_"))
		body = fmt.Sprintf("tool %s: %s", kind, t.Title)
		if t.Status != "" {
			body += " → " + t.Status
		}
		if t.ExitCode != nil {
			body += fmt.Sprintf(" (exit %d)", *t.ExitCode)
		}
		if out := strings.TrimSpace(t.Output); out != "" {
			body += "\n" + indent(clip(out, 12))
		}
	case *pb.Event_TurnStarted:
		body = "turn started"
	case *pb.Event_TurnEnded:
		t := k.TurnEnded
		body = "turn " + strings.ToLower(strings.TrimPrefix(t.Outcome.String(), "TURN_OUTCOME_"))
		if t.Reason != "" {
			body += " (" + t.Reason + ")"
		}
		if t.DurationMs > 0 {
			body += " after " + (time.Duration(t.DurationMs) * time.Millisecond).Round(100*time.Millisecond).String()
		}
	case *pb.Event_TokenUsage:
		t := k.TokenUsage
		body = fmt.Sprintf("tokens: %d total", t.TotalTokens)
		if t.ContextWindow > 0 {
			body += fmt.Sprintf(", context %d%% of %d", t.LastTotalTokens*100/t.ContextWindow, t.ContextWindow)
		}
	case *pb.Event_TurnContext:
		t := k.TurnContext
		body = fmt.Sprintf("context: model %s %s, approvals %s/%s, sandbox %s, mode %s",
			t.Model, t.Effort, t.ApprovalPolicy, t.ApprovalsReviewer, t.Sandbox, t.CollaborationMode)
	case *pb.Event_ApprovalRequested:
		a := k.ApprovalRequested
		body = fmt.Sprintf("approval %s requested: %s", a.ApprovalId, a.ToolName)
		if a.Command != "" {
			body += ": " + a.Command
		}
		if a.Description != "" {
			body += "\n" + indent(a.Description)
		}
		if a.Deadline != nil {
			body += "\n" + indent("answer by "+a.Deadline.AsTime().Local().Format("15:04:05")+
				" with `remote-agent approve|deny "+shortID(e.SessionId)+" "+a.ApprovalId+"`")
		}
	case *pb.Event_ApprovalResolved:
		a := k.ApprovalResolved
		body = fmt.Sprintf("approval %s: %s", a.ApprovalId,
			strings.ToLower(strings.TrimPrefix(a.Decision.String(), "APPROVAL_DECISION_")))
		if a.Message != "" {
			body += " (" + a.Message + ")"
		}
	case *pb.Event_QuestionAsked:
		q := k.QuestionAsked
		kind := "question"
		if q.Async {
			kind = "question (not waiting)"
		}
		body = fmt.Sprintf("%s %s:", kind, q.QuestionId)
		for _, item := range q.Questions {
			body += "\n" + indent(item.Text)
			for i, o := range item.Options {
				line := fmt.Sprintf("%d. %s", i+1, o.Label)
				if o.Description != "" {
					line += " — " + o.Description
				}
				body += "\n" + indent(indent(line))
			}
		}
		if !q.Async {
			body += "\n" + indent("answer with `remote-agent answer "+shortID(e.SessionId)+" "+q.QuestionId+" <option>`")
		}
	case *pb.Event_QuestionAnswered:
		q := k.QuestionAnswered
		var parts []string
		for _, a := range q.Answers {
			parts = append(parts, strings.Join(a.Answers, ", "))
		}
		body = fmt.Sprintf("question %s answered: %s", q.QuestionId, strings.Join(parts, "; "))
	case *pb.Event_SessionState:
		body = "session " + statusName(k.SessionState.Status)
		if d := k.SessionState.Detail; d != "" {
			body += " (" + d + ")"
		}
	default:
		body = "unknown event"
	}
	return head + body + "\n"
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func indent(s string) string {
	return "      " + strings.ReplaceAll(s, "\n", "\n      ")
}

func clip(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n… %d more lines", len(lines)-maxLines)
}
