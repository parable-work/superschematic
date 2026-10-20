package queueops

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden documents")

// The messages the golden documents name, which the runner's tests insert
// into fixture-queue-db's queue: two dead ones and a ready one.
const (
	deadID  = "00000000-0000-4000-8000-000000000001"
	dead2ID = "00000000-0000-4000-8000-000000000002"
	readyID = "00000000-0000-4000-8000-000000000003"
)

// goldenRequests are the documents of testdata/golden, which the runner's
// tests run (runtime/migrate/go/cmd/superschematic-migrate): on
// fixture-queue-db's OrderPlaced, a listing, a retry and a drop of IDs and
// of every dead message, and a retry that names a message that is not dead.
var goldenRequests = map[string]Request{
	"dead":          {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Dead, Limit: 1},
	"retry":         {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Retry, IDs: []string{deadID}},
	"retry-all":     {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Retry, All: true},
	"retry-refused": {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Retry, IDs: []string{deadID, readyID, "00000000-0000-4000-8000-0000000000ff"}},
	"drop":          {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Drop, IDs: []string{dead2ID}},
	"drop-all":      {Service: "fixture-queue-db", Queue: "OrderPlaced", Operation: Drop, All: true},
}

// TestGoldenDocuments writes each golden request's document and compares
// it with testdata/golden, which -update rewrites.
func TestGoldenDocuments(t *testing.T) {
	for name, req := range goldenRequests {
		t.Run(name, func(t *testing.T) {
			got, err := Document(req)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", "golden", name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (go test ./internal/queueops -run TestGoldenDocuments -update writes it)", err)
			}
			if string(got) != string(want) {
				t.Errorf("%s differs from what Document writes; -update rewrites it:\n%s", path, got)
			}
		})
	}
}

// TestDocumentRefuses holds Document to what each operation takes.
func TestDocumentRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		req  Request
		want string
	}{
		"no queue":        {Request{Service: "shop-db", Operation: Dead, Limit: 1}, "names its DB service and its queue"},
		"no limit":        {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Dead}, "a limit of one or more"},
		"dead with ids":   {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Dead, Limit: 1, IDs: []string{deadID}}, "takes no IDs"},
		"retry nothing":   {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Retry}, "retry takes the IDs of dead messages, or all of them"},
		"drop both":       {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Drop, All: true, IDs: []string{deadID}}, "drop takes the IDs of dead messages, or all of them"},
		"upper-case id":   {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: Drop, IDs: []string{strings.ToUpper("0000000a-0000-4000-8000-000000000001")}}, "not a message's ID in lower case"},
		"other operation": {Request{Service: "shop-db", Queue: "OrderPlaced", Operation: "prune"}, `operation "prune" is not dead, retry or drop`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Document(tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestNormalizeID takes a UUID in either case and refuses anything else.
func TestNormalizeID(t *testing.T) {
	if got, err := NormalizeID("3F2C8A10-5B1E-4C7A-9D2E-8F6B4A1C0E93"); err != nil || got != "3f2c8a10-5b1e-4c7a-9d2e-8f6b4a1c0e93" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, id := range []string{"", "42", "3f2c8a10-5b1e-4c7a-9d2e-8f6b4a1c0e9", "3f2c8a10-5b1e-4c7a-9d2e-8f6b4a1c0e93 ", "'; DROP TABLE x; --"} {
		if _, err := NormalizeID(id); err == nil {
			t.Errorf("NormalizeID(%q) took it", id)
		}
	}
}

// TestReadResult reads the runner's result of a request and refuses one
// of another operation or version.
func TestReadResult(t *testing.T) {
	req := goldenRequests["dead"]
	r, err := ReadResult(req, []byte(`{"version":1,"service":"fixture-queue-db","queue":"OrderPlaced","operation":"dead","total":2,"messages":[{"id":"`+deadID+`","attempts":6,"lastError":"boom","diedAt":"2026-10-19T12:00:00Z","message":{"note":null}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 2 || len(r.Messages) != 1 || r.Messages[0].ID != deadID || string(r.Messages[0].Message) != `{"note":null}` {
		t.Fatalf("read %+v", r)
	}
	if _, err := ReadResult(req, []byte(`{"version":1,"service":"fixture-queue-db","queue":"OrderPlaced","operation":"drop","ids":[]}`)); err == nil || !strings.Contains(err.Error(), "not of dead on fixture-queue-db.OrderPlaced") {
		t.Fatalf("got %v", err)
	}
	if _, err := ReadResult(req, []byte(`{"version":2}`)); err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("got %v", err)
	}
}
