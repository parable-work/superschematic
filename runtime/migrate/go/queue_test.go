package migrate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// queueGolden reads a queue document the compiler writes for
// fixture-queue-db's OrderPlaced, checked in as its golden
// (internal/queueops/testdata/golden): the contract between
// `superschematic stack queue`, which does not import the runner, and the
// runner.
func queueGolden(t *testing.T, dir, name string) []byte {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join(dir, "internal", "queueops", "testdata", "golden", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestReadsTheQueueDocuments reads every golden queue document, each
// operation with what it takes.
func TestReadsTheQueueDocuments(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for name, want := range map[string]struct {
		op    migrate.QueueOperation
		limit int
		ids   int
		all   bool
	}{
		"dead":          {op: migrate.QueueDead, limit: 1},
		"retry":         {op: migrate.QueueRetry, ids: 1},
		"retry-all":     {op: migrate.QueueRetry, all: true},
		"retry-refused": {op: migrate.QueueRetry, ids: 3},
		"drop":          {op: migrate.QueueDrop, ids: 1},
		"drop-all":      {op: migrate.QueueDrop, all: true},
	} {
		q, err := migrate.ReadQueueDocument(queueGolden(t, root, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if q.Service != "fixture-queue-db" || q.Queue != "OrderPlaced" || q.Operation != want.op || q.Limit != want.limit || len(q.IDs) != want.ids || q.All != want.all {
			t.Errorf("%s: read %+v", name, q)
		}
		if !strings.Contains(q.Statements.Dead, "FROM order_placed_queue AS q") {
			t.Errorf("%s: the listing reads another table:\n%s", name, q.Statements.Dead)
		}
	}
}

// TestReadQueueDocumentRefuses refuses a document of another version, an
// operation with what it does not take or without what it needs, an ID that
// is no lower-case UUID or is listed twice, and a missing statement.
func TestReadQueueDocumentRefuses(t *testing.T) {
	base := queueGolden(t, filepath.Join("..", "..", ".."), "retry")
	edit := func(change func(map[string]any)) []byte {
		var doc map[string]any
		if err := json.Unmarshal(base, &doc); err != nil {
			t.Fatal(err)
		}
		change(doc)
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	id := "00000000-0000-4000-8000-000000000001"
	for name, tc := range map[string]struct {
		doc  []byte
		want string
	}{
		"version":        {edit(func(d map[string]any) { d["version"] = 2 }), "version 2; this runner reads version 1"},
		"unknown member": {edit(func(d map[string]any) { d["sql"] = "DROP TABLE x" }), `unknown field "sql"`},
		"no queue":       {edit(func(d map[string]any) { delete(d, "queue") }), "names no service or no queue"},
		"operation":      {edit(func(d map[string]any) { d["operation"] = "prune" }), `operation "prune" is not dead, retry or drop`},
		"ids and all":    {edit(func(d map[string]any) { d["all"] = true }), "retry takes either ids or all"},
		"neither":        {edit(func(d map[string]any) { delete(d, "ids") }), "retry takes either ids or all"},
		"limit on retry": {edit(func(d map[string]any) { d["limit"] = 5 }), "retry takes no limit"},
		"dead no limit":  {edit(func(d map[string]any) { d["operation"] = "dead"; delete(d, "ids") }), "one or more; got 0"},
		"dead with ids":  {edit(func(d map[string]any) { d["operation"] = "dead"; d["limit"] = 5 }), "dead takes no ids and no all"},
		"upper case id":  {edit(func(d map[string]any) { d["ids"] = []string{"0000000A-0000-4000-8000-000000000001"} }), "is not a message's ID"},
		"injected id":    {edit(func(d map[string]any) { d["ids"] = []string{id + "'::uuid[]); --"} }), "is not a message's ID"},
		"id twice":       {edit(func(d map[string]any) { d["ids"] = []string{id, id} }), "is listed twice"},
		"no statement":   {edit(func(d map[string]any) { d["statements"].(map[string]any)["dropAll"] = " " }), "statement dropAll is empty"},
	} {
		if _, err := migrate.ReadQueueDocument(tc.doc); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, tc.want)
		}
	}
}
