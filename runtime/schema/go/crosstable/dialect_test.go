package crosstable

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// recordHost is a Host that records each statement and its values and
// answers an occurrence for a read of occurrences and a row for a read of
// a row: the words of each dialect, without a database. ormgen's
// TestTheGoReadsAcrossTablesOnSQLite runs them on SQLite and Postgres.
type recordHost struct {
	sqlite     bool
	statements []string
	args       [][]any
	defs       *Definitions
}

type sqliteRecordHost struct{ *recordHost }

func (sqliteRecordHost) Dialect() string { return DialectSQLite }

func (h *recordHost) host() Host {
	if h.sqlite {
		return sqliteRecordHost{h}
	}
	return h
}

func (h *recordHost) Query(_ context.Context, sql string, args ...any) ([]Row, error) {
	h.statements = append(h.statements, sql)
	h.args = append(h.args, args)
	switch {
	case strings.Contains(sql, "AS due_at"):
		return []Row{{"row": "00000000-0000-4000-8000-000000000001", "key": "2027-03-01T09:00", "due": "2027-03-01T09:00", "due_at": "2027-03-01T09:00:00.000Z", "state": "upcoming"}}, nil
	case strings.Contains(sql, "AS deleted"):
		return []Row{{"title": "A note", "b0": "Its text", "key": "00000000-0000-4000-8000-000000000002", "deleted": int64(0)}}, nil
	}
	return nil, nil
}

func (h *recordHost) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	h.statements = append(h.statements, sql)
	h.args = append(h.args, args)
	return 1, nil
}

func (h *recordHost) Transaction(_ context.Context, fn func(Host) error) error { return fn(h.host()) }
func (h *recordHost) As(ctx context.Context, _ access.Principal) context.Context {
	return ctx
}
func (h *recordHost) System(ctx context.Context) context.Context { return ctx }
func (h *recordHost) Conditions(context.Context, string, *[]any, int) []string {
	return nil
}
func (h *recordHost) Definitions() *Definitions { return h.defs }
func (h *recordHost) Grants() Grants            { return nil }

var dialectDefinitions = &Definitions{
	Calendar: map[string]CalendarTable{
		"Contract": {Table: "contract", Key: "id", KeyType: "uuid", UUID: true, Sources: []CalendarSource{{Name: "contract", Kind: "renewal", Title: "title", Field: "ends_on", Owner: "owner_id", OwnerUUID: true}}},
		"Filing": {Table: "filing", Key: "id", KeyType: "uuid", UUID: true, Sources: []CalendarSource{{Name: "filing", Kind: "deadline", Title: "title",
			Occurrences: &CalendarOccurrences{Table: "filing_recurrence_occurrences", Row: "filing_id", Key: "key", Due: "due", DueAt: "due_at", State: "state"}}}},
	},
	Searchable: map[string]Searchable{
		"Note": {Index: "search_index", Document: "si.document", Table: "note", Key: "id", KeyType: "uuid", UUID: true, Title: "title", Body: []string{"text"}, DeletedAt: "deleted_at"},
	},
}

// sqliteDefinitions are dialectDefinitions on SQLite, its search's words
// the FTS5 table, as ormgen's SQLiteSearchable writes them.
var sqliteDefinitions = &Definitions{
	Calendar: dialectDefinitions.Calendar,
	Searchable: map[string]Searchable{
		"Note": {Index: "search_index", Table: "note", Key: "id", KeyType: "text", UUID: true, Title: "title", Body: []string{"text"}, DeletedAt: "deleted_at", FTS: "search_index_fts",
			IndexAudience: &SearchIndexAudience{Contains: sqliteAudienceContains, Placeholder: sqliteAudiencePlaceholder}},
	},
}

// postgresWords and sqliteWords are words only each dialect's statements
// hold.
var (
	postgresWords = []string{"::", "$1", "ANY("}
	sqliteWords   = []string{"?1", "CAST("}
)

func holdsWords(t *testing.T, what string, statements []string, want, not []string) {
	t.Helper()
	all := strings.Join(statements, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("%s holds no %q:\n%s", what, w, all)
		}
	}
	for _, w := range not {
		if strings.Contains(all, w) {
			t.Errorf("%s holds %q:\n%s", what, w, all)
		}
	}
}

