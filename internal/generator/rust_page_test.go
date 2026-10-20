package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestTheRustServerRendersPages builds fixture-paged-api's Rust server
// through Run, as a build does, and finds each paged operation's page in
// the crate it writes and in RustAPIOf, the record the in-process call
// and Topcoat read (D60): its limits, the element of its page, the page it
// returns, and listItems's sort (D60, amended), which its PAGE holds and
// its Args carry in page alone.
func TestTheRustServerRendersPages(t *testing.T) {
	service := "fixture-paged-api"
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Outputs["types"] = map[string]any{LangRust: map[string]any{"enabled": true}}
	cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust}
	cfg.Outputs["sdk"] = map[string]any{}
	root := t.TempDir()
	paths := naming.LocalPaths{HTTPRuntimeRust: filepath.Join(root, "runtime", "http", "rust")}
	opts := Options{
		OutputRoot: root,
		Naming:     naming.Default(),
		Paths:      paths,
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	}
	if _, err := Run(schema, cfg, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	interfaces, err := os.ReadFile(filepath.Join(APIDir(root, service), "src", "interfaces.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "-> Result<runtime::page::Page<types::ItemView>, ApiError>;"; !strings.Contains(string(interfaces), want) {
		t.Errorf("interfaces.rs does not contain %q", want)
	}
	if strings.Contains(string(interfaces), "pub sort:") {
		t.Error("interfaces.rs gives the sort a field of its own; the page holds it")
	}
	router, err := os.ReadFile(filepath.Join(APIDir(root, service), "src", "router.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `pub const PAGE: PageSpec = PageSpec::new(2, 5).sorted(SortSpec::new("sort", &["name", "sku"], "name"));`; !strings.Contains(string(router), want) {
		t.Errorf("router.rs does not contain %q", want)
	}

	outputs, err := ParseOutputs(cfg.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newRun(schema, cfg, outputs, opts, CoreRegistry(opts.Naming))
	output, err := RustAPIOf(r.GenerateContext)
	if err != nil || output == nil {
		t.Fatalf("RustAPIOf: %v, %v", output, err)
	}
	for _, endpoint := range output.Endpoints {
		if endpoint.Name != "listItems" {
			continue
		}
		if page := endpoint.Page; page == nil || page.DefaultLimit != 2 || page.MaxLimit != 5 || page.ItemRustType != "types::ItemView" {
			t.Errorf("catalog.listItems page = %+v", page)
		} else if sort := page.Sort; sort == nil || sort.Param != "sort" || strings.Join(sort.Fields, ",") != "name,sku" || sort.Default != "name" {
			t.Errorf("catalog.listItems sort = %+v", sort)
		}
		for _, arg := range endpoint.QueryArgs {
			if arg.Name == "sort" {
				t.Error("catalog.listItems's Args carry the sort as a query field")
			}
		}
		return
	}
	t.Error("RustAPIOf has no catalog.listItems")
}
