package crosstable

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

/*
Search across tables (D155): one search finds a row of any @searchable
table, for a UI and for an agent alike.

The database keeps one index table. Each @searchable table's rows have a
row each in it: the record's table and key, its title, its body, the text
of its conversation (Comments) and its files' extracted text
(@attachments), its scope, an optional embedding, and its effective
audience and agent access (D156). A trigger on the table deletes the index
row with its row, and copies a change of the row's audience into it, in
the writer's transaction; every write that changes the indexed text
enqueues the row on the declaration's queue, whose worker calls
IndexSearch. So a search's access check is never behind the row, and its
text is behind it by the queue's drain.

SearchTables ranks every table's index rows in one query, so its pages and
its total are exact and a row the caller may not read never shows, as a gap
or otherwise:

- The words: the query is read into a plan (query.go, #740): quoted
  phrases, prefixes (wal*), excluded terms (-oak) and OR between terms,
  each term a phrase of the words Postgres's parser finds in it. A row
  matches when it holds a term of every group and no excluded term, in the
  definition's text search configuration (simple, or search_unaccent,
  which folds diacritics).
- The order: ts_rank_cd over the row's words, weighed by where each is
  (title A, body B, conversation C, files D), then the table and the key.
  With an embedding, the first RRFWindow by text are re-ranked by
  reciprocal rank fusion of their text place and their place by cosine
  similarity, RRFK the engine's constant; a row past the window keeps its
  text order after them. A query with no term to hold that brings a
  vector ranks by the vector alone: every index row the caller may read
  whose embedding is of the model, by cosine similarity (#740).
- Access: the caller's audience is checked on the index row's copied
  columns, as a repository checks a row's. A caller with grants, a party
  or an agent run, reaches a table's rows only through its grants'
  relation paths, which no copy can hold: for it each table's rows are
  kept by an EXISTS back to the table, with the conditions the table's own
  repository applies for that caller (Host.Conditions). A table no grant
  reaches is left out of the query.
- Scope is the app's filter: the value of the field each declaration names
  as its scope, copied into the index row.
*/

// SearchRequest is a search.
type SearchRequest struct {
	// Q is the query (#740): words and quoted phrases a row holds each
	// of, prefixes (wal*), excluded terms (-oak) and OR between terms.
	Q string
	// Tables are the tables it searches, by type name; every @searchable
	// table the caller may read when nil.
	Tables []string
	// Scope keeps to the rows whose scope is one of these values.
	Scope []string
	// Limit is the most hits of the page (20 unless set, at most 100), and
	// Offset the hits before it (at most 10000).
	Limit, Offset int
	// Vector is a query embedding, of SearchOptions.Embeddings' model,
	// which re-ranks the first hits, or ranks alone when Q holds no term to
	// hold; the embeddings compute one when nil.
	Vector []float64
}

// SnippetPart is a part of a snippet: text, which a match is marked in.
type SnippetPart struct {
	Text  string `json:"text"`
	Match bool   `json:"match,omitempty"`
}

// SearchHit is one hit.
type SearchHit struct {
	// Table is the row's table, by type name, and Key its key in its wire
	// form.
	Table string `json:"table"`
	Key   string `json:"key"`
	Title string `json:"title"`
	// Matched is where the words matched first: title, body, comments or
	// files.
	Matched string `json:"matched"`
	// Snippet is a few words around the matches of that text.
	Snippet []SnippetPart `json:"snippet"`
	// Place is its place in the whole ranking, from 1.
	Place int `json:"place"`
}

// SearchResult is a page of hits.
type SearchResult struct {
	Items []SearchHit `json:"items"`
	// Total is how many rows match, of those the caller may read.
	Total int `json:"total"`
	// More reports that rows follow the page.
	More bool `json:"more"`
	// Reranked reports that embeddings re-ranked the first hits.
	Reranked bool `json:"reranked"`
}

// Embedder embeds texts, one vector each, in order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float64, error)
}

// EmbedderFunc is a function as an Embedder.
type EmbedderFunc func(ctx context.Context, texts []string) ([][]float64, error)

// Embed calls f.
func (f EmbedderFunc) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	return f(ctx, texts)
}

// Embeddings are what a search re-ranks by and IndexSearch computes: a
// model's name, which an index row records with its embedding, what
// embeds texts, and the most characters of a row's text it embeds, 8000
// when zero. Off unless given: no index row is embedded and no search
// re-ranked.
type Embeddings struct {
	Model    string
	Embedder Embedder
	MaxChars int
}

