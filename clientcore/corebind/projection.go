// Package corebind exposes coarse-grained protobuf-byte APIs for gomobile and WASM bindings.
package corebind

import (
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/projection"
)

// Projection holds an incremental event projection reducer behind a gomobile-safe handle.
type Projection struct {
	reducer *projection.Reducer
}

// NewProjection creates a new Projection configured for the given remoteagent.v1.Agent enum number.
func NewProjection(agent int32) *Projection {
	return &Projection{
		reducer: projection.NewReducer(v1.Agent(agent)),
	}
}

// Reset clears the projection state and switches to agent.
func (p *Projection) Reset(agent int32) {
	if p == nil {
		return
	}
	if p.reducer == nil {
		p.reducer = projection.NewReducer(v1.Agent(agent))
		return
	}
	p.reducer.Reset(v1.Agent(agent))
}

// Append decodes a serialized remoteagent.core.v1.EventBatch, appends its events, and returns
// the serialized remoteagent.core.v1.ProjectionState.
func (p *Projection) Append(eventBatchBytes []byte) ([]byte, error) {
	if p == nil || p.reducer == nil {
		return (&corev1.ProjectionState{}).Marshal()
	}
	if len(eventBatchBytes) > 0 {
		var batch corev1.EventBatch
		if err := batch.Unmarshal(eventBatchBytes); err != nil {
			return nil, err
		}
		p.reducer.Append(batch.Events)
	}
	return p.reducer.Snapshot().Marshal()
}

// Snapshot returns the current serialized remoteagent.core.v1.ProjectionState without appending events.
func (p *Projection) Snapshot() ([]byte, error) {
	if p == nil || p.reducer == nil {
		return (&corev1.ProjectionState{}).Marshal()
	}
	return p.reducer.Snapshot().Marshal()
}

// CurrentActions overlays a serialized remoteagent.v1.LiveSnapshot (or empty bytes for nil)
// onto the reducer's historical state and returns the serialized ProjectionState.
func (p *Projection) CurrentActions(liveSnapshotBytes []byte) ([]byte, error) {
	if p == nil || p.reducer == nil {
		return (&corev1.ProjectionState{}).Marshal()
	}
	var live *v1.LiveSnapshot
	if len(liveSnapshotBytes) > 0 {
		live = &v1.LiveSnapshot{}
		if err := live.Unmarshal(liveSnapshotBytes); err != nil {
			return nil, err
		}
	}
	return p.reducer.CurrentActions(live).Marshal()
}

// Reduce performs a stateless full replay of an EventBatch and returns the serialized ProjectionState.
func Reduce(agent int32, eventBatchBytes []byte) ([]byte, error) {
	var batch corev1.EventBatch
	if len(eventBatchBytes) > 0 {
		if err := batch.Unmarshal(eventBatchBytes); err != nil {
			return nil, err
		}
	}
	return projection.Derive(batch.Events, v1.Agent(agent)).Marshal()
}
