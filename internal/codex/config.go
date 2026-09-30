package codex

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// configStrings reads the string values of config.toml that decide the
// approval mode: top-level keys, and those of [profiles.<profile>] for the
// profile the top level selects. It understands only `key = "value"`
// lines, which is all these keys take; ok is false when the file cannot
// be read.
func configStrings(path string) (map[string]string, bool) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, true
	}
	if err != nil {
		return nil, false
	}
	top := map[string]string{}
	tables := map[string]map[string]string{}
	table := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			table = strings.Trim(line, "[] ")
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 && !strings.HasPrefix(v, `"`) {
			v = strings.TrimSpace(v[:i])
		}
		if !strings.HasPrefix(v, `"`) {
			continue
		}
		s, err := strconv.Unquote(v[:strings.LastIndex(v, `"`)+1])
		if err != nil {
			continue
		}
		if table == "" {
			top[k] = s
		} else {
			if tables[table] == nil {
				tables[table] = map[string]string{}
			}
			tables[table][k] = s
		}
	}
	if p := top["profile"]; p != "" {
		for _, name := range []string{"profiles." + p, `profiles."` + p + `"`} {
			for k, v := range tables[name] {
				top[k] = v
			}
		}
	}
	return top, true
}

// DefaultMode is the mode config.toml gives new sessions. Unset keys take
// Codex's defaults: on-request approvals by the user in a workspace-write
// sandbox, which is ASK.
func (a *Adapter) DefaultMode(string) (pb.ApprovalMode, bool, bool) {
	cfg, ok := configStrings(filepath.Join(a.home, "config.toml"))
	if !ok {
		return pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, false, false
	}
	c := &pb.TurnContext{ApprovalPolicy: "on-request", Sandbox: "workspace-write", ApprovalsReviewer: "user"}
	if v := cfg["approval_policy"]; v != "" {
		c.ApprovalPolicy = v
	}
	if v := cfg["sandbox_mode"]; v != "" {
		c.Sandbox = v
	}
	if v := cfg["approvals_reviewer"]; v != "" {
		c.ApprovalsReviewer = v
	}
	return contextMode(c), true, false
}
