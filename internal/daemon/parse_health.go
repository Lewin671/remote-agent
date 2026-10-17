package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Lewin671/remote-agent/internal/agent"
)

// Parse diagnostics contain positions and schema labels, never record bodies
// or parser error messages (which may quote an input value). Save before
// skipping a complete bad line. A high-water mark avoids counting replay
// after a crash between this save and the next event-log checkpoint twice.
type parseHealth struct {
	Failures uint64 `json:"failures"`
	Unknown  uint64 `json:"unknown"`
	Path     string `json:"path"`
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	Type     string `json:"type"`
	Category string `json:"category"`
}

func loadParseHealth(dir string) (parseHealth, error) {
	var h parseHealth
	b, err := os.ReadFile(filepath.Join(dir, "parse-health.json"))
	if os.IsNotExist(err) {
		return h, nil
	}
	if err != nil {
		return h, err
	}
	err = json.Unmarshal(b, &h)
	return h, err
}

// schemaLabel admits only short schema identifiers. Arbitrary strings in a
// type field must not turn diagnostics into a second transcript/log channel.
func schemaLabel(s string) string {
	if s == "" {
		return "missing"
	}
	if len(s) > 64 {
		return "invalid"
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "invalid"
		}
	}
	return s
}

func recordType(raw []byte) string {
	var h struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Payload struct {
			Type string `json:"type"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return "invalid_json"
	}
	t := schemaLabel(h.Type)
	if h.Payload.Type != "" {
		t += "/" + schemaLabel(h.Payload.Type)
	} else if h.Subtype != "" {
		t += "/" + schemaLabel(h.Subtype)
	}
	return t
}

func parseErrorCategory(err error) string {
	var syntax *json.SyntaxError
	var value *json.UnmarshalTypeError
	switch {
	case errors.Is(err, agent.ErrUnknownRecord):
		return "unknown_record"
	case errors.As(err, &syntax):
		return "json_syntax"
	case errors.As(err, &value):
		return "json_type"
	default:
		return "invalid_record"
	}
}

// noteParseFailure serializes with event commits through mu; readMu is held.
func (s *session) noteParseFailure(path string, start, end int64, raw []byte, parseErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.parseHealth
	if h.Path == path && end <= h.End {
		return nil
	}
	h.Path, h.Start, h.End, h.Type, h.Category = path, start, end, recordType(raw), parseErrorCategory(parseErr)
	if errors.Is(parseErr, agent.ErrUnknownRecord) {
		h.Unknown++
	} else {
		h.Failures++
	}
	b, err := json.Marshal(h)
	if err == nil {
		err = writeFileSync(filepath.Join(s.dir, "parse-health.json"), append(b, '\n'))
	}
	if err != nil {
		s.noteStorageLocked(err)
		return fmt.Errorf("save parse diagnostic: %w", err)
	}
	s.parseHealth = h
	s.log.Poke()
	return nil
}

func (h parseHealth) detail() string {
	if h.Failures == 0 && h.Unknown == 0 {
		return ""
	}
	return fmt.Sprintf("CLI parse failures=%d unknown=%d; latest bytes=%d..%d type=%s category=%s", h.Failures, h.Unknown, h.Start, h.End, h.Type, h.Category)
}
