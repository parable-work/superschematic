package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAgentsRoundTrip (D104): tables that implement Lease and Retries,
// Presence, and Budget are written in every format as their uses, in each
// type's order, `implements Lease<Job, { ... }>, Retries<Job, { ... }>`,
// `implements Presence<Session, { ... }>` and `implements Budget<Account,
// { ... }>` in TypeScript and `{ name, implements: true, config }` in the
// data forms, with a backoff object, then, window, keyField, ttlByState,
// releaseLeases and a rolling window, without the owned fields, sub-tables
// and deadline queues the loader adds for them, and read back to the same
// IR; the API that serves their behavior operations and runs their
// deadlines reads back over its database to the same expanded IR.
func TestAgentsRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-agents-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	schema.Description = ""
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`implements Lease<Job, { escalate: { from: ["running"]; transition: "failed" }; maxExpiries: 3; onExpiry: { from: ["running"]; transition: "queued" }; overridePermission: "jobs.override"; requireToken: true }>, Retries<Job, {`,
			`timeout: { attempts: 5; backoff: { firstMs: 60000; maxMs: 600000 } }`,
			`then: { from: ["waiting"]; transition: "queued" }`,
			`window: { attempts: 4; ms: 3600000 }`,
			`keyField: "revision"`,
			`implements Presence<Session, {`,
			`releaseLeases: ["Job"]`,
			`ttlByState: { turn: 900000 }`,
			`implements Budget<Account, {`,
			`tokens: { limit: 1000000; reset: "daily"; scope: "workspace"; window: "rolling" }`,
			`implements Budget<Workspace, {`,
		},
		FormatJSON: {`"implements": true`, `"name": "Lease"`, `"name": "Retries"`, `"name": "Presence"`, `"name": "Budget"`, `"backoff": {`, `"ttlByState": {`, `"window": "rolling"`, `"keyField": "revision"`},
		FormatYAML: {"implements: true", "name: Lease", "name: Retries", "name: Presence", "name: Budget", "backoff:\n", "ttlByState:\n", "window: rolling", "keyField: revision"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			source := writtenSource(t, schema, format)
			for _, want := range contains[format] {
				if !strings.Contains(source, want) {
					t.Errorf("written %s lacks %q:\n%s", format, want, source)
				}
			}
			for _, added := range []string{"JobLeaseDirectives", "JobRetriesAttempts", "JobDeadline", "SessionDeadline", "AccountBudgetMeters", "behaviorTable", "behaviorQueue", "optimistic", "leaseHolder", "presenceDeadline", "statusEnteredAt", "@behavior"} {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			if got := normalizeIR(t, writeAndReload(t, schema, format)); got != want {
				t.Errorf("IR mismatch after the %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
			}
		})
	}

	db := schema
	api, err := loader.LoadService(tsFixtures+"/fixture-agents-api", loader.WithWarnings(func(*verify.Diagnostic) {}))
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	wantAPI := normalizeIR(t, api)
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run("api "+string(format), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := WriteService(api, format, dir); err != nil {
				t.Fatalf("WriteService: %v", err)
			}
			cfg := fmt.Sprintf(`{"name": %q, "kind": "API", "dependencies": [{"name": "fixture-agents-db", "kind": "DB"}], "outputs": {}}`+"\n", api.Name)
			if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if format == FormatTS {
				writeTSProject(t, dir, api)
			}
			source := writtenSource(t, api, format)
			for _, added := range []string{"leaseAcquire", "JobPreconditions", "JobRetriesClass", "AccountBudgetMeter", "presenceBeat", "\"origin\""} {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			reloaded, err := loader.LoadService(dir, loader.WithWarnings(func(*verify.Diagnostic) {}), loader.WithDependencyLoader(func(name string) (*ir.Schema, error) {
				if name != db.Name {
					return nil, fmt.Errorf("no service %s", name)
				}
				return db, nil
			}))
			if err != nil {
				dumpService(t, dir)
				t.Fatalf("reloading %s output: %v", format, err)
			}
			if got := normalizeIR(t, reloaded); got != wantAPI {
				t.Errorf("IR mismatch after writing %s\nwant:\n%s\ngot:\n%s", format, wantAPI, got)
			}
		})
	}
}
