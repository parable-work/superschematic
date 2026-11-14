package sqlmigrate

import (
	"fmt"
	"strconv"
	"strings"
)

// The facts of a SQLite object that only its statement in sqlite_schema
// holds (D116). PRAGMA table_xinfo, index_list, index_xinfo and
// foreign_key_list answer the rest, and ReadDatabase takes it from them.
// The statement alone holds:
//
//   - a column's COLLATE, the expression of a generated column, and
//     AUTOINCREMENT;
//   - a CHECK constraint;
//   - a foreign key's DEFERRABLE INITIALLY DEFERRED, and an ON CONFLICT
//     clause;
//   - the keys of an index with a key that is not a plain column, and a
//     partial index's WHERE;
//   - what a statement of the schema's raw SQL creates.
//
// Each is read off the statement's tokens (lexSQLite), at the depth of
// parentheses its clause is written at, never by a pattern over the text.

type sqlTokenKind int

const (
	// sqlWord is a bare identifier or a keyword.
	sqlWord sqlTokenKind = iota
	// sqlName is a quoted identifier: "x", `x` or [x].
	sqlName
	// sqlString is a string literal.
	sqlString
	sqlNumber
	// sqlPunct is one character of punctuation.
	sqlPunct
)

// sqlToken is one token of a SQLite statement.
type sqlToken struct {
	kind sqlTokenKind
	// text is the token as written. value is a name without its quotes, a
	// string's content, and text for every other token.
	text, value string
	// start and end bound the token in the statement.
	start, end int
}

// is reports whether t is the keyword word. A quoted name is never one.
func (t sqlToken) is(word string) bool {
	return t.kind == sqlWord && strings.EqualFold(t.text, word)
}

func (t sqlToken) punct(p string) bool {
	return t.kind == sqlPunct && t.text == p
}

// name is t as the name of an object or a column. SQLite takes a string
// where a name must stand, as in CREATE TABLE 'notes_data'.
func (t sqlToken) name() (string, bool) {
	if t.kind == sqlWord || t.kind == sqlName || t.kind == sqlString {
		return t.value, true
	}
	return "", false
}

