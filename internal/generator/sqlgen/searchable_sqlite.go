package sqlgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
)

// Search across tables on SQLite (D186), which has no trigger functions,
// no statement triggers and no full-text search of Postgres's. The index
// table, search_index, is the same table, its audience a JSON array of ids
// (D182); its words are an FTS5 table beside it, search_index_fts, which
// triggers on search_index keep (SearchFTS), so whatever writes an index
// row, the worker's indexSearch or a trigger's delete, keeps the words in
// step in the same statement. The triggers of the @searchable tables and
// their sub-tables are row triggers under the Postgres triggers' names, an
// event after each where it needs one:
//
//   - SearchTrigger: after an insert, the row is enqueued unless it is
//     soft deleted; after a delete, its index row is deleted; after an
//     update, the index row of its old key is deleted when the key changed
//     and of its key when it was soft deleted, an @audience table's resolved
//     columns are copied into its index row, and it is enqueued when its
//     key, a watched column or its restore changed what the index row holds.
//   - SearchChildTrigger: after an insert and a delete of a message or a
//     file, the row it belongs to is enqueued, and after an update of its
//     text, its state or its row, the row it belongs to now and the one it
//     belonged to.
//
// Each statement of a trigger is guarded by its own WHERE, as SQLite's
// trigger body has no IF, and an enqueue ends with the queue's
// SQLiteConflict, which skips a row that holds a ready message already.
// A key is compared as text, CAST(... AS TEXT), as Postgres's ::text.

// SearchFTSTable is the FTS5 table of the search index's words on SQLite
const SearchFTSTable = sqlutil.SearchFTSTable

// SearchFTSColumns are the FTS5 table's columns in order: the four texts a
// search ranks, weighed as the columns of SearchFTSWeights; ref, a token
// that names the index row (searchFTSRef), which its triggers find the
// row's words by; and the index row's table and key, unindexed, which a
// search joins the index on.
var SearchFTSColumns = []string{"title", "body", "comments", "files", "ref", "source_table", "source_key"}

// SearchFTSTokenizer reads words as Postgres's simple configuration does
// for the engine's index: Unicode letters and digits, folded to lower
// case, no diacritic removed and no word stemmed (D58, D155).
const SearchFTSTokenizer = "unicode61 remove_diacritics 0"

// SearchFTSFoldingTokenizer is the tokenizer of an index that folds
// diacritics (outputs.sql.search.foldDiacritics, #740), as Postgres's
// search_unaccent configuration reads words: diacritics removed too, the
// engine's own tokenizer (D58).
const SearchFTSFoldingTokenizer = "unicode61 remove_diacritics 2"

// searchFTSRef is the ref token of an index row, of row's source table and
// key: an r and the hex of both with a unit separator between, one token
// of letters and digits, so a MATCH of ref finds the row's words alone.
func searchFTSRef(row string) string {
	return fmt.Sprintf("'r' || hex(%[1]s.source_table || char(31) || %[1]s.source_key)", row)
}

