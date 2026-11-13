package relay

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

func (c *conn) syncDiskAlerts(ctx context.Context) (retErr error) {
	now := time.Now()
	if c.diskAlertNow != nil {
		now = c.diskAlertNow()
	}
	if now.Before(c.nextDiskAlert) {
		return nil
	}
	defer func() {
		if retErr != nil {
			c.nextDiskAlert = now.Add(time.Minute)
		}
	}()
	b, ok := c.r.backend.(interface {
		MachineDiskAlerts() []*pb.DiskStatus
		AckMachineDiskAlert(int64, pb.ResourcePressure)
	})
	if !ok {
		return nil
	}
	for _, s := range b.MachineDiskAlerts() {
		meta := c.r.backend.MachineMeta()
		id := fmt.Sprintf("disk:%d:%d", s.ObservedMs, s.Pressure)
		content := &pb.NotifyContent{MachineId: c.acct.DeviceID, Kind: pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK, MachineName: meta.Hostname, DiskStatus: s}
		env, e := c.security.sealMessage(&pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: c.acct.DeviceID, RequestId: id, Sender: c.acct.DeviceID}, deviceauth.Notify, nil, content)
		if e != nil {
			return e
		}
		if _, e = c.machine.Notify(ctx, connect.NewRequest(&pb.NotifyRequest{Kind: content.Kind, Content: env, DedupeKey: c.acct.DeviceID + ":" + id})); e != nil {
			return e
		}
		b.AckMachineDiskAlert(s.ObservedMs, s.Pressure)
	}
	return nil
}
