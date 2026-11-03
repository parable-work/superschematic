package tsreader

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// titleTag is the JSDoc tag an enum member's title is declared with (D48,
// amended): `/** @title Picked and packed */`. TypeScript takes no
// decorator on an enum member, and a tag needs no evaluation.
const titleTag = "@title"

// The refusals of memberTitle, which the walker prefixes with the enum and
// the member.
var (
	errTitleEmpty     = errors.New("@title needs the member's title after it")
	errTitleUnspaced  = errors.New("@title needs a space before the member's title")
	errTitleTwice     = errors.New("@title is given twice")
	errTitleWrapped   = errors.New("@title must fit on its line")
	errTitleMisplaced = errors.New("@title is read from a JSDoc comment (/** @title … */), not a // or /* */ comment")
	errTitleMidLine   = errors.New("@title must begin a line of the member's JSDoc comment")
	errTitleBlocks    = errors.New("@title is read from the member's only JSDoc comment, and it has more than one")
	errTitleDetached  = errors.New("@title is read from the member's own comment, and a blank line parts this one from the member")
)

// commentKind is the kind of one comment of a node's leading trivia.
type commentKind int

const (
	lineComment commentKind = iota
	blockComment
	jsdocComment
)

// commentSpan is one comment of a node's leading trivia, its lines with
// the comment's markers stripped, and its offsets in the trivia: start at
// its first "/", end past its last character, before a // comment's line
// terminator.
type commentSpan struct {
	kind       commentKind
	lines      []commentText
	start, end int
}

// commentText is one line of a comment: its text, trimmed, without the
// comment's markers or a block's leading "*" on a line after its first,
// and whether the line adds a line to the node's comment as leadingComment
// reads it. A // line always does. A block's line does when it has text,
// but for a line after the first without its "*", which leadingComment
// leaves out.
type commentText struct {
	text string
	kept bool
}

// commentSpans splits trivia, the whitespace and comments before a node,
// into its comments in order. A // comment ends at a line terminator; a
// /* */ or /** */ comment at its first "*/" ("/**/" is an empty /* */
// comment), its lines split at each line terminator TypeScript reads.
func commentSpans(trivia string) []commentSpan {
	var spans []commentSpan
	for i := 0; i < len(trivia); {
		rest := trivia[i:]
		switch {
		case strings.HasPrefix(rest, "//"):
			end := len(rest)
			if at := strings.IndexAny(rest, "\n\r\u2028\u2029"); at >= 0 {
				end = at
			}
			spans = append(spans, commentSpan{kind: lineComment, lines: []commentText{{text: strings.TrimSpace(rest[2:end]), kept: true}}, start: i, end: i + end})
			i += end
		case strings.HasPrefix(rest, "/*"):
			kind, open := blockComment, len("/*")
			if strings.HasPrefix(rest, "/**") && !strings.HasPrefix(rest, "/**/") {
				kind, open = jsdocComment, len("/**")
			}
			body, size := rest[open:], len(rest)
			if end := strings.Index(rest[2:], "*/"); end >= 0 {
				body, size = rest[open:2+end], 2+end+2
			}
			span := commentSpan{kind: kind, start: i, end: i + size}
			for n, line := range splitLines(body) {
				text := strings.TrimSpace(line)
				kept := text != ""
				if n > 0 {
					var starred bool
					if text, starred = strings.CutPrefix(text, "*"); starred {
						text = strings.TrimSpace(text)
					}
					kept = starred && text != ""
				}
				span.lines = append(span.lines, commentText{text: text, kept: kept})
			}
			spans = append(spans, span)
			i += size
		default:
			i++
		}
	}
	return spans
}

// splitLines splits text at each line terminator TypeScript reads: \r\n,
// \n, \r, U+2028 and U+2029.
func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var lines []string
	start := 0
	for i, r := range text {
		if r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029' {
			lines = append(lines, text[start:i])
			start = i + utf8.RuneLen(r)
		}
	}
	return append(lines, text[start:])
}

// attachedFrom is the index in spans, the comments of trivia, of the first
// comment attached to the node trivia leads (D100): the comments after the
// last blank line between two of them, or between the last and the node.
// It is len(spans) when a blank line parts the last comment from the node.
// A comment so parted, such as a section's banner, is not the node's.
func attachedFrom(trivia string, spans []commentSpan) int {
	next := len(trivia)
	for i := len(spans) - 1; i >= 0; i-- {
		// A blank line is two line terminators with nothing but
		// whitespace between them.
		if len(splitLines(trivia[spans[i].end:next])) > 2 {
			return i + 1
		}
		next = spans[i].start
	}
	return 0
}

// triviaComment is leadingComment over a node's leading trivia: the lines
// of the comments attached to the node (attachedFrom), each line's comment
// markers stripped, undecorated.
func triviaComment(trivia string) string {
	spans := commentSpans(trivia)
	from := len(trivia)
	if at := attachedFrom(trivia, spans); at < len(spans) {
		from = spans[at].start
	}
	var lines []string
	for _, raw := range strings.Split(trivia[from:], "\n") {
		if s, ok := commentLine(strings.TrimSpace(raw)); ok {
			lines = append(lines, s)
		}
	}
	return strings.Join(undecorated(lines), "\n")
}

// ruleRunes are the characters a rule, or a banner's runs, are drawn
// with.
const ruleRunes = "-=*#~_+/"

