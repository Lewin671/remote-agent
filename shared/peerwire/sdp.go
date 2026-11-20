package peerwire

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The SDP checks are hand-written instead of regexp (which would add about
// 87 KB to the client core); TestSDPWithoutRegexpAgrees pins them to the
// original patterns, quoted next to each matcher.

func all(v string, ok func(byte) bool) bool {
	for i := 0; i < len(v); i++ {
		if !ok(v[i]) {
			return false
		}
	}
	return true
}
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isHex(c byte) bool   { _, ok := unhexDigit(c); return ok }
func isBase64(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '+' || c == '/'
}

// ^m=application [1-9][0-9]* UDP/DTLS/SCTP webrtc-datachannel$
func mediaLine(l string) bool {
	rest, ok := strings.CutPrefix(l, "m=application ")
	port, tail, ok2 := strings.Cut(rest, " ")
	return ok && ok2 && tail == "UDP/DTLS/SCTP webrtc-datachannel" && port != "" && port[0] != '0' && all(port, isDigit)
}

// ^[vostcm]=
func typedLine(l string) bool {
	return len(l) >= 2 && l[1] == '=' && strings.IndexByte("vostcm", l[0]) >= 0
}

// ^sha-256 ([0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}$
func fingerprintValue(fp string) bool {
	v, ok := strings.CutPrefix(fp, "sha-256 ")
	if !ok || len(v) != 95 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if i%3 == 2 {
			if v[i] != ':' {
				return false
			}
		} else if !isHex(v[i]) {
			return false
		}
	}
	return true
}

// ^[A-Za-z0-9+/]{min,256}$
func iceToken(v string, min int) bool { return len(v) >= min && len(v) <= 256 && all(v, isBase64) }

func ValidateSDP(raw []byte) error {
	fail := errors.New("invalid SDP")
	if len(raw) > 60*1024 || !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) || !bytes.HasSuffix(raw, []byte("\r\n")) {
		return fail
	}
	without := strings.ReplaceAll(string(raw), "\r\n", "")
	if strings.ContainsAny(without, "\r\n\v\f\u0085\u2028\u2029") {
		return fail
	}
	scopes := [2]map[string]string{{}, {}}
	section := 0
	allowed := strings.Fields("group msid-semantic fingerprint ice-ufrag ice-pwd ice-options setup mid sctp-port max-message-size end-of-candidates extmap-allow-mixed sendrecv")
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\r\n"), "\r\n") {
		if strings.HasPrefix(line, "m=") {
			section++
			if section != 1 || !mediaLine(line) {
				return fail
			}
		}
		if line == "a=ice-lite" {
			return fail
		}
		if strings.HasPrefix(line, "a=") {
			name, value, _ := strings.Cut(line[2:], ":")
			ok := false
			for _, a := range allowed {
				if a == name {
					ok = true
				}
			}
			if !ok {
				return fail
			}
			if _, exists := scopes[section][name]; exists {
				return fail
			}
			scopes[section][name] = value
		} else if !typedLine(line) {
			return fail
		}
	}
	if section != 1 {
		return fail
	}
	fingerprint := ""
	for _, scope := range scopes {
		if fp, exists := scope["fingerprint"]; exists {
			if !fingerprintValue(fp) {
				return fail
			}
			if fingerprint != "" && !strings.EqualFold(fingerprint, fp) {
				return fail
			}
			fingerprint = fp
		}
	}
	if fingerprint == "" {
		return fail
	}
	values := map[string]string{}
	for _, attr := range []string{"ice-ufrag", "ice-pwd", "setup"} {
		for _, scope := range scopes {
			if v, ok := scope[attr]; ok {
				if old, exists := values[attr]; exists && old != v {
					return fail
				}
				values[attr] = v
			}
		}
		if _, ok := values[attr]; !ok {
			return fail
		}
	}
	if !iceToken(values["ice-ufrag"], 4) || !iceToken(values["ice-pwd"], 22) {
		return fail
	}
	if setup := values["setup"]; setup != "active" && setup != "passive" && setup != "actpass" {
		return fail
	}
	// ^[0-9]+$
	port := scopes[1]["sctp-port"]
	n, err := strconv.Atoi(port)
	if port == "" || !all(port, isDigit) || err != nil || n > 65535 {
		return fail
	}
	return nil
}

type SDPInfo struct{ Raw, Ufrag, Mid, Fingerprint []byte }

func InspectSDP(raw []byte) (SDPInfo, error) {
	if e := ValidateSDP(raw); e != nil {
		return SDPInfo{}, e
	}
	r := SDPInfo{Raw: raw}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, "a=ice-ufrag:") {
			r.Ufrag = []byte(strings.TrimPrefix(line, "a=ice-ufrag:"))
		}
		if strings.HasPrefix(line, "a=mid:") {
			r.Mid = []byte(strings.TrimPrefix(line, "a=mid:"))
		}
		if strings.HasPrefix(line, "a=fingerprint:sha-256 ") {
			r.Fingerprint = fingerprintBytes(strings.TrimPrefix(line, "a=fingerprint:sha-256 "))
		}
	}
	return r, nil
}

// fingerprintBytes decodes a fingerprint ValidateSDP already accepted.
func fingerprintBytes(v string) []byte {
	out := make([]byte, 0, 32)
	for i := 0; i+1 < len(v); i += 3 {
		hi, _ := unhexDigit(v[i])
		lo, _ := unhexDigit(v[i+1])
		out = append(out, hi<<4|lo)
	}
	return out
}
