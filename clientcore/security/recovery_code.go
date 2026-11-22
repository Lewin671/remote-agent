package security

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	errInvalidServer       = errors.New(text.KeyErrorsTheServerMustUseHttps)
	errInvalidRecoveryCode = errors.New(text.KeySecurityInvalidRecoveryCode)
)

// EncodeRecoveryCode encodes 10 bytes of genesis prefix + 32-byte Ed25519 seed in Crockford Base32.
func EncodeRecoveryCode(seed, genesis [32]byte) string {
	var payload [42]byte
	copy(payload[:10], genesis[:10])
	copy(payload[10:], seed[:])

	var raw [68]byte
	rawLen := 0
	var bits, value uint32
	for _, b := range payload {
		value = (value << 8) | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			raw[rawLen] = crockfordAlphabet[(value>>bits)&31]
			rawLen++
		}
	}
	if bits > 0 {
		raw[rawLen] = crockfordAlphabet[(value<<(5-bits))&31]
		rawLen++
	}
	var out strings.Builder
	out.Grow(rawLen + rawLen/4)
	for i := range rawLen {
		if i > 0 && i%4 == 0 {
			out.WriteByte('-')
		}
		out.WriteByte(raw[i])
	}
	return out.String()
}

// DecodeRecoveryCode parses Crockford Base32 into a 32-byte recovery seed and 10-byte genesis prefix.
func DecodeRecoveryCode(text string) (seed [32]byte, genesisPrefix [10]byte, ok bool) {
	var out []byte
	var bits, value uint32
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == ' ' || c == '-' || c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		switch c {
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		idx := strings.IndexByte(crockfordAlphabet, c)
		if idx < 0 {
			return seed, genesisPrefix, false
		}
		value = (value << 5) | uint32(idx)
		bits += 5
		if bits >= 8 {
			bits -= 8
			out = append(out, byte((value>>bits)&0xff))
			if len(out) > 42 {
				return seed, genesisPrefix, false
			}
		}
	}
	if bits >= 5 || (value&((1<<bits)-1)) != 0 || len(out) != 42 {
		return seed, genesisPrefix, false
	}
	copy(genesisPrefix[:], out[:10])
	copy(seed[:], out[10:42])
	return seed, genesisPrefix, true
}

// CheckServerURL validates and normalizes a server origin matching Web/Android/iOS policy.
func CheckServerURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errInvalidServer
	}
	schemeEnd := strings.Index(s, "://")
	if schemeEnd <= 0 {
		return "", errInvalidServer
	}
	scheme := strings.ToLower(s[:schemeEnd])
	if scheme != "https" && scheme != "http" {
		return "", errInvalidServer
	}
	rest := s[schemeEnd+3:]
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" || strings.Contains(rest, "@") {
		return "", errInvalidServer
	}
	host := rest
	if strings.HasPrefix(host, "[") {
		end := strings.IndexByte(host, ']')
		if end < 0 {
			return "", errInvalidServer
		}
		host = host[:end+1]
	} else if colon := strings.LastIndexByte(host, ':'); colon >= 0 {
		host = host[:colon]
	}
	hostLower := strings.ToLower(host)
	isLocal := hostLower == "localhost" || hostLower == "127.0.0.1" || hostLower == "[::1]"
	if scheme != "https" && !isLocal {
		return "", errInvalidServer
	}
	return scheme + "://" + rest, nil
}

const b64urlAlpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

func b64urlEncode(src []byte) string {
	if len(src) == 0 {
		return ""
	}
	var out strings.Builder
	out.Grow((len(src)*4 + 2) / 3)
	for i := 0; i < len(src); i += 3 {
		var b uint32
		rem := len(src) - i
		b = uint32(src[i]) << 16
		if rem > 1 {
			b |= uint32(src[i+1]) << 8
		}
		if rem > 2 {
			b |= uint32(src[i+2])
		}
		out.WriteByte(b64urlAlpha[(b>>18)&63])
		out.WriteByte(b64urlAlpha[(b>>12)&63])
		if rem > 1 {
			out.WriteByte(b64urlAlpha[(b>>6)&63])
		}
		if rem > 2 {
			out.WriteByte(b64urlAlpha[b&63])
		}
	}
	return out.String()
}