// undecorated is a comment's lines without the decoration a source file's
// sections are marked with (D100). At its head it drops each rule, each
// banner and a line framed between two rules, and the empty lines after
// them; at its tail, each rule and the empty lines before it. A rule
// between lines of text stays: in a doc comment it is Markdown's.
func undecorated(lines []string) []string {
	start := 0
head:
	for start < len(lines) {
		switch line := lines[start]; {
		case isRule(line) || isBanner(line):
			start++
		case start > 0 && line == "":
			start++
		case start > 0 && start+1 < len(lines) && isRule(lines[start-1]) && isRule(lines[start+1]):
			// "=====", "Accounts", "=====": a title framed by rules.
			start += 2
		default:
			break head
		}
	}
	end := len(lines)
	for end > start && isRule(lines[end-1]) {
		end--
		for end > start && lines[end-1] == "" {
			end--
		}
	}
	return lines[start:end]
}

// isRule reports whether line is a rule: three or more of ruleRunes and
// nothing but spaces beside them ("-----", "=====", "*****", "= = =").
func isRule(line string) bool {
	n := 0
	for _, r := range line {
		switch {
		case strings.ContainsRune(ruleRunes, r):
			n++
		case r != ' ' && r != '\t':
			return false
		}
	}
	return n >= 3
}

// isBanner reports whether line is a banner: a title between two runs of
// three or more of ruleRunes, each set off from it by a space ("---- Plaid
// ----", "=== Accounts ==="). "***Bold***" and "Amount - in cents" are
// text.
func isBanner(line string) bool {
	left := len(line) - len(strings.TrimLeft(line, ruleRunes))
	right := len(line) - len(strings.TrimRight(line, ruleRunes))
	if left < 3 || right < 3 || left+right >= len(line) {
		return false
	}
	title := line[left : len(line)-right]
	return strings.HasPrefix(title, " ") && strings.HasSuffix(title, " ") && strings.TrimSpace(title) != ""
}

// memberTitle reads an enum member's @title from its leading trivia (D48,
// amended), and the member's comment without the tag's line. Every
// comment on the member is read for the tag. The tag is the first token of
// a line of the member's one /** */ comment, after the line's "*": "@title
// <text>", the text the rest of that line. The lines after it, up to the
// comment's next tag or its end, must be blank, so a title cannot wrap.
//
// It refuses an empty tag, one without a space before its text, a
// second, one that wraps, one in a // or /* */ comment, one that does not
// begin its line, and one on a member with a second /** */ comment, so a
// tag is never read as comment text instead. "@titles" or "a@title" is no
// tag.
func memberTitle(node *astNode) (title, comment string, err error) {
	return triviaTitle(leadingTrivia(node))
}

// triviaTitle is memberTitle over a member's leading trivia. The
// member's comment is the comments attached to it, undecorated, as
// triviaComment reads them (D100); a tag in a comment a blank line parts
// from the member is refused.
func triviaTitle(trivia string) (title, comment string, err error) {
	spans := commentSpans(trivia)
	from := attachedFrom(trivia, spans)
	jsdocs := 0
	for _, span := range spans[from:] {
		if span.kind == jsdocComment {
			jsdocs++
		}
	}
	fail := func(e error) {
		if err == nil {
			err = e
		}
	}
	seen := false
	var lines []string
	for n, span := range spans {
		attached := n >= from
		// wrapping is set after the span's @title line, until its next tag.
		wrapping := false
		for _, line := range span.lines {
			at := titleAt(line.text)
			if at < 0 {
				if wrapping && strings.HasPrefix(line.text, "@") {
					wrapping = false
				}
				if wrapping && line.text != "" {
					fail(errTitleWrapped)
					continue
				}
				if line.kept && attached {
					lines = append(lines, line.text)
				}
				continue
			}
			// A line that holds the tag leaves the comment.
			switch {
			case span.kind != jsdocComment:
				fail(errTitleMisplaced)
			case !attached:
				fail(errTitleDetached)
			case at > 0:
				fail(errTitleMidLine)
			case seen:
				fail(errTitleTwice)
			default:
				seen, wrapping = true, true
				rest := line.text[len(titleTag):]
				switch first, _ := utf8.DecodeRuneInString(rest); {
				case strings.TrimSpace(rest) == "":
					fail(errTitleEmpty)
				case !unicode.IsSpace(first):
					fail(errTitleUnspaced)
				default:
					title = strings.TrimSpace(rest)
				}
			}
		}
	}
	if seen && jsdocs > 1 {
		fail(errTitleBlocks)
	}
	return title, strings.Join(undecorated(lines), "\n"), err
}

// titleAt is the byte offset in text of its first @title tag, or -1: the
// tag's name with no identifier character or "@" before it, and no
// identifier character or "-" after it, so "@titles", "@title-case" and
// "a@title" are not it, and "@title:" is. The TypeScript writer refuses a
// member's comment that holds one (holdsTitleTag).
func titleAt(text string) int {
	for from := 0; ; {
		i := strings.Index(text[from:], titleTag)
		if i < 0 {
			return -1
		}
		i += from
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[i+len(titleTag):])
		if (i == 0 || !identifierRune(before) && before != '@') &&
			(i+len(titleTag) == len(text) || !identifierRune(after) && after != '-') {
			return i
		}
		from = i + 1
	}
}

// identifierRune reports whether r may continue a JavaScript identifier, as
// far as a tag's name is concerned.
func identifierRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
