// Package patterndialect rewrites a schema pattern, which is RE2 syntax as
// Go's regexp reads it, so JavaScript's RegExp (with the u flag) and
// Python's re match what RE2 matches. The generated TypeScript and Python
// validators and SDKs hold the rewritten pattern. The TypeScript and
// Python schema runtimes rewrite with a port of this file
// (runtime/schema/typescript/src/runtime/validation/pattern.ts,
// runtime/schema/python/superschematic_schema_runtime/validation/pattern.py),
// and the Rust runtime with its own (runtime/schema/rust/src/pattern.rs).
//
// What RE2 means, and what each target needs written for it:
//
//   - \d, \w, \s, \b and their negations are ASCII, and \s is [\t\n\f\r ]
//     (no \v). JavaScript with the u flag reads \d, \w and \b so already;
//     its \s is Unicode. Python's are Unicode: every one is rewritten.
//   - "." is any code point but \n, and any at all under (?s).
//     JavaScript's also refuses \r, U+2028 and U+2029.
//   - ^ and $ are the text's ends, and a line's under (?m). Python's $
//     also matches before a final \n; JavaScript's (?m) lines also end at
//     \r, U+2028 and U+2029. \A and \z are the text's ends: JavaScript has
//     neither, and Python spells \z as \Z.
//   - (?m), (?s) and (?U) are applied by rewriting what they change, so
//     neither target sees them. (?i) becomes a scoped (?i:...) group, or
//     JavaScript's i flag when the pattern opens with it.
//   - \pL, \p{Lu}, \p{Greek}: JavaScript spells them \p{L} and
//     \p{Script=Greek}; Python has none, so they become code point ranges.
//   - [[:alpha:]] and the other ASCII classes: neither target has them.
//   - \Q...\E, \x{10FFFF}, octal \123, \a, an escaped punctuation mark and
//     a { that opens no repetition are literals. JavaScript's u flag
//     refuses \_, \- outside a class and a lone { or }.
//
// A pattern RE2 refuses is not made valid: the build refuses it first.
package patterndialect

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type target int

const (
	javaScript target = iota
	python
)

// JavaScript returns the source and the flags of a RegExp that matches what
// RE2 matches for pattern. The flags are "u", or "iu" when the pattern
// opens with (?i).
func JavaScript(pattern string) (source, flags string) {
	tr := translator{target: javaScript, src: []rune(pattern)}
	source = tr.run()
	if tr.caseless {
		return source, "iu"
	}
	return source, "u"
}

// Python returns a pattern that re.compile, with no flags, reads as RE2
// reads pattern. Every character outside printable ASCII is an escape, so
// the pattern can sit in a raw string literal.
func Python(pattern string) string {
	tr := translator{target: python, src: []rune(pattern)}
	return tr.run()
}

type flagSet struct{ i, m, s, ungreedy bool }

// frame is one open group. baseI is the case flag the output holds at the
// group's own level; mods counts the (?i:...) groups opened inside it since,
// which close with it or at its next |.
type frame struct {
	flags flagSet
	baseI bool
	mods  int
}

type translator struct {
	target   target
	src      []rune
	pos      int
	out      strings.Builder
	stack    []frame
	caseless bool
}

