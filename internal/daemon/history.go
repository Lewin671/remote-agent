package daemon

import (
	"encoding/json"
	"strings"

	"github.com/Lewin671/remote-agent/internal/tail"
)

// history follows ~/.codex/history.jsonl, where Codex appends one line per
// submitted message. It is how remote-agent confirms that injected input
// was actually submitted.
type history struct {
	r *tail.Reader
}

type historyEntry struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

func newHistory(path string) *history {
	h := &history{r: tail.NewReader(path, 0)}
	h.skip()
	return h
}

// skip moves past everything written so far.
func (h *history) skip() { _, _, _ = h.r.Next() }

// submitted reports whether a line with text was appended since the last
// call.
func (h *history) submitted(text string) bool {
	lines, _, err := h.r.Next()
	if err != nil {
		return false
	}
	want := normalizeText(text)
	for _, l := range lines {
		var e historyEntry
		if json.Unmarshal(l.Data, &e) == nil && normalizeText(e.Text) == want {
			return true
		}
	}
	return false
}

func normalizeText(s string) string { return strings.Join(strings.Fields(s), " ") }
