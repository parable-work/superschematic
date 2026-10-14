package servergen

import (
	"path"
	"regexp"
	"strings"
)

// The patterns a Rust implementation's mirror leaves files out by (D65;
// mirror.go): the lines of the crate's .gitignore files, and its
// [package] include and exclude, which Cargo reads in the same syntax,
// git's. A pattern holding a slash, other than a trailing one, is
// anchored to the directory of the file it is read from (the crate's for
// include and exclude); one without matches a name at any depth below it.
// `*` and `?` match within a name, `**` across names, a trailing slash
// matches directories alone, and `!` takes a match back. The last
// pattern that matches a path decides it, and a directory left out
// leaves out everything under it, which no pattern takes back.

// ignoreRule is one pattern.
type ignoreRule struct {
	// base is the slash-separated directory the pattern is read from,
	// relative to the crate, "" for the crate's own.
	base string

	negate  bool
	dirOnly bool
	re      *regexp.Regexp
}

// parseIgnore reads the patterns of lines, a .gitignore's or a manifest
// list's, as read from the directory base. A line it cannot read as a
// pattern is skipped, as git skips it.
func parseIgnore(base string, lines []string) []ignoreRule {
	var rules []ignoreRule
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Trailing spaces go, unless a backslash escapes the last.
		for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
			line = line[:len(line)-1]
		}
		r := ignoreRule{base: base}
		switch {
		case strings.HasPrefix(line, "!"):
			r.negate, line = true, line[1:]
		case strings.HasPrefix(line, `\!`), strings.HasPrefix(line, `\#`):
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimSuffix(line, "/")
		}
		if line == "" {
			continue
		}
		re, err := regexp.Compile(globPattern(line))
		if err != nil {
			continue
		}
		r.re = re
		rules = append(rules, r)
	}
	return rules
}

// globPattern is the regular expression of a pattern, without its
// trailing slash, over a path relative to the pattern's directory.
func globPattern(glob string) string {
	anchored := strings.Contains(glob, "/")
	glob = strings.TrimPrefix(glob, "/")
	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(glob); {
		atName := i == 0 || glob[i-1] == '/'
		switch {
		case atName && strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case atName && glob[i:] == "**":
			b.WriteString(".*")
			i += 2
		case glob[i] == '*':
			b.WriteString("[^/]*")
			i++
		case glob[i] == '?':
			b.WriteString("[^/]")
			i++
		case glob[i] == '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end <= 0 {
				b.WriteString(`\[`)
				i++
				continue
			}
			class := glob[i+1 : i+1+end]
			b.WriteString("[")
			if strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^") {
				b.WriteString("^/")
				class = class[1:]
			}
			b.WriteString(strings.NewReplacer(`\`, `\\`, "[", `\[`).Replace(class))
			b.WriteString("]")
			i += end + 2
		case glob[i] == '\\' && i+1 < len(glob):
			b.WriteString(regexp.QuoteMeta(glob[i+1 : i+2]))
			i += 2
		default:
			b.WriteString(regexp.QuoteMeta(glob[i : i+1]))
			i++
		}
	}
	b.WriteString("$")
	return b.String()
}

// matches reports whether r matches p, a slash-separated path relative
// to the crate, which is a directory when dir is set.
func (r ignoreRule) matches(p string, dir bool) bool {
	if r.dirOnly && !dir {
		return false
	}
	if r.base != "" {
		rest, ok := strings.CutPrefix(p, r.base+"/")
		if !ok {
			return false
		}
		p = rest
	}
	return r.re.MatchString(p)
}

// decide is what the last of rules that matches p says: whether one
// matches, and whether it leaves p out (or, for include, takes it in)
// rather than taking it back.
func decide(rules []ignoreRule, p string, dir bool) (matched, set bool) {
	for i := len(rules) - 1; i >= 0; i-- {
		if rules[i].matches(p, dir) {
			return true, !rules[i].negate
		}
	}
	return false, false
}

// ignored reports whether rules leave p out.
func ignored(rules []ignoreRule, p string, dir bool) bool {
	_, set := decide(rules, p, dir)
	return set
}

// includedBy reports whether [package] include's rules take in the file
// p: what the last pattern matching p says, or else the nearest directory
// above it that one matches, as Cargo reads the list.
func includedBy(rules []ignoreRule, p string) bool {
	for q, dir := p, false; q != "." && q != "/" && q != ""; q, dir = path.Dir(q), true {
		if matched, set := decide(rules, q, dir); matched {
			return set
		}
	}
	return false
}
