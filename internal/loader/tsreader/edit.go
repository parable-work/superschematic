package tsreader

import (
	"fmt"
	"strconv"
	"strings"
)

// Bootstrap records a value its target read from the cloud, such as the
// GCP project's number, in the schema source (docs/stack-model.md, section
// 7.3, D47). In a TypeScript schema it does so through this edit: it
// parses the file with the compiler's parser, finds the property in the
// syntax tree and changes that property's text alone, so every other byte
// of the file, comments and layout included, stays as it was.

// EnvironmentValue names one target value of an @environment class:
// projectNumber in
//
//	@environment({ target: "gcp", gcp: { project: "acme-staging", projectNumber: "123456789012" } })
//	export abstract class Staging {}
//
// stackdeploy.EnvironmentValue has the same fields in the same order, so
// the CLI converts one to the other when it hands bootstrap this edit:
// stackdeploy does not import this package, which would link the compiler.
type EnvironmentValue struct {
	// Class is the environment's class.
	Class string

	// Target is the key the decorator's object holds the target's values
	// under: the target's name (`gcp`).
	Target string

	// Key is the value's key (`projectNumber`), and Value the string it is
	// set to.
	Key   string
	Value string

	// Beside is the key whose property a new Key is written after
	// (`project`).
	Beside string
}

// SetEnvironmentValue sets v's key to v.Value in the target values of an
// @environment class of the TypeScript schema file fileName, whose text is
// src. It returns the file's new text and the value the key held, with
// found false when the file set none.
//
// A key set to a string literal has that literal's text replaced, in the
// literal's own quotes; a key set to the same value leaves src as it is.
// A key the file does not set is written right after v.Beside's property,
// in the object's layout: on a line of its own at that property's
// indentation in an object written one property per line, and after it on
// the same line in an object written on one line, with a comma as the
// object's other properties have one.
//
// It refuses, and says what to change by hand, when the file declares no
// such class, the class has no @environment decorator with an object
// literal, that object sets no object literal of the target's values, or
// those values do not set v.Beside and need it; and when the key's value is
// not a string literal, such as a reference to a constant, which the edit
// cannot change without changing what else reads it.
func SetEnvironmentValue(fileName string, src []byte, v EnvironmentValue) (out []byte, previous string, found bool, err error) {
	text := string(src)
	e := &valueEdit{fileName: fileName, text: text, v: v}
	file, diags, err := parseOne(fileName, text)
	if err != nil {
		return nil, "", false, err
	}
	if len(diags) > 0 {
		d := diags[0]
		line, col := lineCol(text, d.Pos())
		return nil, "", false, fmt.Errorf("%s:%d:%d: %s does not parse: %s; fix it, or %s", fileName, line, col, fileName, d.String(), e.byHand())
	}
	if file.Text() != text {
		return nil, "", false, fmt.Errorf("%s: the parser read the file's text differently, so it cannot be edited in place; %s", fileName, e.byHand())
	}
	values, err := e.values(file)
	if err != nil {
		return nil, "", false, err
	}
	prop, err := e.property(values, v.Key)
	if err != nil {
		return nil, "", false, err
	}
	if prop != nil {
		init := prop.AsPropertyAssignment().Initializer
		if init == nil || init.Kind != kindStringLiteral {
			return nil, "", false, e.errorAt(prop, "%s in the %s values of class %s is not a string literal; %s", v.Key, v.Target, v.Class, e.byHand())
		}
		previous = init.Text()
		if previous == v.Value {
			return src, previous, true, nil
		}
		start := skipTrivia(text, init.Pos())
		return []byte(text[:start] + quoteJS(v.Value, text[start]) + text[init.End():]), previous, true, nil
	}
	beside, err := e.property(values, v.Beside)
	if err != nil {
		return nil, "", false, err
	}
	if beside == nil {
		return nil, "", false, e.errorAt(values, "the %s values of class %s do not set %s, which %s goes beside; %s", v.Target, v.Class, v.Beside, v.Key, e.byHand())
	}
	edited, err := e.insertAfter(values, beside)
	if err != nil {
		return nil, "", false, err
	}
	return []byte(edited), "", false, nil
}

// valueEdit is one SetEnvironmentValue.
type valueEdit struct {
	fileName string
	text     string
	v        EnvironmentValue
}

// byHand says what to change by hand when the edit refuses.
func (e *valueEdit) byHand() string {
	return fmt.Sprintf("set %s: %s in the %s values of %s by hand", propertyKey(e.v.Key), quoteJS(e.v.Value, '"'), e.v.Target, e.v.Class)
}

