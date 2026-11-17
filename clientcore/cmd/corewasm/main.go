//go:build js && wasm

package main

import (
	"sync"
	"syscall/js"

	"github.com/Lewin671/remote-agent/clientcore/corebind"
)

var (
	mu          sync.Mutex
	nextHandle  int64 = 1
	projections       = make(map[int64]*corebind.Projection)
	cores             = make(map[int64]*corebind.Core)
)

func bytesFromJS(v js.Value) []byte {
	if v.IsUndefined() || v.IsNull() {
		return nil
	}
	n := v.Get("byteLength").Int()
	if n == 0 {
		return nil
	}
	buf := make([]byte, n)
	js.CopyBytesToGo(buf, v)
	return buf
}

func bytesToJS(b []byte) js.Value {
	out := js.Global().Get("Uint8Array").New(len(b))
	if len(b) > 0 {
		js.CopyBytesToJS(out, b)
	}
	return out
}

func resultBytes(b []byte, err error) any {
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"data": bytesToJS(b)}
}

func lookup(handle int64) *corebind.Projection {
	mu.Lock()
	defer mu.Unlock()
	return projections[handle]
}

func lookupCore(handle int64) *corebind.Core {
	mu.Lock()
	defer mu.Unlock()
	return cores[handle]
}

func main() {
	api := js.Global().Get("Object").New()

	api.Set("new", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		mu.Lock()
		id := nextHandle
		nextHandle++
		cores[id] = corebind.New()
		mu.Unlock()
		return float64(id)
	}))

	api.Set("dispatch", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 {
			return map[string]any{"error": "missing handle"}
		}
		c := lookupCore(int64(args[0].Float()))
		if c == nil {
			return map[string]any{"error": "unknown handle"}
		}
		var raw []byte
		if len(args) > 1 {
			raw = bytesFromJS(args[1])
		}
		return resultBytes(c.Dispatch(raw))
	}))

	api.Set("view", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 {
			return map[string]any{"error": "missing handle"}
		}
		c := lookupCore(int64(args[0].Float()))
		if c == nil {
			return map[string]any{"error": "unknown handle"}
		}
		var raw []byte
		if len(args) > 1 {
			raw = bytesFromJS(args[1])
		}
		return resultBytes(c.View(raw))
	}))

	api.Set("close", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 {
			return nil
		}
		id := int64(args[0].Float())
		mu.Lock()
		c := cores[id]
		delete(cores, id)
		mu.Unlock()
		if c != nil {
			c.Close()
		}
		return nil
	}))

	api.Set("newProjection", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var agent int32
		if len(args) > 0 {
			agent = int32(args[0].Int())
		}
		mu.Lock()
		id := nextHandle
		nextHandle++
		projections[id] = corebind.NewProjection(agent)
		mu.Unlock()
		return float64(id)
	}))

	api.Set("projectionReset", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 2 {
			return nil
		}
		if p := lookup(int64(args[0].Float())); p != nil {
			p.Reset(int32(args[1].Int()))
		}
		return nil
	}))

	api.Set("projectionAppend", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 {
			return map[string]any{"error": "missing handle"}
		}
		p := lookup(int64(args[0].Float()))
		if p == nil {
			return map[string]any{"error": "unknown handle"}
		}
		var raw []byte
		if len(args) > 1 {
			raw = bytesFromJS(args[1])
		}
		return resultBytes(p.Append(raw))
	}))

	api.Set("projectionSnapshot", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 {
			return map[string]any{"error": "missing handle"}
		}
		p := lookup(int64(args[0].Float()))
		if p == nil {
			return map[string]any{"error": "unknown handle"}
		}
		return resultBytes(p.Snapshot())
	}))

	api.Set("projectionCurrentActions", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 {
			return map[string]any{"error": "missing handle"}
		}
		p := lookup(int64(args[0].Float()))
		if p == nil {
			return map[string]any{"error": "unknown handle"}
		}
		var raw []byte
		if len(args) > 1 {
			raw = bytesFromJS(args[1])
		}
		return resultBytes(p.CurrentActions(raw))
	}))

	api.Set("projectionFree", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 {
			return nil
		}
		id := int64(args[0].Float())
		mu.Lock()
		delete(projections, id)
		mu.Unlock()
		return nil
	}))

	api.Set("reduceProjection", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var agent int32
		var raw []byte
		if len(args) > 0 {
			agent = int32(args[0].Int())
		}
		if len(args) > 1 {
			raw = bytesFromJS(args[1])
		}
		return resultBytes(corebind.Reduce(agent, raw))
	}))

	api.Set("buildDirectViews", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var raw []byte
		if len(args) > 0 {
			raw = bytesFromJS(args[0])
		}
		return resultBytes(corebind.BuildDirectViews(raw))
	}))

	js.Global().Set("__remoteAgentCore", api)
	if cb := js.Global().Get("__remoteAgentCoreReady"); cb.Type() == js.TypeFunction {
		cb.Invoke(api)
	}
	select {}
}