// SearchFTS is the search index's words on SQLite (D186): the FTS5 table
// and the three triggers on the index table that keep it, in the order
// create.sql runs them, and the down that drops them. quote quotes an
// identifier as the SQLite dialect does; index is the index table's name;
// fold removes diacritics (#740). The up drops what it makes first and
// fills the table from the index rows last, so it runs again over what it
// made, as a plan runs a changed piece, or every piece after it rebuilds a
// table, and the words it holds are the index's, whatever tokenizer they
// were read with before.
func SearchFTS(index string, quote func(string) string, fold bool) (up, down []string) {
	table := quote(index)
	fts := quote(SearchFTSTable)
	columns := make([]string, len(SearchFTSColumns))
	for i, c := range SearchFTSColumns {
		columns[i] = c
		if c == "source_table" || c == "source_key" {
			columns[i] += " UNINDEXED"
		}
	}
	insert := func(row string) string {
		return fmt.Sprintf("  INSERT INTO %s (%s)\n    VALUES (%s.title, %s.body, %s.comments, %s.files, %s, %s.source_table, %s.source_key);\n",
			fts, strings.Join(SearchFTSColumns, ", "), row, row, row, row, searchFTSRef(row), row, row)
	}
	remove := func(row string) string {
		return fmt.Sprintf("  DELETE FROM %s WHERE %s MATCH 'ref : \"' || %s || '\"';\n", fts, fts, searchFTSRef(row))
	}
	name := func(event string) string { return quote(SearchFTSTable + "_" + event) }
	tokenizer := SearchFTSTokenizer
	if fold {
		tokenizer = SearchFTSFoldingTokenizer
	}
	down = []string{
		"DROP TRIGGER IF EXISTS " + name("delete"),
		"DROP TRIGGER IF EXISTS " + name("update"),
		"DROP TRIGGER IF EXISTS " + name("insert"),
		"DROP TABLE IF EXISTS " + fts,
	}
	up = append(append([]string(nil), down...),
		fmt.Sprintf("CREATE VIRTUAL TABLE %s USING fts5(%s, tokenize = %s)", fts, strings.Join(columns, ", "), stringLiteral(tokenizer)),
		fmt.Sprintf("CREATE TRIGGER %s AFTER INSERT ON %s FOR EACH ROW\nBEGIN\n  -- An index row's words (D186).\n%sEND", name("insert"), table, insert("NEW")),
		fmt.Sprintf("CREATE TRIGGER %s AFTER UPDATE OF title, body, comments, files, source_table, source_key ON %s FOR EACH ROW\nBEGIN\n  -- An index row's words, written again (D186).\n%s%sEND", name("update"), table, remove("OLD"), insert("NEW")),
		fmt.Sprintf("CREATE TRIGGER %s AFTER DELETE ON %s FOR EACH ROW\nBEGIN\n  -- An index row's words, deleted with it (D186).\n%sEND", name("delete"), table, remove("OLD")),
		fmt.Sprintf("INSERT INTO %s (%s)\n  SELECT si.title, si.body, si.comments, si.files, %s, si.source_table, si.source_key FROM %s AS si",
			fts, strings.Join(SearchFTSColumns, ", "), searchFTSRef("si"), table),
	)
	return up, down
}

// sqliteEnqueue is the statement that enqueues the row whose key is key,
// as text, when every condition of when holds, unless it holds a ready
// message: an INSERT ... SELECT, whose WHERE SQLite needs to read the ON
// CONFLICT that follows as the upsert's.
func (q SearchQueue) sqliteEnqueue(quote func(string) string, origin, key string, when []string) string {
	columns := make([]string, len(q.Columns))
	for i, c := range q.Columns {
		columns[i] = quote(unquoted(c))
	}
	where := "1"
	if len(when) > 0 {
		where = strings.Join(when, "\n    AND ")
	}
	s := fmt.Sprintf("  INSERT INTO %s (%s)\n    SELECT %s, CAST(%s AS TEXT) WHERE %s", quote(unquoted(q.Table)), strings.Join(columns, ", "), stringLiteral(origin), key, where)
	if q.SQLiteConflict != "" {
		s += "\n    " + q.SQLiteConflict
	}
	return s + ";\n"
}

