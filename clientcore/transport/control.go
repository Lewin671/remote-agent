package transport

import (
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

// decodeControl handles an authenticated Class=2 control frame delivered by linkcore.Session.
func (c *Coordinator) decodeControl(now int64, r *coordRound, eff linkcore.HandleControl) (uint64, []byte) {
	o := eff.Frame
	switch o.Type {
	case 1: // Capabilities
		exp, err := peerwire.CapabilityExpiry(o, uint64(now))
		if err != nil {
			return 0, nil
		}
		download, eventPush, phase2Cmds, terminalDirect, ok := parseCapabilitiesPayload(o.Payload)
		if !ok {
			return 0, nil
		}
		dlUntil := int64(0)
		if download {
			dlUntil = int64(exp)
		}
		c.handleEngine(now, Capabilities{
			Round:          r.id,
			Seq:            o.Context.Seq,
			DownloadUntil:  dlUntil,
			EventPush:      eventPush,
			Phase2Commands: phase2Cmds,
			Terminal:       terminalDirect,
		})
		if terminalDirect {
			c.attachDirectTerminalsForMachine(now, r.machine)
		}
		return 4, ackPayload(o)

	case 14: // CapabilitySnapshot
		exp, err := peerwire.CapabilityExpiry(o, uint64(now))
		if err != nil {
			return 0, nil
		}
		caps, ok := parseCapabilitySnapshotPayload(o.Payload)
		if !ok || caps == nil {
			return 0, nil
		}
		c.handleEngine(now, CapabilitySnapshot{
			Round:     r.id,
			Seq:       o.Context.Seq,
			Caps:      caps,
			ExpiresAt: int64(exp),
		})
		return 4, ackPayload(o)

	case linkcore.ControlAck, linkcore.ControlStreamFailed, linkcore.ControlInputStatusReply, linkcore.ControlBarrierReply:
		return c.handleInboundTerminalControl(now, r, o)

	default:
		return 0, nil
	}
}

func ackPayload(o peerwire.Opened) []byte {
	return peerwire.PB(
		peerwire.U(4, o.Context.Class),
		peerwire.U(5, o.Context.Seq),
		peerwire.B(6, o.Hash),
		peerwire.U(7, o.Context.Generation),
		peerwire.U(8, o.Context.Use),
	)
}

func encodeSubscription(sessionIDs []string) []byte {
	fields := make([]peerwire.Field, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		fields = append(fields, peerwire.B(1, []byte(sid)))
	}
	return peerwire.PB(fields...)
}

func (c *Coordinator) maybeSendClientCapabilities(now int64, r *coordRound) {
	if r == nil || r.ended || r.clientCapsSent || r.session == nil {
		return
	}
	var capID [16]byte
	if _, err := c.randPool.Read(capID[:]); err != nil {
		c.maybeNeedEntropy(CoordEntropyRefillBytes)
		return
	}
	c.maybeLowWater()
	r.clientCapsSent = true
	body := encodeClientCapabilities(capID, now+55_000)
	_ = c.stepSession(now, r, linkcore.QueueControl{Type: 1, Body: body})
}

// encodeClientCapabilities encodes Control 1 declaring the client's Phase 2/3
// support so a Phase 3 daemon enables direct-only large terminal output only
// for negotiated Phase 3 clients (H4) without breaking Phase 1 daemons.
func encodeClientCapabilities(capID [16]byte, expiresAtMs int64) []byte {
	return peerwire.PB(
		peerwire.B(1, capID[:]),
		peerwire.U(2, 1), peerwire.U(2, 2), peerwire.U(2, 3), peerwire.U(2, 4), peerwire.U(2, 5),
		peerwire.U(2, 6), peerwire.U(2, 7), peerwire.U(2, 8), peerwire.U(2, 9), peerwire.U(2, 10),
		peerwire.U(2, 11), peerwire.U(2, 12), peerwire.U(2, 13), peerwire.U(2, 14), peerwire.U(2, 15),
		peerwire.U(3, 1), peerwire.U(3, 2), peerwire.U(3, 3), peerwire.U(3, 4), peerwire.U(3, 5),
		peerwire.U(3, 6), peerwire.U(3, 7), peerwire.U(3, 8), peerwire.U(3, 9),
		peerwire.U(4, uint64(expiresAtMs)),
		peerwire.U(5, 1),
		peerwire.U(6, 1<<20),
	)
}

// parseCapabilitiesPayload extracts field 5 (file_download) and whether field 2
// (control_types, packed or unpacked) contains 15 (event push) and Phase 2/3
// continuity controls (2, 3, 5, 6, 7, 8, 11, 12).
func parseCapabilitiesPayload(raw []byte) (fileDownload bool, eventPush bool, phase2Commands bool, terminalDirect bool, ok bool) {
	var termMask uint32
	noteControl := func(v uint64) {
		switch v {
		case 15:
			eventPush = true
		case 2, 3, 5, 6, 7, 8, 11, 12:
			termMask |= 1 << v
		}
	}
	const wantTermMask uint32 = (1 << 2) | (1 << 3) | (1 << 5) | (1 << 6) | (1 << 7) | (1 << 8) | (1 << 11) | (1 << 12)
	pos := 0
	for pos < len(raw) {
		tag, n := readUvarint(raw[pos:])
		if n <= 0 {
			return false, false, false, false, false
		}
		pos += n
		fieldNum := tag >> 3
		wire := tag & 7
		switch wire {
		case 0:
			v, m := readUvarint(raw[pos:])
			if m <= 0 {
				return false, false, false, false, false
			}
			pos += m
			switch fieldNum {
			case 2:
				noteControl(v)
			case 5:
				fileDownload = v != 0
			}
		case 2:
			size, m := readUvarint(raw[pos:])
			if m <= 0 || size > uint64(len(raw)-pos-m) {
				return false, false, false, false, false
			}
			pos += m
			sub := raw[pos : pos+int(size)]
			pos += int(size)
			if fieldNum == 2 {
				for sp := 0; sp < len(sub); {
					v, k := readUvarint(sub[sp:])
					if k <= 0 {
						return false, false, false, false, false
					}
					sp += k
					noteControl(v)
				}
			}
		default:
			nSkip := skipWireField(raw[pos:], wire)
			if nSkip < 0 {
				return false, false, false, false, false
			}
			pos += nSkip
		}
	}
	hasTerm := (termMask & wantTermMask) == wantTermMask
	return fileDownload, eventPush, hasTerm, hasTerm, true
}

// parseCapabilitySnapshotPayload extracts CapabilitySnapshot.capabilities (field 2).
func parseCapabilitySnapshotPayload(raw []byte) (*Caps, bool) {
	f, err := peerwire.ParseMessage(raw, "CapabilitySnapshot")
	if err != nil {
		return nil, false
	}
	capBytes, hasCap := f[2]
	if !hasCap || !capBytes.Bytes {
		return nil, false
	}
	caps := &Caps{}
	pos := 0
	sub := capBytes.B
	for pos < len(sub) {
		tag, n := readUvarint(sub[pos:])
		if n <= 0 {
			return nil, false
		}
		pos += n
		fieldNum := tag >> 3
		wire := tag & 7
		switch wire {
		case 0:
			v, m := readUvarint(sub[pos:])
			if m <= 0 {
				return nil, false
			}
			pos += m
			switch fieldNum {
			case 2:
				caps.ImageUpload = v != 0
			case 5:
				caps.MaxChunkBytes = int64(int32(v))
			case 13:
				caps.FileUpload = v != 0
			}
		default:
			nSkip := skipWireField(sub[pos:], wire)
			if nSkip < 0 {
				return nil, false
			}
			pos += nSkip
		}
	}
	return caps, true
}

func readUvarint(b []byte) (uint64, int) {
	var x uint64
	var s uint
	for i := 0; i < len(b) && i < 10; i++ {
		c := b[i]
		if c < 0x80 {
			if i == 9 && c > 1 {
				return 0, 0
			}
			return x | uint64(c)<<s, i + 1
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0
}

func skipWireField(b []byte, wire uint64) int {
	switch wire {
	case 0:
		_, n := readUvarint(b)
		if n <= 0 {
			return -1
		}
		return n
	case 1:
		if len(b) < 8 {
			return -1
		}
		return 8
	case 2:
		l, n := readUvarint(b)
		if n <= 0 || l > uint64(len(b)-n) {
			return -1
		}
		return n + int(l)
	case 5:
		if len(b) < 4 {
			return -1
		}
		return 4
	default:
		return -1
	}
}
