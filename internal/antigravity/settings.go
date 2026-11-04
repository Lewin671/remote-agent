package antigravity

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func applySettings(ctx *pb.TurnContext, ch *pb.SettingsChanged) *pb.TurnContext {
	if ch == nil || ch.Model == "" && ch.Effort == "" && ch.Mode == pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		return ctx
	}
	next := &pb.TurnContext{}
	if ctx != nil {
		next = proto.Clone(ctx).(*pb.TurnContext)
	}
	if ch.Model != "" {
		next.Model = ch.Model
	}
	if ch.Effort != "" {
		next.Effort = ch.Effort
	}
	switch ch.Mode {
	case pb.ApprovalMode_APPROVAL_MODE_ASK:
		next.PermissionMode = "default"
	case pb.ApprovalMode_APPROVAL_MODE_AUTO:
		next.PermissionMode = "auto"
	case pb.ApprovalMode_APPROVAL_MODE_PLAN:
		next.PermissionMode = "plan"
	case pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS:
		next.ApprovalPolicy = "always-proceed"
		next.PermissionMode = "default"
	}
	return next
}
