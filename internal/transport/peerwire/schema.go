package peerwire

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"
)

type schemaRule struct {
	N        uint64
	Kind     string
	Repeated bool
}

func schemaLimit(name string, n uint64) int {
	if name == "Candidates" && n == 1 {
		return 16
	}
	if name == "Restart" && n == 1 {
		return 2
	}
	if name == "Capabilities" {
		return 64
	}
	if name == "DirectEventSubscription" && n == 1 {
		return 4
	}
	return 128
}
func scalarLimit(kind string) uint64 {
	switch kind {
	case "bool":
		return 1
	case "u32":
		return 1<<32 - 1
	case "i32":
		return 1<<31 - 1
	case "i64":
		return 1<<63 - 1
	}
	if strings.HasPrefix(kind, "e") {
		n, _ := strconv.ParseUint(kind[1:], 10, 64)
		return n
	}
	return ^uint64(0)
}

// Decode only after authentication. Defaults are materialized for local readers;
// signatures, hashes and retransmissions always retain the original bytes.
func NormalizeMessage(raw []byte, name string) ([]byte, error) {
	return normalizeMessage(raw, name, 0, false)
}
func normalizeMessage(raw []byte, name string, depth int, shape bool) ([]byte, error) {
	rules, ok := schemaRules[name]
	limit := 65536
	if name == "LogicalFrame" {
		limit = 1 << 20
	}
	if name == "Candidate" {
		limit = 1024
	}
	if !ok || depth > 8 || len(raw) > limit {
		return nil, ErrPacket
	}
	spec := map[uint64]schemaRule{}
	values := map[uint64][]Field{}
	for _, r := range rules {
		spec[r.N] = r
	}
	last := uint64(0)
	read := func() (uint64, error) {
		v, n := binary.Uvarint(raw)
		if n <= 0 || !bytes.Equal(raw[:n], varint(v)) {
			return 0, ErrPacket
		}
		raw = raw[n:]
		return v, nil
	}
	for len(raw) > 0 {
		tag, e := read()
		if e != nil {
			return nil, e
		}
		n, wire := tag>>3, tag&7
		r, found := spec[n]
		if !found || n < last || (!r.Repeated && len(values[n]) != 0) {
			return nil, ErrPacket
		}
		last = n
		numeric := r.Kind != "b" && !strings.HasPrefix(r.Kind, "m")
		if numeric && wire == 0 {
			v, e := read()
			if e != nil || v > scalarLimit(r.Kind) {
				return nil, ErrPacket
			}
			values[n] = append(values[n], U(n, v))
		} else if wire == 2 {
			size, e := read()
			if e != nil || size > uint64(len(raw)) {
				return nil, ErrPacket
			}
			body := raw[:size]
			raw = raw[size:]
			if numeric {
				if !r.Repeated {
					return nil, ErrPacket
				}
				for len(body) > 0 {
					if len(values[n]) >= schemaLimit(name, n) {
						return nil, ErrPacket
					}
					v, k := binary.Uvarint(body)
					if k <= 0 || !bytes.Equal(body[:k], varint(v)) || v > scalarLimit(r.Kind) {
						return nil, ErrPacket
					}
					body = body[k:]
					values[n] = append(values[n], U(n, v))
				}
			}
			if !numeric {
				if strings.HasPrefix(r.Kind, "m") {
					if _, e := normalizeMessage(body, r.Kind[1:], depth+1, shape); e != nil {
						return nil, e
					}
				}
				values[n] = append(values[n], B(n, append([]byte(nil), body...)))
			}
		} else {
			return nil, ErrPacket
		}
		if len(values[n]) > schemaLimit(name, n) {
			return nil, ErrPacket
		}
	}
	if shape && !schemaRelations(name, values) {
		return nil, ErrPacket
	}
	var out []byte
	for _, r := range rules {
		list := values[r.N]
		if len(list) == 0 && !r.Repeated && !strings.HasPrefix(r.Kind, "m") {
			if r.Kind == "b" {
				list = []Field{B(r.N, nil)}
			} else {
				list = []Field{U(r.N, 0)}
			}
		}
		for _, f := range list {
			if shape {
				if b, ok := schemaBounds[name][r.N]; ok {
					v := f.U
					if f.Bytes {
						v = uint64(len(f.B))
					}
					if v < b[0] || v > b[1] {
						return nil, ErrPacket
					}
				}
			}
			out = append(out, PB(f)...)
		}
	}
	return out, nil
}
func ParseMessage(raw []byte, name string) (map[uint64]Field, error) {
	normalized, e := NormalizeMessage(raw, name)
	if e != nil {
		return nil, e
	}
	// normalizeMessage already validated repeated fields. This convenience
	// view returns the first occurrence; list consumers retain the original.
	out := map[uint64]Field{}
	for len(normalized) > 0 {
		tag, n := binary.Uvarint(normalized)
		normalized = normalized[n:]
		field := tag >> 3
		f := U(field, 0)
		if tag&7 == 0 {
			f.U, n = binary.Uvarint(normalized)
			normalized = normalized[n:]
		} else {
			size, k := binary.Uvarint(normalized)
			normalized = normalized[k:]
			f = B(field, normalized[:size])
			normalized = normalized[size:]
		}
		if _, exists := out[field]; !exists {
			out[field] = f
		}
	}
	return out, nil
}

var signalSchemas = map[uint64]string{1: "Probe", 2: "Challenge", 3: "SDP", 4: "SDP", 5: "Candidates", 6: "EndCandidates", 7: "Confirm", 8: "Restart", 9: "Close"}
var controlSchemas = map[uint64]string{1: "Capabilities", 2: "AttachPath", 3: "InputStatus", 4: "Ack", 5: "Replay", 6: "Barrier", 7: "StreamFailed", 8: "ClosePath", 9: "DirectPolicy", 10: "Unsupported", 11: "InputStatusReply", 12: "BarrierReply", 13: "PolicyReply", 14: "CapabilitySnapshot", 15: "DirectEventSubscription"}

func KnownPayload(class, typ uint64, raw []byte) ([]byte, error) {
	names := signalSchemas
	if class == 2 {
		names = controlSchemas
	}
	name, ok := names[typ]
	if !ok {
		return raw, nil
	}
	return normalizeMessage(raw, name, 0, true)
}

// Restart can address two PC uses at peer scope, or exactly its PC at PC scope.
func ReadRestart(raw []byte) (map[uint64]uint64, error) {
	normalized, e := NormalizeMessage(raw, "Restart")
	if e != nil {
		return nil, e
	}
	raw = normalized
	targets := map[uint64]uint64{}
	for len(raw) > 0 && raw[0] == 10 {
		raw = raw[1:]
		size, n := binary.Uvarint(raw)
		if n <= 0 || size > uint64(len(raw)-n) {
			return nil, ErrPacket
		}
		raw = raw[n:]
		f, e := ParseMessage(raw[:size], "RestartPC")
		raw = raw[size:]
		if e != nil || f[1].U < 1 || f[1].U > 2 {
			return nil, ErrPacket
		}
		if _, ok := targets[f[1].U]; ok {
			return nil, ErrPacket
		}
		targets[f[1].U] = f[2].U
	}
	tail, e := Parse(raw, 1)
	if e != nil || len(tail) != 1 || tail[2].Bytes || tail[2].U > 1<<32-1 || len(targets) == 0 {
		return nil, ErrPacket
	}
	return targets, nil
}