// SearchOptions is what a search and its indexing take beside the
// database.
type SearchOptions struct {
	// Permissions is the permission each table's rows need, by type
	// name: a caller without it finds none of them.
	Permissions map[string]string
	// Embeddings re-rank, off when nil.
	Embeddings *Embeddings
}

// SearchIndexing is a message of a @searchable declaration's queue: a row
// to index again.
type SearchIndexing struct {
	SourceTable string `json:"sourceTable"`
	SourceKey   string `json:"sourceKey"`
}

// Reciprocal rank fusion's constant and window, the engine's
// (superschematic_engine::search, D16).
const (
	RRFK      = 60
	RRFWindow = 200
)

// The bounds of a search.
const (
	// MaxSearchWords is the most words of a query that count.
	MaxSearchWords = 16
	maxWord        = 200
	// MaxIndexedText is the most characters of each of an index row's
	// texts.
	MaxIndexedText = 262_144
	maxLimit       = 100
	maxOffset      = 10_000
)

// The marks a snippet's matches are between, which indexed text never
// holds.
const (
	openMark  = "\u0002"
	closeMark = "\u0003"
)

// weights are ts_rank_cd's weights of the classes D, C, B and A.
const weights = `'{0.1, 0.2, 0.4, 1}'::real[]`

func dropMarks(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0 || r == 2 || r == 3 {
			return -1
		}
		return r
	}, s)
}

// SearchWords are the words of a query that count: its runs of characters
// that are not white space, the first MaxSearchWords, each at most 200
// characters.
func SearchWords(q string) []string {
	var out []string
	for _, word := range strings.FieldsFunc(q, unicode.IsSpace) {
		word = dropMarks(word)
		if word == "" {
			continue
		}
		if utf8.RuneCountInString(word) > maxWord {
			word = string([]rune(word)[:maxWord])
		}
		out = append(out, word)
		if len(out) == MaxSearchWords {
			break
		}
	}
	return out
}

func whole(value, fallback, lo, hi int, set bool, what string) (int, error) {
	if !set {
		return fallback, nil
	}
	if value < lo || value > hi {
		return 0, invalid("searchTables: %s is a whole number from %d to %d, not %d", what, lo, hi, value)
	}
	return value, nil
}

func unit(vector []float64) ([]float64, error) {
	sum := 0.0
	for _, x := range vector {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, invalid("an embedding holds finite numbers")
		}
		sum += x * x
	}
	norm := math.Sqrt(sum)
	out := make([]float64, len(vector))
	if norm == 0 {
		return out, nil
	}
	for i, x := range vector {
		out[i] = x / norm
	}
	return out, nil
}

// snippetParts are a snippet's parts, from ts_headline's text with the
// marks around each match.
func snippetParts(text string) []SnippetPart {
	parts := []SnippetPart{}
	rest := text
	for rest != "" {
		open := strings.Index(rest, openMark)
		if open < 0 {
			parts = append(parts, SnippetPart{Text: rest})
			break
		}
		if open > 0 {
			parts = append(parts, SnippetPart{Text: rest[:open]})
		}
		after := rest[open+len(openMark):]
		close := strings.Index(after, closeMark)
		end := len(after)
		if close >= 0 {
			end = close
		}
		if end > 0 {
			parts = append(parts, SnippetPart{Text: after[:end], Match: true})
		}
		if close < 0 {
			rest = ""
		} else {
			rest = after[close+len(closeMark):]
		}
	}
	return parts
}

// audienceConditions are the conditions of an index row caller may see on
// its copied audience columns (D156), as the generated ORM's audience
// condition reads a row's: its effective audience is everyone or holds
// the caller's id, or for an agent run its own or whom it acts for, and
// for an agent run its agent access is not none.
func audienceConditions(effective, agents string, caller access.Principal, args *[]any) []string {
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
		placeholders = append(placeholders, fmt.Sprintf("$%d::uuid", len(*args)))
	}
	part := effective + " IS NULL"
	if len(placeholders) > 0 {
		part = "(" + effective + " IS NULL OR " + effective + " && ARRAY[" + strings.Join(placeholders, ", ") + "]::uuid[])"
	}
	parts := []string{part}
	if caller.Kind == access.KindAgent {
		parts = append(parts, agents+" <> 'none'")
	}
	return []string{strings.Join(parts, " AND ")}
}

