package agent

import (
	"regexp"
	"strings"
)

var csiRE = regexp.MustCompile("\x1b\\[[0-9;:?]*[A-Za-z~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")

// StripANSI removes SGR and other CSI sequences, and OSC sequences such as
// hyperlinks.
func StripANSI(s string) string { return csiRE.ReplaceAllString(s, "") }

// Style is the SGR state that screen analysis looks at.
type Style struct {
	Bold, Dim, Reverse bool
	// FG is the 256-color foreground set with 38;5;n, or -1.
	FG int
	// BG is the 256-color background set with 48;5;n, or -1.
	BG int
}

// Reset is the style after ESC[0m.
var Reset = Style{FG: -1, BG: -1}

// Apply updates st with one SGR sequence's parameters.
func (st *Style) Apply(params string) {
	ps := strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ':' })
	if len(ps) == 0 {
		*st = Reset // ESC[m resets
		return
	}
	for i := 0; i < len(ps); i++ {
		switch ps[i] {
		case "0":
			*st = Reset
		case "1":
			st.Bold = true
		case "2":
			st.Dim = true
		case "7":
			st.Reverse = true
		case "22":
			st.Bold, st.Dim = false, false
		case "27":
			st.Reverse = false
		case "39":
			st.FG = -1
		case "49":
			st.BG = -1
		case "38", "48", "58":
			// Extended colors carry their own arguments: 5;n or 2;r;g;b.
			if i+2 < len(ps) && ps[i+1] == "5" {
				n := atoi(ps[i+2])
				switch ps[i] {
				case "38":
					st.FG = n
				case "48":
					st.BG = n
				}
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Run is a stretch of text drawn in one style.
type Run struct {
	Style
	Text string
}

// Runs splits a line captured with escape sequences into styled text.
// Sequences other than SGR are dropped.
func Runs(line string) []Run {
	var out []Run
	st := Reset
	for line != "" {
		loc := csiRE.FindStringIndex(line)
		if loc == nil {
			out = append(out, Run{st, line})
			break
		}
		if loc[0] > 0 {
			out = append(out, Run{st, line[:loc[0]]})
		}
		if seq := line[loc[0]:loc[1]]; strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			st.Apply(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"))
		}
		line = line[loc[1]:]
	}
	return out
}
