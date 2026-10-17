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

func TestMalformedTurnEndDoesNotConsumePendingView(t *testing.T) {
	for _, kind := range []string{"task_complete", "turn_aborted"} {
		t.Run(kind, func(t *testing.T) {
			p := NewParser()
			_, err := p.Parse([]byte(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"ImageView","id":"view","path":"/tmp/pending.png"}}}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.Parse([]byte(`{"type":"event_msg","payload":{"type":"` + kind + `","duration_ms":"bad"}}`)); err == nil {
				t.Fatal("invalid duration accepted")
			}
			evs, err := p.Parse([]byte(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t"}}`))
			if err != nil || len(evs) != 2 || evs[0].GetToolCall().GetCallId() != "view" || evs[1].GetTurnEnded() == nil {
				t.Fatalf("earlier valid pending view lost: %v %v", evs, err)
			}
		})
	}
}
