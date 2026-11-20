package peerwire

import (
	"bytes"
	"encoding/hex"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hand-written SDP checks must accept and reject exactly what the regexp
// version did. Three layers, all deterministic:
//  1. each matcher against its original pattern over a boundary-heavy
//     generated set of strings;
//  2. every frozen SDP vector plus seeded byte and line mutations of it,
//     comparing ValidateSDP and InspectSDP with the regexp oracle;
//  3. hand-picked edge cases at each pattern boundary.

// Characters that sit on a boundary of some pattern, plus multi-byte runes.
var sdpAlphabet = []string{"0", "1", "9", "a", "f", "g", "z", "A", "F", "G", "Z", "+", "/", ":", " ", "=", "-", "_", "\r", "\n", "\r\n", "\t", "\x00", "\v", "m", "a=", "é", "\u2028", "\u0085", "\xff"}

func TestSDPMatchersAgreeWithPatterns(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	random := func(max int) string {
		var b strings.Builder
		for n := r.IntN(max + 1); n > 0; n-- {
			b.WriteString(sdpAlphabet[r.IntN(len(sdpAlphabet))])
		}
		return b.String()
	}
	// Each valid seed is mutated at every position with every alphabet entry,
	// truncated, and extended, and random strings are mixed in.
	variants := func(seed string, randomMax int) []string {
		out := []string{seed, "", seed + seed}
		for i := 0; i <= len(seed); i++ {
			out = append(out, seed[:i], seed[i:])
			for _, c := range sdpAlphabet {
				out = append(out, seed[:i]+c+seed[i:])
				if i < len(seed) {
					out = append(out, seed[:i]+c+seed[i+1:])
				}
			}
		}
		for i := 0; i < 3000; i++ {
			out = append(out, random(randomMax))
		}
		return out
	}
	fp := "sha-256 " + strings.TrimSuffix(strings.Repeat("aB:", 32), ":")
	cases := []struct {
		pattern string
		match   func(string) bool
		seeds   []string
		random  int
	}{
		{`^m=application [1-9][0-9]* UDP/DTLS/SCTP webrtc-datachannel$`, mediaLine, []string{"m=application 9 UDP/DTLS/SCTP webrtc-datachannel", "m=application 65535 UDP/DTLS/SCTP webrtc-datachannel"}, 8},
		{`^[vostcm]=`, typedLine, []string{"v=0", "o=-", "s=", "t=0 0", "c=IN", "m=x", "b=AS"}, 4},
		{`^sha-256 ([0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}$`, fingerprintValue, []string{fp, strings.ToLower(fp), strings.ToUpper(fp)}, 6},
		{`^[A-Za-z0-9+/]{4,256}$`, func(v string) bool { return iceToken(v, 4) }, []string{"abcd", "ab+/", strings.Repeat("Z", 256), strings.Repeat("9", 255)}, 8},
		{`^[A-Za-z0-9+/]{22,256}$`, func(v string) bool { return iceToken(v, 22) }, []string{strings.Repeat("p", 22), strings.Repeat("/", 23), strings.Repeat("Q", 256)}, 30},
		{`^[0-9]+$`, func(v string) bool { return v != "" && all(v, isDigit) }, []string{"0", "5000", "65535", "65536"}, 6},
	}
	n := 0
	for _, c := range cases {
		for _, seed := range c.seeds {
			for _, v := range variants(seed, c.random) {
				if c.match(v) != matches(c.pattern, v) {
					t.Fatalf("%s disagrees on %q", c.pattern, v)
				}
				n++
			}
		}
	}
	for _, v := range []string{"active", "passive", "actpass", "Active", "activ", "active ", " active", "actpassive", ""} {
		got := v == "active" || v == "passive" || v == "actpass"
		if got != matches(`^(active|passive|actpass)$`, v) {
			t.Fatalf("setup disagrees on %q", v)
		}
		n++
	}
	t.Logf("%d matcher inputs agree", n)
}

func TestSDPWithoutRegexpAgrees(t *testing.T) {
	files, err := filepath.Glob("../../testdata/transport/sdp/*.*")
	if err != nil {
		t.Fatal(err)
	}
	var corpus [][]byte
	for _, f := range files {
		if strings.HasSuffix(f, ".sdp") || strings.HasSuffix(f, ".bin") {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			corpus = append(corpus, raw)
		}
	}
	if len(corpus) < 40 {
		t.Fatalf("only %d SDP vectors", len(corpus))
	}
	corpus = append(corpus, sdpEdgeCases(corpus)...)
	r := rand.New(rand.NewPCG(1, 2))
	n, accepted := 0, 0
	check := func(b []byte) {
		want := regexpValidateSDP(b) == nil
		if got := ValidateSDP(b) == nil; got != want {
			t.Fatalf("accept=%v, regexp accept=%v for %q", got, want, b)
		}
		if want {
			accepted++
			got, _ := InspectSDP(b)
			old, _ := regexpInspectSDP(b)
			if !bytes.Equal(got.Ufrag, old.Ufrag) || !bytes.Equal(got.Mid, old.Mid) || !bytes.Equal(got.Fingerprint, old.Fingerprint) || len(got.Fingerprint) != 32 {
				t.Fatalf("InspectSDP differs for %q: %s vs %s", b, hex.EncodeToString(got.Fingerprint), hex.EncodeToString(old.Fingerprint))
			}
		}
		n++
	}
	for _, raw := range corpus {
		check(raw)
		if len(raw) > 8192 {
			continue // the oversize and long-candidate vectors fail on size alone
		}
		for i := 0; i < 3000; i++ {
			b := append([]byte(nil), raw...)
			for k := 0; k <= i%3; k++ {
				at := r.IntN(len(b))
				c := sdpAlphabet[r.IntN(len(sdpAlphabet))]
				switch r.IntN(3) {
				case 0: // replace
					b = append(b[:at:at], append([]byte(c), b[at+1:]...)...)
				case 1: // insert
					b = append(b[:at:at], append([]byte(c), b[at:]...)...)
				default: // delete
					b = append(b[:at:at], b[at+1:]...)
				}
			}
			check(b)
		}
		lines := strings.SplitAfter(string(raw), "\r\n")
		for i := 0; i < 600; i++ {
			l := append([]string(nil), lines...)
			a, z := r.IntN(len(l)), r.IntN(len(l))
			switch r.IntN(4) {
			case 0:
				l = append(l[:a:a], l[a+1:]...)
			case 1:
				l = append(l[:a:a], append([]string{l[z]}, l[a:]...)...)
			case 2:
				l[a], l[z] = l[z], l[a]
			default:
				l[a] = strings.ToUpper(l[a])
			}
			check([]byte(strings.Join(l, "")))
		}
	}
	if accepted < 1000 {
		t.Fatalf("only %d accepted inputs; mutations no longer exercise acceptance", accepted)
	}
	t.Logf("%d inputs agree (%d accepted)", n, accepted)
}

// sdpEdgeCases rewrites one accepted vector at each pattern boundary.
func sdpEdgeCases(corpus [][]byte) [][]byte {
	var base string
	for _, raw := range corpus {
		if regexpValidateSDP(raw) == nil && strings.Contains(string(raw), "a=ice-pwd:") {
			base = string(raw)
			break
		}
	}
	attr := func(name string) string {
		for _, l := range strings.Split(base, "\r\n") {
			if strings.HasPrefix(l, "a="+name+":") {
				return l
			}
		}
		return ""
	}
	set := func(name, value string) []byte {
		return []byte(strings.Replace(base, attr(name), "a="+name+":"+value, 1))
	}
	fp := strings.TrimPrefix(attr("fingerprint"), "a=fingerprint:")
	var out [][]byte
	for _, v := range []string{"abc", "abcd", "ab+/", "ab-d", strings.Repeat("u", 256), strings.Repeat("u", 257), "abcé"} {
		out = append(out, set("ice-ufrag", v))
	}
	for _, v := range []string{strings.Repeat("p", 21), strings.Repeat("p", 22), strings.Repeat("p", 256), strings.Repeat("p", 257), strings.Repeat("p", 21) + "="} {
		out = append(out, set("ice-pwd", v))
	}
	for _, v := range []string{"active", "passive", "actpass", "holdconn", "Active", "actpass "} {
		out = append(out, set("setup", v))
	}
	for _, v := range []string{"0", "65535", "65536", "", "+1", "1 ", "99999999999999999999", "\u0663"} {
		out = append(out, set("sctp-port", v))
	}
	for _, v := range []string{fp, strings.ToLower(fp), strings.ToUpper(fp), fp + ":", strings.Replace(fp, ":", "-", 1), "SHA-256" + strings.TrimPrefix(fp, "sha-256"), fp[:len(fp)-3]} {
		out = append(out, set("fingerprint", v))
	}
	media := ""
	for _, l := range strings.Split(base, "\r\n") {
		if strings.HasPrefix(l, "m=") {
			media = l
		}
	}
	for _, v := range []string{"m=application 0 UDP/DTLS/SCTP webrtc-datachannel", "m=application 09 UDP/DTLS/SCTP webrtc-datachannel", "m=application 1 UDP/DTLS/SCTP webrtc-datachannel", "m=application  9 UDP/DTLS/SCTP webrtc-datachannel", "m=application 9 UDP/DTLS/SCTP webrtc-datachannel ", "m=application 9 UDP/DTLS/SCTP  webrtc-datachannel"} {
		out = append(out, []byte(strings.Replace(base, media, v, 1)))
	}
	return out
}
