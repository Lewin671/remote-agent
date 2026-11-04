package antigravity

import "github.com/Lewin671/remote-agent/internal/agent"

var (
	_ agent.Adapter              = (*Adapter)(nil)
	_ agent.StatusLineSource     = (*Adapter)(nil)
	_ agent.NativeApprover       = (*Adapter)(nil)
	_ agent.NativeApprovalSource = (*Adapter)(nil)
	_ agent.ConversationEvents   = (*Adapter)(nil)
	_ agent.PlanInspector        = (*Adapter)(nil)
	_ agent.PlanSource           = (*Parser)(nil)
)