func (t *translator) run() string {
	t.stack = []frame{{}}
	// A pattern that opens with (?i) is caseless throughout: JavaScript
	// takes the i flag, Python the global flag, which it reads only first.
	if f, _, closer, ok := t.parseFlags(2, flagSet{}); ok && closer == ')' && f.i && strings.HasPrefix(string(t.src), "(?") {
		t.caseless = true
		t.stack[0].flags.i = true
		t.stack[0].baseI = true
		if t.target == python {
			t.out.WriteString("(?i)")
		}
	}
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		t.pos++
		top := &t.stack[len(t.stack)-1]
		switch c {
		case '\\':
			t.escape()
		case '[':
			t.class()
		case '(':
			t.open()
		case ')':
			t.close()
		case '|':
			t.closeMods(top)
			t.out.WriteByte('|')
			if top.flags.i != top.baseI {
				t.openMod(top)
			}
		case '.':
			switch {
			case top.flags.s:
				t.out.WriteString(`[\s\S]`)
			case t.target == javaScript:
				t.out.WriteString(`[^\n]`)
			default:
				t.out.WriteByte('.')
			}
		case '^':
			if top.flags.m {
				t.out.WriteString(`(?<![^\n])`)
			} else {
				t.out.WriteByte('^')
			}
		case '$':
			switch {
			case top.flags.m:
				t.out.WriteString(`(?![^\n])`)
			case t.target == python:
				t.out.WriteString(`\Z`)
			default:
				t.out.WriteByte('$')
			}
		case '*', '+', '?':
			t.out.WriteRune(c)
			t.quantifierSuffix(top)
		case '{':
			if n := repetition(t.src[t.pos:]); n > 0 {
				t.out.WriteByte('{')
				t.out.WriteString(string(t.src[t.pos : t.pos+n]))
				t.pos += n
				t.quantifierSuffix(top)
			} else {
				t.out.WriteString(`\{`)
			}
		default:
			t.literal(c)
		}
	}
	t.closeMods(&t.stack[0])
	return t.out.String()
}

// quantifierSuffix copies a quantifier's ? that makes it lazy, swapped
// under (?U).
func (t *translator) quantifierSuffix(top *frame) {
	lazy := t.pos < len(t.src) && t.src[t.pos] == '?'
	if lazy {
		t.pos++
	}
	if lazy != top.flags.ungreedy {
		t.out.WriteByte('?')
	}
}

// repetition is the length of a {n}, {n,} or {n,m} that rest (after the {)
// opens, through its }, or 0.
func repetition(rest []rune) int {
	i := 0
	digits := func() int {
		start := i
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		return i - start
	}
	if digits() == 0 {
		return 0
	}
	if i < len(rest) && rest[i] == ',' {
		i++
		digits()
	}
	if i < len(rest) && rest[i] == '}' {
		return i + 1
	}
	return 0
}

// parseFlags reads the flags of a (?flags) or (?flags: group from at, just
// after the (?: the flags it sets on base, the index after its closer, and
// the closer.
func (t *translator) parseFlags(at int, base flagSet) (flagSet, int, rune, bool) {
	f := base
	on := true
	letters := 0
	for i := at; i < len(t.src); i++ {
		switch c := t.src[i]; c {
		case 'i':
			f.i = on
		case 'm':
			f.m = on
		case 's':
			f.s = on
		case 'U':
			f.ungreedy = on
		case '-':
			if !on {
				return f, 0, 0, false
			}
			on = false
			letters = 0
			continue
		case ')', ':':
			if (c == ')' && letters == 0 && on) || (!on && letters == 0) {
				return f, 0, 0, false
			}
			return f, i + 1, c, true
		default:
			return f, 0, 0, false
		}
		letters++
	}
	return f, 0, 0, false
}

func (t *translator) open() {
	parent := &t.stack[len(t.stack)-1]
	rest := string(t.src[t.pos:])
	if strings.HasPrefix(rest, "?P<") || (strings.HasPrefix(rest, "?<") && !strings.HasPrefix(rest, "?<=") && !strings.HasPrefix(rest, "?<!")) {
		start := strings.IndexByte(rest, '<') + 1
		end := strings.IndexByte(rest, '>')
		if end > start {
			name := rest[start:end]
			if t.target == python {
				t.out.WriteString("(?P<" + name + ">")
			} else {
				t.out.WriteString("(?<" + name + ">")
			}
			t.pos += len([]rune(rest[:end+1]))
			t.stack = append(t.stack, frame{flags: parent.flags, baseI: parent.flags.i})
			return
		}
	}
	if strings.HasPrefix(rest, "?") {
		if f, end, closer, ok := t.parseFlags(t.pos+1, parent.flags); ok {
			t.pos = end
			if closer == ')' {
				was := parent.flags.i
				parent.flags = f
				if f.i != was {
					t.openMod(parent)
				}
				return
			}
			switch {
			case f.i == parent.flags.i:
				t.out.WriteString("(?:")
			case f.i:
				t.out.WriteString("(?i:")
			default:
				t.out.WriteString("(?-i:")
			}
			t.stack = append(t.stack, frame{flags: f, baseI: f.i})
			return
		}
	}
	t.out.WriteByte('(')
	t.stack = append(t.stack, frame{flags: parent.flags, baseI: parent.flags.i})
}

