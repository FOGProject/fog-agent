//go:build linux

package printers

import (
	"os/exec"
	"strings"
)

// The probes. Variables so the tests drive the parsers against captured
// output rather than against whatever this build machine happens to have
// installed.
var (
	// lpstat -v, not -p. -v prints the device URI beside the queue name and
	// -p does not, and the URI is the whole point of design 0010 §2 -- the
	// legacy client's `lpstat -p` could only ever learn the names.
	runLpstatV = func() (string, bool) { return run("lpstat", "-v") }
	// lpstat -d prints the system default destination.
	runLpstatD = func() (string, bool) { return run("lpstat", "-d") }
	// lpoptions -p <name> prints that queue's attributes on one line,
	// including printer-make-and-model. One call per queue, which is bounded
	// by how many printers a machine has -- single digits in practice.
	runLpoptions = func(name string) (string, bool) { return run("lpoptions", "-p", name) }
)

func run(name string, args ...string) (string, bool) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// gather asks CUPS.
//
// lpstat failing to run at all means there is no CUPS here, and that is
// (zero, false): "no collector ran". A machine with CUPS and no queues
// answers successfully with an empty list, which is a different statement
// and the one the server is allowed to act on.
func gather() (Printers, bool) {
	out, ok := runLpstatV()
	if !ok {
		return Printers{}, false
	}
	p := Printers{
		Subsystem: SubsystemCUPS,
		Installed: parseLpstatV(out),
	}
	if d, ok := runLpstatD(); ok {
		p.Default = parseLpstatD(d)
	}
	for i := range p.Installed {
		opts, ok := runLpoptions(p.Installed[i].Name)
		if !ok {
			continue
		}
		attrs := parseLpoptions(opts)
		p.Installed[i].Driver = attrs["printer-make-and-model"]
		p.Installed[i].Shared = attrs["printer-is-shared"] == "true"
	}
	return p, true
}

// parseLpstatV reads `device for NAME: URI` lines.
//
// The queue name can contain neither a space nor a colon (CUPS rejects both),
// so cutting on the first colon after the "device for " prefix is exact
// rather than a heuristic.
func parseLpstatV(out string) []Printer {
	var list []Printer
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest, found := strings.CutPrefix(line, "device for ")
		if !found {
			continue
		}
		name, uri, found := strings.Cut(rest, ":")
		if !found {
			continue
		}
		list = append(list, Printer{
			Name: strings.TrimSpace(name),
			URI:  strings.TrimSpace(uri),
		})
	}
	return list
}

// parseLpstatD reads `system default destination: NAME`, and answers empty
// for the other line CUPS prints here -- "no system default destination" --
// which is a real answer and not a parse failure.
func parseLpstatD(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest, found := strings.CutPrefix(line, "system default destination:")
		if !found {
			continue
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

// parseLpoptions splits one lpoptions line into its key=value attributes.
//
// The values are shell-quoted: printer-make-and-model='HP LaserJet 4550' has
// a space inside quotes, so splitting on whitespace would truncate the model
// name at the first word. This walks the string tracking quote state instead.
func parseLpoptions(out string) map[string]string {
	attrs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		for _, tok := range splitQuoted(line) {
			k, v, found := strings.Cut(tok, "=")
			if !found {
				continue
			}
			attrs[strings.TrimSpace(k)] = unquote(v)
		}
	}
	return attrs
}

// splitQuoted splits on whitespace that is not inside single or double
// quotes.
func splitQuoted(s string) []string {
	var (
		out    []string
		cur    strings.Builder
		quote  rune
		escape bool
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case escape:
			// CUPS backslash-escapes spaces inside some values
			// (marker-names='Canon\ Cartridge\ 067...'). The escaped
			// character is never a separator, whatever it is.
			escape = false
			cur.WriteRune(r)
		case r == '\\':
			escape = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// unquote strips one matching pair of surrounding quotes.
func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
