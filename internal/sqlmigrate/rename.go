package sqlmigrate

import (
	"fmt"
	"strings"
)

// renameForms is the part of every ParseRename error that shows the two
// forms a rename takes.
const renameForms = "want old=new for a table (purchase=order) or oldTable.oldColumn=newTable.newColumn for a column (order.total=order.amount)"

// ParseRename parses a --rename value: "old=new" for a table, or
// "oldTable.oldColumn=newTable.newColumn" for a column. Names are the
// database's, as the plan's subjects write them. The value is taken as
// written: no space is trimmed, so the plan records it unchanged.
func ParseRename(value string) (Rename, error) {
	from, to, ok := strings.Cut(value, "=")
	if !ok || strings.Contains(to, "=") {
		return Rename{}, fmt.Errorf("rename %q: %s", value, renameForms)
	}
	fromParts, fromOK := renameParts(from)
	toParts, toOK := renameParts(to)
	if !fromOK || !toOK || fromParts != toParts {
		return Rename{}, fmt.Errorf("rename %q: %s", value, renameForms)
	}
	if from == to {
		return Rename{}, fmt.Errorf("rename %q: renames %s to itself", value, from)
	}
	return Rename{From: from, To: to}, nil
}

// renameParts reports how many dot-separated names side has: 1 for a
// table, 2 for a table and a column. ok is false for an empty name, more
// than one dot, or a space.
func renameParts(side string) (parts int, ok bool) {
	names := strings.Split(side, ".")
	if len(names) > 2 {
		return 0, false
	}
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, " \t\n") {
			return 0, false
		}
	}
	return len(names), true
}
