package rowrefs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// recordingHost records the statements and arguments it is given, and
// answers each read with rows, and each key Visible asks of with true.
type recordingHost struct {
	dialect    string
	statements []string
	args       [][]any
	rows       []Row
}

func (h *recordingHost) Dialect() string { return h.dialect }

func (h *recordingHost) Query(_ context.Context, sql string, args ...any) ([]Row, error) {
	h.statements = append(h.statements, sql)
	h.args = append(h.args, args)
	return h.rows, nil
}

func (h *recordingHost) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	h.statements = append(h.statements, sql)
	h.args = append(h.args, args)
	return 1, nil
}

func (h *recordingHost) Visible(_ context.Context, _ string, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, k := range keys {
		out[k] = true
	}
	return out, nil
}

func (h *recordingHost) Transaction(_ context.Context, fn func(Host) error) error { return fn(h) }

func (h *recordingHost) ActingUser(context.Context) (string, bool) { return "", false }

func (h *recordingHost) Caller(context.Context) (access.Principal, bool) {
	return access.Principal{}, false
}

func (h *recordingHost) NotFound(message string) error {
	return &Error{Code: "not_found", Message: message}
}

var referencesForTest = &ReferencesDefinition{
	Holders: map[string]ReferencesHolder{"Note": {RowTable: RowTable{Table: `"note_references"`, Row: `"note_id"`, UUID: true}, MaxPerRow: 10}},
	Targets: map[string]ReferenceTarget{"Note": {Table: `"note"`, Key: `"id"`, UUID: true}},
	Comments: map[string]CommentsSource{"Ticket": {
		RowTable: RowTable{Table: `"ticket_comments_messages"`, Row: `"ticket_id"`, UUID: true},
		Blocks:   `"blocks"`, State: `"state"`, ID: `"message"`,
	}},
}

// TestStatementsOnSQLiteAreSQLitesWords: a DialectHost of SQLite runs
// each statement with ?n parameters, its casts CAST(... AS TEXT), a ref
// block found through json_each and no advisory lock, and binds a time as
// its RFC 3339 text to the millisecond (D182, #611); a Host that names no
// dialect runs Postgres's.
func TestStatementsOnSQLiteAreSQLitesWords(t *testing.T) {
	ctx := context.Background()
	key := "00000000-0000-4000-8000-0000000000a1"
	row := RowRef{Table: "Note", Key: UUIDFromDB(key)}
	lite := &recordingHost{dialect: DialectSQLite, rows: []Row{{"key": key}}}
	refs := NewReferences(lite, referencesForTest)
	if _, err := refs.ReferencedBy(ctx, row, ReferencedByOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := refs.Add(ctx, AddInput{From: row, To: row}); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(lite.statements, "\n")
	for _, absent := range []string{"$1", "::", "@>", "pg_advisory"} {
		if strings.Contains(all, absent) {
			t.Errorf("a statement on SQLite holds %q:\n%s", absent, all)
		}
	}
	for _, want := range []string{"?1", "CAST(", "json_each(\"blocks\")", "json_extract(block.value, '$.kind') = 'ref'"} {
		if !strings.Contains(all, want) {
			t.Errorf("no statement on SQLite holds %q:\n%s", want, all)
		}
	}

	pg := &recordingHost{rows: []Row{{"key": key}}}
	if _, err := NewReferences(pg, referencesForTest).ReferencedBy(ctx, row, ReferencedByOptions{}); err != nil {
		t.Fatal(err)
	}
	all = strings.Join(pg.statements, "\n")
	for _, want := range []string{"$1", "::text", "@> $1::jsonb"} {
		if !strings.Contains(all, want) {
			t.Errorf("no statement on Postgres holds %q:\n%s", want, all)
		}
	}

	at := time.Date(2026, 11, 26, 10, 0, 0, 123_000_000, time.FixedZone("x", 3600))
	h := inDialect(&recordingHost{dialect: DialectSQLite})
	if _, err := h.Exec(ctx, `UPDATE t SET a = $1, b = $2 WHERE c = $10`, at, &at); err != nil {
		t.Fatal(err)
	}
	inner := h.(sqliteHost).Host.(*recordingHost)
	if got := inner.statements[0]; got != `UPDATE t SET a = ?1, b = ?2 WHERE c = ?10` {
		t.Errorf("the statement on SQLite: %s", got)
	}
	if got := inner.args[0]; got[0] != "2026-11-26T09:00:00.123Z" || got[1] != "2026-11-26T09:00:00.123Z" {
		t.Errorf("the times bound on SQLite: %v", got)
	}
	if read := timeOf(Row{"at": "2026-11-26T09:00:00.123Z"}, "at"); !read.Equal(at) {
		t.Errorf("a time read from its text: %v", read)
	}
	if !boolean(Row{"b": int64(1)}, "b") || boolean(Row{"b": int64(0)}, "b") {
		t.Error("a boolean read as SQLite's 1 or 0")
	}
}
