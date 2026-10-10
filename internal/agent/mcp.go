package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// MCPConfig writes an additional process-scoped Claude Code MCP configuration.
// Explicit --mcp-config sources are merged here so adding ours cannot replace
// the user's sources. Global/project MCP configuration remains CLI-owned.
func MCPConfig(l *Launch, argv []string) ([]string, error) {
	if l.Exe == "" || l.RemoteSession == "" {
		return argv, nil
	}
	servers := map[string]json.RawMessage{}
	var kept []string
	for i := 0; i < len(argv); i++ {
		flag, value, inline := strings.Cut(argv[i], "=")
		if flag != "--mcp-config" {
			kept = append(kept, argv[i])
			continue
		}
		var sources []string
		if inline {
			sources = append(sources, value)
		} else {
			for i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				i++
				sources = append(sources, argv[i])
			}
		}
		if len(sources) == 0 {
			return nil, errors.New("--mcp-config needs a source")
		}
		for _, source := range sources {
			b := []byte(source)
			if !strings.HasPrefix(strings.TrimSpace(source), "{") {
				// The user gave it relative to the folder the CLI runs
				// in, not to the daemon's.
				if !filepath.IsAbs(source) {
					source = filepath.Join(l.Cwd, source)
				}
				var err error
				b, err = os.ReadFile(source)
				if err != nil {
					return nil, errors.New("cannot read explicit MCP config")
				}
			}
			var cfg struct {
				Servers map[string]json.RawMessage `json:"mcpServers"`
			}
			if json.Unmarshal(b, &cfg) != nil {
				return nil, errors.New("invalid explicit MCP config")
			}
			for name, server := range cfg.Servers {
				servers[name] = server
			}
		}
	}
	server, _ := json.Marshal(map[string]any{"command": l.Exe, "args": []string{"mcp", "--session", l.RemoteSession}})
	servers["remote_agent"] = server
	b, _ := json.Marshal(map[string]any{"mcpServers": servers})
	path := filepath.Join(l.Dir, "mcp.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return append(kept, "--mcp-config", path), nil
}
