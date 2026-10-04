package sqlmigrate

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Postgres names the constraints create.sql leaves unnamed: a primary key
// and a table's UNIQUE (...) clauses. The model carries those names, so a
// plan can rename or drop the constraints, and the functions here compute
// them the way Postgres does (src/backend/commands/indexcmds.c and
// src/backend/commands/tablecmds.c).

// namedataLen is Postgres's NAMEDATALEN: an identifier holds at most
// namedataLen-1 bytes.
const namedataLen = 64

// makeObjectName is Postgres's makeObjectName: name1, then name2 when it is
// not empty, then label, joined by underscores and cut to fit 63 bytes by
// shortening the longer of the two names first. A name is never cut in the
// middle of a character.
func makeObjectName(name1, name2, label string) string {
	overhead := 0
	name1chars, name2chars := len(name1), 0
	if name2 != "" {
		name2chars = len(name2)
		overhead++
	}
	if label != "" {
		overhead += len(label) + 1
	}
	avail := namedataLen - 1 - overhead
	for name1chars+name2chars > avail {
		if name1chars > name2chars {
			name1chars--
		} else {
			name2chars--
		}
	}
	var b strings.Builder
	b.WriteString(clipUTF8(name1, name1chars))
	if name2 != "" {
		b.WriteByte('_')
		b.WriteString(clipUTF8(name2, name2chars))
	}
	if label != "" {
		b.WriteByte('_')
		b.WriteString(label)
	}
	return b.String()
}

// clipUTF8 is pg_mbcliplen for UTF-8: the longest prefix of s of at most n
// bytes that ends on a character boundary.
func clipUTF8(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// indexNameAddition is Postgres's ChooseIndexNameAddition: the column names
// joined by underscores, stopping once the result is NAMEDATALEN bytes or
// longer.
func indexNameAddition(columns []string) string {
	var b []byte
	for _, name := range columns {
		if len(b) > 0 {
			b = append(b, '_')
		}
		if len(name) > namedataLen-1 {
			name = name[:namedataLen-1]
		}
		b = append(b, name...)
		if len(b) >= namedataLen {
			break
		}
	}
	return string(b)
}

// pgNamespace holds the relation and constraint names of one schema while
// the model replays create.sql, so a chosen name avoids every name taken
// before it, as Postgres's ChooseRelationName does.
type pgNamespace struct {
	relations   map[string]bool
	constraints map[string]bool
}

func newPGNamespace() *pgNamespace {
	return &pgNamespace{relations: map[string]bool{}, constraints: map[string]bool{}}
}

// relation records a table, an index or a view.
func (ns *pgNamespace) relation(name string) {
	ns.relations[name] = true
}

// chooseConstraintIndexName is the name Postgres gives the index of an
// unnamed primary key ("<table>_pkey") or unique constraint
// ("<table>_<columns>_key"): the first of label, label1, label2, ... that no
// relation and no constraint of the schema has. The name is recorded as
// both, since the constraint and its index share it.
func (ns *pgNamespace) chooseConstraintIndexName(table string, columns []string, primary bool) string {
	name2, label := indexNameAddition(columns), "key"
	if primary {
		name2, label = "", "pkey"
	}
	modlabel := label
	for pass := 1; ; pass++ {
		name := makeObjectName(table, name2, modlabel)
		if !ns.relations[name] && !ns.constraints[name] {
			ns.relations[name] = true
			ns.constraints[name] = true
			return name
		}
		modlabel = label + strconv.Itoa(pass)
	}
}
