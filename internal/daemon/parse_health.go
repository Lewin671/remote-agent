package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

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
	// Until a new event is committed, the event log's durable transcript
	// checkpoint still belongs to the source before this reset.
	ResetPath string `json:"reset_path,omitempty"`
	ResetSeq  uint64 `json:"reset_seq,omitempty"`
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
	if err := s.saveParseHealthLocked(h); err != nil {
		return err
	}
	if !errors.Is(parseErr, agent.ErrUnknownRecord) && (s.parseWarnAt.IsZero() || time.Since(s.parseWarnAt) >= time.Minute) {
		slog.Warn("CLI parse failure", "session", s.meta.ID, "start", start, "end", end, "type", h.Type, "category", h.Category, "failures", h.Failures)
		s.parseWarnAt = time.Now()
	}
	return nil
}

// resetParseWatermark preserves cumulative counts and the latest diagnostic,
// but invalidates the source of its byte watermark. Persist even an empty
// replacement: a restart must not suppress a new short bad line using the old
// file's offsets. readMu is held; a failed save must be retried before reading.
func (s *session) resetParseWatermark(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parseHealthResetPending = true
	h := s.parseHealth
	h.Path = ""
	h.ResetPath, h.ResetSeq = path, s.log.CheckpointSeq()
	if err := s.saveParseHealthLocked(h); err != nil {
		return err
	}
	s.parseHealthResetPending = false
	return nil
}

// rolloutOffset ignores an obsolete event checkpoint until a post-reset
// event makes a checkpoint for the new source durable. Hold mu or be the sole
// owner during startup. Replaying diagnostics alone must not signal another
// reset and count those same records again after restart.
func (s *session) rolloutOffset(path string, offset int64) int64 {
	if s.parseHealth.ResetPath == path && s.parseHealth.ResetSeq == s.log.CheckpointSeq() {
		return 0
	}
	return offset
}

func (s *session) saveParseHealthLocked(h parseHealth) error {
	b, err := json.Marshal(h)
	if err == nil {
		err = writeFileSync(filepath.Join(s.dir, "parse-health.json"), append(b, '\n'))
	}
	if err != nil {
		s.noteStorageLocked(err)
		return fmt.Errorf("save parse diagnostic: %w", err)
	}
	s.parseHealth = h
	return nil
}

func (h parseHealth) detail() string {
	if h.Failures == 0 && h.Unknown == 0 {
		return ""
	}
	if h.Failures == 0 {
		return fmt.Sprintf("unknown records=%d; latest bytes=%d..%d type=%s", h.Unknown, h.Start, h.End, h.Type)
	}
	return fmt.Sprintf("parse failures=%d unknown=%d; latest bytes=%d..%d type=%s category=%s", h.Failures, h.Unknown, h.Start, h.End, h.Type, h.Category)
}
