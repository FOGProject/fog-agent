// Package procs holds what every provider that runs a program needs and
// none should own: a bounded output tail, a clip that keeps whole lines,
// a shell-style argument splitter, and the per-OS process-group handling
// that makes a timeout kill the children too.
package procs

import "strings"

// Tail keeps the last max bytes written to it.
type Tail struct {
	buf []byte
	max int
}

// NewTail returns a Tail keeping max bytes.
func NewTail(max int) *Tail {
	return &Tail{max: max}
}

// Write implements io.Writer.
func (t *Tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

// String is the kept output, trimmed.
func (t *Tail) String() string {
	return strings.TrimSpace(string(t.buf))
}

// Clip keeps the last max bytes of s, whole lines where it can, marking a
// cut with an ellipsis.
func Clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[len(s)-max:]
		if i := strings.IndexByte(s, '\n'); i >= 0 && i < max/4 {
			s = s[i+1:]
		}
		s = "…" + s
	}
	return s
}

// SplitArgs splits an argument string the way a shell reads a command
// line: on whitespace, honoring single and double quotes and a backslash
// inside double quotes or bare text. FOG stores arguments as one string,
// the same string the legacy client handed the OS.
func SplitArgs(s string) []string {
	var (
		args  []string
		cur   strings.Builder
		open  bool
		quote rune
		esc   bool
	)
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && quote != '\'':
			esc = true
			open = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			open = true
		case r == ' ' || r == '\t' || r == '\n':
			if open {
				args = append(args, cur.String())
				cur.Reset()
				open = false
			}
		default:
			cur.WriteRune(r)
			open = true
		}
	}
	if open {
		args = append(args, cur.String())
	}
	return args
}
