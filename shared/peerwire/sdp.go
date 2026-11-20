package peerwire

import (
	"bytes"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

func matches(pattern, value string) bool { return regexp.MustCompile(pattern).MatchString(value) }

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
			if section != 1 || !matches(`^m=application [1-9][0-9]* UDP/DTLS/SCTP webrtc-datachannel$`, line) {
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
		} else if !matches(`^[vostcm]=`, line) {
			return fail
		}
	}
	if section != 1 {
		return fail
	}
	fingerprint := ""
	for _, scope := range scopes {
		if fp, exists := scope["fingerprint"]; exists {
			if !matches(`^sha-256 ([0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}$`, fp) {
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
	if !matches(`^[A-Za-z0-9+/]{4,256}$`, values["ice-ufrag"]) || !matches(`^[A-Za-z0-9+/]{22,256}$`, values["ice-pwd"]) {
		return fail
	}
	if !matches(`^(active|passive|actpass)$`, values["setup"]) {
		return fail
	}
	port := scopes[1]["sctp-port"]
	n, err := strconv.Atoi(port)
	if !matches(`^[0-9]+$`, port) || err != nil || n > 65535 {
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
			r.Fingerprint, _ = hex.DecodeString(strings.ReplaceAll(strings.TrimPrefix(line, "a=fingerprint:sha-256 "), ":", ""))
		}
	}
	return r, nil
}
