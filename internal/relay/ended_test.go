package relay

import (
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The time the server pages ended sessions by is the one clients order
// them by: the recorded end, else the creation, and never 0 once ended.
func TestEndedMs(t *testing.T) {
	created := timestamppb.New(time.UnixMilli(1000))
	ended := timestamppb.New(time.UnixMilli(5000))
	exited := pb.SessionStatus_SESSION_STATUS_EXITED
	for name, c := range map[string]struct {
		info *pb.Session
		want int64
	}{
		"running":         {&pb.Session{Status: pb.SessionStatus_SESSION_STATUS_RUNNING, CreatedAt: created, EndedAt: ended}, 0},
		"ended":           {&pb.Session{Status: exited, CreatedAt: created, EndedAt: ended}, 5000},
		"no recorded end": {&pb.Session{Status: exited, CreatedAt: created}, 1000},
		"no times at all": {&pb.Session{Status: exited}, 1},
	} {
		if got := EndedMs(c.info); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}