// errorAt is a refusal at node's position.
func (e *valueEdit) errorAt(node *astNode, format string, args ...any) error {
	line, col := lineCol(e.text, skipTrivia(e.text, node.Pos()))
	return fmt.Errorf("%s:%d:%d: %s", e.fileName, line, col, fmt.Sprintf(format, args...))
}

// values returns the object literal of the class's target values: the
// `<target>` property of its @environment decorator's object.
func (e *valueEdit) values(file *astSourceFile) (*astNode, error) {
	v := e.v
	var class *astNode
	for _, stmt := range file.Statements.Nodes {
		if stmt.Kind == kindClassDeclaration && stmt.Name() != nil && stmt.Name().Text() == v.Class {
			class = stmt
			break
		}
	}
	if class == nil {
		return nil, fmt.Errorf("%s declares no class %s; %s", e.fileName, v.Class, e.byHand())
	}
	var calls []*astNode
	for _, d := range class.Decorators() {
		expr := d.AsDecorator().Expression
		if expr.Kind != kindCallExpression {
			continue
		}
		if calleeName(expr.AsCallExpression().Expression) == "environment" {
			calls = append(calls, expr)
		}
	}
	switch len(calls) {
	case 0:
		return nil, e.errorAt(class, "class %s has no @environment({...}) decorator; %s", v.Class, e.byHand())
	case 1:
	default:
		return nil, e.errorAt(calls[1], "class %s has more than one @environment decorator; %s", v.Class, e.byHand())
	}
	call := calls[0].AsCallExpression()
	if call.Arguments == nil || len(call.Arguments.Nodes) != 1 || call.Arguments.Nodes[0].Kind != kindObjectLiteralExpression {
		return nil, e.errorAt(calls[0], "the @environment decorator of class %s takes no object literal; %s", v.Class, e.byHand())
	}
	prop, err := e.property(call.Arguments.Nodes[0], v.Target)
	if err != nil {
		return nil, err
	}
	if prop == nil {
		return nil, e.errorAt(calls[0], "the @environment decorator of class %s sets no %s values; %s", v.Class, v.Target, e.byHand())
	}
	values := prop.AsPropertyAssignment().Initializer
	if values == nil || values.Kind != kindObjectLiteralExpression {
		return nil, e.errorAt(prop, "the %s values of class %s are not an object literal; %s", v.Target, v.Class, e.byHand())
	}
	return values, nil
}

// calleeName is the name a decorator calls: `environment` in
// @environment(...) and in @stack.environment(...).
func calleeName(callee *astNode) string {
	switch callee.Kind {
	case kindIdentifier:
		return callee.Text()
	case kindPropertyAccessExpression:
		if name := callee.Name(); name != nil && name.Kind == kindIdentifier {
			return name.Text()
		}
	}
	return ""
}

// property returns object's plain property named key, or nil. It refuses
// an element whose key it cannot read, such as a spread, which may set the
// key, a key set twice, and a key set by anything but a plain property.
func (e *valueEdit) property(object *astNode, key string) (*astNode, error) {
	var found *astNode
	props := object.AsObjectLiteralExpression().Properties
	if props == nil {
		return nil, nil
	}
	for _, p := range props.Nodes {
		name, ok := propertyName(p)
		if !ok {
			return nil, e.errorAt(p, "an object of the @environment decorator of class %s holds %s, whose key the edit cannot read; %s",
				e.v.Class, strings.TrimSpace(e.text[skipTrivia(e.text, p.Pos()):p.End()]), e.byHand())
		}
		if name != key {
			continue
		}
		if p.Kind != kindPropertyAssignment {
			return nil, e.errorAt(p, "%s in an object of the @environment decorator of class %s is not a plain property; %s", key, e.v.Class, e.byHand())
		}
		if found != nil {
			return nil, e.errorAt(p, "an object of the @environment decorator of class %s sets %s twice; %s", e.v.Class, key, e.byHand())
		}
		found = p
	}
	return found, nil
}

// propertyName reads the key of an object literal's element: an
// identifier, or a string or numeric literal.
func propertyName(p *astNode) (string, bool) {
	name := p.Name()
	if name == nil {
		return "", false
	}
	switch name.Kind {
	case kindIdentifier, kindStringLiteral, kindNumericLiteral:
		return name.Text(), true
	}
	return "", false
}

