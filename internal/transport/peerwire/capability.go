package peerwire

// CapabilityExpiry applies to authenticated capability bodies only. Their
// lifetime is bounded by the signed issue time, not the receiver's clock skew.
func CapabilityExpiry(o Opened, now uint64) (uint64, error) {
	name, field := "Capabilities", uint64(4)
	if o.Type == 14 {
		name, field = "CapabilitySnapshot", 3
	} else if o.Type != 1 {
		return 0, ErrPacket
	}
	if o.Context.Class != 2 {
		return 0, ErrPacket
	}
	if _, err := KnownPayload(2, o.Type, o.Payload); err != nil {
		return 0, err
	}
	f, err := ParseMessage(o.Payload, name)
	if err != nil {
		return 0, err
	}
	expires := f[field].U
	if expires <= now || expires <= o.Context.Issued || expires-o.Context.Issued > 60000 {
		return 0, ErrPacket
	}
	return expires, nil
}
