package generator

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
)

const envLoaderSkippedLine = "  - env-config: values-schema.json only, no loader; enable outputs.types.go for the Go loader, which imports the Go types, outputs.types.rust for the Rust one, or outputs.types.typescript for the TypeScript one\n"

// The standalone env loader's language follows outputs.types. The Go
// loader's go.mod requires and replaces the Go types module, so it is
// written only when that module is; with neither Go nor Rust types the run
// writes the language-neutral values schema alone. The TypeScript loader is
// config.ts in the TypeScript types package, beside whichever standalone
// loader the run writes. A run with no loader in any language logs why.
func TestRunWritesTheEnvLoaderInATypesLanguage(t *testing.T) {
	cases := []struct {
		name  string
		types []string
		want  []string
	}{
		{"typescript and python", []string{LangTypeScript, LangPython}, []string{"values-schema.json"}},
		{"python", []string{LangPython}, []string{"values-schema.json"}},
		{"none", nil, []string{"values-schema.json"}},
		{"go", []string{LangGo}, []string{"config.go", "go.mod", "values-schema.json"}},
		{"rust", []string{LangRust}, []string{"src/config.rs", "values-schema.json"}},
		{"go and rust", []string{LangGo, LangRust}, []string{"config.go", "go.mod", "values-schema.json"}},
		{"go and typescript", []string{LangGo, LangTypeScript}, []string{"config.go", "go.mod", "values-schema.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-general"))
			if err != nil {
				t.Fatal(err)
			}
			types := map[string]any{}
			for _, lang := range tc.types {
				types[lang] = map[string]any{"enabled": true}
			}
			cfg.Outputs = map[string]any{"types": types}

			out := t.TempDir()
			var log bytes.Buffer
			if _, err := Run(schema, cfg, Options{OutputRoot: out, Naming: naming.Default(), Log: &log}); err != nil {
				t.Fatal(err)
			}

			dir := APIDir(out, "fixture-general")
			var got []string
			if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, err := filepath.Rel(dir, path)
				got = append(got, filepath.ToSlash(rel))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("%s holds %v, want %v", dir, got, tc.want)
			}

			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				if _, err := os.Stat(filepath.Join(TypesDir(out, LangGo, "fixture-general"), "go.mod")); err != nil {
					t.Errorf("the env loader's go.mod requires a Go types module the run did not write: %v", err)
				}
			}

			wantTS := slices.Contains(tc.types, LangTypeScript)
			_, err = os.Stat(filepath.Join(TypesDir(out, LangTypeScript, "fixture-general"), "config.ts"))
			if hasTS := err == nil; hasTS != wantTS {
				t.Errorf("the TypeScript types package has config.ts = %t, want %t", hasTS, wantTS)
			}

			skipped := strings.Contains(log.String(), envLoaderSkippedLine)
			if wantSkipped := tc.want[0] == "values-schema.json" && !wantTS; skipped != wantSkipped {
				t.Errorf("log has the no-loader line = %t, want %t; log:\n%s", skipped, wantSkipped, log.String())
			}
		})
	}
}
