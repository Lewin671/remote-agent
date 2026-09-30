package daemon

import (
	"os"
	"strings"

	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/tail"
)

// history follows the file where the CLI records submitted input: Codex's
// history.jsonl, Claude Code's transcript. It is how remote-agent confirms
// that injected input was actually submitted.
type history struct {
	r  *tail.Reader
	ag agent.Adapter
}

// newHistory starts reading the session's submit log at offset; a negative
// offset means the current end of the file, found without reading it.
func (s *session) newHistory(offset int64) *history {
	s.mu.Lock()
	path := s.ag.SubmitLog(s.meta.RolloutPath)
	s.mu.Unlock()
	if offset < 0 {
		offset = 0
		if st, err := os.Stat(path); err == nil {
			offset = st.Size()
		}
	}
	h := &history{ag: s.ag}
	if path != "" {
		h.r = tail.NewReader(path, offset)
	}
	return h
}

// submitted reports whether a line recording text was appended since the
// last call.
func (h *history) submitted(match func(recorded string) bool, cliSessionID string) bool {
	if h.r == nil {
		return false
	}
	lines, _, err := h.r.Next()
	if err != nil {
		return false
	}
	for _, l := range lines {
		if h.ag.Submitted(l.Data, match, cliSessionID) {
			return true
		}
	}
	return false
}

// offset is where reading stands.
func (h *history) offset() int64 {
	if h.r == nil {
		return 0
	}
	return h.r.Offset()
}

func normalizeText(s string) string { return strings.Join(strings.Fields(s), " ") }
