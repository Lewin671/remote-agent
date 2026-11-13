package daemon

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/i18n"
	"github.com/Lewin671/remote-agent/internal/machinestatus"
	"github.com/Lewin671/remote-agent/internal/protocol"
)

func (d *Daemon) machineRoots() map[int]string {
	out := map[int]string{}
	if d.cfg.Tmux == nil {
		return out
	}
	infos, e := d.cfg.Tmux.ListSessions()
	if e != nil {
		return out
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, i := range infos {
		if s := d.sessions[i.SessionID]; s != nil {
			s.mu.Lock()
			live := !s.meta.Exited && s.meta.Tmux == i.Name
			s.mu.Unlock()
			if live && i.PanePID > 0 {
				out[i.PanePID] = i.SessionID
			}
		}
	}
	return out
}
func (h *handler) GetMachineStatus(ctx context.Context, _ *connect.Request[pb.GetMachineStatusRequest]) (*connect.Response[pb.MachineStatus], error) {
	if h.d.statusCollector == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New(i18n.MachineStatusUnavailable("zh-CN")))
	}
	s, e := h.d.statusCollector.Get(ctx)
	if e != nil {
		if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
			return nil, rpcErr(e)
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New(i18n.MachineStatusUnavailable("zh-CN")))
	}
	if guard := h.d.diskGuard; guard != nil {
		if disk := guard.Snapshot(); disk != nil && s.Disk != nil {
			s.Disk.EmergencyReserveBytes = disk.EmergencyReserveBytes
			s.Disk.DeferredWritesPaused = disk.DeferredWritesPaused
		}
	}
	return connect.NewResponse(s), nil
}
func (d *Daemon) diskLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if disk, e := machinestatus.Volume(d.dir); e == nil {
			if e = d.diskGuard.Check(disk); e != nil {
				slog.Error("emergency reserve", "err", e)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
func (d *Daemon) reserveSize() uint64 {
	if d.diskGuard == nil {
		return 0
	}
	return d.diskGuard.ReserveSize()
}
func (d *Daemon) diskError(err error) error {
	if !machinestatus.NoSpace(err) {
		return nil
	}
	// The raw error stays local. Never send paths from a filesystem error.
	slog.Error("machine write failed", "err", err)
	if d.diskGuard != nil {
		disk, _ := machinestatus.Volume(d.dir)
		if e := d.diskGuard.WriteFailedAt(err, disk); e != nil {
			slog.Error("release emergency reserve", "err", e)
		}
	}
	message := i18n.MachineStatusDiskFull("zh-CN")
	if queueAccepted(err) { // Preserve retry identity after durable queue admission.
		e := connect.NewError(connect.CodeAborted, errors.New(message))
		if detail, err := connect.NewErrorDetail(&pb.MachineError{Code: protocol.DiskFull}); err == nil {
			e.AddDetail(detail)
		}
		return e
	}
	return protocol.NewMachineError(protocol.DiskFull, message)
}
func (d *Daemon) rpcErr(err error) error {
	if e := d.diskError(err); e != nil {
		return e
	}
	return rpcErr(err)
}
func (d *Daemon) uploadErr(err error) error {
	if e := d.diskError(err); e != nil {
		return e
	}
	return uploadErr(err)
}
func (d *Daemon) MachineDiskAlerts() []*pb.DiskStatus {
	if d.diskGuard == nil {
		return nil
	}
	return d.diskGuard.Alerts()
}
func (d *Daemon) AckMachineDiskAlert(at int64, p pb.ResourcePressure) {
	if d.diskGuard != nil {
		d.diskGuard.Ack(at, p)
	}
}
