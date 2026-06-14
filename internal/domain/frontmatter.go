package domain

import "strings"

// ParseFrontMatter extracts a leading YAML-style front-matter block — a `---`
// fence, `key: value` lines, then a closing `---` fence — into Metadata, and
// returns the document body with that block removed. It is a deliberately small
// parser, not full YAML: each line is a single `key: value` scalar; a value
// wrapped in matching quotes is unquoted, and a `[a, b, c]` bracket list is
// flattened to a comma-joined string (so tag membership via the ~ operator
// works). Blank lines, `#` comments, and lines without a colon are ignored.
//
// If text does not begin with a `---` fence, or the block is never closed, the
// text is returned unchanged with nil metadata — front matter is opt-in, never
// guessed. An empty block strips cleanly and yields nil metadata.
func ParseFrontMatter(text string) (Metadata, string) {
	const fence = "---"
	rest, ok := cutFenceLine(text, fence)
	if !ok {
		return nil, text
	}

	md := Metadata{}
	for {
		line, after, hasNL := strings.Cut(rest, "\n")
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) == fence {
			// Closing fence: the body is everything after it.
			if len(md) == 0 {
				md = nil
			}
			return md, after
		}
		if !hasNL {
			// Reached EOF without a closing fence: not front matter.
			return nil, text
		}
		if k, v, ok := parseFrontMatterLine(trimmed); ok {
			md[k] = v
		}
		rest = after
	}
}

// cutFenceLine reports whether text begins with the fence as its own first line
// (allowing a CRLF), returning the remainder after that line.
func cutFenceLine(text, fence string) (string, bool) {
	line, rest, ok := strings.Cut(text, "\n")
	if !ok {
		return "", false
	}
	if strings.TrimRight(line, "\r") != fence {
		return "", false
	}
	return rest, true
}

// parseFrontMatterLine parses one `key: value` line, reporting whether it held a
// non-empty key. Blank lines, comments, and colon-less lines yield ok=false.
func parseFrontMatterLine(line string) (string, string, bool) {
	if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
		return "", "", false
	}
	key, value, ok := strings.Cut(line, ":")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return "", "", false
	}
	return key, normalizeFrontMatterValue(strings.TrimSpace(value)), true
}

// normalizeFrontMatterValue unquotes a quoted scalar and flattens a bracket list
// to a comma-joined value; other values are returned trimmed.
func normalizeFrontMatterValue(v string) string {
	if len(v) >= 2 && strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		parts := strings.Split(v[1:len(v)-1], ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(unquote(p)); p != "" {
				out = append(out, p)
			}
		}
		return strings.Join(out, ",")
	}
	return unquote(v)
}

// unquote strips a single pair of matching surrounding quotes (single or double).
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