// lexSQLite splits a SQLite statement into its tokens, leaving out space
// and comments.
func lexSQLite(sql string) []sqlToken {
	var out []sqlToken
	word := func(ch byte) bool {
		return ch == '_' || ch == '$' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch >= 0x80
	}
	digit := func(ch byte) bool { return ch >= '0' && ch <= '9' }
	// quoted reads a token that ends at close, where close written twice
	// stands for itself.
	quoted := func(i int, close byte, doubles bool) (end int, value string) {
		var b strings.Builder
		for i++; i < len(sql); i++ {
			if sql[i] == close {
				if doubles && i+1 < len(sql) && sql[i+1] == close {
					b.WriteByte(close)
					i++
					continue
				}
				return i + 1, b.String()
			}
			b.WriteByte(sql[i])
		}
		return len(sql), b.String()
	}
	for i := 0; i < len(sql); {
		ch := sql[i]
		start := i
		switch {
		case strings.IndexByte(" \t\n\r\f", ch) >= 0:
			i++
		case ch == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case ch == '/' && i+1 < len(sql) && sql[i+1] == '*':
			if end := strings.Index(sql[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				i = len(sql)
			}
		case ch == '\'':
			end, value := quoted(i, '\'', true)
			i = end
			out = append(out, sqlToken{kind: sqlString, text: sql[start:i], value: value, start: start, end: i})
		case ch == '"' || ch == '`':
			end, value := quoted(i, ch, true)
			i = end
			out = append(out, sqlToken{kind: sqlName, text: sql[start:i], value: value, start: start, end: i})
		case ch == '[':
			end, value := quoted(i, ']', false)
			i = end
			out = append(out, sqlToken{kind: sqlName, text: sql[start:i], value: value, start: start, end: i})
		case digit(ch) || ch == '.' && i+1 < len(sql) && digit(sql[i+1]):
			for i < len(sql) && (word(sql[i]) || sql[i] == '.' ||
				(sql[i] == '+' || sql[i] == '-') && (sql[i-1] == 'e' || sql[i-1] == 'E') && !strings.HasPrefix(strings.ToLower(sql[start:i]), "0x")) {
				i++
			}
			out = append(out, sqlToken{kind: sqlNumber, text: sql[start:i], value: sql[start:i], start: start, end: i})
		case word(ch):
			for i < len(sql) && word(sql[i]) {
				i++
			}
			out = append(out, sqlToken{kind: sqlWord, text: sql[start:i], value: sql[start:i], start: start, end: i})
		default:
			i++
			out = append(out, sqlToken{kind: sqlPunct, text: sql[start:i], value: sql[start:i], start: start, end: i})
		}
	}
	return out
}

// closing returns the index of the parenthesis that closes the one at
// tokens[open], or -1.
func closing(tokens []sqlToken, open int) int {
	depth := 0
	for i := open; i < len(tokens); i++ {
		switch {
		case tokens[i].punct("("):
			depth++
		case tokens[i].punct(")"):
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTop splits tokens at each sep outside parentheses.
func splitTop(tokens []sqlToken, sep func(sqlToken) bool) [][]sqlToken {
	var out [][]sqlToken
	depth, start := 0, 0
	for i, t := range tokens {
		switch {
		case t.punct("("):
			depth++
		case t.punct(")"):
			depth--
		case depth == 0 && sep(t):
			out = append(out, tokens[start:i])
			start = i + 1
		}
	}
	return append(out, tokens[start:])
}

// sameSQL reports whether two pieces of SQL are the same tokens: keywords
// and bare names in any case, a name quoted or bare, space and comments
// aside.
func sameSQL(a, b string) bool {
	return sameTokens(lexSQLite(a), lexSQLite(b))
}

func sameTokens(a, b []sqlToken) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		named := func(t sqlToken) bool { return t.kind == sqlWord || t.kind == sqlName }
		switch {
		case named(x) && named(y):
			if !strings.EqualFold(x.value, y.value) {
				return false
			}
		case x.kind != y.kind || x.value != y.value:
			return false
		}
	}
	return true
}

// sqliteTableSQL is what a CREATE TABLE statement alone says of its table.
type sqliteTableSQL struct {
	// columns are the columns' own clauses, by name in lower case.
	columns map[string]*sqliteColumnClauses
	// checks are the CHECK constraints, in the statement's order.
	checks []sqliteCheckSQL
	// unmodeled are the clauses the model has no form of, as phrases, in
	// the statement's order.
	unmodeled []string
}

// sqliteColumnClauses is what a column's definition alone says.
type sqliteColumnClauses struct {
	// collation is the COLLATE clause's name in upper case, or "".
	collation string
	// generated is the expression of a generated column, as written.
	generated string
}

// sqliteCheckSQL is a CHECK constraint: its predicate as written, and the
// column whose definition holds it, or "" for one of the table's own.
type sqliteCheckSQL struct {
	column, expr string
}

// parseSQLiteTable reads a CREATE TABLE statement as sqlite_schema keeps
// it: CREATE TABLE name (item, ...) options, each item a column's
// definition or a constraint of the table.
func parseSQLiteTable(sql string) (*sqliteTableSQL, error) {
	tokens := lexSQLite(sql)
	open := -1
	for i, t := range tokens {
		if t.punct("(") {
			open = i
			break
		}
	}
	end := -1
	if open >= 0 {
		end = closing(tokens, open)
	}
	if end < 0 {
		return nil, fmt.Errorf("its statement is no CREATE TABLE with a column list: %s", sql)
	}
	def := &sqliteTableSQL{columns: map[string]*sqliteColumnClauses{}}
	for _, item := range splitTop(tokens[open+1:end], func(t sqlToken) bool { return t.punct(",") }) {
		if len(item) == 0 {
			continue
		}
		first := item[0]
		if first.is("CONSTRAINT") && len(item) > 2 {
			first = item[2]
		}
		if first.is("PRIMARY") || first.is("UNIQUE") || first.is("CHECK") || first.is("FOREIGN") {
			def.clauses(sql, item, "", nil)
			continue
		}
		name, ok := item[0].name()
		if !ok {
			return nil, fmt.Errorf("its statement has a column with no name: %s", sql)
		}
		col := &sqliteColumnClauses{}
		def.columns[strings.ToLower(name)] = col
		def.clauses(sql, item[1:], name, col)
	}
	return def, nil
}

// clauses reads the clauses of one item of a table's definition: column's
// definition after its name, with col to fill, or a table constraint, with
// col nil. Only a clause outside parentheses counts: one inside is part of
// a type, a default or an expression.
func (def *sqliteTableSQL) clauses(sql string, item []sqlToken, column string, col *sqliteColumnClauses) {
	at := func(i int) sqlToken {
		if i >= 0 && i < len(item) {
			return item[i]
		}
		return sqlToken{kind: sqlPunct}
	}
	of := ""
	if column != "" {
		of = " on " + column
	}
	for i := 0; i < len(item); i++ {
		t := item[i]
		switch {
		case t.punct("("):
			if end := closing(item, i); end > 0 {
				i = end
			}
		case t.is("CONSTRAINT"):
			// The constraint's name follows.
			i++
		case t.is("DEFAULT"):
			// A default is a literal, a signed number or an expression in
			// parentheses; a literal may be a word.
			if next := at(i + 1); next.punct("+") || next.punct("-") {
				i += 2
			} else if !next.punct("(") {
				i++
			}
		case t.is("COLLATE") && col != nil:
			if name, ok := at(i + 1).name(); ok {
				col.collation = strings.ToUpper(name)
				i++
			}
		case t.is("AS") && col != nil && at(i+1).punct("("):
			if end := closing(item, i+1); end > 0 {
				col.generated = strings.TrimSpace(sql[item[i+1].end:item[end].start])
				i = end
			}
		case t.is("CHECK") && at(i+1).punct("("):
			if end := closing(item, i+1); end > 0 {
				def.checks = append(def.checks, sqliteCheckSQL{column: column, expr: strings.TrimSpace(sql[item[i+1].end:item[end].start])})
				i = end
			}
		case t.is("AUTOINCREMENT"):
			def.unmodeled = append(def.unmodeled, "AUTOINCREMENT"+of)
		case t.is("ON") && at(i+1).is("CONFLICT"):
			def.unmodeled = append(def.unmodeled, strings.TrimSpace("ON CONFLICT "+strings.ToUpper(at(i+2).text))+of)
			i += 2
		case t.is("DEFERRABLE") && !at(i-1).is("NOT") && at(i+1).is("INITIALLY") && at(i+2).is("DEFERRED"):
			def.unmodeled = append(def.unmodeled, "DEFERRABLE INITIALLY DEFERRED on a foreign key"+strings.Replace(of, " on ", " of ", 1))
			i += 2
		}
	}
}

// parseSQLiteIndex reads the two parts of a CREATE INDEX statement that
// PRAGMA index_xinfo does not answer: the key list as written, and the
// predicate after WHERE, or "".
func parseSQLiteIndex(sql string) (keys, where string, err error) {
	tokens := lexSQLite(sql)
	on := -1
	for i, t := range tokens {
		if t.is("ON") {
			on = i
			break
		}
	}
	open := -1
	for i := on + 1; on >= 0 && i < len(tokens); i++ {
		if tokens[i].punct("(") {
			open = i
			break
		}
	}
	end := -1
	if open >= 0 {
		end = closing(tokens, open)
	}
	if end < 0 {
		return "", "", fmt.Errorf("its statement is no CREATE INDEX with a key list: %s", sql)
	}
	keys = strings.TrimSpace(sql[tokens[open].end:tokens[end].start])
	if end+1 < len(tokens) && tokens[end+1].is("WHERE") {
		where = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql[tokens[end+1].end:]), ";"))
	}
	return keys, where, nil
}

