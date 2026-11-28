package crosstable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strconv"
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

func nullable(s string, ok bool) *string {
	if !ok {
		return nil
	}
	return &s
}

// IndexSearch builds the index row of the row a @searchable declaration's
// queue names (D155), as the system, which its worker calls for each
// message: the row's title, body, scope, conversation and files' text,
// and its access columns, read under a share lock of the row, so a change
// of its audience waits for it and copies into the row it writes. A row
// that is gone, or soft deleted, has its index row deleted. It runs in a
// transaction of its own; with embeddings on, a row whose text changed is
// embedded after it commits, and its embedding stored only while its text
// is still what was embedded. It is indexRows of one key, which
// ReindexSearch runs a page of keys at a time.
func IndexSearch(ctx context.Context, h Host, message SearchIndexing, options SearchOptions) (Indexed, error) {
	ctx = h.System(ctx)
	d, ok := h.Definitions().Searchable[message.SourceTable]
	if !ok {
		return IndexedUnknown, nil
	}
	if onSQLite(h) {
		if options.Embeddings != nil {
			return "", fmt.Errorf("indexSearch: %w", ErrSQLiteEmbeddings)
		}
		written, err := indexOnSQLite(ctx, h, d, message)
		if err != nil {
			return "", err
		}
		if !written {
			return IndexedRemoved, nil
		}
		return IndexedRow, nil
	}
	indexed, err := indexRows(ctx, h, message.SourceTable, d, []string{message.SourceKey}, options)
	if err != nil {
		return "", err
	}
	if !indexed[message.SourceKey] {
		return IndexedRemoved, nil
	}
	return IndexedRow, nil
}

