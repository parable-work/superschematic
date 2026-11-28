package crosstable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

/*
Search across tables on SQLite (D186, #615), as the TypeScript runtime's
searchOnSQLite and indexOnSQLite: the index is the same table, its
audience a JSON array of ids (D182), and its words an FTS5 table beside it
(Searchable.FTS, search_index_fts), which the index table's triggers keep
in the statement that writes an index row.

- The words: each word with a letter or a digit is a quoted FTS5 phrase of
  the words the unicode61 tokenizer finds in it (folded to lower case, no
  diacritic removed and none stemmed, as the simple configuration reads
  them), every one required, and only in the four texts.
- The order: bm25 with the weights Postgres gives the title, the body, the
  conversation and the files (1, 0.4, 0.2, 0.1), best first, then the table
  and the key. A hit's Matched is the first of the four texts that holds
  every word, and its snippet FTS5's of that text.
- Access is checked inside the query as on Postgres: the caller's
  audience through json_each over the index row's copy
  (Searchable.IndexAudience, sqlutil.SQLiteAudienceContains), and a grant's
  reach by an EXISTS back to each table with the conditions its repository
  applies on SQLite (Host.Conditions); a table no grant reaches is left out.
- Embeddings are Postgres's alone: a search or an indexing on SQLite that
  asks for them is refused.
*/

// SearchIndexAudience is how a search on SQLite compares an index row's
// audience, a JSON array: the condition that it holds one of a list of
// parameters, its two %s the column and the list, and a parameter, its %d
// the number (sqlutil.SQLiteAudienceContains, SQLiteAudiencePlaceholder).
type SearchIndexAudience struct {
	Contains    string `json:"contains"`
	Placeholder string `json:"placeholder"`
}

// The SQLite audience formats a definition with none takes, sqlutil's.
const (
	sqliteAudienceContains    = "EXISTS (SELECT 1 FROM json_each(%s) WHERE json_each.value IN (%s))"
	sqliteAudiencePlaceholder = "?%d"
)

// ftsWeights are bm25's weights of the FTS5 table's columns on SQLite: the
// title, the body, the conversation and the files as Postgres weighs A to
// D, and none for the row's ref, table and key (D186).
const ftsWeights = "1.0, 0.4, 0.2, 0.1, 0.0, 0.0, 0.0"

// ftsTexts are the texts a search on SQLite matches, the FTS5 table's first
// four columns, in the order a hit's Matched names them.
var ftsTexts = []string{"title", "body", "comments", "files"}

// ErrSQLiteEmbeddings is the refusal of embeddings on SQLite, which
// re-rank and are stored on Postgres alone (D186).
var ErrSQLiteEmbeddings = errors.New("embeddings re-rank and are stored on Postgres alone; a search on SQLite and D1 ranks by its words (D186)")

// ftsPhrases are a query's words as an FTS5 expression, each a quoted
// phrase, every one required; "" for words with no letter or digit, which
// match nothing on Postgres either.
func ftsPhrases(words []string) string {
	var phrases []string
	for _, word := range words {
		if !strings.ContainsFunc(word, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
			continue
		}
		phrases = append(phrases, `"`+strings.ReplaceAll(word, `"`, `""`)+`"`)
	}
	return strings.Join(phrases, " ")
}

// sqliteAudienceConditions are audienceConditions on SQLite: the index
// row's copied audience, a JSON array, held to the caller's ids through
// json_each.
func sqliteAudienceConditions(effective, agents string, formats *SearchIndexAudience, caller access.Principal, args *[]any) []string {
	contains, placeholder := sqliteAudienceContains, sqliteAudiencePlaceholder
	if formats != nil {
		contains, placeholder = formats.Contains, formats.Placeholder
	}
	subjects := []string{caller.Subject}
	if caller.Kind == access.KindAgent && caller.ActingFor != nil {
		subjects = append(subjects, caller.ActingFor.Subject)
	}
	var placeholders []string
	for _, subject := range subjects {
		id := rowrefs.UUIDToDB(subject)
		if subject == "" || id == "" {
			continue
		}
		*args = append(*args, id)
		placeholders = append(placeholders, fmt.Sprintf(placeholder, len(*args)))
	}
	part := effective + " IS NULL"
	if len(placeholders) > 0 {
		part = "(" + effective + " IS NULL OR " + fmt.Sprintf(contains, effective, strings.Join(placeholders, ", ")) + ")"
	}
	parts := []string{part}
	if caller.Kind == access.KindAgent {
		parts = append(parts, agents+" <> 'none'")
	}
	return []string{strings.Join(parts, " AND ")}
}

