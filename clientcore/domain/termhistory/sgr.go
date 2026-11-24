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

// ParsePage parses all raw lines of one history page into styled, width-typed
// runs and computes the page-wide maximum cell width.
func ParsePage(id string, lines []string) *corev1.TerminalHistoryParsedPage {
	parsed := make([]*corev1.TerminalHistoryLine, len(lines))
	var maxCells int32
	for i, raw := range lines {
		runs, cells := ParseSgrLine(raw)
		if cells > maxCells {
			maxCells = cells
		}
		parsed[i] = &corev1.TerminalHistoryLine{
			Runs: runs,
		}
	}
	return &corev1.TerminalHistoryParsedPage{
		Id:       id,
		Lines:    parsed,
		MaxCells: maxCells,
	}
}

// ParseSgrLine parses one line of `tmux capture-pane -e` output into styled,
// width-typed runs, starting from the default style. Only SGR (ESC [ ... m with
// decimal/colon parameters) modifies style; all other escape and control
// sequences (except horizontal tab) are dropped. Adjacent mergeable runs with
// identical style and width kind are merged, multi-codepoint clusters stay
// indivisible, and empty runs are omitted.
func ParseSgrLine(line string) ([]*corev1.TerminalHistoryRun, int32) {
	if len(line) == 0 {
		return nil, 0
	}
	var runs []*corev1.TerminalHistoryRun
	var style sgrStyle
	var rawBuf strings.Builder

	var curStyle sgrStyle
	var curWidth corev1.TerminalRunWidth
	var runBuf strings.Builder
	var lineCells int32

	flushRun := func() {
		if runBuf.Len() == 0 {
			return
		}
		runs = append(runs, buildRun(runBuf.String(), curStyle, curWidth))
		runBuf.Reset()
	}

	emitPiece := func(piece string, st sgrStyle) {
		for i := 0; i < len(piece); {
			end, cells, multi := nextCluster(piece, i)
			var wKind corev1.TerminalRunWidth
			mergeable := true
			if cells < 0 {
				// Tab expands to the next 8-column stop and stays in NARROW runs.
				lineCells += 8 - lineCells%8
				wKind = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_NARROW
			} else {
				lineCells += int32(cells)
				switch {
				case cells == 0:
					wKind = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_ZERO
				case cells == 1 && !multi:
					wKind = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_NARROW
				case cells == 2 && !multi:
					wKind = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_WIDE
				case cells == 1 && multi:
					wKind = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_CLUSTER_NARROW
					mergeable = false
				default:
					wKind = corev1.TerminalRunWidth_TERMINAL_RUN_WIDTH_CLUSTER_WIDE
					mergeable = false
				}
			}
			cluster := piece[i:end]
			if !mergeable {
				flushRun()
				runs = append(runs, buildRun(cluster, st, wKind))
			} else {
				if runBuf.Len() > 0 && (curStyle != st || curWidth != wKind) {
					flushRun()
				}
				if runBuf.Len() == 0 {
					curStyle = st
					curWidth = wKind
				}
				runBuf.WriteString(cluster)
			}
			i = end
		}
	}

	flushRaw := func() {
		if rawBuf.Len() == 0 {
			return
		}
		emitPiece(rawBuf.String(), style)
		rawBuf.Reset()
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
							flushRaw()
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
			rawBuf.WriteByte('\t')
			i++
		} else if b < 0x20 || b == 0x7f {
			i++
		} else {
			start := i
			for i < n && line[i] >= 0x20 && line[i] != 0x7f {
				i++
			}
			rawBuf.WriteString(line[start:i])
		}
	}
	flushRaw()
	flushRun()
	if len(runs) == 0 {
		return nil, 0
	}
	return runs, lineCells
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

func buildRun(text string, s sgrStyle, width corev1.TerminalRunWidth) *corev1.TerminalHistoryRun {
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
		Width:     width,
	}
}
