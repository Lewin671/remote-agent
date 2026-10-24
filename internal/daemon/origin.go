package daemon

import (
	"context"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
)

// Origins say who caused an event (architecture §9). Commands relayed from
// a client carry the origin the relay established from the command's
// signature; calls on the local socket come from this computer.

var (
	originLocal  = &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_LOCAL}
	originSystem = &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_SYSTEM}
)

// originOf is the origin of a call.
func originOf(ctx context.Context) *pb.Origin {
	if o := agent.OriginFrom(ctx); o != nil {
		return o
	}
	return originLocal
}

// storedOrigin is an origin as kept in queue.json.
type storedOrigin struct {
	ScheduledTaskID string        `json:"scheduled_task_id,omitempty"`
	ScheduledRunID  string        `json:"scheduled_run_id,omitempty"`
	Kind            pb.OriginKind `json:"kind"`
	DeviceID        string        `json:"device_id,omitempty"`
	RequestID       string        `json:"request_id,omitempty"`
}

func storeOrigin(o *pb.Origin) *storedOrigin {
	if o == nil {
		return nil
	}
	return &storedOrigin{ScheduledTaskID: o.ScheduledTaskId, ScheduledRunID: o.ScheduledRunId, Kind: o.Kind, DeviceID: o.DeviceId, RequestID: o.RequestId}
}

func (o *storedOrigin) pb() *pb.Origin {
	if o == nil {
		return nil
	}
	return &pb.Origin{ScheduledTaskId: o.ScheduledTaskID, ScheduledRunId: o.ScheduledRunID, Kind: o.Kind, DeviceId: o.DeviceID, RequestId: o.RequestID}
}

// withOrigin sets e's origin to a copy of o and returns e.
func withOrigin(e *pb.Event, o *pb.Origin) *pb.Event {
	if o != nil {
		e.Origin = proto.CloneOf(o)
	}
	return e
}
