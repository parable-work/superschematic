package tsreader

import (
	"errors"
	"strings"
	"unicode"
)

// titleTag is the JSDoc tag an enum member's title is declared with (D48,
// amended): `/** @title Picked and packed */`. TypeScript takes no
// decorator on an enum member, and a tag needs no evaluation.
const titleTag = "@title"

// The refusals of memberTitle, which the walker prefixes with the enum and
// the member.
var (
	errTitleEmpty     = errors.New("@title needs the member's title after it")
	errTitleTwice     = errors.New("@title is given twice")
	errTitleMisplaced = errors.New("@title is read from a JSDoc comment (/** @title … */), not a // or /* */ comment")
)

// commentKind is the kind of comment a line of leading trivia belongs to.
type commentKind int

const (
	noComment commentKind = iota
	lineComment
	blockComment
	jsdocComment
)

// memberTitle reads an enum member's @title from its leading trivia (D48,
// amended): a line of a /** */ block that is "@title <text>", after the
// block's "*". The tag's line leaves the comment; the rest is the comment,
// as leadingComment strips it. A @title in a // or /* */ comment, an
// empty one, or a second is an error. A line that only mentions @title
// later in its text, or a tag such as @titles, is comment text.
func memberTitle(node *astNode) (title, comment string, err error) {
	var lines []string
	seen := false
	open := noComment // the block comment a line continues, if any
	for _, raw := range strings.Split(leadingTrivia(node), "\n") {
		line := strings.TrimSpace(raw)
		continued := open != noComment
		kind, body := open, line
		if !continued {
			switch {
			case strings.HasPrefix(line, "//"):
				kind = lineComment
			case strings.HasPrefix(line, "/**") && !strings.HasPrefix(line, "/**/"):
				kind, body = jsdocComment, line[len("/**"):]
			case strings.HasPrefix(line, "/*"):
				kind, body = blockComment, line[len("/*"):]
			}
		}
		if kind == blockComment || kind == jsdocComment {
			open = kind
			if strings.Contains(body, "*/") {
				open = noComment
			}
		}

		text, kept := commentLine(line)
		if !kept && kind == jsdocComment && continued && !strings.HasPrefix(line, "*") {
			// A JSDoc block's line need not start with "*", which
			// leadingComment leaves out of the comment.
			text = strings.TrimSpace(strings.TrimSuffix(line, "*/"))
		}
		tag, isTag := titleTagText(text)
		switch {
		case !isTag || kind == noComment:
		case kind != jsdocComment:
			if err == nil {
				err = errTitleMisplaced
			}
		case seen:
			if err == nil {
				err = errTitleTwice
			}
			continue
		case tag == "":
			seen = true
			if err == nil {
				err = errTitleEmpty
			}
			continue
		default:
			seen, title = true, tag
			continue
		}
		if kept {
			lines = append(lines, text)
		}
	}
	return title, strings.Join(lines, "\n"), err
}

// titleTagText reports whether a comment line is a @title tag, and the
// text after it.
func titleTagText(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, titleTag)
	if !ok {
		return "", false
	}
	if rest != "" && !unicode.IsSpace([]rune(rest)[0]) {
		return "", false
	}
	return strings.TrimSpace(rest), true
}