// indexRows builds the index rows of keys, rows of the @searchable table
// table, as IndexSearch does one, in as many statements for a page of keys
// as for one: in one transaction, a read of the rows under a share lock,
// one of their conversations and one of their files (string_agg, GROUP BY
// the row), a delete of the index rows of the rows gone or soft deleted
// and an upsert of the others; then, with embeddings on, one model call
// with the texts of the rows whose embedding is not of the model, and one
// update of their embeddings. Each key is deleted by the text it is given
// in, and indexed by its row's key's text. It answers which keys had a row
// to index.
func indexRows(ctx context.Context, h Host, table string, d Searchable, keys []string, options SearchOptions) (map[string]bool, error) {
	type written struct{ key, text, hash, model string }
	var writes []written
	indexed := map[string]bool{}
	given := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, seen := indexed[k]; !seen {
			indexed[k] = false
			given = append(given, k)
		}
	}
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
		// Each key's row, locked in the keys' order, beside the key as
		// given; a key with no row has none.
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT given.key AS given, r.* FROM unnest($1::text[]) WITH ORDINALITY AS given(key, n)
       CROSS JOIN LATERAL (SELECT %s FROM %s WHERE %s = given.key::%s FOR SHARE) AS r ORDER BY given.n`, strings.Join(columns, ", "), d.Table, d.Key, d.KeyType), given)
		if err != nil {
			return err
		}
		var live []Row
		seen := map[string]bool{}
		for _, row := range rows {
			if row["deleted"] == true {
				continue
			}
			indexed[text(row, "given")] = true
			// Two keys given of one row index it once.
			if key := text(row, "key"); !seen[key] {
				seen[key] = true
				live = append(live, row)
			}
		}
		var removed []string
		for _, k := range given {
			if !indexed[k] {
				removed = append(removed, k)
			}
		}
		if len(removed) > 0 {
			if _, err := tx.Exec(ctx, "DELETE FROM "+d.Index+" WHERE source_table = $1 AND source_key = ANY($2::text[])", table, removed); err != nil {
				return err
			}
		}
		if len(live) == 0 {
			return nil
		}
		liveKeys := make([]string, len(live))
		for i, row := range live {
			liveKeys[i] = text(row, "key")
		}
		// The conversations' and the files' text, of every row at once.
		aggregate := func(sql string) (map[string]any, error) {
			read, err := tx.Query(ctx, sql, liveKeys)
			if err != nil {
				return nil, err
			}
			out := make(map[string]any, len(read))
			for _, r := range read {
				out[text(r, "key")] = r["text"]
			}
			return out, nil
		}
		var comments, files map[string]any
		if c := d.Comments; c != nil {
			if comments, err = aggregate(fmt.Sprintf(`SELECT %s::text AS key, string_agg(%s, E'\n' ORDER BY %s) AS text FROM %s WHERE %s = ANY($1::text[]::%s[]) AND %s IN ('posted', 'interrupted') AND %s <> '' GROUP BY %s`, c.Row, c.Body, c.Order, c.Table, c.Row, d.KeyType, c.State, c.Body, c.Row)); err != nil {
				return err
			}
		}
		if f := d.Files; f != nil {
			if files, err = aggregate(fmt.Sprintf(`SELECT %s::text AS key, string_agg(%s, E'\n' ORDER BY %s) AS text FROM %s WHERE %s = ANY($1::text[]::%s[]) AND %s = 'ready' AND %s IS NOT NULL GROUP BY %s`, f.Row, f.Text, f.Text, f.Table, f.Row, d.KeyType, f.State, f.Text, f.Row)); err != nil {
				return err
			}
		}
		n := len(live)
		titles, hashes := make([]string, n), make([]string, n)
		bodies, commentTexts, fileTexts, scopes := make([]*string, n), make([]*string, n), make([]*string, n), make([]*string, n)
		byKey := make(map[string]int, n)
		for i, row := range live {
			key := liveKeys[i]
			title, _ := clean(row["title"])
			var parts []string
			for j := range d.Body {
				if v := row[fmt.Sprintf("b%d", j)]; v != nil {
					parts = append(parts, fmt.Sprint(v))
				}
			}
			body, hasBody := clean(nilIfEmpty(parts))
			comment, hasComments := clean(comments[key])
			file, hasFiles := clean(files[key])
			texts := []string{title}
			for _, t := range []struct {
				s  string
				ok bool
			}{{body, hasBody}, {comment, hasComments}, {file, hasFiles}} {
				if t.ok {
					texts = append(texts, t.s)
				}
			}
			all := strings.Join(texts, "\n")
			sum := sha256.Sum256([]byte(all))
			hash := hex.EncodeToString(sum[:])
			titles[i], hashes[i] = title, hash
			bodies[i], commentTexts[i], fileTexts[i] = nullable(body, hasBody), nullable(comment, hasComments), nullable(file, hasFiles)
			if d.Scope != "" && row["scope"] != nil {
				scope := text(row, "scope")
				scopes[i] = &scope
			}
			byKey[key] = i
			writes = append(writes, written{key: key, text: all, hash: hash})
		}
		// The access columns come from the row itself, which the share
		// lock holds as it is until the index row is written.
		audience, agents := "NULL::uuid[]", "'followRole'"
		if a := d.Audience; a != nil {
			audience = "s." + lastPart(a.Effective)
			agents = "s." + lastPart(a.Agents)
		}
		stored, err := tx.Query(ctx, `INSERT INTO `+d.Index+` AS i (source_table, source_key, title, body, comments, files, scope, audience, agents, text_hash, indexed_at)
       SELECT $1::text, v.key, v.title, v.body, v.comments, v.files, v.scope, `+audience+`, `+agents+`, v.hash, now()
       FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[]) AS v(key, title, body, comments, files, scope, hash), `+d.Table+` AS s
       WHERE s.`+d.Key+` = v.key::`+d.KeyType+`
       ON CONFLICT (source_table, source_key) DO UPDATE SET
         title = EXCLUDED.title, body = EXCLUDED.body, comments = EXCLUDED.comments, files = EXCLUDED.files, scope = EXCLUDED.scope,
         audience = EXCLUDED.audience, agents = EXCLUDED.agents, indexed_at = EXCLUDED.indexed_at,
         embedding = CASE WHEN i.text_hash = EXCLUDED.text_hash THEN i.embedding END,
         embedding_model = CASE WHEN i.text_hash = EXCLUDED.text_hash THEN i.embedding_model END,
         text_hash = EXCLUDED.text_hash
       RETURNING source_key, embedding_model`,
			table, liveKeys, titles, bodies, commentTexts, fileTexts, scopes, hashes)
		if err != nil {
			return err
		}
		for _, r := range stored {
			if i, ok := byKey[text(r, "source_key")]; ok {
				writes[i].model = text(r, "embedding_model")
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	e := options.Embeddings
	if e == nil || e.Embedder == nil {
		return indexed, nil
	}
	most := e.MaxChars
	if most <= 0 {
		most = 8000
	}
	var pending []written
	var texts []string
	for _, w := range writes {
		if w.model == e.Model {
			continue
		}
		embedded := w.text
		if utf8.RuneCountInString(embedded) > most {
			embedded = string([]rune(embedded)[:most])
		}
		pending = append(pending, w)
		texts = append(texts, embedded)
	}
	if len(texts) == 0 {
		return indexed, nil
	}
	vectors, err := e.Embedder.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	// Each embedding as an array literal, which unnest keeps whole; a
	// row the model gave none keeps none, and an embedding that is not
	// finite stops the rows after it, as one at a time would.
	var embeddedKeys, embeddedHashes, literals []string
	var failed error
	for i, w := range pending {
		if i >= len(vectors) || len(vectors[i]) == 0 {
			continue
		}
		vector, err := unit(vectors[i])
		if err != nil {
			failed = err
			break
		}
		embeddedKeys = append(embeddedKeys, w.key)
		embeddedHashes = append(embeddedHashes, w.hash)
		literals = append(literals, floatArray(vector))
	}
	if len(embeddedKeys) > 0 {
		if _, err := h.Exec(ctx, `UPDATE `+d.Index+` AS i SET embedding = v.embedding::float8[], embedding_model = $2::text
       FROM unnest($3::text[], $4::text[], $5::text[]) AS v(key, hash, embedding)
       WHERE i.source_table = $1::text AND i.source_key = v.key AND i.text_hash = v.hash`, table, e.Model, embeddedKeys, embeddedHashes, literals); err != nil {
			return nil, err
		}
	}
	if failed != nil {
		return nil, failed
	}
	return indexed, nil
}

// floatArray is a vector as a float8[] literal, each number in the fewest
// digits that read back as it.
func floatArray(vector []float64) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, x := range vector {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	}
	b.WriteByte('}')
	return b.String()
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
// order, Batch rows a read and an indexRows: what a table that became @searchable with
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
	for _, table := range slices.Sorted(maps.Keys(defs)) {
		if options.Tables != nil && !slices.Contains(options.Tables, table) {
			continue
		}
		d := defs[table]
		if onSQLite(h) {
			if options.Embeddings != nil {
				return count, fmt.Errorf("reindexSearch: %w", ErrSQLiteEmbeddings)
			}
			n, err := reindexOnSQLite(ctx, h, table, d, batch)
			count += n
			if err != nil {
				return count, err
			}
			continue
		}
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
			// A page's rows are indexed together: as many statements and
			// one model call whatever its size.
			if len(rows) > 0 {
				keys := make([]string, len(rows))
				for i, row := range rows {
					keys[i] = text(row, "key")
				}
				if _, err := indexRows(ctx, h, table, d, keys, options.SearchOptions); err != nil {
					return count, err
				}
				count += len(rows)
			}
			if len(rows) < batch {
				break
			}
			after = text(rows[len(rows)-1], "key")
		}
	}
	return count, nil
}
