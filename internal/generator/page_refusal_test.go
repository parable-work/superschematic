package generator

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
)

// D60: until a server or SDK generator renders @paginated, it refuses an
// API with a paged operation, naming itself and the operation, so a
// half-built batch fails rather than answer or read a list where another
// generator answers a page. Each row goes when its generator renders
// pages.
func TestServersAndSDKsRefusePagesTheyDoNotRender(t *testing.T) {
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	for _, tc := range []struct {
		name      string
		lang      string // the types the generator reads
		api       string // the server, or "" for an SDK
		generator string
	}{
		{"TypeScript server", LangTypeScript, APILanguageTypeScript, "the TypeScript server (tsrestgen)"},
		{"Rust server", LangRust, APILanguageRust, "the Rust server (rustrestgen)"},
		{"Go SDK", LangGo, "", "the Go SDK (gosdkgen)"},
		{"TypeScript SDK", LangTypeScript, "", "the TypeScript SDK (sdkgen)"},
		{"Python SDK", LangPython, "", "the Python SDK (pysdkgen)"},
		{"Rust SDK", LangRust, "", "the Rust SDK (rustsdkgen)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-paged-api"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.Outputs["types"] = map[string]any{tc.lang: map[string]any{"enabled": true}}
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": tc.api}
			cfg.Outputs["sdk"] = map[string]any{}
			if tc.api == "" {
				cfg.Outputs["api"] = map[string]any{"enabled": false}
				cfg.Outputs["sdk"] = map[string]any{tc.lang: map[string]any{"enabled": true}}
			}
			root := t.TempDir()
			paths := naming.LocalPaths{HTTPRuntimeRust: filepath.Join(root, "runtime", "http", "rust")}
			_, err = Run(schema, cfg, Options{OutputRoot: root, Naming: naming.Default(), Paths: paths, Clock: clock})
			want := tc.generator + " does not render @paginated yet (D60): Catalog.listItems"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Run = %v, want %s", err, want)
			}
		})
	}
}
