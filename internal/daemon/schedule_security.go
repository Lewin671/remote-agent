package daemon

import (
	"errors"
	"path/filepath"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func mcpScheduleDenied() error {
	return connect.NewError(connect.CodePermissionDenied, errors.New("MCP scheduling is limited to this session's own tasks, project, CLI and exact standard permissions; use the user CLI or remote client for other targets"))
}

// restrictMCPTask bounds the capability delegated through an MCP tool. The
// session ID is context, not OS authentication: code already running as the
// same local user can independently access the daemon's private socket.
// Check again before delayed dispatch so an old full-access task cannot
// reopen a full-access CLI after its source permissions were narrowed, and
// a task made to continue an asking session does not run in it once it has
// full access. freeze, when the model creates or redefines the task, binds
// it to the source as it is now.
func (d *Daemon) restrictMCPTask(t *schedule.Task, freeze bool) error {
	if t.ScopedSession == "" || t.SourceSession != t.ScopedSession {
		return mcpScheduleDenied()
	}
	source, err := d.exact(t.ScopedSession)
	if err != nil {
		return mcpScheduleDenied()
	}
	if freeze {
		tr := t.Spec.Trigger
		if tr.Type == "interval" && tr.IntervalSeconds < 300 || tr.Type == "after_turn" && tr.Repeat && tr.DelaySeconds < 60 {
			return connect.NewError(connect.CodePermissionDenied, errors.New("MCP recurring tasks require interval_seconds >= 300 or repeat delay_seconds >= 60"))
		}
	}
	info := source.info()
	mode := exactScheduleMode(info)
	if mode == "" {
		return mcpScheduleDenied()
	}
	if freeze {
		t.ScopedMode = mode
	}
	target := &t.Spec.Target
	switch target.Type {
	case "resume_session":
		if freeze && (target.Session == "current" || target.Session == "") {
			target.Session = info.Id
		}
		// A new session's mode is in its target; a resumed one runs under
		// whatever the source has by then, so the mode the delegation was
		// made under is what is compared. A task without one is not the
		// model's to run.
		if target.Session != info.Id || t.ScopedMode != mode {
			return mcpScheduleDenied()
		}
	case "new_session":
		agent := "codex"
		if info.Agent == pb.Agent_AGENT_CLAUDE {
			agent = "claude"
		}
		if freeze {
			if target.Mode == "" {
				target.Mode = mode
			}
			if target.Agent == "" {
				target.Agent = agent
			}
			if target.Cwd == "" {
				target.Cwd = info.Cwd
			}
		}
		if target.Mode != mode || target.Agent != agent || filepath.Clean(target.Cwd) != filepath.Clean(info.Cwd) {
			return mcpScheduleDenied()
		}
	default:
		return mcpScheduleDenied()
	}
	return nil
}

// exactScheduleMode is the standard mode a session's complete settings
// are, or "". Client-facing ASK/AUTO labels may round custom settings (e.g.
// read-only Codex sandboxes). Never use those labels to authorize a new
// CLI's broader standard mode. Plan/unknown/custom settings fail closed.
func exactScheduleMode(info *pb.Session) string {
	if info.Mode == pb.ApprovalMode_APPROVAL_MODE_PLAN || info.CollaborationMode == "plan" {
		return ""
	}
	if info.Agent == pb.Agent_AGENT_CLAUDE {
		switch info.PermissionMode {
		case "default":
			return "ask"
		case "auto":
			return "auto"
		case "bypassPermissions":
			return "full"
		}
		return ""
	}
	switch {
	case info.ApprovalsReviewer == "user" && info.ApprovalPolicy == "on-request" && info.Sandbox == "workspace-write":
		return "ask"
	case info.ApprovalsReviewer == "auto_review" && info.ApprovalPolicy == "on-request" && info.Sandbox == "workspace-write":
		return "auto"
	case info.ApprovalsReviewer == "user" && info.ApprovalPolicy == "never" && info.Sandbox == "danger-full-access":
		return "full"
	}
	return ""
}
