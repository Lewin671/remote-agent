//go:build js && wasm

package main

import (
	"sync"
	"syscall/js"

	"github.com/Lewin671/remote-agent/clientcore/corebind"
)

var (
	mu         sync.Mutex
	nextHandle int64 = 1
	cores            = make(map[int64]*corebind.Core)
	imagePlans       = make(map[int64]*corebind.ImagePlan)
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

func lookupCore(handle int64) *corebind.Core {
	mu.Lock()
	defer mu.Unlock()
	return cores[handle]
}

func lookupImagePlan(handle int64) *corebind.ImagePlan {
	mu.Lock()
	defer mu.Unlock()
	return imagePlans[handle]
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

	api.Set("sniffImageFormat", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var raw []byte
		if len(args) > 0 {
			raw = bytesFromJS(args[0])
		}
		return int(corebind.SniffImageFormat(raw))
	}))

	api.Set("tiffOrientation", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var raw []byte
		if len(args) > 0 {
			raw = bytesFromJS(args[0])
		}
		return int(corebind.TiffOrientation(raw))
	}))

	api.Set("imagePlanMaxSteps", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var totalLen int64
		if len(args) > 0 {
			totalLen = int64(args[0].Float())
		}
		return float64(corebind.ImagePlanMaxSteps(totalLen))
	}))

	api.Set("newImagePlan", js.FuncOf(func(_ js.Value, args []js.Value) any {
		var mode, expected int32
		var totalLen int64
		if len(args) > 0 {
			mode = int32(args[0].Int())
		}
		if len(args) > 1 {
			expected = int32(args[1].Int())
		}
		if len(args) > 2 {
			totalLen = int64(args[2].Float())
		}
		mu.Lock()
		id := nextHandle
		nextHandle++
		imagePlans[id] = corebind.NewImagePlan(mode, expected, totalLen)
		mu.Unlock()
		return float64(id)
	}))

	api.Set("imagePlanStep", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 2 {
			return map[string]any{"error": "missing arguments"}
		}
		p := lookupImagePlan(int64(args[0].Float()))
		if p == nil {
			return map[string]any{"error": "unknown handle"}
		}
		offset := int64(args[1].Float())
		var raw []byte
		if len(args) > 2 {
			raw = bytesFromJS(args[2])
		}
		return resultBytes(p.Step(offset, raw))
	}))

	api.Set("imagePlanFree", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 {
			return nil
		}
		id := int64(args[0].Float())
		mu.Lock()
		delete(imagePlans, id)
		mu.Unlock()
		return nil
	}))

	js.Global().Set("__remoteAgentCore", api)
	if cb := js.Global().Get("__remoteAgentCoreReady"); cb.Type() == js.TypeFunction {
		cb.Invoke(api)
	}
	select {}
}
