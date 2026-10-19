package relay

import (
	"fmt"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
)

// currentCheckpoint writes the owning machine's durable current session state
// before publication. Every content epoch republishes this same bootstrap path;
// no readable event prefix or recovery-specific side effect is required.
func (c *conn) currentCheckpoint(id string, info *pb.Session) ([]byte, error) {
	var snapshot *pb.LiveSnapshot
	if owner, ok := c.r.backend.(interface {
		LiveSnapshot(string) (*pb.LiveSnapshot, error)
	}); ok {
		var err error
		snapshot, err = owner.LiveSnapshot(id)
		if err != nil {
			return nil, err
		}
	} else {
		snapshot = &pb.LiveSnapshot{Session: proto.CloneOf(info)}
	}
	if snapshot.Session == nil || snapshot.Session.Id != id {
		return nil, fmt.Errorf("invalid current checkpoint")
	}
	snapshot.Session = proto.CloneOf(snapshot.Session)
	snapshot.Session.CurrentCheckpoint = nil
	// The outer session metadata carries these proofs once.
	snapshot.Session.MachineReplacements = nil
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	// SESSION_META is bounded. A missing/oversized checkpoint must stay pending,
	// never claim a truncated snapshot is complete.
	if len(raw) > 48<<10 {
		return nil, fmt.Errorf("current checkpoint exceeds session metadata capacity")
	}
	return raw, nil
}

func (c *conn) persistCheckpoint(id string, request *pb.PutSessionRequest) error {
	dir := c.r.backend.SessionDir(id)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		return err
	}
	return writeFileSync(filepath.Join(dir, "current-checkpoint.pb"), raw)
}
