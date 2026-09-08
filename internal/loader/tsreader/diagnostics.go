package tsreader

import (
	"fmt"
	"strings"
)

// SchemaError is the diagnostics contract for the TypeScript frontend: every
// schema mistake -- whether surfaced by the compiler (a semantic diagnostic)
// or by the walker (an illegal import, a non-literal decorator argument) --
// reads as a schema message with a source location, not a compiler dump.
type SchemaError struct {
	// File is the schema source file path.
	File string

	// Line is the 1-based line number.
	Line int

	// Col is the 1-based column number.
	Col int

	// Msg is the human-readable schema message.
	Msg string
}

// Error formats the error as file:line:col: message.
func (e *SchemaError) Error() string {
	if e.File == "" {
		return e.Msg
	}
	return fmt.Sprintf("%s:%d:%d: %s", e.File, e.Line, e.Col, e.Msg)
}

// SchemaErrorList aggregates schema errors from one service load.
type SchemaErrorList []*SchemaError

// Error joins all schema errors, one per line.
func (l SchemaErrorList) Error() string {
	msgs := make([]string, len(l))
	for i, e := range l {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "\n")
}

// errorAtNode builds a SchemaError pointing at a node's source location.
func errorAtNode(node *astNode, format string, args ...any) *SchemaError {
	file, line, col := locationOfNode(node)
	return &SchemaError{
		File: file,
		Line: line,
		Col:  col,
		Msg:  fmt.Sprintf(format, args...),
	}
}

// locationOfNode resolves a node's file:line:col source location, skipping
// leading trivia so it points at the declaration token.
func locationOfNode(node *astNode) (string, int, int) {
	file := getSourceFileOfNode(node)
	if file == nil {
		return "", 0, 0
	}
	pos := skipTrivia(file.Text(), node.Pos())
	line, col := lineCol(file.Text(), pos)
	return file.FileName(), line, col
}

// fromDiagnostics converts compiler diagnostics into schema errors.
func fromDiagnostics(diags []*astDiagnostic) SchemaErrorList {
	var errs SchemaErrorList
	for _, d := range diags {
		e := &SchemaError{Msg: d.String()}
		if f := d.File(); f != nil {
			line, col := lineCol(f.Text(), d.Pos())
			e.File = f.FileName()
			e.Line = line
			e.Col = col
		}
		errs = append(errs, e)
	}
	return errs
}

// lineCol converts a 0-based byte offset into 1-based line and column.
func lineCol(text string, pos int) (int, int) {
	if pos > len(text) {
		pos = len(text)
	}
	line := 1
	col := 1
	for _, c := range []byte(text[:pos]) {
		if c == '\n' {
			line++
			col = 1
			continue
		}
		col++
	}
	return line, col
}

// skipTrivia advances pos past whitespace and comments so locations point at
// the declaration token instead of its leading trivia.
func skipTrivia(text string, pos int) int {
	for pos < len(text) {
		switch text[pos] {
		case ' ', '\t', '\r', '\n':
			pos++
		case '/':
			if pos+1 < len(text) && text[pos+1] == '/' {
				for pos < len(text) && text[pos] != '\n' {
					pos++
				}
				continue
			}
			if pos+1 < len(text) && text[pos+1] == '*' {
				end := strings.Index(text[pos+2:], "*/")
				if end < 0 {
					return len(text)
				}
				pos += 2 + end + 2
				continue
			}
			return pos
		default:
			return pos
		}
	}
	return pos
}

// leadingComment extracts the comment block immediately preceding a node,
// with comment markers stripped. It returns "" when the node has no attached
// comment. Comments are IR metadata and round-trip through all formats.
func leadingComment(node *astNode) string {
	file := getSourceFileOfNode(node)
	if file == nil {
		return ""
	}
	text := file.Text()
	start := node.Pos()
	end := skipTrivia(text, start)
	if end > len(text) {
		end = len(text)
	}
	trivia := text[start:end]

	var lines []string
	for _, raw := range strings.Split(trivia, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "//"):
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "//")))
		case strings.HasPrefix(line, "/**"):
			line = strings.TrimPrefix(line, "/**")
			line = strings.TrimSuffix(line, "*/")
			if s := strings.TrimSpace(line); s != "" {
				lines = append(lines, s)
			}
		case strings.HasPrefix(line, "/*"):
			line = strings.TrimPrefix(line, "/*")
			line = strings.TrimSuffix(line, "*/")
			if s := strings.TrimSpace(line); s != "" {
				lines = append(lines, s)
			}
		case strings.HasPrefix(line, "*/"):
			// closing line of a block comment
		case strings.HasPrefix(line, "*"):
			if s := strings.TrimSpace(strings.TrimPrefix(line, "*")); s != "" {
				lines = append(lines, s)
			}
		}
	}
	return strings.Join(lines, "\n")
}
