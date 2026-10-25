package memorystoregen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// buildRust builds shop-cache-common's Rust types and shop-cache's Rust
// definitions into one new output root, against the checkout's runtimes,
// with strict set on StockBin (@denyUnknownFields) when strict is true,
// and returns the definitions crate's directory.
func buildRust(t *testing.T, strict bool) string {
	t.Helper()
	f := load(t)
	if strict {
		f.schemas["shop-cache-common"].Types["StockBin"].DenyUnknownFields = true
	}
	out := testpaths.TempDir(t)
	opts := generator.Options{
		OutputRoot:     out,
		Naming:         naming.Default(),
		Paths:          testpaths.Local(t),
		LoadDependency: f.dependency,
		DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			cfg, ok := f.configs[name]
			return cfg, ok
		},
	}
	common := *f.configs["shop-cache-common"]
	common.Outputs = map[string]any{"types": map[string]any{"rust": map[string]any{"enabled": true}}}
	if _, err := generator.Run(f.schemas["shop-cache-common"], &common, opts); err != nil {
		t.Fatalf("build shop-cache-common: %v", err)
	}
	cfg := *f.configs[store]
	cfg.Outputs = map[string]any{"types": map[string]any{"rust": map[string]any{"enabled": true}}}
	if _, err := generator.Run(f.schemas[store], &cfg, opts); err != nil {
		t.Fatalf("build %s: %v", store, err)
	}
	return generator.TypesDir(out, "rust", store)
}

// definitionsTest exercises the generated definitions against the
// runtime's in-process store: each For's name, a refused part and TTL,
// and a payload whose JSON holds keys StockLevel and StockBin do not
// declare.
const definitionsTest = `use std::sync::{Arc, Mutex};
use std::time::Duration;

use schemas_shop_cache_common_types::{Region, StockLevel};
use schemas_shop_cache_types::{PRODUCT_VIEWS, RECEIPT_SENT, STOCK_CHANGED, STOCK_SAMPLES};
use superschematic_http_runtime::memorystore::{InProcessMemoryStore, MemoryStore, MemoryStoreError};

fn product() -> superscalar::Uuid {
    "3f2a9c1e-7b4d-4e8a-9c2f-1d5e6a7b8c9d".parse().unwrap()
}

#[test]
fn each_definition_names_its_parts_in_order() {
    let id = product();
    let changed = STOCK_CHANGED.for_(&id).unwrap();
    assert_eq!(changed.name(), format!("stock-changed:{id}"));
    assert_eq!(changed.declaration(), "stock-changed");
    let views = PRODUCT_VIEWS
        .for_ttl(Duration::from_secs(3600), &id, 20261025, Region::North, true, "a label")
        .unwrap();
    assert_eq!(views.name(), format!("views:{id}:20261025:north:true:a label"));
    assert_eq!(views.ttl(), Duration::from_secs(3600));
    assert_eq!(RECEIPT_SENT.for_(&id).unwrap().ttl(), Duration::from_secs(600));
    assert!(matches!(
        PRODUCT_VIEWS.for_ttl(Duration::from_secs(86401), &id, 1, Region::South, false, "a"),
        Err(MemoryStoreError::Ttl(_))
    ));
    assert!(matches!(
        PRODUCT_VIEWS.for_ttl(Duration::from_secs(1), &id, 1, Region::South, false, "a:b"),
        Err(MemoryStoreError::Name(_))
    ));
}

#[tokio::test]
async fn a_payload_with_keys_it_does_not_declare_decodes() {
    let store = InProcessMemoryStore::new("shop-cache");
    let samples = STOCK_SAMPLES.for_(&product()).unwrap();
    let got = Arc::new(Mutex::new(Vec::new()));
    let seen = Arc::clone(&got);
    let _subscription = samples
        .subscribe(&store, move |level: StockLevel| seen.lock().unwrap().push(level.bins.len()))
        .await
        .unwrap();
    let message = format!(
        r#"{{"productId":"{}","onHand":3,"bins":[{{"bin":"a","count":3,"shelf":7}}],"since":"later"}}"#,
        product()
    );
    store
        .publish(samples.reference(), message.as_bytes())
        .await
        .unwrap();
    assert_eq!(*got.lock().unwrap(), vec![1]);
}
`

// TestRustDefinitionsBuild builds the generated definitions crate with
// cargo: rustfmt finds nothing to change in it, clippy nothing to warn of,
// and its test names each declaration's parts and decodes a payload that
// holds keys its types do not declare, once as the fixture declares them
// and once with @denyUnknownFields on a nested type, which the crate then
// decodes through a decoder of its own.
func TestRustDefinitionsBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the cargo build in short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available")
	}
	targetDir := os.Getenv("CARGO_TARGET_DIR")
	if targetDir == "" {
		targetDir = filepath.Join(t.TempDir(), "target")
	}
	for _, strict := range []bool{false, true} {
		name := "lenient"
		if strict {
			name = "strict"
		}
		t.Run(name, func(t *testing.T) {
			dir := buildRust(t, strict)
			lib, err := os.ReadFile(filepath.Join(dir, "src", "lib.rs"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(lib), "fn decode_stock_level("); got != strict {
				t.Fatalf("a decoder of its own: %v, want %v:\n%s", got, strict, lib)
			}
			cargoIn := func(args ...string) {
				t.Helper()
				cmd := exec.Command(cargo, args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+targetDir)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("cargo %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
			// The crate as the build wrote it.
			cargoIn("fmt", "--check")
			cargoIn("clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
			manifest := filepath.Join(dir, "Cargo.toml")
			data, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("\n[dev-dependencies]\ntokio = { version = \"1.52.2\", features = [\"macros\", \"rt\"] }\n")...)
			if err := os.WriteFile(manifest, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "tests", "definitions.rs"), []byte(definitionsTest), 0o644); err != nil {
				t.Fatal(err)
			}
			cargoIn("test", "--quiet")
		})
	}
}