// The kinds of object a statement creates (createdBy).
const (
	objectTable        = "table"
	objectVirtualTable = "virtual table"
	objectIndex        = "index"
	objectView         = "view"
	objectTrigger      = "trigger"
)

// createdBy returns the kind and name of the object a statement creates:
// CREATE [TEMP] [UNIQUE] INDEX | TABLE | VIRTUAL TABLE | VIEW | TRIGGER
// [IF NOT EXISTS] [schema.]name. body is the statement's tokens without IF
// NOT EXISTS and the schema, which sqlite_schema's copy of the statement
// leaves out too. ok is false for any other statement.
func createdBy(statement string) (kind, name string, body []sqlToken, ok bool) {
	tokens := lexSQLite(statement)
	for len(tokens) > 0 && tokens[len(tokens)-1].punct(";") {
		tokens = tokens[:len(tokens)-1]
	}
	if len(tokens) == 0 || !tokens[0].is("CREATE") {
		return "", "", nil, false
	}
	i := 1
	for i < len(tokens) && (tokens[i].is("TEMP") || tokens[i].is("TEMPORARY") || tokens[i].is("UNIQUE")) {
		i++
	}
	switch {
	case i >= len(tokens):
		return "", "", nil, false
	case tokens[i].is("VIRTUAL") && i+1 < len(tokens) && tokens[i+1].is("TABLE"):
		kind = objectVirtualTable
		i += 2
	case tokens[i].is("TABLE"):
		kind = objectTable
		i++
	case tokens[i].is("INDEX"):
		kind = objectIndex
		i++
	case tokens[i].is("VIEW"):
		kind = objectView
		i++
	case tokens[i].is("TRIGGER"):
		kind = objectTrigger
		i++
	default:
		return "", "", nil, false
	}
	body = append(body, tokens[:i]...)
	if i+2 < len(tokens) && tokens[i].is("IF") && tokens[i+1].is("NOT") && tokens[i+2].is("EXISTS") {
		i += 3
	}
	if i+2 < len(tokens) && tokens[i+1].punct(".") {
		i += 2
	}
	if i >= len(tokens) {
		return "", "", nil, false
	}
	if name, ok = tokens[i].name(); !ok {
		return "", "", nil, false
	}
	return kind, name, append(body, tokens[i:]...), true
}

