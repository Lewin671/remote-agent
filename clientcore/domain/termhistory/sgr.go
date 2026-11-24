// Package termhistory implements the shared terminal history paging state
// machine and SGR line parser for captured tmux history (capture-pane -e).
package termhistory

import (
	"strings"
	"unicode/utf8"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
)

type sgrStyle struct {
	fgKind    corev1.TerminalSgrColorKind
	fgVal     uint32
	bgKind    corev1.TerminalSgrColorKind
	bgVal     uint32
	bold      bool
	dim       bool
	italic    bool
	underline bool
	inverse   bool
}

// IsWideRune reports whether r occupies two terminal cells (East Asian Wide,
// Fullwidth, and wide Emoji ranges).
func IsWideRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x115f) ||
		(r >= 0x2e80 && r <= 0x303e) ||
		(r >= 0x3041 && r <= 0x33ff) ||
		(r >= 0x3400 && r <= 0x4dbf) ||
		(r >= 0x4e00 && r <= 0x9fff) ||
		(r >= 0xa000 && r <= 0xa4cf) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1faff) ||
		(r >= 0x20000 && r <= 0x3fffd)
}

// LineCells returns the terminal column width of a parsed line, expanding tabs
// to 8-column stops and counting wide runes as 2 cells.
func LineCells(runs []*corev1.TerminalHistoryRun) int32 {
	var cells int32
	for _, run := range runs {
		if run == nil {
			continue
		}
		for _, r := range run.Text {
			if r == '\t' {
				cells += 8 - cells%8
			} else if IsWideRune(r) {
				cells += 2
			} else {
				cells++
			}
		}
	}
	return cells
}

// ParsePage parses all raw lines of one history page into styled runs and
// computes per-line and page-wide maximum cell widths.
func ParsePage(id string, lines []string) *corev1.TerminalHistoryParsedPage {
	parsed := make([]*corev1.TerminalHistoryLine, len(lines))
	var maxCells int32
	for i, raw := range lines {
		runs, cells := ParseSgrLine(raw)
		if cells > maxCells {
			maxCells = cells
		}
		parsed[i] = &corev1.TerminalHistoryLine{
			Runs:  runs,
			Cells: cells,
		}
	}
	return &corev1.TerminalHistoryParsedPage{
		Id:       id,
		Lines:    parsed,
		MaxCells: maxCells,
	}
}

// ParseSgrLine parses one line of `tmux capture-pane -e` output into styled
// runs, starting from the default style. Only SGR (ESC [ ... m with decimal
// parameters) modifies style; all other escape and control sequences (except
// horizontal tab) are dropped. Adjacent runs with identical style are merged
// and empty runs are omitted.
func ParseSgrLine(line string) ([]*corev1.TerminalHistoryRun, int32) {
	if len(line) == 0 {
		return nil, 0
	}
	var runs []*corev1.TerminalHistoryRun
	var style sgrStyle
	var buf strings.Builder

	flush := func() {
		if buf.Len() == 0 {
			return
		}
		piece := buf.String()
		buf.Reset()
		if n := len(runs); n > 0 && runMatchesStyle(runs[n-1], style) {
			runs[n-1].Text += piece
			return
		}
		runs = append(runs, buildRun(piece, style))
	}

	n := len(line)
	for i := 0; i < n; {
		b := line[i]
		if b == 0x1b {
			if i+1 >= n {
				break
			}
			switch line[i+1] {
			case '[':
				j := i + 2
				aborted := false
				for j < n {
					c := line[j]
					if c == 0x1b {
						aborted = true
						break
					}
					if c >= 0x40 && c <= 0x7e {
						break
					}
					j++
				}
				if aborted {
					i = j
					continue
				}
				if j >= n {
					i = n
					break
				}
				if line[j] == 'm' {
					params := line[i+2 : j]
					if isValidSgrParams(params) {
						next := applySgr(style, params)
						if next != style {
							flush()
							style = next
						}
					}
				}
				i = j + 1
			case ']', 'P', 'X', '^', '_':
				i = skipControlString(line, i+2)
			case 0x1b:
				// Do not consume a second ESC so `ESC ESC [31m` still parses the CSI.
				i++
			default:
				if line[i+1] >= 0x20 && line[i+1] <= 0x2f {
					// ECMA-48 nF escape sequence (e.g. `ESC ( B`, `ESC # 8`):
					// intermediate bytes 0x20-0x2f followed by a final byte 0x30-0x7e.
					j := i + 1
					for j < n && line[j] >= 0x20 && line[j] <= 0x2f {
						j++
					}
					if j < n && line[j] >= 0x30 && line[j] <= 0x7e {
						j++
					}
					i = j
				} else {
					// Skip one full UTF-8 rune so multibyte characters after ESC
					// are never split into invalid UTF-8.
					_, size := utf8.DecodeRuneInString(line[i+1:])
					i += 1 + size
				}
			}
		} else if b == '\t' {
			buf.WriteByte('\t')
			i++
		} else if b < 0x20 || b == 0x7f {
			i++
		} else {
			start := i
			for i < n && line[i] >= 0x20 && line[i] != 0x7f {
				i++
			}
			buf.WriteString(line[start:i])
		}
	}
	flush()
	if len(runs) == 0 {
		return nil, 0
	}
	return runs, computeCells(runs)
}

