package claude

import (
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"testing"
)

func TestRejectedModelAndModeHaveCompletionEvidence(t *testing.T) {
	a, _ := testAdapter(t)
	// nil terminal makes any accidental native access fail the test.
	err := a.SetModel(nil, "not-a-model", "high")
	if !errors.Is(err, agent.ErrNotFound) || !outcome.IsFinalError(err) {
		t.Fatal(err)
	}
	err = a.SetMode(nil, pb.ApprovalMode(99), false, agent.ModeFacts{})
	if err == nil || !outcome.IsFinalError(err) {
		t.Fatal(err)
	}
}
