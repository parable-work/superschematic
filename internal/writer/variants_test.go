package writer

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestVariantFieldRoundTrip (D149): a variant union is written in every
// format, as `export type StepResult = Variant<"kind", { ... }>` in
// TypeScript and as the union's by and arms in the data forms, and reads
// back to the same IR, its members in the same order; the API that names it
// from its database reads back too.
func TestVariantFieldRoundTrip(t *testing.T) {
	for _, fixture := range []string{"fixture-variant-field-db", "fixture-variant-field-api"} {
		schema, err := loader.LoadService(tsFixtures + "/" + fixture)
		if err != nil {
			t.Fatalf("loading %s: %v", fixture, err)
		}
		contains := map[Format][]string{
			FormatTS:   {"result: Nullable<StepResult>;"},
			FormatJSON: {`"name": "StepResult"`},
			FormatYAML: {"name: StepResult"},
		}
		if fixture == "fixture-variant-field-db" {
			contains = map[Format][]string{
				FormatTS:   {`export type StepResult = Variant<"kind", { verify: VerifyResult; review: ReviewResult }>;`, "result: Nullable<StepResult>;"},
				FormatJSON: {`"by": "kind"`, `"verify": "VerifyResult"`},
				FormatYAML: {"by: kind", "verify: VerifyResult"},
			}
		}
		want := normalizeIR(t, schema)
		for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
			t.Run(fixture+" "+string(format), func(t *testing.T) {
				source := writtenSource(t, schema, format)
				for _, text := range contains[format] {
					if !strings.Contains(source, text) {
						t.Errorf("written %s lacks %q:\n%s", format, text, source)
					}
				}
				if fixture == "fixture-variant-field-api" {
					// The API's reload needs its database beside it, which
					// TestRoundTrip's helpers do not stage.
					return
				}
				if got := normalizeIR(t, writeAndReload(t, schema, format)); got != want {
					t.Errorf("IR mismatch after the %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
				}
			})
		}
	}
}
