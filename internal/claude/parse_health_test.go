package claude

import (
	"errors"
	"github.com/Lewin671/remote-agent/internal/agent"
	"testing"
)

func TestRecordClassification(t *testing.T) {
	for _, raw := range []string{`{"type":"queue-operation"}`, `{"type":"api-request"}`, `{"type":"system","subtype":"stop_hook_summary"}`} {
		evs, err := NewParser().Parse([]byte(raw))
		if err != nil || len(evs) != 0 {
			t.Fatalf("metadata: %s %v", raw, err)
		}
	}
	for _, raw := range []string{`{"type":"future_type","message":"secret"}`, `{"type":"system","subtype":"future_subtype"}`} {
		_, err := NewParser().Parse([]byte(raw))
		if !errors.Is(err, agent.ErrUnknownRecord) {
			t.Fatalf("unknown: %v", err)
		}
	}
}
