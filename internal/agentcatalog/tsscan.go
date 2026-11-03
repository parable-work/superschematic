package agentcatalog

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// export is one top-level exported declaration of an authoring package's
// TypeScript source: its text without the body, and the comment directly
// above it.
type export struct {
	name    string
	kind    string // function, const, type, interface, class, abstract class, enum
	file    string
	line    int
	text    string
	comment string
	// overload marks a function declaration that ends in `;`: one signature
	// of an overloaded function, whose implementation follows it.
	overload bool
}

var (
	exportLine = regexp.MustCompile(`^export\s+(?:declare\s+)?(function|const|type|interface|abstract class|class|enum)\s+([A-Za-z_$][\w$]*)`)
	identifier = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
	underscore = regexp.MustCompile(`([(,]\s*)_([A-Za-z])`)
)

// signature is the declaration as the catalog prints it: without `export`
// and `declare`, and for a function without the underscore its parameter
// names carry because the body never reads them.
func (d export) signature() string {
	s := strings.TrimPrefix(d.text, "export ")
	s = strings.TrimPrefix(s, "declare ")
	if d.kind == "function" {
		s = underscore.ReplaceAllString(s, "$1$2")
	}
	return s
}

// names is every identifier the declaration's code names but its own,
// comments and string literals left out.
func (d export) names() []string {
	seen := map[string]bool{d.name: true}
	var out []string
	for _, id := range identifier.FindAllString(stripCommentsAndStrings(d.text), -1) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// readPackage reads the exports of every source file in dir of fsys, tests
// and type-check fixtures left out, in file order.
func readPackage(fsys fs.FS, dir string) ([]export, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".test.ts") || strings.HasSuffix(name, ".typecheck.ts") || strings.HasSuffix(name, ".d.ts") {
			continue
		}
		files = append(files, name)
	}
	sort.Strings(files)
	var out []export
	for _, name := range files {
		src, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return nil, err
		}
		exports, err := parseFile(name, string(src))
		if err != nil {
			return nil, err
		}
		out = append(out, exports...)
	}
	return out, nil
}

// parseFile finds each line that starts an exported declaration and scans
// from it to the declaration's end.
func parseFile(file, src string) ([]export, error) {
	lines := strings.SplitAfter(src, "\n")
	var out []export
	offset := 0
	for i, line := range lines {
		start := offset
		offset += len(line)
		m := exportLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		d := export{name: m[2], kind: m[1], file: file, line: i + 1}
		end, overload, err := declarationEnd(src, start+len(m[0]), d.kind)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %s %s: %w", file, i+1, d.kind, d.name, err)
		}
		d.text = strings.TrimRight(src[start:end], " \t\r\n=")
		d.overload = overload
		d.comment = commentAbove(lines[:i])
		out = append(out, d)
	}
	return out, nil
}

// declarationEnd scans src from pos, just past a declaration's name, to
// where its text ends: a function before its body or after its `;`, a
// const before its `=`, a type alias at its `;`, and an interface, class or
// enum after its closing brace. Brackets, string literals and comments are
// skipped as the scan nests through them. overload reports a function that
// ended in `;`.
func declarationEnd(src string, pos int, kind string) (end int, overload bool, err error) {
	paren, brace, bracket, angle := 0, 0, 0, 0
	sawParams, sawBrace := false, false
	flat := func() bool { return paren == 0 && brace == 0 && bracket == 0 && angle == 0 }
	for i := pos; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			close := strings.Index(src[i+2:], "*/")
			if close < 0 {
				return 0, false, fmt.Errorf("unterminated comment")
			}
			i += close + 3
			continue
		case c == '"' || c == '\'' || c == '`':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			i = j
			continue
		case c == '=' && i+1 < len(src) && src[i+1] == '>':
			i++
			continue
		}
		switch c {
		case '(':
			paren++
		case ')':
			paren--
			if paren == 0 {
				sawParams = true
			}
		case '[':
			bracket++
		case ']':
			bracket--
		case '<':
			angle++
		case '>':
			angle--
		case '{':
			if kind == "function" && sawParams && flat() {
				return i, false, nil
			}
			brace++
			sawBrace = true
		case '}':
			brace--
			if (kind == "interface" || kind == "enum" || strings.HasSuffix(kind, "class")) && sawBrace && flat() {
				return i + 1, false, nil
			}
		case '=':
			if kind == "const" && flat() {
				return i, false, nil
			}
		case ';':
			if flat() {
				return i, kind == "function", nil
			}
		}
	}
	return 0, false, fmt.Errorf("no end before the end of the file")
}

// commentAbove is the comment directly above a declaration, a `/** */`
// block or a run of `//` lines with no blank line between it and the
// declaration, as Markdown lines.
func commentAbove(above []string) string {
	n := len(above)
	if n == 0 {
		return ""
	}
	last := strings.TrimSpace(above[n-1])
	var raw []string
	switch {
	case strings.HasSuffix(last, "*/"):
		i := n - 1
		for i >= 0 && !strings.HasPrefix(strings.TrimSpace(above[i]), "/*") {
			i--
		}
		if i < 0 {
			return ""
		}
		for _, l := range above[i:] {
			l = strings.TrimSpace(l)
			l = strings.TrimPrefix(l, "/**")
			l = strings.TrimPrefix(l, "/*")
			l = strings.TrimSuffix(l, "*/")
			l = strings.TrimPrefix(strings.TrimSpace(l), "*")
			raw = append(raw, strings.TrimPrefix(l, " "))
		}
	case strings.HasPrefix(last, "//"):
		i := n - 1
		for i >= 0 && strings.HasPrefix(strings.TrimSpace(above[i]), "//") {
			i--
		}
		for _, l := range above[i+1:] {
			l = strings.TrimPrefix(strings.TrimSpace(l), "//")
			raw = append(raw, strings.TrimPrefix(l, " "))
		}
	default:
		return ""
	}
	return strings.TrimSpace(strings.Join(trimRight(raw), "\n"))
}

func trimRight(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(l, " \t\r")
	}
	return out
}

// stripCommentsAndStrings blanks out s's comments and string literals, so
// an identifier scan sees only code.
func stripCommentsAndStrings(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			close := strings.Index(s[i+2:], "*/")
			if close < 0 {
				return b.String()
			}
			i += close + 3
			b.WriteByte(' ')
		case c == '"' || c == '\'' || c == '`':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			i = j
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