// TestTheCalendarInEachDialect: the calendar's reads are Postgres's words
// on a Host and SQLite's on a DialectHost of SQLite (D186, #615): a
// parameter ?n, a cast CAST(... AS TEXT), a window bound as RFC 3339 text,
// an owner by its hyphenated text, and the rows of the occurrences read
// one parameter a key.
func TestTheCalendarInEachDialect(t *testing.T) {
	caller := Caller{Principal: access.Principal{Subject: "00000000-0000-4000-8000-0000000000a1", Kind: access.KindUser}}
	read := CalendarRead{From: "2027-01-01T00:00:00Z", To: "2027-06-01T00:00:00Z"}
	for _, sqlite := range []bool{false, true} {
		h := &recordHost{sqlite: sqlite, defs: dialectDefinitions}
		got, err := ReadCalendar(context.Background(), h.host(), caller, read, CalendarOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(h.statements) != 3 {
			t.Fatalf("sqlite %t: %d statements: %q", sqlite, len(h.statements), h.statements)
		}
		if sqlite {
			holdsWords(t, "the calendar on SQLite", h.statements, append(sqliteWords, "IN (?1)", "ends_on >= ?1"), postgresWords)
			if from, ok := h.args[0][0].(string); !ok || from != "2027-01-01" {
				t.Errorf("a date source's bound on SQLite: %#v", h.args[0][0])
			}
			if from, ok := h.args[1][0].(string); !ok || from != "2027-01-01T00:00:00.000Z" {
				t.Errorf("an occurrence's bound on SQLite: %#v", h.args[1][0])
			}
		} else {
			holdsWords(t, "the calendar on Postgres", h.statements, []string{"$1::date", "::text AS key", "= ANY($1::uuid[])"}, []string{"?1", "CAST("})
			if _, ok := h.args[1][0].(time.Time); !ok {
				t.Errorf("an occurrence's bound on Postgres: %#v", h.args[1][0])
			}
		}
		// The occurrence's row is read, and none is: no entry.
		if len(got.Entries) != 0 {
			t.Errorf("entries of no row: %+v", got.Entries)
		}
		// A read of mine binds the owner's id as text.
		h.statements, h.args = nil, nil
		mine := read
		mine.Mine = true
		if _, err := ReadCalendar(context.Background(), h.host(), caller, mine, CalendarOptions{}); err != nil {
			t.Fatal(err)
		}
		if sqlite {
			holdsWords(t, "a read of mine on SQLite", h.statements[:1], []string{`owner_id = ?3`}, []string{"::uuid"})
		} else {
			holdsWords(t, "a read of mine on Postgres", h.statements[:1], []string{`owner_id = $3::uuid`}, nil)
		}
	}
}

// TestSearchInEachDialect: a search is Postgres's full text on a Host and
// FTS5 ranked by bm25 on a DialectHost of SQLite, its audience through
// json_each over the index row's copy, and an embedding refused there;
// an indexing on SQLite writes the index row in SQLite's words.
func TestSearchInEachDialect(t *testing.T) {
	caller := Caller{Principal: access.Principal{Subject: "00000000-0000-4000-8000-0000000000a1", Kind: access.KindUser}}
	pg := &recordHost{defs: dialectDefinitions}
	if _, err := SearchTables(context.Background(), pg.host(), caller, SearchRequest{Q: "walnut desk"}, SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	holdsWords(t, "a search on Postgres", pg.statements, []string{"phraseto_tsquery", "ts_rank_cd", "&& ARRAY[$4::uuid]::uuid[]"}, []string{"MATCH", "bm25"})

	lite := &recordHost{sqlite: true, defs: sqliteDefinitions}
	if _, err := SearchTables(context.Background(), lite.host(), caller, SearchRequest{Q: `walnut "desk" --`}, SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	holdsWords(t, "a search on SQLite", lite.statements, []string{"search_index_fts MATCH ?1", "bm25(search_index_fts, 1.0, 0.4, 0.2, 0.1, 0.0, 0.0, 0.0)", "json_each(si.audience) WHERE json_each.value IN (?3)", "si.source_table IN (?2)"}, []string{"$", "::", "phraseto_tsquery"})
	if got := lite.args[0][0]; got != `{title body comments files} : ("walnut" """desk""")` {
		t.Errorf("the words on SQLite: %v", got)
	}
	if got := lite.args[0][2]; got != "00000000-0000-4000-8000-0000000000a1" {
		t.Errorf("the caller's id on SQLite: %v", got)
	}

	var invalid Invalid
	if _, err := SearchTables(context.Background(), lite.host(), caller, SearchRequest{Q: "walnut", Vector: []float64{1, 0}}, SearchOptions{}); !errors.As(err, &invalid) {
		t.Errorf("a vector on SQLite: %v", err)
	}
	embeddings := SearchOptions{Embeddings: &Embeddings{Model: "m"}}
	if _, err := SearchTables(context.Background(), lite.host(), caller, SearchRequest{Q: "walnut"}, embeddings); !errors.Is(err, ErrSQLiteEmbeddings) {
		t.Errorf("embeddings on SQLite: %v", err)
	}
	if _, err := IndexSearch(context.Background(), lite.host(), SearchIndexing{SourceTable: "Note", SourceKey: "k"}, embeddings); !errors.Is(err, ErrSQLiteEmbeddings) {
		t.Errorf("an indexing with embeddings on SQLite: %v", err)
	}
	if _, err := ReindexSearch(context.Background(), lite.host(), ReindexOptions{SearchOptions: embeddings}); !errors.Is(err, ErrSQLiteEmbeddings) {
		t.Errorf("a reindex with embeddings on SQLite: %v", err)
	}

	lite.statements = nil
	indexed, err := IndexSearch(context.Background(), lite.host(), SearchIndexing{SourceTable: "Note", SourceKey: "00000000-0000-4000-8000-000000000002"}, SearchOptions{})
	if err != nil || indexed != IndexedRow {
		t.Fatalf("an indexing on SQLite: %s, %v", indexed, err)
	}
	holdsWords(t, "an indexing on SQLite", lite.statements, []string{"CAST(id AS TEXT) AS key", "WHERE id = ?1", "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')", "excluded.text_hash", "s.id = ?9"}, []string{"$", "::", "unnest", "FOR SHARE"})
}

// TestFTSPhrases: each word with a letter or a digit is a quoted FTS5
// phrase, a quote doubled, and a word of neither is left out.
func TestFTSPhrases(t *testing.T) {
	for words, want := range map[string]string{
		`walnut desk`:   `"walnut" "desk"`,
		`say "hi"`:      `"say" """hi"""`,
		`-- ** café 42`: `"café" "42"`,
		`--`:            ``,
	} {
		if got := ftsPhrases(SearchWords(words)); got != want {
			t.Errorf("ftsPhrases(%q) = %q, want %q", words, got, want)
		}
	}
}
