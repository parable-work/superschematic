package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestTSWriterEmitsPaginated: the TypeScript writer emits @paginated with
// both limits, as the loader filled them in from a YAML page that left
// them out, and reading it back gives the same IR.
func TestTSWriterEmitsPaginated(t *testing.T) {
	schema, err := loader.LoadService(dataFixtures + "/fixture-paged-api-yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "catalog.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`import { HttpMethod, QueryParam, paginated, rest } from "@superschematic/api";`,
		"  @rest(HttpMethod.GET, \"items\")\n  @paginated({ defaultLimit: 2, maxLimit: 5 })\n  listItems(",
		"  @rest(HttpMethod.GET, \"tags\")\n  @paginated({ defaultLimit: 20, maxLimit: 100 })\n  listTags(",
	} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("TS output missing\n%s\n\ngot:\n%s", want, got)
		}
	}
	if strings.Count(string(got), "@paginated") != 2 {
		t.Fatalf("TS output pages an operation it should not:\n%s", got)
	}
	if got, want := normalizeIR(t, writeAndReload(t, schema, FormatTS)), normalizeIR(t, schema); got != want {
		t.Fatalf("IR changed after a YAML -> TS round trip\nwant:\n%s\ngot:\n%s", want, got)
	}
}
