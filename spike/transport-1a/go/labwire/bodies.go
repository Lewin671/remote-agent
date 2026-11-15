package labwire

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf8"
)

func SDPBody(raw []byte) []byte { return PB(B(1, raw), B(2, Hash(raw))) }
func ReadSDPBody(raw []byte) ([]byte, SDPInfo, error) {
	f, e := ParseMessage(raw, "SDP")
	if e != nil || need(f, "bb") != nil || !bytes.Equal(Hash(f[1].B), f[2].B) {
		return nil, SDPInfo{}, ErrPacket
	}
	info, e := InspectSDP(f[1].B)
	return f[1].B, info, e
}

type Candidate struct {
	Original, Ufrag, Mid []byte
	Index, Generation    uint64
}

func (c Candidate) Encode() []byte {
	return PB(B(1, c.Original), B(2, c.Ufrag), B(3, c.Mid), U(4, c.Index), U(5, c.Generation))
}
func CandidateBody(candidates []Candidate, index uint64) []byte {
	var out []byte
	for _, c := range candidates {
		raw := c.Encode()
		out = append(out, 0x0a)
		out = append(out, varint(uint64(len(raw)))...)
		out = append(out, raw...)
	}
	return append(out, PB(U(2, index))...)
}
func ReadCandidates(raw []byte) ([]Candidate, uint64, error) {
	var result []Candidate
	var e error
	raw, e = NormalizeMessage(raw, "Candidates")
	if e != nil {
		return nil, 0, e
	}
	for len(raw) > 0 && raw[0] == 0x0a {
		raw = raw[1:]
		size, n := binary.Uvarint(raw)
		if n <= 0 || !bytes.Equal(raw[:n], varint(size)) || size > 1024 || size > uint64(len(raw)-n) || len(result) >= 16 {
			return nil, 0, ErrPacket
		}
		raw = raw[n:]
		f, e := ParseMessage(raw[:size], "Candidate")
		raw = raw[size:]
		if e != nil || need(f, "bbbuu") != nil {
			return nil, 0, ErrPacket
		}
		if !utf8.Valid(f[1].B) || bytes.ContainsAny(f[1].B, "\x00\r\n") || !bytes.HasPrefix(f[1].B, []byte("candidate:")) {
			return nil, 0, ErrPacket
		}
		for _, b := range f[1].B {
			if b < 32 || b > 126 {
				return nil, 0, ErrPacket
			}
		}
		tokens := strings.Fields(string(f[1].B))
		ufrags := 0
		for i, v := range tokens {
			if strings.EqualFold(v, "ufrag") {
				ufrags++
				if v != "ufrag" || ufrags > 1 || i+1 >= len(tokens) || tokens[i+1] != string(f[2].B) {
					return nil, 0, ErrPacket
				}
			}
		}
		result = append(result, Candidate{f[1].B, f[2].B, f[3].B, f[4].U, f[5].U})
	}
	fields, e := Parse(raw, 1)
	if e != nil || len(fields) != 1 || fields[2].Bytes || fields[2].N != 2 || fields[2].U > 3 {
		return nil, 0, ErrPacket
	}
	return result, fields[2].U, nil
}
func EndBody(ufrag []byte, count, batches uint64) []byte {
	return PB(B(1, ufrag), U(2, count), U(3, batches))
}
