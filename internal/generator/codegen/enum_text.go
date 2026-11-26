package codegen

import (
	"regexp"
	"strings"
)

// An enum member's comment can carry the words a person reads for that
// member, one line each (D165):
//
//	// title: The upload was refused
//	// action: Pick a smaller file and try again.
//	// The file is larger than the bucket takes.
//	TOO_LARGE = 'UP-001',
//
// The comment keeps both lines as written; a generator that reads them
// writes the text beside the member, so code reads it from the generated
// enum rather than from a copy of its own. A key is matched in any case,
// and the first line that starts with it is the member's text.
var enumTextLineRe = regexp.MustCompile(`(?i)^(title|action):\s*(.*)$`)

// enumTextLine returns the text of the first comment line of the value that
// starts with key ("title" or "action"), or "".
func (v EnumValueInfo) enumTextLine(key string) string {
	for _, line := range strings.Split(v.Doc(), "\n") {
		m := enumTextLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m != nil && strings.EqualFold(m[1], key) {
			return strings.TrimSpace(m[2])
		}
	}
	return ""
}

// Title returns the value's `title:` comment line: the heading a person
// reads first. Empty when the comment has none.
func (v EnumValueInfo) Title() string {
	return v.enumTextLine("title")
}

// Action returns the value's `action:` comment line: what the person can do
// next. Empty when the comment has none.
func (v EnumValueInfo) Action() string {
	return v.enumTextLine("action")
}

// EnumValuesHaveText reports whether any value carries a `title:` or an
// `action:` line.
func EnumValuesHaveText(values []EnumValueInfo) bool {
	for _, v := range values {
		if v.Title() != "" || v.Action() != "" {
			return true
		}
	}
	return false
}