// parseSQLiteCheck reads a CHECK predicate in one of the forms a schema's
// checks take (sqlutil.Check.Expression), over one of columns: col IN
// ('a', 'b'), or col >= n, col <= n, length(col) >= n and length(col) <= n
// joined by AND, each at most once. ok is false for any other predicate,
// which the model has no form of. The check's column is spelled as columns
// spells it.
func parseSQLiteCheck(expr string, columns []string) (check *Check, ok bool) {
	tokens := unwrap(lexSQLite(expr))
	column := func(t sqlToken) (string, bool) {
		if t.kind != sqlWord && t.kind != sqlName {
			return "", false
		}
		for _, name := range columns {
			if strings.EqualFold(name, t.value) {
				return name, true
			}
		}
		return "", false
	}
	if len(tokens) >= 5 && tokens[1].is("IN") && tokens[2].punct("(") && closing(tokens, 2) == len(tokens)-1 {
		name, ok := column(tokens[0])
		if !ok {
			return nil, false
		}
		check = &Check{Column: name}
		for _, value := range splitTop(tokens[3:len(tokens)-1], func(t sqlToken) bool { return t.punct(",") }) {
			if len(value) != 1 || value[0].kind != sqlString {
				return nil, false
			}
			check.In = append(check.In, value[0].value)
		}
		return check, true
	}
	check = &Check{}
	for _, term := range splitTop(tokens, func(t sqlToken) bool { return t.is("AND") }) {
		term = unwrap(term)
		length := len(term) > 4 && term[0].is("length") && term[1].punct("(") && term[3].punct(")")
		operand := 0
		if length {
			operand = 2
			term = append([]sqlToken{term[2]}, term[4:]...)
		}
		// term is now: column, < or >, =, an optional sign, a number.
		if len(term) < 4 || !term[1].punct(">") && !term[1].punct("<") || !term[2].punct("=") {
			return nil, false
		}
		name, ok := column(term[0])
		if !ok || check.Column != "" && check.Column != name {
			return nil, false
		}
		check.Column = name
		number := ""
		for _, t := range term[3:] {
			number += t.text
		}
		lower := term[1].punct(">")
		if operand == 2 {
			n, err := strconv.Atoi(number)
			switch {
			case err != nil, lower && check.MinLength != nil, !lower && check.MaxLength != nil:
				return nil, false
			case lower:
				check.MinLength = &n
			default:
				check.MaxLength = &n
			}
			continue
		}
		f, err := strconv.ParseFloat(number, 64)
		switch {
		case err != nil, len(term) > 5, len(term) == 5 && !term[3].punct("-") && !term[3].punct("+"),
			lower && check.Min != nil, !lower && check.Max != nil:
			return nil, false
		case lower:
			check.Min = &f
		default:
			check.Max = &f
		}
	}
	return check, check.Column != ""
}

// unwrap strips the parentheses that wrap all of tokens.
func unwrap(tokens []sqlToken) []sqlToken {
	for len(tokens) >= 2 && tokens[0].punct("(") && closing(tokens, 0) == len(tokens)-1 {
		tokens = tokens[1 : len(tokens)-1]
	}
	return tokens
}

// sqliteTriggerRefuses reports whether a CREATE TRIGGER statement makes a
// trigger that refuses a write (Trigger.Refuses, D118): one of its
// statements, or its WHEN, calls RAISE, which fails the write that fired
// the trigger or, with IGNORE, leaves it out. It reads the statement's
// tokens, so a RAISE inside a string or a quoted name is none.
func sqliteTriggerRefuses(sql string) bool {
	tokens := lexSQLite(sql)
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].is("RAISE") && tokens[i+1].punct("(") {
			return true
		}
	}
	return false
}
