package machinestatus

import (
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
	"sync"
	"time"
)

type batteryCache struct {
	mu      sync.Mutex
	now     func() time.Time
	checked bool
	at      time.Time
	value   *pb.MachineBattery
}

func (c *batteryCache) get(ctx context.Context, read func(context.Context) *pb.MachineBattery) *pb.MachineBattery {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !c.checked || now.Sub(c.at) >= 30*time.Second {
		c.at = now
		c.checked = true
		c.value = read(ctx)
	}
	if c.value == nil {
		return nil
	}
	return proto.Clone(c.value).(*pb.MachineBattery)
}
