package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// meta is the persisted description of a session, session.json.
type meta struct {
	ID string `json:"id"`
	// Agent is the CLI; sessions from before Claude Code support have none
	// and run Codex.
	Agent     pb.Agent  `json:"agent,omitempty"`
	Cwd       string    `json:"cwd"`
	Tmux      string    `json:"tmux"`
	Args      []string  `json:"args,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// CodexSessionID is the CLI's own session ID, and RolloutPath its
	// transcript; the names date from when only Codex was supported.
	CodexSessionID string `json:"codex_session_id,omitempty"`
	RolloutPath    string `json:"rollout_path,omitempty"`
	Exited         bool   `json:"exited,omitempty"`
	// NoFullAccess: the CLI would not switch this session to full access,
	// as Claude Code started without bypass allowed.
	NoFullAccess bool `json:"no_full_access,omitempty"`
	// StandardContext: a Claude Code session held to the standard context
	// window; its environment says so to the CLI, also when resumed.
	StandardContext bool `json:"standard_context,omitempty"`
	// Starting: the session is being created and its CLI may not have
	// started yet; a restart that finds no tmux session undoes it.
	Starting bool `json:"starting,omitempty"`
}

func loadMeta(dir string) (*meta, error) {
	b, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return nil, err
	}
	m := &meta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

// save writes session.json atomically.
func (m *meta) save(dir string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileSync(filepath.Join(dir, "session.json"), append(b, '\n'))
}
