package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPConfigPreservesSourcesWithoutGlobalChanges(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.json")
	original := []byte(`{"mcpServers":{"other":{"command":"custom","args":["secret"]}}}`)
	if err := os.WriteFile(user, original, 0o600); err != nil {
		t.Fatal(err)
	}
	l := &Launch{Dir: dir, Exe: "/path with spaces/remote-agent", RemoteSession: "ours"}
	args, err := MCPConfig(l, []string{"--model", "haiku", "--mcp-config=" + user, "prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args[:3], " ") != "--model haiku prompt" {
		t.Fatalf("lost args %v", args)
	}
	var cfg struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	b, _ := os.ReadFile(args[len(args)-1])
	if json.Unmarshal(b, &cfg) != nil || cfg.Servers["other"] == nil || cfg.Servers["remote_agent"] == nil {
		t.Fatalf("lost MCP %s", b)
	}
	after, _ := os.ReadFile(user)
	if string(after) != string(original) {
		t.Fatal("changed original configuration")
	}
	st, _ := os.Stat(args[len(args)-1])
	if st.Mode().Perm() != 0o600 {
		t.Fatal("MCP config not private")
	}
	plain, err := MCPConfig(&Launch{Dir: dir}, []string{"--model", "haiku"})
	if err != nil || len(plain) != 2 {
		t.Fatal("affected unmanaged launch")
	}
}

// A relative --mcp-config is the user's, relative to the folder they ran
// the CLI in. It is read by the daemon, whose own working directory is
// somewhere else and may hold a file of the same name.
func TestMCPConfigReadsRelativeSourcesFromTheSessionFolder(t *testing.T) {
	project, elsewhere := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(project, "tools.json"), []byte(`{"mcpServers":{"project":{"command":"p"}}}`), 0o600)
	os.WriteFile(filepath.Join(elsewhere, "tools.json"), []byte(`{"mcpServers":{"other":{"command":"o"}}}`), 0o600)
	t.Chdir(elsewhere)
	l := &Launch{Dir: t.TempDir(), Cwd: project, Exe: "remote-agent", RemoteSession: "ours"}
	for _, argv := range [][]string{{"--mcp-config", "./tools.json"}, {"--mcp-config=tools.json"}} {
		args, err := MCPConfig(l, argv)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		b, _ := os.ReadFile(args[len(args)-1])
		if json.Unmarshal(b, &cfg) != nil || cfg.Servers["project"] == nil || cfg.Servers["other"] != nil {
			t.Fatalf("%v read as %s", argv, b)
		}
	}
}
