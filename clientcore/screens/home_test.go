package screens

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestBuildHomeStructuredAttentionAndHighlight(t *testing.T) {
	st := NewState()
	st.Cal = lists.Calendar{NowMs: 1_700_000_000_000}
	st.World.Connection = lists.ConnConnected
	st.World.LiveLoaded = true
	st.World.Machines = []*lists.Machine{
		{
			ID:     "m1",
			Online: true,
			Meta: &v1.MachineMeta{
				Hostname: "dev-box",
				Capabilities: &v1.MachineCapabilities{
					ApprovalAlways: true,
				},
			},
		},
	}
	st.World.MachinesListed = true
	st.World.SessionsListed = true
	st.World.Sessions = []*lists.Session{
		{
			ID:        "s-cmd",
			MachineID: "m1",
			UpdatedMs: 1_700_000_000_000,
			Info: &v1.Session{
				Id:     "s-cmd",
				Cwd:    "/work/app",
				Title:  "Fix tests",
				Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
			},
			Pending: lists.PendingAction{
				HasApproval:     true,
				ApprovalID:      "ap-1",
				ApprovalCommand: "go test ./...",
			},
		},
		{
			ID:        "s-tool",
			MachineID: "m1",
			UpdatedMs: 1_699_999_990_000,
			Info: &v1.Session{
				Id:     "s-tool",
				Cwd:    "/work/app",
				Title:  "Edit config",
				Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
			},
			Pending: lists.PendingAction{
				HasApproval:      true,
				ApprovalID:       "ap-2",
				ApprovalToolName: "Write",
			},
		},
	}

	vm := BuildHome(st, 0)
	if vm.Headline == nil || vm.Headline.HighlightArg != "count" || vm.Headline.HighlightCount != 2 {
		t.Fatalf("expected headline highlightArg=count highlightCount=2, got %+v", vm.Headline)
	}
	if vm.TopAttentionCard == nil {
		t.Fatalf("expected TopAttentionCard")
	}
	if vm.TopAttentionCard.AlwaysKind != corev1.AlwaysAllowKind_ALWAYS_ALLOW_KIND_COMMAND_PREFIX {
		t.Fatalf("expected COMMAND_PREFIX AlwaysKind, got %v", vm.TopAttentionCard.AlwaysKind)
	}
	if vm.TopAttentionCard.AlwaysLabel == nil || vm.TopAttentionCard.AlwaysLabel.Key != "approval.always_allow" {
		t.Fatalf("expected approval.always_allow label, got %+v", vm.TopAttentionCard.AlwaysLabel)
	}
	if len(vm.Attention) != 2 || vm.Attention[0].Detail == nil || vm.Attention[0].Detail.GetLiteral() != "go test ./..." {
		t.Fatalf("expected structured Detail literal=\"go test ./...\", got %+v", vm.Attention)
	}

	// Tool approval without command populates TOOL_CATEGORY + AlwaysArg.
	st.World.Sessions = st.World.Sessions[1:]
	vmTool := BuildHome(st, 0)
	if vmTool.TopAttentionCard == nil ||
		vmTool.TopAttentionCard.AlwaysKind != corev1.AlwaysAllowKind_ALWAYS_ALLOW_KIND_TOOL_CATEGORY ||
		vmTool.TopAttentionCard.AlwaysArg != "Write" {
		t.Fatalf("expected TOOL_CATEGORY AlwaysKind with AlwaysArg=Write, got %+v", vmTool.TopAttentionCard)
	}
}
