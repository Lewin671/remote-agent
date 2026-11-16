package lists

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/projection"
)

const (
	// ApprovalTruncatedNote marks approval fields shortened by the relay.
	ApprovalTruncatedNote = "\n…（remote-agent：内容过长，已截断）"
	// TitleMaxCodePoints is the daemon's maximum session title length in Unicode code points.
	TitleMaxCodePoints = 60
)

// samePlace normalizes macOS /private/{tmp,var,etc} prefixes to /{tmp,var,etc}.
func samePlace(p string) string {
	const prefix = "/private"
	if !strings.HasPrefix(p, prefix) {
		return p
	}
	rest := p[len(prefix):]
	for _, root := range []string{"/tmp", "/var", "/etc"} {
		if strings.HasPrefix(rest, root) {
			after := rest[len(root):]
			if after == "" || after[0] == '/' {
				return rest
			}
		}
	}
	return p
}

func isLineSuffix(rest string) bool {
	if len(rest) < 2 || rest[0] != ':' {
		return false
	}
	i := 0
	for i < len(rest) {
		if rest[i] != ':' {
			return false
		}
		i++
		start := i
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	return true
}

func underDir(p, dir string) (string, bool) {
	if dir == "" || !strings.HasPrefix(p, dir) {
		return "", false
	}
	rest := p[len(dir):]
	if rest == "" || rest[0] == '/' || isLineSuffix(rest) {
		return rest, true
	}
	return "", false
}

func matchHomeDir(p string) string {
	for _, prefix := range []string{"/Users/", "/home/"} {
		if strings.HasPrefix(p, prefix) {
			rest := p[len(prefix):]
			end := 0
			for end < len(rest) && rest[end] != '/' && rest[end] != ':' {
				end++
			}
			if end > 0 {
				return p[:len(prefix)+end]
			}
		}
	}
	return ""
}

// ShortenPath shortens a single whole path relative to cwd (else home), leaving unknown paths unchanged.
func ShortenPath(path, cwd, home string) string {
	base := strings.TrimRight(samePlace(cwd), "/")
	h := strings.TrimRight(samePlace(home), "/")
	p := samePlace(path)
	if r, ok := underDir(p, base); ok {
		if r == "" || r == "/" {
			return "."
		}
		if r[0] == '/' {
			return r[1:]
		}
		return "." + r
	}
	hm := h
	if hm == "" {
		hm = matchHomeDir(p)
	}
	if r, ok := underDir(p, hm); ok {
		return "~" + r
	}
	return path
}

func isPathBeforeRune(r rune) bool {
	return unicode.IsSpace(r) || r == '=' || r == ':' || r == '(' || r == '[' || r == '{' || r == ','
}

// ShortenPaths shortens absolute path tokens inside a tool title using the same rules across all clients.
func ShortenPaths(text, cwd, home string) string {
	var b strings.Builder
	b.Grow(len(text))
	i := 0
	for i < len(text) {
		var before string
		j := i
		matched := false

		// Case 1: start of string without a preceding delimiter.
		if i == 0 {
			if path, end, quote, ok := scanPathToken(text, 0); ok {
				short := ShortenPath(path, cwd, home)
				if short == path {
					b.WriteString(text[0:end])
				} else if quote != 0 {
					b.WriteByte(quote)
					b.WriteString(short)
					b.WriteByte(quote)
				} else {
					b.WriteString(short)
				}
				i = end
				matched = true
			}
		}

		// Case 2: delimiter rune followed by a path token.
		if !matched {
			r, size := utf8.DecodeRuneInString(text[i:])
			if isPathBeforeRune(r) {
				before = text[i : i+size]
				j = i + size
				if path, end, quote, ok := scanPathToken(text, j); ok {
					short := ShortenPath(path, cwd, home)
					if short == path {
						b.WriteString(text[i:end])
					} else if quote != 0 {
						b.WriteString(before)
						b.WriteByte(quote)
						b.WriteString(short)
						b.WriteByte(quote)
					} else {
						b.WriteString(before)
						b.WriteString(short)
					}
					i = end
					matched = true
				}
			}
			if !matched {
				b.WriteString(text[i : i+size])
				i += size
			}
		}
	}
	return b.String()
}

func scanPathToken(text string, j int) (path string, end int, quote byte, ok bool) {
	if j >= len(text) {
		return "", 0, 0, false
	}
	if text[j] == '"' || text[j] == '\'' {
		q := text[j]
		if j+1 >= len(text) || text[j+1] != '/' {
			return "", 0, 0, false
		}
		k := j + 2
		for k < len(text) && text[k] != '"' && text[k] != '\'' && text[k] != '\n' {
			k++
		}
		if k < len(text) && text[k] == q {
			return text[j+1 : k], k + 1, q, true
		}
		return "", 0, 0, false
	}
	if text[j] != '/' {
		return "", 0, 0, false
	}
	k := j + 1
	for k < len(text) {
		r, size := utf8.DecodeRuneInString(text[k:])
		if unicode.IsSpace(r) || r == '"' || r == '\'' || r == '`' {
			break
		}
		k += size
	}
	return text[j:k], k, 0, true
}

// ShortPath replaces a leading /Users/<name> or /home/<name> folder with ~.
func ShortPath(p string) string {
	for _, prefix := range []string{"/Users/", "/home/"} {
		if strings.HasPrefix(p, prefix) {
			rest := p[len(prefix):]
			slash := strings.IndexByte(rest, '/')
			if slash == -1 {
				if len(rest) > 0 {
					return "~"
				}
			} else if slash > 0 {
				return "~" + rest[slash:]
			}
		}
	}
	return p
}

// PathCrumbs splits a path into breadcrumb pieces for the folder picker.
func PathCrumbs(path string) []string {
	rawParts := strings.Split(ShortPath(path), "/")
	parts := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) > 0 && parts[0] == "~" {
		return parts
	}
	if len(parts) == 0 {
		return []string{"/"}
	}
	parts[0] = "/" + parts[0]
	return parts
}

