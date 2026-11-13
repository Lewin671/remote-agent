package relay

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/i18n"
)

func (c *conn) syncDiskAlerts(ctx context.Context) (retErr error) {
	if time.Now().Before(c.nextDiskAlert) {
		return nil
	}
	defer func() {
		if retErr != nil {
			c.nextDiskAlert = time.Now().Add(time.Minute)
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
		size := i18n.MachineStatusSizeGb("zh-CN", fmt.Sprintf("%.1f", float64(s.AvailableBytes)/1e9))
		title, text := i18n.MachineStatusNotifyWarning("zh-CN", meta.Hostname, size), i18n.MachineStatusNotifyWarningBody("zh-CN")
		if s.Pressure == pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL {
			title, text = i18n.MachineStatusNotifyCritical("zh-CN", meta.Hostname, size), i18n.MachineStatusNotifyCriticalBody("zh-CN")
		}
		id := fmt.Sprintf("disk:%d:%d", s.ObservedMs, s.Pressure)
		content := &pb.NotifyContent{MachineId: c.acct.DeviceID, Kind: pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK, Title: title, Text: text, DiskStatus: s}
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