// searchOnSQLite is SearchTables on SQLite (D186): the rows whose FTS5
// words hold every word, of those caller may read, ranked by bm25, one page
// of them and how many there are, each with where the words matched first
// and a snippet of that text.
func searchOnSQLite(ctx context.Context, h Host, caller Caller, words []string, sources []string, all map[string]Searchable, scope []string, limit, offset int) (SearchResult, error) {
	empty := SearchResult{Items: []SearchHit{}}
	phrases := ftsPhrases(words)
	if phrases == "" {
		return empty, nil
	}
	first := all[sources[0]]
	fts := first.FTS
	if fts == "" {
		fts = `"search_index_fts"`
	}
	as := h.As(ctx, caller.Principal)
	var args []any
	param := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("?%d", len(args))
	}
	conditions := []string{fts + " MATCH " + param("{"+strings.Join(ftsTexts, " ")+"} : ("+phrases+")")}
	if caller.Principal.Grants == nil {
		names := make([]string, len(sources))
		for i, table := range sources {
			names[i] = param(table)
		}
		conditions = append(conditions, "si.source_table IN ("+strings.Join(names, ", ")+")")
	} else {
		var branches []string
		for _, table := range sources {
			definition := all[table]
			kept := h.Conditions(as, table, &args, len(args))
			if len(kept) == 1 && kept[0] == "FALSE" {
				continue
			}
			name := param(table)
			match := fmt.Sprintf("%s.%s = si.source_key", definition.Table, definition.Key)
			branches = append(branches, fmt.Sprintf("(si.source_table = %s AND EXISTS (SELECT 1 FROM %s WHERE %s))", name, definition.Table, strings.Join(append([]string{match}, kept...), " AND ")))
		}
		if len(branches) == 0 {
			return empty, nil
		}
		conditions = append(conditions, "("+strings.Join(branches, " OR ")+")")
	}
	conditions = append(conditions, sqliteAudienceConditions("si.audience", "si.agents", first.IndexAudience, caller.Principal, &args)...)
	if scope != nil {
		var forms []string
		for _, value := range scope {
			if !slices.Contains(forms, value) {
				forms = append(forms, value)
			}
			if db := rowrefs.UUIDToDB(value); db != "" && !slices.Contains(forms, db) {
				forms = append(forms, db)
			}
		}
		placeholders := make([]string, len(forms))
		for i, form := range forms {
			placeholders[i] = param(form)
		}
		conditions = append(conditions, "si.scope IN ("+strings.Join(placeholders, ", ")+")")
	}
	sql := `WITH hits AS (
      SELECT si.source_table, si.source_key, si.title, ` + fts + `.rowid AS fts, bm25(` + fts + `, ` + ftsWeights + `) AS rank
      FROM ` + fts + ` JOIN ` + first.Index + ` AS si ON si.source_table = ` + fts + `.source_table AND si.source_key = ` + fts + `.source_key
      WHERE ` + strings.Join(conditions, " AND ") + `
    ),
    ranked AS (
      SELECT h.*, row_number() OVER (ORDER BY h.rank, h.source_table, h.source_key) AS place FROM hits AS h
    )
    SELECT c.total, p.source_table, p.source_key, p.title, p.fts, p.place
    FROM (SELECT count(*) AS total FROM hits) AS c
    LEFT JOIN (SELECT * FROM ranked ORDER BY place LIMIT ` + fmt.Sprint(limit) + ` OFFSET ` + fmt.Sprint(offset) + `) AS p ON 1
    ORDER BY p.place`
	system := h.System(ctx)
	rows, err := h.Query(system, sql, args...)
	if err != nil {
		return empty, err
	}
	result := SearchResult{Items: []SearchHit{}}
	if len(rows) > 0 {
		result.Total = int(integer(rows[0], "total"))
	}
	var page []Row
	for _, row := range rows {
		if row["source_table"] != nil {
			page = append(page, row)
		}
	}
	// Where the words matched first, and a snippet of each text, for the
	// page.
	matched := map[int64]int{}
	snippets := map[int64]Row{}
	if len(page) > 0 {
		ids := make([]any, len(page))
		for i, row := range page {
			ids[i] = integer(row, "fts")
		}
		among := func(from int) string {
			out := make([]string, len(ids))
			for i := range ids {
				out[i] = fmt.Sprintf("?%d", from+i)
			}
			return strings.Join(out, ", ")
		}
		var texts []any
		var branches []string
		for _, column := range ftsTexts {
			texts = append(texts, column+" : ("+phrases+")")
			at := len(texts)
			texts = append(texts, ids...)
			branches = append(branches, fmt.Sprintf("SELECT '%s' AS text, rowid AS fts FROM %s WHERE %s MATCH ?%d AND rowid IN (%s)", column, fts, fts, at, among(at+1)))
		}
		found, err := h.Query(system, strings.Join(branches, " UNION ALL "), texts...)
		if err != nil {
			return empty, err
		}
		for _, row := range found {
			id := integer(row, "fts")
			place := slices.Index(ftsTexts, text(row, "text"))
			if current, ok := matched[id]; !ok || place < current {
				matched[id] = place
			}
		}
		parts := make([]string, len(ftsTexts))
		for i := range ftsTexts {
			parts[i] = fmt.Sprintf("snippet(%s, %d, ?2, ?3, '', 24) AS s%d", fts, i, i)
		}
		read, err := h.Query(system, fmt.Sprintf("SELECT rowid AS fts, %s FROM %s WHERE %s MATCH ?1 AND rowid IN (%s)", strings.Join(parts, ", "), fts, fts, among(4)),
			append([]any{"{" + strings.Join(ftsTexts, " ") + "} : (" + phrases + ")", openMark, closeMark}, ids...)...)
		if err != nil {
			return empty, err
		}
		for _, row := range read {
			snippets[integer(row, "fts")] = row
		}
	}
	for i, row := range page {
		table := text(row, "source_table")
		id := integer(row, "fts")
		where, ok := matched[id]
		if !ok {
			where = 0
		}
		result.Items = append(result.Items, SearchHit{
			Table:   table,
			Key:     wireKey(all[table].UUID, text(row, "source_key")),
			Title:   text(row, "title"),
			Matched: ftsTexts[where],
			Snippet: snippetParts(text(snippets[id], fmt.Sprintf("s%d", where))),
			Place:   offset + i + 1,
		})
	}
	result.More = offset+len(result.Items) < result.Total
	return result, nil
}

