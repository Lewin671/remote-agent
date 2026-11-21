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
		download, eventPush, ok := parseCapabilitiesPayload(o.Payload)
		if !ok {
			return 0, nil
		}
		dlUntil := int64(0)
		if download {
			dlUntil = int64(exp)
		}
		c.handleEngine(now, Capabilities{
			Round:         r.id,
			Seq:           o.Context.Seq,
			DownloadUntil: dlUntil,
			EventPush:     eventPush,
		})
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

// parseCapabilitiesPayload extracts field 5 (file_download) and whether field 2
// (control_types, packed or unpacked) contains 15.
func parseCapabilitiesPayload(raw []byte) (fileDownload bool, eventPush bool, ok bool) {
	pos := 0
	for pos < len(raw) {
		tag, n := readUvarint(raw[pos:])
		if n <= 0 {
			return false, false, false
		}
		pos += n
		fieldNum := tag >> 3
		wire := tag & 7
		switch wire {
		case 0:
			v, m := readUvarint(raw[pos:])
			if m <= 0 {
				return false, false, false
			}
			pos += m
			switch fieldNum {
			case 2:
				if v == 15 {
					eventPush = true
				}
			case 5:
				fileDownload = v != 0
			}
		case 2:
			size, m := readUvarint(raw[pos:])
			if m <= 0 || size > uint64(len(raw)-pos-m) {
				return false, false, false
			}
			pos += m
			sub := raw[pos : pos+int(size)]
			pos += int(size)
			if fieldNum == 2 {
				for sp := 0; sp < len(sub); {
					v, k := readUvarint(sub[sp:])
					if k <= 0 {
						return false, false, false
					}
					sp += k
					if v == 15 {
						eventPush = true
					}
				}
			}
		default:
			nSkip := skipWireField(raw[pos:], wire)
			if nSkip < 0 {
				return false, false, false
			}
			pos += nSkip
		}
	}
	return fileDownload, eventPush, true
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
