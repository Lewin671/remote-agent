package corebind

import (
	"errors"

	"github.com/Lewin671/remote-agent/clientcore/app"
	"github.com/Lewin671/remote-agent/clientcore/domain/machine"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
)

var errNilCore = errors.New("corebind: nil Core handle")

// Core holds an initialized application core behind a gomobile-safe handle.
type Core struct {
	inner *app.Core
}

// New creates a new Core instance.
func New() *Core {
	return &Core{inner: app.New()}
}

// Dispatch processes a serialized Event protobuf and returns a serialized EffectBatch protobuf.
func (c *Core) Dispatch(eventBytes []byte) ([]byte, error) {
	if c == nil || c.inner == nil {
		return nil, errNilCore
	}
	var ev corev1.Event
	if len(eventBytes) > 0 {
		if err := ev.Unmarshal(eventBytes); err != nil {
			return nil, err
		}
	}
	effects := c.inner.Dispatch(&ev)
	return effects.Marshal()
}

// View computes a serialized ViewModel protobuf for a serialized Screen protobuf.
func (c *Core) View(screenBytes []byte) ([]byte, error) {
	if c == nil || c.inner == nil {
		return nil, errNilCore
	}
	var sc corev1.Screen
	if len(screenBytes) > 0 {
		if err := sc.Unmarshal(screenBytes); err != nil {
			return nil, err
		}
	}
	vm, err := c.inner.View(&sc)
	if err != nil {
		return nil, err
	}
	return vm.Marshal()
}

// Close releases internal state associated with Core.
func (c *Core) Close() {
	if c == nil || c.inner == nil {
		return
	}
	c.inner.Close()
}

// BuildDirectViews computes ConnectionGroupView and ConnCheckDirectRowView from a DirectViewsRequest protobuf.
func BuildDirectViews(reqBytes []byte) ([]byte, error) {
	var req corev1.DirectViewsRequest
	if len(reqBytes) > 0 {
		if err := req.Unmarshal(reqBytes); err != nil {
			return nil, err
		}
	}
	resp := &corev1.DirectViewsResponse{
		Connection:      machine.BuildConnectionGroup(req.Online, req.PrefEnabled, req.Report),
		ConnCheckDirect: machine.BuildConnCheckDirectRow(req.Online, req.PrefEnabled, req.Report),
	}
	return resp.Marshal()
}
