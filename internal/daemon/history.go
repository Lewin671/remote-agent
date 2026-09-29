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

// newHistory starts reading path at offset; a negative offset means the
// current end of the file.
func newHistory(path string, offset int64) *history {
	if offset < 0 {
		h := &history{r: tail.NewReader(path, 0)}
		h.skip()
		return h
	}
	return &history{r: tail.NewReader(path, offset)}
}

// skip moves past everything written so far and returns the new offset.
func (h *history) skip() int64 {
	_, _, _ = h.r.Next()
	return h.r.Offset()
}

// submitted reports whether a line with text was appended since the last
// call. When the Codex session ID is known, only that session's lines
// count, so identical text sent to another session is not mistaken for
// this one's.
func (h *history) submitted(text, codexSessionID string) bool {
	lines, _, err := h.r.Next()
	if err != nil {
		return false
	}
	want := normalizeText(text)
	for _, l := range lines {
		var e historyEntry
		if json.Unmarshal(l.Data, &e) != nil {
			continue
		}
		if codexSessionID != "" && e.SessionID != codexSessionID {
			continue
		}
		if normalizeText(e.Text) == want {
			return true
		}
	}
	return false
}

func normalizeText(s string) string { return strings.Join(strings.Fields(s), " ") }