func computeCells(runs []*corev1.TerminalHistoryRun) int32 {
	var cells int32
	for _, run := range runs {
		s := run.Text
		for i := 0; i < len(s); {
			b := s[i]
			if b < utf8.RuneSelf {
				if b == '\t' {
					cells += 8 - cells%8
				} else {
					cells++
				}
				i++
				continue
			}
			r, size := utf8.DecodeRuneInString(s[i:])
			if IsWideRune(r) {
				cells += 2
			} else {
				cells++
			}
			i += size
		}
	}
	return cells
}

// skipControlString advances past an OSC/DCS/SOS/PM/APC string terminated by
// BEL (0x07) or ST (ESC \). Another ESC not followed by '\' ends the string
// and leaves i at that ESC so the next escape sequence is parsed.
func skipControlString(line string, start int) int {
	n := len(line)
	for i := start; i < n; i++ {
		if line[i] == 0x07 {
			return i + 1
		}
		if line[i] == 0x1b {
			if i+1 < n && line[i+1] == '\\' {
				return i + 2
			}
			return i
		}
	}
	return n
}

func isValidSgrParams(params string) bool {
	for i := 0; i < len(params); i++ {
		c := params[i]
		if (c < '0' || c > '9') && c != ';' && c != ':' {
			return false
		}
	}
	return true
}

func parseSgrCodes(params string, scratch []int) []int {
	if len(params) == 0 {
		return append(scratch[:0], 0)
	}
	out := scratch[:0]
	val := 0
	hasColon := false
	for i := 0; i <= len(params); i++ {
		if i == len(params) || params[i] == ';' {
			if hasColon {
				// Colon subparameter (e.g. 4:3, 58:2::r:g:b): ignore only this parameter.
				out = append(out, -1)
			} else {
				out = append(out, val)
			}
			val = 0
			hasColon = false
			continue
		}
		if params[i] == ':' {
			hasColon = true
			continue
		}
		if !hasColon && val < 1000000 {
			val = val*10 + int(params[i]-'0')
		}
	}
	return out
}

func applySgr(style sgrStyle, params string) sgrStyle {
	var buf [16]int
	codes := parseSgrCodes(params, buf[:0])
	s := style
	for i := 0; i < len(codes); i++ {
		code := codes[i]
		switch {
		case code < 0:
			// Ignored colon subparameter token.
		case code == 0:
			s = sgrStyle{}
		case code == 1:
			s.bold = true
		case code == 2:
			s.dim = true
		case code == 3:
			s.italic = true
		case code == 4:
			s.underline = true
		case code == 7:
			s.inverse = true
		case code == 22:
			s.bold = false
			s.dim = false
		case code == 23:
			s.italic = false
		case code == 24:
			s.underline = false
		case code == 27:
			s.inverse = false
		case code == 39:
			s.fgKind = corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_DEFAULT
			s.fgVal = 0
		case code == 49:
			s.bgKind = corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_DEFAULT
			s.bgVal = 0
		case code >= 30 && code <= 37:
			s.fgKind = corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_PALETTE
			s.fgVal = uint32(code - 30)
		case code >= 90 && code <= 97:
			s.fgKind = corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_PALETTE
			s.fgVal = uint32(code - 90 + 8)
		case code >= 40 && code <= 47:
			s.bgKind = corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_PALETTE
			s.bgVal = uint32(code - 40)
		case code >= 100 && code <= 107:
			s.bgKind = corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_PALETTE
			s.bgVal = uint32(code - 100 + 8)
		case code == 38 || code == 48 || code == 58:
			if i+1 >= len(codes) {
				return s
			}
			switch codes[i+1] {
			case 5:
				if i+2 >= len(codes) {
					return s
				}
				idx := codes[i+2]
				if code != 58 && idx >= 0 && idx <= 255 {
					setStyleColor(&s, code == 38, corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_PALETTE, uint32(idx))
				}
				i += 2
			case 2:
				if i+4 >= len(codes) {
					return s
				}
				r, g, b := codes[i+2], codes[i+3], codes[i+4]
				if code != 58 && r >= 0 && r <= 255 && g >= 0 && g <= 255 && b >= 0 && b <= 255 {
					rgb := uint32(r)<<16 | uint32(g)<<8 | uint32(b)
					setStyleColor(&s, code == 38, corev1.TerminalSgrColorKind_TERMINAL_SGR_COLOR_KIND_RGB, rgb)
				}
				i += 4
			default:
				return s
			}
		case code == 59:
			// Default underline color: parsed and ignored (not rendered).
		}
	}
	return s
}

func setStyleColor(s *sgrStyle, fg bool, kind corev1.TerminalSgrColorKind, val uint32) {
	if fg {
		s.fgKind = kind
		s.fgVal = val
	} else {
		s.bgKind = kind
		s.bgVal = val
	}
}

func runMatchesStyle(r *corev1.TerminalHistoryRun, s sgrStyle) bool {
	return r.FgKind == s.fgKind &&
		r.FgValue == s.fgVal &&
		r.BgKind == s.bgKind &&
		r.BgValue == s.bgVal &&
		r.Bold == s.bold &&
		r.Dim == s.dim &&
		r.Italic == s.italic &&
		r.Underline == s.underline &&
		r.Inverse == s.inverse
}

func buildRun(text string, s sgrStyle) *corev1.TerminalHistoryRun {
	return &corev1.TerminalHistoryRun{
		Text:      text,
		FgKind:    s.fgKind,
		FgValue:   s.fgVal,
		BgKind:    s.bgKind,
		BgValue:   s.bgVal,
		Bold:      s.bold,
		Dim:       s.dim,
		Italic:    s.italic,
		Underline: s.underline,
		Inverse:   s.inverse,
	}
}