func (t *translator) openMod(top *frame) {
	if top.flags.i {
		t.out.WriteString("(?i:")
	} else {
		t.out.WriteString("(?-i:")
	}
	top.mods++
}

func (t *translator) closeMods(top *frame) {
	t.out.WriteString(strings.Repeat(")", top.mods))
	top.mods = 0
}

func (t *translator) close() {
	if len(t.stack) == 1 {
		t.out.WriteByte(')')
		return
	}
	t.closeMods(&t.stack[len(t.stack)-1])
	t.out.WriteByte(')')
	t.stack = t.stack[:len(t.stack)-1]
}

// escape rewrites the escape after a \ outside a class.
func (t *translator) escape() {
	if t.pos >= len(t.src) {
		t.out.WriteByte('\\')
		return
	}
	c := t.src[t.pos]
	t.pos++
	switch c {
	case 'd', 'w', 'D', 'W':
		if t.target == javaScript {
			t.out.WriteRune('\\')
			t.out.WriteRune(c)
			return
		}
		t.set(perlClass(c), c == 'D' || c == 'W')
	case 's', 'S':
		t.set(perlClass(c), c == 'S')
	case 'b', 'B':
		if t.target == javaScript {
			t.out.WriteRune('\\')
			t.out.WriteRune(c)
			return
		}
		word := "[0-9A-Za-z_]"
		if c == 'b' {
			t.out.WriteString("(?:(?<=" + word + ")(?!" + word + ")|(?<!" + word + ")(?=" + word + "))")
		} else {
			t.out.WriteString("(?:(?<=" + word + ")(?=" + word + ")|(?<!" + word + ")(?!" + word + "))")
		}
	case 'A':
		if t.target == javaScript {
			t.out.WriteByte('^')
		} else {
			t.out.WriteString(`\A`)
		}
	case 'z':
		if t.target == javaScript {
			t.out.WriteByte('$')
		} else {
			t.out.WriteString(`\Z`)
		}
	case 'p', 'P':
		t.property(c == 'P', false)
	case 'Q':
		for t.pos < len(t.src) {
			if t.src[t.pos] == '\\' && t.pos+1 < len(t.src) && t.src[t.pos+1] == 'E' {
				t.pos += 2
				return
			}
			t.literal(t.src[t.pos])
			t.pos++
		}
	default:
		if t.keepsEscape(c, false) {
			t.out.WriteRune('\\')
			t.out.WriteRune(c)
			return
		}
		if cp, ok := t.escapedChar(c); ok {
			t.literal(cp)
			return
		}
		t.out.WriteRune('\\')
		t.out.WriteRune(c)
	}
}

