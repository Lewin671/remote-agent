package corebind

import (
	"errors"

	"github.com/Lewin671/remote-agent/clientcore/app"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
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

// SecCall executes a stateless security primitive call on a serialized
// corev1.SecCallRequest and returns a serialized corev1.SecCallResponse.
func SecCall(reqBytes []byte) ([]byte, error) {
	var req corev1.SecCallRequest
	if len(reqBytes) > 0 {
		if err := req.Unmarshal(reqBytes); err != nil {
			return nil, err
		}
	}
	return security.SecCall(&req).Marshal()
}