// SQLiteTriggers renders the table's triggers on SQLite: one after each
// insert, update and delete of a row. quote quotes an identifier as the
// SQLite dialect does.
func (t SearchTrigger) SQLiteTriggers(quote func(string) string) []SQLiteTrigger {
	q := func(s string) string { return quote(unquoted(s)) }
	table, index, key := quote(t.Table), q(t.Index), q(t.Key)
	where := func(row string) string {
		return fmt.Sprintf("%s = %s AND %s = CAST(%s.%s AS TEXT)", q(t.IndexTable), stringLiteral(t.Origin), q(t.IndexKey), row, key)
	}
	comment := fmt.Sprintf("  -- The search index row of a row of %s (D155, D186).\n", t.Origin)

	var insert strings.Builder
	fmt.Fprintf(&insert, "CREATE TRIGGER %s AFTER INSERT ON %s FOR EACH ROW\n", quote(t.TriggerName+"_insert"), table)
	if t.DeletedAt != "" {
		fmt.Fprintf(&insert, "WHEN NEW.%s IS NULL\n", q(t.DeletedAt))
	}
	fmt.Fprintf(&insert, "BEGIN\n%s%sEND", comment, t.Queue.sqliteEnqueue(quote, t.Origin, "NEW."+key, nil))

	var update strings.Builder
	fmt.Fprintf(&update, "CREATE TRIGGER %s AFTER UPDATE ON %s FOR EACH ROW\nBEGIN\n%s", quote(t.TriggerName+"_update"), table, comment)
	fmt.Fprintf(&update, "  DELETE FROM %s WHERE %s AND OLD.%s IS NOT NEW.%s;\n", index, where("OLD"), key, key)
	live := []string{}
	if t.DeletedAt != "" {
		deleted := q(t.DeletedAt)
		fmt.Fprintf(&update, "  DELETE FROM %s WHERE %s AND NEW.%s IS NOT NULL;\n", index, where("NEW"), deleted)
		live = append(live, fmt.Sprintf("NEW.%s IS NULL", deleted))
	}
	if t.Effective != "" {
		effective, agents := q(t.Effective), q(t.EffectiveAgents)
		audience, indexAgents := q(t.IndexAudience), q(t.IndexAgents)
		fmt.Fprintf(&update, "  UPDATE %s SET %s = NEW.%s, %s = NEW.%s\n    WHERE %s\n      AND (%s IS NOT NEW.%s OR %s IS NOT NEW.%s);\n",
			index, audience, effective, indexAgents, agents, where("NEW"), audience, effective, indexAgents, agents)
	}
	changed := []string{fmt.Sprintf("OLD.%s IS NOT NEW.%s", key, key)}
	if t.DeletedAt != "" {
		changed = append(changed, fmt.Sprintf("OLD.%s IS NOT NULL", q(t.DeletedAt)))
	}
	for _, c := range t.Watched {
		changed = append(changed, fmt.Sprintf("OLD.%s IS NOT NEW.%s", q(c), q(c)))
	}
	when := append(live, "("+strings.Join(changed, "\n      OR ")+")")
	update.WriteString(t.Queue.sqliteEnqueue(quote, t.Origin, "NEW."+key, when))
	update.WriteString("END")

	deleteTrigger := fmt.Sprintf("CREATE TRIGGER %s AFTER DELETE ON %s FOR EACH ROW\nBEGIN\n%s  DELETE FROM %s WHERE %s;\nEND",
		quote(t.TriggerName+"_delete"), table, comment, index, where("OLD"))

	return []SQLiteTrigger{
		{Name: t.TriggerName + "_insert", Table: t.Table, Definition: insert.String()},
		{Name: t.TriggerName + "_update", Table: t.Table, Definition: update.String()},
		{Name: t.TriggerName + "_delete", Table: t.Table, Definition: deleteTrigger},
	}
}

// SQLiteTriggers renders the sub-table's triggers on SQLite: one after
// each insert, update and delete. quote quotes an identifier as the SQLite
// dialect does.
func (c SearchChildTrigger) SQLiteTriggers(quote func(string) string) []SQLiteTrigger {
	q := func(s string) string { return quote(unquoted(s)) }
	table, row := quote(c.Table), q(c.RowColumn)
	comment := fmt.Sprintf("  -- The row of %s whose text the write of %s changed (D155, D186).\n", c.Row, c.Origin)
	trigger := func(event, body string) SQLiteTrigger {
		name := c.TriggerName + "_" + event
		return SQLiteTrigger{Name: name, Table: c.Table, Definition: fmt.Sprintf("CREATE TRIGGER %s AFTER %s ON %s FOR EACH ROW\nBEGIN\n%s%sEND", quote(name), strings.ToUpper(event), table, comment, body)}
	}
	changed := make([]string, len(c.Watched))
	for i, w := range c.Watched {
		changed[i] = fmt.Sprintf("OLD.%s IS NOT NEW.%s", q(w), q(w))
	}
	moved := fmt.Sprintf("OLD.%s IS NOT NEW.%s", row, row)
	update := c.Queue.sqliteEnqueue(quote, c.Row, "NEW."+row, []string{"(" + strings.Join(append(changed, moved), " OR ") + ")"}) +
		c.Queue.sqliteEnqueue(quote, c.Row, "OLD."+row, []string{moved})
	return []SQLiteTrigger{
		trigger("insert", c.Queue.sqliteEnqueue(quote, c.Row, "NEW."+row, nil)),
		trigger("update", update),
		trigger("delete", c.Queue.sqliteEnqueue(quote, c.Row, "OLD."+row, nil)),
	}
}