// indexOnSQLite is IndexSearch on SQLite (D186): the row read by its key,
// its conversation's and files' text in their order, and its index row
// written with the row's access columns, read by the write itself, or
// deleted for a row that is gone or soft deleted. The index table's
// triggers write its words into the FTS5 table in the same statement. It
// answers whether the row had an index row to write.
func indexOnSQLite(ctx context.Context, h Host, d Searchable, message SearchIndexing) (bool, error) {
	written := false
	err := h.Transaction(ctx, func(tx Host) error {
		columns := []string{d.Title + " AS title"}
		for i, c := range d.Body {
			columns = append(columns, fmt.Sprintf("%s AS b%d", c, i))
		}
		columns = append(columns, "CAST("+d.Key+" AS TEXT) AS key")
		if d.Scope != "" {
			columns = append(columns, "CAST("+d.Scope+" AS TEXT) AS scope")
		}
		if d.DeletedAt != "" {
			columns = append(columns, d.DeletedAt+" IS NOT NULL AS deleted")
		}
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?1", strings.Join(columns, ", "), d.Table, d.Key), message.SourceKey)
		if err != nil {
			return err
		}
		if len(rows) == 0 || integer(rows[0], "deleted") == 1 {
			_, err := tx.Exec(ctx, "DELETE FROM "+d.Index+" WHERE source_table = ?1 AND source_key = ?2", message.SourceTable, message.SourceKey)
			return err
		}
		row := rows[0]
		title, _ := clean(row["title"])
		var parts []string
		for j := range d.Body {
			if v := row[fmt.Sprintf("b%d", j)]; v != nil {
				parts = append(parts, text(Row{"v": v}, "v"))
			}
		}
		body, hasBody := clean(nilIfEmpty(parts))
		aggregate := func(sql string) (string, bool, error) {
			read, err := tx.Query(ctx, sql, message.SourceKey)
			if err != nil || len(read) == 0 {
				return "", false, err
			}
			s, ok := clean(read[0]["text"])
			return s, ok, nil
		}
		var comment, file string
		var hasComments, hasFiles bool
		if c := d.Comments; c != nil {
			if comment, hasComments, err = aggregate(fmt.Sprintf("SELECT group_concat(text, char(10)) AS text FROM (SELECT %s AS text FROM %s WHERE %s = ?1 AND %s IN ('posted', 'interrupted') AND %s <> '' ORDER BY %s)", c.Body, c.Table, c.Row, c.State, c.Body, c.Order)); err != nil {
				return err
			}
		}
		if f := d.Files; f != nil {
			if file, hasFiles, err = aggregate(fmt.Sprintf("SELECT group_concat(text, char(10)) AS text FROM (SELECT %s AS text FROM %s WHERE %s = ?1 AND %s = 'ready' AND %s IS NOT NULL ORDER BY %s)", f.Text, f.Table, f.Row, f.State, f.Text, f.Text)); err != nil {
				return err
			}
		}
		texts := []string{title}
		for _, t := range []struct {
			s  string
			ok bool
		}{{body, hasBody}, {comment, hasComments}, {file, hasFiles}} {
			if t.ok {
				texts = append(texts, t.s)
			}
		}
		sum := sha256.Sum256([]byte(strings.Join(texts, "\n")))
		hash := hex.EncodeToString(sum[:])
		var scope any
		if d.Scope != "" && row["scope"] != nil {
			scope = text(row, "scope")
		}
		audience, agents := "NULL", "'followRole'"
		if a := d.Audience; a != nil {
			audience = "s." + lastPart(a.Effective)
			agents = "s." + lastPart(a.Agents)
		}
		_, err = tx.Exec(ctx, `INSERT INTO `+d.Index+` (source_table, source_key, title, body, comments, files, scope, audience, agents, text_hash, indexed_at)
       SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, `+audience+`, `+agents+`, ?8, `+sqliteNow+` FROM `+d.Table+` AS s WHERE s.`+d.Key+` = ?9
       ON CONFLICT (source_table, source_key) DO UPDATE SET
         title = excluded.title, body = excluded.body, comments = excluded.comments, files = excluded.files, scope = excluded.scope,
         audience = excluded.audience, agents = excluded.agents, indexed_at = excluded.indexed_at,
         embedding = CASE WHEN `+d.Index+`.text_hash = excluded.text_hash THEN `+d.Index+`.embedding END,
         embedding_model = CASE WHEN `+d.Index+`.text_hash = excluded.text_hash THEN `+d.Index+`.embedding_model END,
         text_hash = excluded.text_hash`,
			message.SourceTable, text(row, "key"), title, nullable(body, hasBody), nullable(comment, hasComments), nullable(file, hasFiles), scope, hash, message.SourceKey)
		if err != nil {
			return err
		}
		written = true
		return nil
	})
	return written, err
}