func b64urlDecode(s string) ([]byte, bool) {
	s = strings.TrimRight(s, "=")
	if len(s)%4 == 1 {
		return nil, false
	}
	var out []byte
	for i := 0; i < len(s); i += 4 {
		rem := len(s) - i
		var vals [4]int
		for j := range 4 {
			if j < rem {
				idx := strings.IndexByte(b64urlAlpha, s[i+j])
				if idx < 0 {
					return nil, false
				}
				vals[j] = idx
			}
		}
		v := uint32(vals[0])<<18 | uint32(vals[1])<<12 | uint32(vals[2])<<6 | uint32(vals[3])
		out = append(out, byte((v>>16)&0xff))
		if rem > 2 {
			out = append(out, byte((v>>8)&0xff))
		}
		if rem > 3 {
			out = append(out, byte(v&0xff))
		}
	}
	return out, true
}

func urlQueryEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

func urlQueryUnescape(s string) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '+':
			b.WriteByte(' ')
		case '%':
			if i+2 >= len(s) {
				return "", false
			}
			hi, ok1 := fromHexNibble(s[i+1])
			lo, ok2 := fromHexNibble(s[i+2])
			if !ok1 || !ok2 {
				return "", false
			}
			b.WriteByte((hi << 4) | lo)
			i += 2
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), true
}

func fromHexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func is8Digits(s string) bool {
	if len(s) != 8 {
		return false
	}
	for i := range 8 {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// FormatPairLink formats a pairing deep link URL using webOrigin, server, 8-digit code, and 32-byte commit.
func FormatPairLink(webOrigin, server, code string, commit [32]byte) string {
	base := strings.TrimRight(webOrigin, "/")
	return base + "/#/pair?code=" + urlQueryEscape(code) + "&c=" + b64urlEncode(commit[:]) + "&s=" + urlQueryEscape(server)
}

// ParsePairLink extracts code, normalized server origin, and optional 32-byte commit from a pairing URL or query.
func ParsePairLink(raw string) (code, server string, commit []byte, ok bool) {
	qStr := raw
	if idx := strings.Index(raw, "#/pair?"); idx >= 0 {
		qStr = raw[idx+len("#/pair?"):]
	} else if idx := strings.IndexByte(raw, '?'); idx >= 0 {
		qStr = raw[idx+1:]
	}
	var rawServer string
	var hasCommit bool
	for _, part := range strings.Split(qStr, "&") {
		k, v, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		dk, ok1 := urlQueryUnescape(k)
		dv, ok2 := urlQueryUnescape(v)
		if !ok1 || !ok2 {
			continue
		}
		switch dk {
		case "code":
			code = dv
		case "s":
			rawServer = dv
		case "c":
			dec, decOK := b64urlDecode(dv)
			if !decOK || len(dec) != 32 {
				return "", "", nil, false
			}
			commit = dec
			hasCommit = true
		}
	}
	if !is8Digits(code) || rawServer == "" {
		return "", "", nil, false
	}
	srv, err := CheckServerURL(rawServer)
	if err != nil {
		return "", "", nil, false
	}
	if !hasCommit {
		commit = nil
	}
	return code, srv, commit, true
}

func deriveAccountSignSeed(root []byte) ([]byte, error) {
	if len(root) != 32 {
		return nil, devicesec.ErrInvalid
	}
	return hkdf.Key(sha256.New, root, nil, "remote-agent/v1/account-sign", 32)
}

func zeroBytes(b []byte) {
	clear(b)
}

func sealPairingEnvelopeAESGCM(key, nonce, aad, plain []byte) ([]byte, error) {
	if len(key) != 32 || len(nonce) != 12 {
		return nil, devicesec.ErrInvalid
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nonce, plain, aad), nil
}

func openPairingEnvelopeAESGCM(key, nonce, aad, ct []byte) ([]byte, error) {
	if len(key) != 32 || len(nonce) != 12 || len(ct) < 16 {
		return nil, devicesec.ErrUntrusted
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, devicesec.ErrUntrusted
	}
	return plain, nil
}

func sameBytes(a, b []byte) bool {
	return bytes.Equal(a, b)
}

func sortedMembersCopy(members []devicesec.Member) []devicesec.Member {
	out := slices.Clone(members)
	slices.SortFunc(out, func(a, b devicesec.Member) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return out
}
