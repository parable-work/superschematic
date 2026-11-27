package crosstable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Indexed is what IndexSearch did with a message's row.
type Indexed string

// What IndexSearch did: wrote the row's index row, removed it (the row is
// gone or soft deleted), or nothing, for a table that is not @searchable.
const (
	IndexedRow     Indexed = "indexed"
	IndexedRemoved Indexed = "removed"
	IndexedUnknown Indexed = "unknown"
)

// clean is an index row's text: its marks made spaces, at most
// MaxIndexedText characters, and false for one with no text.
func clean(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	s := fmt.Sprint(value)
	if b, ok := value.([]byte); ok {
		s = string(b)
	}
	s = strings.Map(func(r rune) rune {
		if r == 0 || r == 2 || r == 3 {
			return ' '
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > MaxIndexedText {
		s = string([]rune(s)[:MaxIndexedText])
	}
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

func nullable(s string, ok bool) any {
	if !ok {
		return nil
	}
	return s
}

// IndexSearch builds the index row of the row a @searchable declaration's
// queue names (D155), as the system, which its worker calls for each
// message: the row's title, body, scope, conversation and files' text,
// and its access columns, read under a share lock of the row, so a change
// of its audience waits for it and copies into the row it writes. A row
// that is gone, or soft deleted, has its index row deleted. It runs in a
// transaction of its own; with embeddings on, a row whose text changed is
// embedded after it commits, and its embedding stored only while its text
// is still what was embedded.
func IndexSearch(ctx context.Context, h Host, message SearchIndexing, options SearchOptions) (Indexed, error) {
	ctx = h.System(ctx)
	d, ok := h.Definitions().Searchable[message.SourceTable]
	if !ok {
		return IndexedUnknown, nil
	}
	const where = `source_table = $1 AND source_key = $2`
	type written struct{ key, text, hash, model string }
	var w *written
	err := h.Transaction(ctx, func(tx Host) error {
		columns := []string{d.Title + " AS title"}
		for i, c := range d.Body {
			columns = append(columns, fmt.Sprintf("%s AS b%d", c, i))
		}
		columns = append(columns, d.Key+"::text AS key")
		if d.Scope != "" {
			columns = append(columns, d.Scope+"::text AS scope")
		}
		if d.DeletedAt != "" {
			columns = append(columns, d.DeletedAt+" IS NOT NULL AS deleted")
		}
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s = $1::%s FOR SHARE", strings.Join(columns, ", "), d.Table, d.Key, d.KeyType), message.SourceKey)
		if err != nil {
			return err
		}
		if len(rows) == 0 || rows[0]["deleted"] == true {
			_, err := tx.Exec(ctx, "DELETE FROM "+d.Index+" WHERE "+where, message.SourceTable, message.SourceKey)
			return err
		}
		row := rows[0]
		title, _ := clean(row["title"])
		var parts []string
		for i := range d.Body {
			if v := row[fmt.Sprintf("b%d", i)]; v != nil {
				parts = append(parts, fmt.Sprint(v))
			}
		}
		body, hasBody := clean(nilIfEmpty(parts))
		var comments, files string
		var hasComments, hasFiles bool
		if c := d.Comments; c != nil {
			read, err := tx.Query(ctx, fmt.Sprintf(`SELECT string_agg(%s, E'\n' ORDER BY %s) AS text FROM %s WHERE %s = $1::%s AND %s IN ('posted', 'interrupted') AND %s <> ''`, c.Body, c.Order, c.Table, c.Row, d.KeyType, c.State, c.Body), message.SourceKey)
			if err != nil {
				return err
			}
			if len(read) > 0 {
				comments, hasComments = clean(read[0]["text"])
			}
		}
		if f := d.Files; f != nil {
			read, err := tx.Query(ctx, fmt.Sprintf(`SELECT string_agg(%s, E'\n' ORDER BY %s) AS text FROM %s WHERE %s = $1::%s AND %s = 'ready' AND %s IS NOT NULL`, f.Text, f.Text, f.Table, f.Row, d.KeyType, f.State, f.Text), message.SourceKey)
			if err != nil {
				return err
			}
			if len(read) > 0 {
				files, hasFiles = clean(read[0]["text"])
			}
		}
		texts := []string{title}
		for _, t := range []struct {
			s  string
			ok bool
		}{{body, hasBody}, {comments, hasComments}, {files, hasFiles}} {
			if t.ok {
				texts = append(texts, t.s)
			}
		}
		all := strings.Join(texts, "\n")
		sum := sha256.Sum256([]byte(all))
		hash := hex.EncodeToString(sum[:])
		key := text(row, "key")
		// The access columns come from the row itself, which the share
		// lock holds as it is until the index row is written.
		audience, agents := "NULL::uuid[]", "'followRole'"
		if a := d.Audience; a != nil {
			audience = "s." + lastPart(a.Effective)
			agents = "s." + lastPart(a.Agents)
		}
		var scope any
		if d.Scope != "" {
			scope = row["scope"]
		}
		stored, err := tx.Query(ctx, `INSERT INTO `+d.Index+` AS i (source_table, source_key, title, body, comments, files, scope, audience, agents, text_hash, indexed_at)
       SELECT $1::text, $2::text, $3::text, $4::text, $5::text, $6::text, $7::text, `+audience+`, `+agents+`, $8::text, now() FROM `+d.Table+` AS s WHERE s.`+d.Key+` = $9::`+d.KeyType+`
       ON CONFLICT (source_table, source_key) DO UPDATE SET
         title = EXCLUDED.title, body = EXCLUDED.body, comments = EXCLUDED.comments, files = EXCLUDED.files, scope = EXCLUDED.scope,
         audience = EXCLUDED.audience, agents = EXCLUDED.agents, indexed_at = EXCLUDED.indexed_at,
         embedding = CASE WHEN i.text_hash = EXCLUDED.text_hash THEN i.embedding END,
         embedding_model = CASE WHEN i.text_hash = EXCLUDED.text_hash THEN i.embedding_model END,
         text_hash = EXCLUDED.text_hash
       RETURNING embedding_model`,
			message.SourceTable, key, title, nullable(body, hasBody), nullable(comments, hasComments), nullable(files, hasFiles), scope, hash, key)
		if err != nil {
			return err
		}
		w = &written{key: key, text: all, hash: hash}
		if len(stored) > 0 {
			w.model = text(stored[0], "embedding_model")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if w == nil {
		return IndexedRemoved, nil
	}
	if e := options.Embeddings; e != nil && e.Embedder != nil && w.model != e.Model {
		most := e.MaxChars
		if most <= 0 {
			most = 8000
		}
		embedded := w.text
		if utf8.RuneCountInString(embedded) > most {
			embedded = string([]rune(embedded)[:most])
		}
		vectors, err := e.Embedder.Embed(ctx, []string{embedded})
		if err != nil {
			return "", err
		}
		if len(vectors) > 0 && len(vectors[0]) > 0 {
			vector, err := unit(vectors[0])
			if err != nil {
				return "", err
			}
			if _, err := h.Exec(ctx, "UPDATE "+d.Index+" SET embedding = $3::float8[], embedding_model = $4::text WHERE "+where+" AND text_hash = $5::text", message.SourceTable, w.key, vector, e.Model, w.hash); err != nil {
				return "", err
			}
		}
	}
	return IndexedRow, nil
}

func nilIfEmpty(parts []string) any {
	if len(parts) == 0 {
		return nil
	}
	return strings.Join(parts, "\n")
}

// lastPart is a qualified column's own name.
func lastPart(column string) string {
	if i := strings.LastIndex(column, "."); i >= 0 {
		return column[i+1:]
	}
	return column
}

// ReindexOptions is what ReindexSearch takes beside the search's options:
// only these tables, every @searchable one when nil; and the rows a read
// takes, 500 when zero.
type ReindexOptions struct {
	SearchOptions
	Tables []string
	Batch  int
}

// ReindexSearch builds the index row of every row of the database's
// @searchable tables, or of Tables, as IndexSearch does one, in key
// order, Batch rows a read: what a table that became @searchable with
// rows in it needs once, and what a change of embeddings' model needs. It
// answers how many rows it indexed.
func ReindexSearch(ctx context.Context, h Host, options ReindexOptions) (int, error) {
	ctx = h.System(ctx)
	batch := options.Batch
	if batch <= 0 {
		batch = 500
	}
	count := 0
	defs := h.Definitions().Searchable
	for _, table := range sortedKeys(defs) {
		if options.Tables != nil && !slices.Contains(options.Tables, table) {
			continue
		}
		d := defs[table]
		after := ""
		for {
			sql := fmt.Sprintf("SELECT %s::text AS key FROM %s ORDER BY %s LIMIT %d", d.Key, d.Table, d.Key, batch)
			var args []any
			if after != "" {
				sql = fmt.Sprintf("SELECT %s::text AS key FROM %s WHERE %s > $1::%s ORDER BY %s LIMIT %d", d.Key, d.Table, d.Key, d.KeyType, d.Key, batch)
				args = append(args, after)
			}
			rows, err := h.Query(ctx, sql, args...)
			if err != nil {
				return count, err
			}
			for _, row := range rows {
				if _, err := IndexSearch(ctx, h, SearchIndexing{SourceTable: table, SourceKey: text(row, "key")}, options.SearchOptions); err != nil {
					return count, err
				}
				count++
			}
			if len(rows) < batch {
				break
			}
			after = text(rows[len(rows)-1], "key")
		}
	}
	return count, nil
}
