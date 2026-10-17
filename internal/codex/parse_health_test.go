package codex

import (
	"errors"
	"github.com/Lewin671/remote-agent/internal/agent"
	"testing"
)

func TestRecordClassification(t *testing.T) {
	for _, raw := range []string{`{"type":"session_meta"}`, `{"type":"event_msg","payload":{"type":"thread_settings_applied"}}`, `{"type":"response_item","payload":{"type":"reasoning"}}`} {
		evs, err := NewParser().Parse([]byte(raw))
		if err != nil || len(evs) != 0 {
			t.Fatalf("metadata: %s %v", raw, err)
		}
	}
	for _, raw := range []string{`{"type":"future_type"}`, `{"type":"event_msg","payload":{"type":"future_type"}}`, `{"type":"response_item","payload":{"type":"future_type"}}`} {
		_, err := NewParser().Parse([]byte(raw))
		if !errors.Is(err, agent.ErrUnknownRecord) {
			t.Fatalf("unknown: %v", err)
		}
	}
}