// TailPath abbreviates a path to fit within max characters by keeping its trailing segments.
func TailPath(p string, max int) string {
	if max <= 0 {
		max = 28
	}
	s := ShortPath(p)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	parts := strings.Split(s, "/")
	out := parts[len(parts)-1]
	parts = parts[:len(parts)-1]
	for len(parts) > 0 {
		next := parts[len(parts)-1]
		if utf8.RuneCountInString(out)+utf8.RuneCountInString(next)+1 > max-2 {
			break
		}
		out = next + "/" + out
		parts = parts[:len(parts)-1]
	}
	return "…/" + out
}

// ChildPath appends name to dir without duplicate slashes.
func ChildPath(dir, name string) string {
	base := strings.TrimRight(dir, "/")
	return base + "/" + name
}

// --- Markdown preview flattening ---

func splitUnescapedPipes(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '|' && (i == 0 || s[i-1] != '\\') {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func tableCells(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	if strings.HasSuffix(t, "|") && (len(t) < 2 || t[len(t)-2] != '\\') {
		t = t[:len(t)-1]
	}
	parts := splitUnescapedPipes(t)
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func isTableSeparatorCell(c string) bool {
	if len(c) < 3 {
		return false
	}
	if c[0] == ':' {
		c = c[1:]
	}
	if len(c) > 0 && c[len(c)-1] == ':' {
		c = c[:len(c)-1]
	}
	if len(c) < 3 {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] != '-' {
			return false
		}
	}
	return true
}

func isTableSeparatorLine(line string) bool {
	if !strings.Contains(line, "|") {
		return false
	}
	for _, c := range tableCells(line) {
		if !isTableSeparatorCell(c) {
			return false
		}
	}
	return true
}

func hasTrailingUnescapedPipe(line string) bool {
	t := strings.TrimRightFunc(line, unicode.IsSpace)
	if !strings.HasSuffix(t, "|") {
		return false
	}
	return len(t) < 2 || t[len(t)-2] != '\\'
}

// PreviewTableText flattens Markdown table rows into separator-joined cells and drops alignment rows.
func PreviewTableText(text, listSeparator string) string {
	if listSeparator == "" {
		listSeparator = "、"
	}
	lines := strings.Split(text, "\n")
	out := make([]string, len(lines))
	inTable := false
	for i, line := range lines {
		if isTableSeparatorLine(line) {
			inTable = true
			out[i] = ""
			continue
		}
		nextSep := i+1 < len(lines) && isTableSeparatorLine(lines[i+1])
		startsPipe := strings.HasPrefix(strings.TrimLeftFunc(line, unicode.IsSpace), "|")
		if strings.Contains(line, "|") && (startsPipe || hasTrailingUnescapedPipe(line) || inTable || nextSep) {
			cells := tableCells(line)
			kept := make([]string, 0, len(cells))
			for _, c := range cells {
				if c != "" {
					kept = append(kept, strings.ReplaceAll(c, "\\|", "|"))
				}
			}
			out[i] = strings.Join(kept, listSeparator)
		} else {
			inTable = false
			out[i] = line
		}
	}
	return strings.Join(out, "\n")
}

func stripImagePlaceholders(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	const prefix = "[Image #"
	for i < len(s) {
		if strings.HasPrefix(s[i:], prefix) {
			j := i + len(prefix)
			digitsStart := j
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j > digitsStart && j < len(s) && s[j] == ']' {
				j++
				for j < len(s) {
					r, sz := utf8.DecodeRuneInString(s[j:])
					if !unicode.IsSpace(r) {
						break
					}
					j += sz
				}
				i = j
				continue
			}
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += sz
	}
	return b.String()
}

func stripCodeFences(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], "```") {
			j := i + 3
			for j < len(s) && ((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func stripInlinePair(s, delim string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], delim) {
			end := strings.Index(s[i+len(delim):], delim)
			if end != -1 {
				inner := s[i+len(delim) : i+len(delim)+end]
				if delim == "`" || len(inner) > 0 {
					b.WriteString(inner)
					i = i + len(delim) + end + len(delim)
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func stripMarkdownLinks(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '[' {
			closeBracket := strings.IndexByte(s[i+1:], ']')
			if closeBracket > 0 {
				labelEnd := i + 1 + closeBracket
				if labelEnd+1 < len(s) && s[labelEnd+1] == '(' {
					closeParen := strings.IndexByte(s[labelEnd+2:], ')')
					if closeParen != -1 {
						b.WriteString(s[i+1 : labelEnd])
						i = labelEnd + 2 + closeParen + 1
						continue
					}
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func stripLineMarkdownPrefix(line string) string {
	// Up to 3 whitespace chars before heading/list/blockquote markers.
	i := 0
	spaces := 0
	for i < len(line) && spaces < 3 {
		r, sz := utf8.DecodeRuneInString(line[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += sz
		spaces++
	}
	if i >= len(line) {
		return line
	}
	rest := line[i:]
	// Heading: #{1,6}\s+
	if rest[0] == '#' {
		h := 0
		for h < len(rest) && h < 6 && rest[h] == '#' {
			h++
		}
		if h >= 1 && h <= 6 && h < len(rest) {
			r, sz := utf8.DecodeRuneInString(rest[h:])
			if unicode.IsSpace(r) {
				k := h + sz
				for k < len(rest) {
					r2, sz2 := utf8.DecodeRuneInString(rest[k:])
					if !unicode.IsSpace(r2) {
						break
					}
					k += sz2
				}
				return rest[k:]
			}
		}
	}
	// Bullet: [-*+]\s+
	if (rest[0] == '-' || rest[0] == '*' || rest[0] == '+') && len(rest) > 1 {
		r, sz := utf8.DecodeRuneInString(rest[1:])
		if unicode.IsSpace(r) {
			k := 1 + sz
			for k < len(rest) {
				r2, sz2 := utf8.DecodeRuneInString(rest[k:])
				if !unicode.IsSpace(r2) {
					break
				}
				k += sz2
			}
			return rest[k:]
		}
	}
	// Blockquote: >\s?
	if rest[0] == '>' {
		if len(rest) > 1 {
			r, sz := utf8.DecodeRuneInString(rest[1:])
			if unicode.IsSpace(r) {
				return rest[1+sz:]
			}
		}
		return rest[1:]
	}
	return line
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Plain converts Markdown text into a single-line list preview.
func Plain(text string, listSeparator ...string) string {
	sep := "、"
	if len(listSeparator) > 0 && listSeparator[0] != "" {
		sep = listSeparator[0]
	}
	s := PreviewTableText(text, sep)
	s = stripImagePlaceholders(s)
	s = stripCodeFences(s)
	s = stripInlinePair(s, "`")
	s = stripInlinePair(s, "**")
	s = stripMarkdownLinks(s)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = stripLineMarkdownPrefix(lines[i])
	}
	return collapseWhitespace(strings.Join(lines, "\n"))
}

// --- Approval / Skeleton / Title display helpers ---

// ApprovalDisplay escapes control and bidi characters in approval text without altering the payload.
func ApprovalDisplay(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if (r < 32 && r != '\n' && r != '\t') ||
			(r >= 127 && r <= 159) ||
			r == 0x061c ||
			(r >= 0x200e && r <= 0x200f) ||
			(r >= 0x202a && r <= 0x202e) ||
			(r >= 0x2066 && r <= 0x2069) {
			fmt.Fprintf(&b, "\\u%04x", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ApprovalBody returns the escaped command or tool_input_json for an approval request.
func ApprovalBody(a *v1.ApprovalRequested) string {
	if a == nil {
		return ""
	}
	if a.Command != "" {
		return ApprovalDisplay(a.Command)
	}
	return ApprovalDisplay(a.ToolInputJson)
}

// ApprovalTruncated reports whether any approval field carries the relay truncation suffix.
func ApprovalTruncated(a *v1.ApprovalRequested) bool {
	if a == nil {
		return false
	}
	return strings.HasSuffix(a.Description, ApprovalTruncatedNote) ||
		strings.HasSuffix(a.Command, ApprovalTruncatedNote) ||
		strings.HasSuffix(a.ToolInputJson, ApprovalTruncatedNote)
}

// IsApprovalSkeleton reports whether any field on an ApprovalRequested carries the skeleton marker.
func IsApprovalSkeleton(a *v1.ApprovalRequested) bool {
	if a == nil {
		return false
	}
	return projection.IsSkeletonText(a.Description) ||
		projection.IsSkeletonText(a.ApprovalId) ||
		projection.IsSkeletonText(a.Command) ||
		projection.IsSkeletonText(a.ToolInputJson) ||
		projection.IsSkeletonText(a.InputDigest)
}

// ParseApprovalAlways classifies native CLI "always allow" wording into structured kind + argument.
func ParseApprovalAlways(message string) (ApprovalAlwaysKind, string) {
	if strings.ContainsRune(message, '\n') || strings.ContainsRune(message, '\r') {
		if strings.Contains(strings.ToLower(message), "accept edits") {
			return AlwaysAutoEdits, ""
		}
		return AlwaysNone, ""
	}
	trimmed := strings.TrimPrefix(message, "Yes, and ")
	const cmdPrefix = "don't ask again for commands that start with "
	if strings.HasPrefix(trimmed, cmdPrefix) {
		arg := strings.TrimPrefix(trimmed, cmdPrefix)
		arg = strings.Trim(arg, "`")
		if arg != "" {
			return AlwaysPrefix, arg
		}
	}
	const dirPrefix = "always allow access to "
	const dirSuffix = " from this project"
	if strings.HasPrefix(trimmed, dirPrefix) && strings.HasSuffix(trimmed, dirSuffix) {
		arg := trimmed[len(dirPrefix) : len(trimmed)-len(dirSuffix)]
		if arg != "" {
			return AlwaysDirectory, arg
		}
	}
	if strings.Contains(strings.ToLower(message), "accept edits") {
		return AlwaysAutoEdits, ""
	}
	return AlwaysNone, ""
}

// OptionLabel splits a trailing "(Recommended)" or "(推荐)" marker off a question option label.
func OptionLabel(label string) (string, bool) {
	trimmed := strings.TrimRightFunc(label, unicode.IsSpace)
	for _, suffix := range []string{"(recommended)", "(推荐)"} {
		if len(trimmed) >= len(suffix) && strings.EqualFold(trimmed[len(trimmed)-len(suffix):], suffix) {
			head := strings.TrimRightFunc(trimmed[:len(trimmed)-len(suffix)], unicode.IsSpace)
			return head, true
		}
	}
	return label, false
}

// TitleInput normalizes a rename input to a single line and clamps it to TitleMaxCodePoints.
func TitleInput(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	count := 0
	inBreak := false
	for _, r := range text {
		if r == '\r' || r == '\n' {
			if !inBreak {
				if count >= TitleMaxCodePoints {
					break
				}
				b.WriteByte(' ')
				count++
				inBreak = true
			}
			continue
		}
		inBreak = false
		if count >= TitleMaxCodePoints {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

// MinutesLeft computes remaining ceiling minutes until deadlineMs, clamped at 0.
func MinutesLeft(deadlineMs, nowMs int64) int64 {
	if deadlineMs <= nowMs {
		return 0
	}
	return (deadlineMs - nowMs + 59_999) / 60_000
}