// escapedChar is the one character the escape \c (c read) stands for:
// \x41, \x{1F600}, octal \101, \a \f \t \n \r \v and an escaped ASCII
// punctuation mark.
func (t *translator) escapedChar(c rune) (rune, bool) {
	switch {
	case c == 'x':
		if t.pos < len(t.src) && t.src[t.pos] == '{' {
			end := t.pos + 1
			for end < len(t.src) && isHex(t.src[end]) {
				end++
			}
			if end == t.pos+1 || end >= len(t.src) || t.src[end] != '}' {
				return 0, false
			}
			cp, err := strconv.ParseUint(string(t.src[t.pos+1:end]), 16, 32)
			t.pos = end + 1
			return rune(cp), err == nil && cp <= unicode.MaxRune
		}
		if t.pos+1 < len(t.src) && isHex(t.src[t.pos]) && isHex(t.src[t.pos+1]) {
			cp, _ := strconv.ParseUint(string(t.src[t.pos:t.pos+2]), 16, 8)
			t.pos += 2
			return rune(cp), true
		}
		return 0, false
	case c >= '0' && c <= '7':
		// A lone \1 to \7 is a backreference, which RE2 refuses.
		if c != '0' && (t.pos >= len(t.src) || !isOctal(t.src[t.pos])) {
			return 0, false
		}
		cp := c - '0'
		for n := 0; n < 2 && t.pos < len(t.src) && isOctal(t.src[t.pos]); n++ {
			cp = cp*8 + t.src[t.pos] - '0'
			t.pos++
		}
		return cp, true
	case c == 'a':
		return 7, true
	case c == 'f':
		return '\f', true
	case c == 't':
		return '\t', true
	case c == 'n':
		return '\n', true
	case c == 'r':
		return '\r', true
	case c == 'v':
		return '\v', true
	case c < 0x80 && !isAlnum(c):
		return c, true
	}
	return 0, false
}

// literal writes one character that matches itself outside a class.
func (t *translator) literal(c rune) {
	if strings.ContainsRune(`\.+*?()|[]{}^$`, c) {
		t.out.WriteRune('\\')
		t.out.WriteRune(c)
		return
	}
	t.out.WriteString(t.char(c, false))
}

// char spells a character that is not syntax: an escape for a control
// character, for one outside ASCII in Python or, when escape is set, in
// JavaScript.
func (t *translator) char(c rune, escape bool) string {
	if c < 0x20 || c == 0x7f || (c > 0x7e && (escape || t.target == python)) {
		return t.codePoint(c)
	}
	return string(c)
}