// insertAfter writes `<key>: "<value>"` right after beside, a property of
// values, in the object's layout.
func (e *valueEdit) insertAfter(values, beside *astNode) (string, error) {
	text := e.text
	quote := byte('"')
	if init := beside.AsPropertyAssignment().Initializer; init != nil && init.Kind == kindStringLiteral {
		quote = text[skipTrivia(text, init.Pos())]
	}
	prop := propertyKey(e.v.Key) + ": " + quoteJS(e.v.Value, quote)
	end := beside.End()
	next := skipTrivia(text, end)
	if next >= len(text) {
		return "", e.errorAt(beside, "the %s values of class %s end in an unexpected place; %s", e.v.Target, e.v.Class, e.byHand())
	}
	switch text[next] {
	case ',':
		// A property follows, or a trailing comma: the new property takes
		// a comma of its own too.
		at, newline := lineEnd(text, next+1)
		if newline {
			return text[:at] + e.newline() + e.indent(values, beside) + prop + "," + text[at:], nil
		}
		space := ""
		if next+1 < len(text) && (text[next+1] == ' ' || text[next+1] == '\t') {
			space = " "
		}
		return text[:next+1] + space + prop + "," + text[next+1:], nil
	case '}':
		// beside is the last property, with no trailing comma: it takes
		// one, and the new property none.
		at, newline := lineEnd(text, end)
		if newline {
			return text[:end] + "," + text[end:at] + e.newline() + e.indent(values, beside) + prop + text[at:], nil
		}
		return text[:end] + ", " + prop + text[end:], nil
	}
	return "", e.errorAt(beside, "the %s values of class %s continue in an unexpected way after %s; %s", e.v.Target, e.v.Class, e.v.Beside, e.byHand())
}

// newline is the file's line ending.
func (e *valueEdit) newline() string {
	if strings.Contains(e.text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// indent is the indentation of a property on a line of its own in values:
// beside's when it starts its line, else that of the first property that
// starts its line, else beside's line's and two spaces more.
func (e *valueEdit) indent(values, beside *astNode) string {
	if indent, ok := e.lineIndent(beside); ok {
		return indent
	}
	for _, p := range values.AsObjectLiteralExpression().Properties.Nodes {
		if indent, ok := e.lineIndent(p); ok {
			return indent
		}
	}
	start := strings.LastIndexByte(e.text[:skipTrivia(e.text, beside.Pos())], '\n') + 1
	line := e.text[start:]
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))] + "  "
}

// lineIndent returns the whitespace before node on its line, and whether
// node starts its line.
func (e *valueEdit) lineIndent(node *astNode) (string, bool) {
	pos := skipTrivia(e.text, node.Pos())
	start := strings.LastIndexByte(e.text[:pos], '\n') + 1
	before := e.text[start:pos]
	if strings.Trim(before, " \t") != "" {
		return "", false
	}
	return before, true
}

// lineEnd scans from pos past spaces, tabs and comments on the same line.
// It returns where the line breaks, with newline true, or where the next
// token on the line starts, with newline false. A block comment that spans
// lines counts as the line's end.
func lineEnd(text string, pos int) (int, bool) {
	for pos < len(text) {
		switch {
		case text[pos] == ' ' || text[pos] == '\t':
			pos++
		case text[pos] == '\n':
			return pos, true
		case text[pos] == '\r':
			return pos, true
		case strings.HasPrefix(text[pos:], "//"):
			n := strings.IndexByte(text[pos:], '\n')
			if n < 0 {
				return len(text), true
			}
			pos += n
			if pos > 0 && text[pos-1] == '\r' {
				pos--
			}
			return pos, true
		case strings.HasPrefix(text[pos:], "/*"):
			n := strings.Index(text[pos+2:], "*/")
			if n < 0 || strings.ContainsAny(text[pos:pos+2+n], "\r\n") {
				return pos, true
			}
			pos += 2 + n + 2
		default:
			return pos, false
		}
	}
	return pos, true
}

// propertyKey writes key as an object literal's key: as it is when it is
// an identifier, else quoted.
func propertyKey(key string) string {
	for i, c := range key {
		letter := c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !letter && (i == 0 || c < '0' || c > '9') {
			return quoteJS(key, '"')
		}
	}
	if key == "" {
		return `""`
	}
	return key
}

// quoteJS writes s as a JavaScript string literal in quote, ' or ".
func quoteJS(s string, quote byte) string {
	if quote != '\'' {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, c := range s {
		switch {
		case c == rune(quote) || c == '\\':
			b.WriteByte('\\')
			b.WriteRune(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c == 0x7f || c == ' ' || c == ' ':
			b.WriteString(`\u`)
			hex := strconv.FormatInt(int64(c), 16)
			b.WriteString(strings.Repeat("0", 4-len(hex)) + hex)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte(quote)
	return b.String()
}