// reindexOnSQLite is ReindexSearch's pages of a table on SQLite: the next
// page starts after the key as the table holds it, and each row is
// indexed by its own statements (indexOnSQLite).
func reindexOnSQLite(ctx context.Context, h Host, table string, d Searchable, batch int) (int, error) {
	count := 0
	var after any
	for {
		sql := fmt.Sprintf("SELECT CAST(%s AS TEXT) AS key, %s AS k FROM %s ORDER BY %s LIMIT %d", d.Key, d.Key, d.Table, d.Key, batch)
		var args []any
		if after != nil {
			sql = fmt.Sprintf("SELECT CAST(%s AS TEXT) AS key, %s AS k FROM %s WHERE %s > ?1 ORDER BY %s LIMIT %d", d.Key, d.Key, d.Table, d.Key, d.Key, batch)
			args = append(args, after)
		}
		rows, err := h.Query(ctx, sql, args...)
		if err != nil {
			return count, err
		}
		for _, row := range rows {
			if _, err := indexOnSQLite(ctx, h, d, SearchIndexing{SourceTable: table, SourceKey: text(row, "key")}); err != nil {
				return count, err
			}
		}
		count += len(rows)
		if len(rows) < batch {
			return count, nil
		}
		after = rows[len(rows)-1]["k"]
	}
}