func (t *translator) codePoint(c rune) string {
	if i := strings.IndexRune("\t\n\v\f\r", c); i >= 0 {
		return `\` + "tnvfr"[i:i+1]
	}
	switch {
	case c < 0x100:
		return fmt.Sprintf(`\x%02x`, c)
	case c < 0x10000:
		return fmt.Sprintf(`\u%04x`, c)
	case t.target == python:
		return fmt.Sprintf(`\U%08x`, c)
	default:
		return fmt.Sprintf(`\u{%x}`, c)
	}
}

// classChar spells a character as a member of a class.
func (t *translator) classChar(c rune, escape bool) string {
	special := `\]-[^`
	if t.target == python {
		// Python warns that [[, &&, ~~ and || may become set operations.
		special += `&~|`
	}
	if strings.ContainsRune(special, c) {
		return `\` + string(c)
	}
	return t.char(c, escape)
}

// set writes a class of ranges outside a class.
func (t *translator) set(ranges [][2]rune, negated bool) {
	t.out.WriteByte('[')
	if negated {
		t.out.WriteByte('^')
	}
	t.ranges(ranges)
	t.out.WriteByte(']')
}

func (t *translator) ranges(ranges [][2]rune) {
	for _, r := range ranges {
		t.out.WriteString(t.classChar(r[0], true))
		if r[1] > r[0] {
			if r[1] > r[0]+1 {
				t.out.WriteByte('-')
			}
			t.out.WriteString(t.classChar(r[1], true))
		}
	}
}

// class rewrites a bracket class, its [ read.
func (t *translator) class() {
	t.out.WriteByte('[')
	if t.pos < len(t.src) && t.src[t.pos] == '^' {
		t.out.WriteByte('^')
		t.pos++
	}
	first := true
	for t.pos < len(t.src) {
		if t.src[t.pos] == ']' && !first {
			t.pos++
			t.out.WriteByte(']')
			return
		}
		// A - first or last is a literal as it stands.
		if t.src[t.pos] == '-' && (first || (t.pos+1 < len(t.src) && t.src[t.pos+1] == ']')) {
			t.pos++
			t.out.WriteByte('-')
			first = false
			continue
		}
		first = false
		if t.classSet() {
			continue
		}
		t.out.WriteString(t.classMember())
		if t.pos+1 < len(t.src) && t.src[t.pos] == '-' && t.src[t.pos+1] != ']' {
			// A range. RE2 refuses one that ends in a class, as the
			// targets refuse what this writes for it.
			t.pos++
			t.out.WriteByte('-')
			t.out.WriteString(t.classMember())
		}
	}
}

// classMember reads one character of a class and spells it. An escape
// that is no character is copied.
func (t *translator) classMember() string {
	c := t.src[t.pos]
	t.pos++
	if c != '\\' {
		return t.classChar(c, false)
	}
	if t.pos >= len(t.src) {
		return `\`
	}
	e := t.src[t.pos]
	t.pos++
	if t.keepsEscape(e, true) {
		return `\` + string(e)
	}
	if cp, ok := t.escapedChar(e); ok {
		return t.classChar(cp, false)
	}
	return `\` + string(e)
}

// keepsEscape reports whether the target reads the escaped punctuation
// mark \c as the mark, as RE2 does: Python any, JavaScript's u flag its
// syntax characters, / and, in a class, -.
func (t *translator) keepsEscape(c rune, inClass bool) bool {
	if c >= 0x80 || isAlnum(c) {
		return false
	}
	return t.target == python || strings.ContainsRune(`^$\.*+?()[]{}|/`, c) || (inClass && c == '-')
}

// classSet writes the class inside a class that starts at pos, if one
// does: [:alpha:], [:^alpha:], \d \w \s and their negations, \p and \P.
func (t *translator) classSet() bool {
	rest := t.src[t.pos:]
	if len(rest) >= 2 && rest[0] == '[' && rest[1] == ':' {
		end := strings.Index(string(rest[2:]), ":]")
		if end < 0 {
			return false
		}
		name := string(rest[2:])[:end]
		negated := strings.HasPrefix(name, "^")
		ranges, ok := posixClasses[strings.TrimPrefix(name, "^")]
		if !ok {
			return false
		}
		t.pos += 2 + len([]rune(name)) + 2
		t.inClassRanges(ranges, negated)
		return true
	}
	if len(rest) < 2 || rest[0] != '\\' {
		return false
	}
	switch c := rest[1]; c {
	case 'd', 'w', 'D', 'W':
		t.pos += 2
		if t.target == javaScript {
			t.out.WriteString(`\` + string(c))
			return true
		}
		t.inClassRanges(perlClass(c), c == 'D' || c == 'W')
		return true
	case 's', 'S':
		t.pos += 2
		t.inClassRanges(perlClass(c), c == 'S')
		return true
	case 'p', 'P':
		t.pos += 2
		t.property(c == 'P', true)
		return true
	}
	return false
}

func (t *translator) inClassRanges(ranges [][2]rune, negated bool) {
	if negated {
		ranges = complement(ranges)
	}
	t.ranges(ranges)
}

// property rewrites \pX, \p{Name} or \p{^Name}, its \p or \P read.
func (t *translator) property(negated, inClass bool) {
	if t.pos >= len(t.src) {
		t.out.WriteString(`\p`)
		return
	}
	var name string
	if t.src[t.pos] == '{' {
		end := strings.IndexRune(string(t.src[t.pos:]), '}')
		if end < 0 {
			t.out.WriteString(`\p`)
			return
		}
		name = string(t.src[t.pos:])[1:end]
		t.pos += len([]rune(string(t.src[t.pos:])[:end+1]))
	} else {
		name = string(t.src[t.pos])
		t.pos++
	}
	if strings.HasPrefix(name, "^") {
		negated = !negated
		name = name[1:]
	}
	if t.target == javaScript {
		t.jsProperty(name, negated)
		return
	}
	ranges, ok := unicodeRanges(name)
	if !ok {
		// Python has no \p: it refuses the pattern, as RE2 refuses a
		// class it does not know.
		t.out.WriteString(`\p{` + name + `}`)
		return
	}
	if inClass {
		t.inClassRanges(ranges, negated)
		return
	}
	t.set(ranges, negated)
}

func (t *translator) jsProperty(name string, negated bool) {
	p := `\p`
	if negated {
		p = `\P`
	}
	if name != "Any" && unicode.Categories[name] == nil && unicode.Scripts[name] != nil {
		name = "Script=" + name
	}
	t.out.WriteString(p + "{" + name + "}")
}

// unicodeRanges are the code points of a category or script, as Go's
// tables hold them.
func unicodeRanges(name string) ([][2]rune, bool) {
	if name == "Any" {
		return [][2]rune{{0, unicode.MaxRune}}, true
	}
	table := unicode.Categories[name]
	if table == nil {
		table = unicode.Scripts[name]
	}
	if table == nil {
		return nil, false
	}
	var out [][2]rune
	add := func(lo, hi, stride rune) {
		for c := lo; c <= hi; c += stride {
			end := c
			if stride == 1 {
				end = hi
			}
			if n := len(out); n > 0 && out[n-1][1]+1 >= c {
				out[n-1][1] = max(out[n-1][1], end)
			} else {
				out = append(out, [2]rune{c, end})
			}
			if stride == 1 {
				break
			}
		}
	}
	for _, r := range table.R16 {
		add(rune(r.Lo), rune(r.Hi), rune(r.Stride))
	}
	for _, r := range table.R32 {
		add(rune(r.Lo), rune(r.Hi), rune(r.Stride))
	}
	return out, true
}

// complement is every code point outside the sorted ranges.
func complement(ranges [][2]rune) [][2]rune {
	var out [][2]rune
	next := rune(0)
	for _, r := range ranges {
		if r[0] > next {
			out = append(out, [2]rune{next, r[0] - 1})
		}
		next = r[1] + 1
	}
	if next <= unicode.MaxRune {
		out = append(out, [2]rune{next, unicode.MaxRune})
	}
	return out
}

func perlClass(c rune) [][2]rune {
	switch unicode.ToLower(c) {
	case 'd':
		return posixClasses["digit"]
	case 'w':
		return posixClasses["word"]
	default:
		return [][2]rune{{'\t', '\n'}, {'\f', '\r'}, {' ', ' '}}
	}
}

// posixClasses are RE2's ASCII classes, [[:name:]].
var posixClasses = map[string][][2]rune{
	"alnum":  {{'0', '9'}, {'A', 'Z'}, {'a', 'z'}},
	"alpha":  {{'A', 'Z'}, {'a', 'z'}},
	"ascii":  {{0, 0x7f}},
	"blank":  {{'\t', '\t'}, {' ', ' '}},
	"cntrl":  {{0, 0x1f}, {0x7f, 0x7f}},
	"digit":  {{'0', '9'}},
	"graph":  {{'!', '~'}},
	"lower":  {{'a', 'z'}},
	"print":  {{' ', '~'}},
	"punct":  {{'!', '/'}, {':', '@'}, {'[', '`'}, {'{', '~'}},
	"space":  {{'\t', '\r'}, {' ', ' '}},
	"upper":  {{'A', 'Z'}},
	"word":   {{'0', '9'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'}},
	"xdigit": {{'0', '9'}, {'A', 'F'}, {'a', 'f'}},
}

func isHex(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isOctal(c rune) bool { return c >= '0' && c <= '7' }

func isAlnum(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
