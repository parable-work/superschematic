package tsgen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestScalarJSDocTagFollowsNaming builds the golden fixtures with
// scalar_jsdoc_tag set. Every scalar-typed field gains one tag line naming
// its canonical scalar, after the field's doc line and before the field;
// with the tag lines removed, types.ts is its golden, which is written with
// the key unset and has none.
func TestScalarJSDocTagFollowsNaming(t *testing.T) {
	const tag = "fixtureScalar"
	tagLine := regexp.MustCompile(`^  /\*\* @` + tag + ` ([A-Za-z0-9]+\.[A-Za-z0-9]+) \*/$`)
	fieldLine := regexp.MustCompile(`^  [A-Za-z_$][A-Za-z0-9_$]*\??: `)

	cases := []struct {
		service string
		deps    []string
	}{
		{service: "fixture-db"},
		{service: "fixture-api", deps: []string{"fixture-db"}},
		{service: "fixture-general"},
	}
	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join(fixturesDir, tc.service))
			if err != nil {
				t.Fatalf("load %s: %v", tc.service, err)
			}
			deps := map[string]*ir.Schema{}
			depPackages := map[string]string{}
			for _, dep := range tc.deps {
				depSchema, err := loader.LoadService(filepath.Join(fixturesDir, dep))
				if err != nil {
					t.Fatalf("load dependency %s: %v", dep, err)
				}
				deps[dep] = depSchema
				depPackages[dep] = naming.Default().NpmTypesPackage(dep)
			}

			output, err := Generate(schema, Options{
				SchemaName:         tc.service,
				Dependencies:       deps,
				DependencyPackages: depPackages,
				Naming:             naming.Naming{ScalarJSDocTag: tag},
				Clock:              codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			outDir := filepath.Join(t.TempDir(), tc.service)
			if err := WriteTypes(output, outDir); err != nil {
				t.Fatalf("write types: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(outDir, "types", "types.ts"))
			if err != nil {
				t.Fatalf("read generated types.ts: %v", err)
			}
			golden, err := os.ReadFile(filepath.Join("testdata", "golden", tc.service, "types", "types.ts"))
			if err != nil {
				t.Fatalf("read golden types.ts: %v", err)
			}

			var tagged []string
			var untagged []string
			lines := strings.Split(string(got), "\n")
			for i, line := range lines {
				m := tagLine.FindStringSubmatch(line)
				if m == nil {
					untagged = append(untagged, line)
					continue
				}
				if i+1 >= len(lines) || !fieldLine.MatchString(lines[i+1]) {
					t.Errorf("tag line %q is not directly above a field", line)
				}
				tagged = append(tagged, m[1])
			}
			if strings.Join(untagged, "\n") != string(golden) {
				t.Errorf("types.ts without its tag lines differs from the golden:\n%s", got)
			}

			var want []string
			for _, typ := range output.Types {
				for _, field := range typ.Fields {
					if field.ScalarInfo != nil {
						want = append(want, field.ScalarInfo.Name)
					}
				}
			}
			if len(want) == 0 {
				t.Fatalf("%s has no scalar-typed field to tag", tc.service)
			}
			if strings.Join(tagged, ",") != strings.Join(want, ",") {
				t.Errorf("tagged scalars = %v, want one per scalar field in order %v", tagged, want)
			}

			if tc.service == "fixture-db" {
				const docThenTag = "  /** Display name shown across the product. */\n  /** @" + tag + " Identity.Name */\n  name: string;\n"
				if !strings.Contains(string(got), docThenTag) {
					t.Errorf("a documented scalar field must carry its doc line, then the tag line:\n%s", got)
				}
			}
		})
	}
}