// SearchTables searches every @searchable table as caller (D155): the
// rows that hold every word of request.Q, of those caller may read, best
// first, one page of them, and how many there are. A table caller lacks
// the permission of (options.Permissions) is left out, as is every table
// a caller with grants reaches no row of. A request it cannot read is
// Invalid.
func SearchTables(ctx context.Context, h Host, caller Caller, request SearchRequest, options SearchOptions) (SearchResult, error) {
	empty := SearchResult{Items: []SearchHit{}}
	limit, err := whole(request.Limit, 20, 1, maxLimit, request.Limit != 0, "limit")
	if err != nil {
		return empty, err
	}
	offset, err := whole(request.Offset, 0, 0, maxOffset, request.Offset != 0, "offset")
	if err != nil {
		return empty, err
	}
	plan := ReadSearchQuery(request.Q)
	// A query with no term to hold matches nothing, unless a vector ranks
	// it.
	if len(plan.All) == 0 && request.Vector == nil {
		return empty, nil
	}
	all := h.Definitions().Searchable
	for _, table := range request.Tables {
		if _, ok := all[table]; !ok {
			return empty, invalid("searchTables: %s is no @searchable table", table)
		}
	}
	var sources []string
	for _, table := range sortedKeys(all) {
		if request.Tables != nil && !slices.Contains(request.Tables, table) {
			continue
		}
		if permission, ok := options.Permissions[table]; ok && !caller.holds(permission) {
			continue
		}
		sources = append(sources, table)
	}
	if len(sources) == 0 {
		return empty, nil
	}
	if onSQLite(h) {
		// Embeddings re-rank on Postgres alone (D186).
		if request.Vector != nil {
			return empty, invalid("searchTables: %v", ErrSQLiteEmbeddings)
		}
		if options.Embeddings != nil {
			return empty, fmt.Errorf("searchTables: %w", ErrSQLiteEmbeddings)
		}
		return searchOnSQLite(ctx, h, caller, plan, sources, all, request.Scope, limit, offset)
	}
	first := all[sources[0]]
	config := first.Config
	if config == "" {
		config = "simple"
	}
	regconfig := "'" + config + "'::regconfig"
	as := h.As(ctx, caller.Principal)

	var args []any
	param := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	byVector := len(plan.All) == 0
	query := PostgresQuery(plan, regconfig, param)
	var conditions []string
	switch {
	case !byVector:
		conditions = []string{first.Document + " @@ q.q"}
	case len(plan.None) > 0:
		conditions = []string{"NOT (" + first.Document + " @@ (" + query + "))"}
	}
	// The tables: for a caller with grants, each through its repository's
	// conditions for that caller, an EXISTS back to its rows.
	if caller.Principal.Grants == nil {
		conditions = append(conditions, "si.source_table = ANY("+param(sources)+"::text[])")
	} else {
		var branches []string
		for _, table := range sources {
			definition := all[table]
			kept := h.Conditions(as, table, &args, len(args))
			if len(kept) == 1 && kept[0] == "FALSE" {
				continue
			}
			name := param(table)
			match := fmt.Sprintf("%s.%s = si.source_key::%s", definition.Table, definition.Key, definition.KeyType)
			branches = append(branches, fmt.Sprintf("(si.source_table = %s::text AND EXISTS (SELECT 1 FROM %s WHERE %s))", name, definition.Table, strings.Join(append([]string{match}, kept...), " AND ")))
		}
		if len(branches) == 0 {
			return empty, nil
		}
		conditions = append(conditions, "("+strings.Join(branches, " OR ")+")")
	}
	// The caller's audience, on the index row's copy of each row's.
	conditions = append(conditions, audienceConditions("si.audience", "si.agents", caller.Principal, &args)...)
	if request.Scope != nil {
		var forms []string
		for _, value := range request.Scope {
			if !slices.Contains(forms, value) {
				forms = append(forms, value)
			}
			if db := rowrefs.UUIDToDB(value); db != "" && !slices.Contains(forms, db) {
				forms = append(forms, db)
			}
		}
		conditions = append(conditions, "si.scope = ANY("+param(forms)+"::text[])")
	}
	if byVector {
		if options.Embeddings == nil {
			return empty, invalid("searchTables: a vector needs embeddings, which say its model")
		}
		vector, err := unit(request.Vector)
		if err != nil {
			return empty, err
		}
		return searchByVector(ctx, h, all, first.Index, conditions, args, vector, options.Embeddings.Model, limit, offset)
	}

	// The query's embedding, when embeddings are on or the request brings
	// one.
	var vector []float64
	model := ""
	if request.Vector != nil {
		if options.Embeddings == nil {
			return empty, invalid("searchTables: a vector needs embeddings, which say its model")
		}
		if vector, err = unit(request.Vector); err != nil {
			return empty, err
		}
		model = options.Embeddings.Model
	} else if e := options.Embeddings; e != nil && e.Embedder != nil {
		// A model that fails leaves the text ranking as it is.
		if embedded, err := e.Embedder.Embed(ctx, []string{request.Q}); err == nil && len(embedded) > 0 && len(embedded[0]) > 0 {
			if v, err := unit(embedded[0]); err == nil {
				vector, model = v, e.Model
			}
		}
	}

	ranked := `ranked AS (
      SELECT h.*, row_number() OVER (ORDER BY h.rank DESC, h.source_table, h.source_key) AS text_place
      FROM hits AS h
    )`
	order := "r.text_place"
	fused := ""
	from := "ranked"
	if vector != nil {
		v := param(vector)
		m := param(model)
		fused = fmt.Sprintf(`,
    scored AS (
      SELECT r.*, CASE WHEN r.text_place <= %[3]d AND r.embedding IS NOT NULL AND r.embedding_model = %[2]s::text
        AND cardinality(r.embedding) = cardinality(%[1]s::float8[])
        THEN (SELECT sum(a * b) FROM unnest(r.embedding, %[1]s::float8[]) AS t(a, b)) END AS similarity
      FROM ranked AS r
    ),
    placed AS (
      SELECT s.*, CASE WHEN s.similarity IS NOT NULL
        THEN row_number() OVER (PARTITION BY s.similarity IS NOT NULL ORDER BY s.similarity DESC, s.text_place) END AS vector_place
      FROM scored AS s
    )`, v, m, RRFWindow)
		order = fmt.Sprintf("(r.text_place > %[1]d), CASE WHEN r.text_place <= %[1]d THEN 1.0 / (%[2]d + r.text_place) + COALESCE(1.0 / (%[2]d + r.vector_place), 0) END DESC, r.text_place", RRFWindow, RRFK)
		from = "placed"
	}
	headline := "'StartSel=" + openMark + ", StopSel=" + closeMark + ", MaxWords=24, MinWords=12, ShortWord=0, HighlightAll=false, MaxFragments=0'"
	matches := func(column string) string {
		return "to_tsvector(" + regconfig + ", COALESCE(p." + column + ", '')) @@ q.q"
	}
	sql := `WITH q AS (SELECT ` + query + ` AS q),
    hits AS (
      SELECT si.source_table, si.source_key, si.title, si.body, si.comments, si.files, si.embedding, si.embedding_model,
        ts_rank_cd(` + weights + `, ` + first.Document + `, q.q) AS rank
      FROM ` + first.Index + ` AS si CROSS JOIN q
      WHERE ` + strings.Join(conditions, " AND ") + `
    ),
    ` + ranked + fused + `
    SELECT c.total, p.source_table, p.source_key, p.title, p.matched,
      ts_headline(` + regconfig + `, p.shown, q.q, ` + headline + `) AS snippet
    FROM (SELECT count(*) AS total FROM hits) AS c
    CROSS JOIN q
    LEFT JOIN LATERAL (
      SELECT r.source_table, r.source_key, r.title, r.ord,
        CASE WHEN ` + matches("title") + ` THEN 'title' WHEN ` + matches("body") + ` THEN 'body' WHEN ` + matches("comments") + ` THEN 'comments' WHEN ` + matches("files") + ` THEN 'files' ELSE 'title' END AS matched,
        CASE WHEN ` + matches("title") + ` THEN p.title WHEN ` + matches("body") + ` THEN p.body WHEN ` + matches("comments") + ` THEN p.comments WHEN ` + matches("files") + ` THEN p.files ELSE p.title END AS shown
      FROM (
        SELECT r.*, row_number() OVER (ORDER BY ` + order + `) AS ord
        FROM ` + from + ` AS r
        ORDER BY ` + order + `
        LIMIT ` + fmt.Sprint(limit) + ` OFFSET ` + fmt.Sprint(offset) + `
      ) AS r
      CROSS JOIN LATERAL (SELECT r.title, r.body, r.comments, r.files) AS p
    ) AS p ON TRUE
    ORDER BY p.ord`
	rows, err := h.Query(h.System(ctx), sql, args...)
	if err != nil {
		return empty, err
	}
	result := SearchResult{Items: []SearchHit{}, Reranked: vector != nil}
	if len(rows) > 0 {
		result.Total = int(integer(rows[0], "total"))
	}
	for _, row := range rows {
		if row["source_table"] == nil {
			continue
		}
		table := text(row, "source_table")
		result.Items = append(result.Items, SearchHit{
			Table:   table,
			Key:     wireKey(all[table].UUID, text(row, "source_key")),
			Title:   text(row, "title"),
			Matched: text(row, "matched"),
			Snippet: snippetParts(text(row, "snippet")),
			Place:   offset + len(result.Items) + 1,
		})
	}
	result.More = offset+len(result.Items) < result.Total
	return result, nil
}
