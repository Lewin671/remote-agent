package codex

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// config reads config.toml: its top-level keys, and over them those of
// [profiles.<profile>] for the profile the top level selects. A missing
// file is an empty one; ok is false when the file cannot be read or is not
// TOML, which Codex does not start with either.
func config(path string) (map[string]any, bool) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, true
	}
	if err != nil {
		return nil, false
	}
	top := map[string]any{}
	if err := toml.Unmarshal(b, &top); err != nil {
		return nil, false
	}
	if name, _ := top["profile"].(string); name != "" {
		profiles, _ := top["profiles"].(map[string]any)
		profile, _ := profiles[name].(map[string]any)
		for k, v := range profile {
			top[k] = v
		}
	}
	return top, true
}

// DefaultMode is the mode config.toml gives new sessions. Unset keys take
// Codex's defaults: on-request approvals by the user in a workspace-write
// sandbox, which is ASK. A key set to something other than a string is not
// understood, and the mode not known.
func (a *Adapter) DefaultMode(string) (pb.ApprovalMode, bool, bool) {
	cfg, ok := config(filepath.Join(a.home, "config.toml"))
	if !ok {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, false, false
	}
	c := &pb.TurnContext{ApprovalPolicy: "on-request", Sandbox: "workspace-write", ApprovalsReviewer: "user"}
	for key, field := range map[string]*string{"approval_policy": &c.ApprovalPolicy, "sandbox_mode": &c.Sandbox, "approvals_reviewer": &c.ApprovalsReviewer} {
		v, set := cfg[key]
		if !set {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, false, false
		}
		if s != "" {
			*field = s
		}
	}
	return contextMode(c), true, false
}
