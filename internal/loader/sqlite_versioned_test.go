package loader

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestVersionedOnSQLite: a @versioned table loads in a DB service that
// lists sqlite with the TypeScript types alone, whose repositories serve
// it there, its history kept by row triggers (D189), and is refused with
// the Go or the Rust types, whose repositories read a history with
// Postgres's statements alone.
func TestVersionedOnSQLite(t *testing.T) {
	const schema = `
import { Identity } from "superscalar";
import { AutoGenerate, key, versioned } from "@superschematic/db";
@versioned()
export abstract class Page {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}`
	base := filepath.ToSlash(filepath.Join(testpaths.RepoRoot(t), "internal", "loader", "tsreader", "testdata", "tsconfig.base.json"))
	for _, tc := range []struct {
		name, types, want string
	}{
		{"TypeScript's types alone", `{"typescript": {"enabled": true}}`, ""},
		{"Go's types too", `{"typescript": {"enabled": true}, "go": {"enabled": true}}`, "@versioned on Page: pages-db lists the sqlite dialect and enables the Go types"},
		{"Rust's types", `{"rust": {"enabled": true}}`, "@versioned on Page: pages-db lists the sqlite dialect and enables the Rust types"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.json":  `{"name": "pages-db", "kind": "DB", "outputs": {"types": ` + tc.types + `, "sql": {"dialects": ["postgres", "sqlite"]}}}`,
				"tsconfig.json":       `{"extends": "` + base + `", "include": ["src/**/*.ts"]}`,
				"src/pages.schema.ts": schema,
			})
			_, err := LoadService(dir)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("LoadService: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("LoadService = %v, want it to hold %q", err, tc.want)
			}
		})
	}
}
