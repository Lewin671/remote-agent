package labwire

import "bytes"

// ValidatePolicyReply compares an authenticated reply with the frozen outgoing
// request record. The tests also authenticate that request at its recipient.
// Context.Seq belongs to the reply; it cannot identify the original request.
func ValidatePolicyReply(request, reply Opened) error {
	a, z := request.Context, reply.Context
	if a.Class != 2 || z.Class != 2 || request.Type != 9 || reply.Type != 13 || !bytes.Equal(a.From, z.To) || !bytes.Equal(a.To, z.From) || !bytes.Equal(a.Connection, z.Connection) {
		return ErrPacket
	}
	q, e := Parse(request.Payload, 4)
	if e != nil || need(q, "buuu") != nil {
		return ErrPacket
	}
	r, e := Parse(reply.Payload, 7)
	if e != nil || need(r, "uuuubub") != nil {
		return ErrPacket
	}
	if q[2].U == 0 || q[3].U > 1 || r[2].U > 1 || r[3].U > 1 {
		return ErrPacket
	}
	if !(q[4].U == 1 && len(q[1].B) == 0 || q[4].U == 2 && len(q[1].B) == 32) {
		return ErrPacket
	}
	if r[1].U != q[2].U || r[4].U != a.Seq || len(r[5].B) != 32 || !bytes.Equal(r[5].B, request.Hash) || r[6].U != q[4].U || !bytes.Equal(r[7].B, q[1].B) {
		return ErrPacket
	}
	if r[2].U == 1 && r[3].U != q[3].U {
		return ErrPacket
	}
	return nil
}
