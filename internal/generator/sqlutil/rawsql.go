package sqlutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// Raw SQL a DB service declares (@rawSql, D78). Verification refuses a
// piece the DDL could not place or run again (CheckRawSQL); sqlgen and the
// migration planner run the pieces in the order OrderRawSQL gives.

// RawSQL is one piece of raw SQL as the DDL runs it.
type RawSQL struct {
	// ID is the piece's id; Origin the type that declares it.
	ID     string
	Origin string
	// Position is where the piece runs: ir.RawSQLAfterTables or
	// ir.RawSQLAfterIndexes, after following its After to a position.
	Position string
	// After is the piece it runs right after, or "".
	After string
	// Statements are Up's statements and Down the down's, each without its
	// semicolon.
	Statements []string
	Down       []string
	// Checksum is the lowercase hex SHA-256 of the statements, each ended by
	// a semicolon and a newline: a plan runs the piece again when it
	// changes.
	Checksum string
}

// RawSQLDialects are the dialects a piece can name.
var RawSQLDialects = []string{"postgres", "sqlite"}

var rawSQLID = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// rawSQLPiece is a piece with the type that declares it.
type rawSQLPiece struct {
	def    ir.RawSQLDef
	origin string
}

// rawSQLPieces lists the schema's pieces by type name, then in declaration
// order.
func rawSQLPieces(schema *ir.Schema) []rawSQLPiece {
	names := make([]string, 0, len(schema.Types))
	for name, td := range schema.Types {
		if len(td.RawSQL) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out []rawSQLPiece
	for _, name := range names {
		for _, def := range schema.Types[name].RawSQL {
			out = append(out, rawSQLPiece{def: def, origin: name})
		}
	}
	return out
}

// CheckRawSQL returns every problem with the schema's raw SQL, each naming
// the piece and the type that declares it: an id that is not a lowercase
// identifier or that two pieces share; an Up that holds no statement; a
// statement that cannot run again over what it made, or that runs
// CONCURRENTLY, which a migration step's transaction refuses; an After that
// names no piece, a piece that runs after itself through others, or one
// that runs on a dialect the piece it follows does not; and a dialect
// other than postgres and sqlite.
func CheckRawSQL(schema *ir.Schema) []error {
	pieces := rawSQLPieces(schema)
	var errs []error
	fail := func(p rawSQLPiece, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: @rawSql %s %s", p.origin, quoteID(p.def.ID), fmt.Sprintf(format, args...)))
	}
	byID := map[string]rawSQLPiece{}
	for _, p := range pieces {
		if !rawSQLID.MatchString(p.def.ID) {
			fail(p, "needs an id that is a lowercase identifier (e.g. line_item_totals)")
			continue
		}
		if prev, dup := byID[p.def.ID]; dup {
			fail(p, "has the id of another piece, on %s; ids are unique in the service", prev.origin)
			continue
		}
		byID[p.def.ID] = p
	}
	for _, p := range pieces {
		for _, d := range p.def.Dialects {
			if !containsString(RawSQLDialects, d) {
				fail(p, "names the dialect %q; a piece runs on postgres, sqlite or both", d)
			}
		}
		for _, part := range []struct{ name, sql string }{{"up", p.def.Up}, {"down", p.def.Down}} {
			if part.name == "down" && strings.TrimSpace(part.sql) == "" {
				continue
			}
			statements, err := SplitStatements(part.sql)
			switch {
			case err != nil:
				fail(p, "%s: %v", part.name, err)
				continue
			case len(statements) == 0:
				fail(p, "has no statement in its %s", part.name)
				continue
			}
			for i, stmt := range statements {
				if problem := statementProblem(stmt); problem != "" {
					fail(p, "%s statement %d %s: %s", part.name, i+1, problem, abbreviate(stmt))
				}
			}
		}
		switch after := p.def.After; after {
		case "", ir.RawSQLAfterTables, ir.RawSQLAfterIndexes:
		default:
			target, ok := byID[after]
			if !ok {
				fail(p, "runs after %s, which no @rawSql of the service declares; name another piece's id, %q or %q", quoteID(after), ir.RawSQLAfterTables, ir.RawSQLAfterIndexes)
				continue
			}
			for _, d := range RawSQLDialects {
				if p.def.RunsOn(d) && !target.def.RunsOn(d) {
					fail(p, "runs on %s, and the piece it runs after, %s, does not; give it %s's dialects", d, quoteID(after), quoteID(after))
				}
			}
		}
	}
	// A piece that reaches itself through the pieces it runs after.
	for _, p := range pieces {
		seen := map[string]bool{p.def.ID: true}
		for cur := byID[p.def.ID].def.After; ; cur = byID[cur].def.After {
			if _, ok := byID[cur]; !ok {
				break
			}
			if seen[cur] {
				if cur == p.def.ID {
					fail(p, "runs after itself, through the pieces it runs after; one of them must run after %q or %q", ir.RawSQLAfterTables, ir.RawSQLAfterIndexes)
				}
				break
			}
			seen[cur] = true
		}
	}
	return errs
}

// OrderRawSQL returns the schema's pieces that run on dialect, in the order
// they run: the pieces that run after the tables, then those that run after
// the indexes, each position's pieces in id order, and each piece followed
// right away by the pieces that run after it, in id order. It fails on a
// schema CheckRawSQL refuses.
func OrderRawSQL(schema *ir.Schema, dialect string) ([]RawSQL, error) {
	if errs := CheckRawSQL(schema); len(errs) > 0 {
		return nil, errs[0]
	}
	children := map[string][]rawSQLPiece{}
	roots := map[string][]rawSQLPiece{}
	for _, p := range rawSQLPieces(schema) {
		if !p.def.RunsOn(dialect) {
			continue
		}
		switch p.def.After {
		case "", ir.RawSQLAfterIndexes:
			roots[ir.RawSQLAfterIndexes] = append(roots[ir.RawSQLAfterIndexes], p)
		case ir.RawSQLAfterTables:
			roots[ir.RawSQLAfterTables] = append(roots[ir.RawSQLAfterTables], p)
		default:
			children[p.def.After] = append(children[p.def.After], p)
		}
	}
	byID := func(list []rawSQLPiece) {
		sort.Slice(list, func(i, j int) bool { return list[i].def.ID < list[j].def.ID })
	}
	var out []RawSQL
	var visit func(p rawSQLPiece, position string) error
	visit = func(p rawSQLPiece, position string) error {
		piece, err := resolveRawSQL(p, position)
		if err != nil {
			return err
		}
		out = append(out, piece)
		next := children[p.def.ID]
		byID(next)
		for _, child := range next {
			if err := visit(child, position); err != nil {
				return err
			}
		}
		return nil
	}
	for _, position := range []string{ir.RawSQLAfterTables, ir.RawSQLAfterIndexes} {
		list := roots[position]
		byID(list)
		for _, p := range list {
			if err := visit(p, position); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func resolveRawSQL(p rawSQLPiece, position string) (RawSQL, error) {
	up, err := SplitStatements(p.def.Up)
	if err != nil {
		return RawSQL{}, fmt.Errorf("%s: @rawSql %s up: %w", p.origin, quoteID(p.def.ID), err)
	}
	down, err := SplitStatements(p.def.Down)
	if err != nil {
		return RawSQL{}, fmt.Errorf("%s: @rawSql %s down: %w", p.origin, quoteID(p.def.ID), err)
	}
	piece := RawSQL{ID: p.def.ID, Origin: p.origin, Position: position, Statements: up, Down: down}
	switch p.def.After {
	case "", ir.RawSQLAfterTables, ir.RawSQLAfterIndexes:
	default:
		piece.After = p.def.After
	}
	piece.Checksum = StatementsChecksum(up)
	return piece, nil
}

// StatementsChecksum is the lowercase hex SHA-256 of statements, each ended
// by a semicolon and a newline.
func StatementsChecksum(statements []string) string {
	h := sha256.New()
	for _, s := range statements {
		h.Write([]byte(s))
		h.Write([]byte(";\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func quoteID(id string) string { return fmt.Sprintf("%q", id) }

// abbreviate shortens a statement for a message: its first line, cut at 60
// characters.
func abbreviate(stmt string) string {
	line, _, _ := strings.Cut(stmt, "\n")
	line = strings.TrimSpace(line)
	if len(line) > 60 {
		return line[:57] + "..."
	}
	return line
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// SplitStatements splits SQL at the semicolons that end its statements:
// not one inside a string ('...', E'...' with its backslash escapes), a
// quoted identifier, a dollar-quoted body ($$...$$, $tag$...$tag$) or a
// comment (-- to the end of the line, /* ... */, nested as Postgres nests
// them). Each statement is trimmed, without its semicolon; one that holds
// only comments is dropped. An unterminated string, identifier, body or
// comment is an error.
func SplitStatements(sql string) ([]string, error) {
	var out []string
	start := 0
	flush := func(end int) {
		stmt := strings.TrimSpace(sql[start:end])
		if len(sqlWords(stmt)) > 0 {
			out = append(out, stmt)
		}
	}
	for i := 0; i < len(sql); {
		next, semicolon, err := skipSQLToken(sql, i)
		if err != nil {
			return nil, err
		}
		if semicolon {
			flush(i)
			start = next
		}
		i = next
	}
	flush(len(sql))
	return out, nil
}

// skipSQLToken returns the offset past the token at i, whether the token is
// a semicolon, or an error for an unterminated one.
func skipSQLToken(sql string, i int) (int, bool, error) {
	ch := sql[i]
	switch {
	case ch == ';':
		return i + 1, true, nil
	case ch == '-' && i+1 < len(sql) && sql[i+1] == '-':
		end := strings.IndexByte(sql[i:], '\n')
		if end < 0 {
			return len(sql), false, nil
		}
		return i + end + 1, false, nil
	case ch == '/' && i+1 < len(sql) && sql[i+1] == '*':
		depth := 0
		for j := i; j+1 < len(sql); j++ {
			switch {
			case sql[j] == '/' && sql[j+1] == '*':
				depth++
				j++
			case sql[j] == '*' && sql[j+1] == '/':
				depth--
				j++
				if depth == 0 {
					return j + 1, false, nil
				}
			}
		}
		return 0, false, fmt.Errorf("a /* comment is not closed")
	case ch == '\'':
		backslash := i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e') && (i < 2 || !isSQLIdentByte(sql[i-2]))
		for j := i + 1; j < len(sql); j++ {
			switch {
			case backslash && sql[j] == '\\':
				j++
			case sql[j] == '\'' && j+1 < len(sql) && sql[j+1] == '\'':
				j++
			case sql[j] == '\'':
				return j + 1, false, nil
			}
		}
		return 0, false, fmt.Errorf("a string is not closed")
	case ch == '"':
		end := strings.IndexByte(sql[i+1:], '"')
		if end < 0 {
			return 0, false, fmt.Errorf("a quoted identifier is not closed")
		}
		return i + 1 + end + 1, false, nil
	case ch == '$' && (i == 0 || !isSQLIdentByte(sql[i-1])):
		if tag := dollarTag(sql[i:]); tag != "" {
			end := strings.Index(sql[i+len(tag):], tag)
			if end < 0 {
				return 0, false, fmt.Errorf("a %s body is not closed", tag)
			}
			return i + len(tag) + end + len(tag), false, nil
		}
	}
	return i + 1, false, nil
}

// dollarTag returns the dollar quote s starts with ($$ or $tag$), or "".
func dollarTag(s string) string {
	for j := 1; j < len(s); j++ {
		switch {
		case s[j] == '$':
			return s[:j+1]
		case !isSQLIdentByte(s[j]) || (j == 1 && s[j] >= '0' && s[j] <= '9'):
			return ""
		}
	}
	return ""
}

func isSQLIdentByte(ch byte) bool {
	return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch >= 0x80
}

// sqlWords lists a statement's words outside strings, quoted identifiers,
// bodies and comments, upper-cased, with each "(" as a word of its own.
func sqlWords(stmt string) []string {
	var words []string
	for i := 0; i < len(stmt); {
		ch := stmt[i]
		switch {
		case isSQLIdentByte(ch):
			j := i
			for j < len(stmt) && isSQLIdentByte(stmt[j]) {
				j++
			}
			words = append(words, strings.ToUpper(stmt[i:j]))
			i = j
			continue
		case ch == '(':
			words = append(words, "(")
		case ch == '\'' || ch == '"' || ch == '$' || ch == '-' || ch == '/':
			next, _, err := skipSQLToken(stmt, i)
			if err != nil {
				return words
			}
			if ch == '"' {
				words = append(words, "IDENT")
			}
			if next > i+1 {
				i = next
				continue
			}
		}
		i++
	}
	return words
}

// statementProblem says why a statement of a piece cannot run again over
// what it made, or cannot run in a migration step's transaction, or is "".
// A statement can run again when it is CREATE OR REPLACE, CREATE ... IF NOT
// EXISTS, DROP ... IF EXISTS, REFRESH, COMMENT ON, GRANT, REVOKE, an INSERT
// with ON CONFLICT (or SQLite's INSERT OR IGNORE and OR REPLACE), or a DO
// block, which its author vouches for.
func statementProblem(stmt string) string {
	words := sqlWords(stmt)
	if len(words) == 0 {
		return ""
	}
	// The words before the object's definition: up to the first (, ON, AS
	// or the end.
	head := words
	for i, w := range words {
		if w == "(" || w == "ON" || w == "AS" || w == "CASCADE" || w == "RESTRICT" {
			head = words[:i]
			break
		}
	}
	has := func(seq ...string) bool {
		for i := 0; i+len(seq) <= len(head); i++ {
			if strings.Join(head[i:i+len(seq)], " ") == strings.Join(seq, " ") {
				return true
			}
		}
		return false
	}
	switch words[0] {
	case "CREATE", "DROP", "REINDEX":
		if has("CONCURRENTLY") {
			return "runs CONCURRENTLY, which a migration step's transaction refuses; declare the index with @index, which a plan builds concurrently"
		}
	}
	switch words[0] {
	case "CREATE":
		if len(words) > 2 && words[1] == "OR" && words[2] == "REPLACE" || has("IF", "NOT", "EXISTS") {
			return ""
		}
	case "DROP":
		if has("IF", "EXISTS") {
			return ""
		}
	case "REFRESH", "COMMENT", "GRANT", "REVOKE", "DO":
		return ""
	case "INSERT":
		if len(words) > 2 && words[1] == "OR" && (words[2] == "IGNORE" || words[2] == "REPLACE") {
			return ""
		}
		for i := 0; i+1 < len(words); i++ {
			if words[i] == "ON" && words[i+1] == "CONFLICT" {
				return ""
			}
		}
	}
	return "cannot run again over what it made, which a plan does when the piece changes; write it as CREATE OR REPLACE, CREATE ... IF NOT EXISTS, DROP ... IF EXISTS, REFRESH, COMMENT ON, GRANT, REVOKE, INSERT ... ON CONFLICT or a DO block"
}
