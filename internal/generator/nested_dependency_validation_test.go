package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// buildDependencyTypesTree runs shop-common, shop-db, shop-ids and
// shop-orders of the dependency-types fixture with every type language on,
// into one output root, as build --with-deps does, with [paths] pointing at
// this checkout. The root is a real path: cargo and Bun resolve the
// generated relative paths from it (macOS /var is /private/var).
func buildDependencyTypesTree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, opts := loadDependencyTypesFixture(t, true)
	opts.OutputRoot = root
	opts.Paths = testpaths.Local(t)
	for _, service := range []string{"shop-common", "shop-db", "shop-ids", "shop-orders"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(dependencyTypesFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		if service != "shop-orders" {
			widened, _ := opts.DependencyConfig(service)
			cfg.Outputs = widened.Outputs
		}
		if _, err := Run(schema, cfg, opts); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
	return root
}

// TestANestedDependencyObjectIsValidated checks that the Rust and
// TypeScript validators of shop-orders' OrderView validate its total, a
// Money that shop-common declares, with shop-common's own validator, as the
// Go and Python validators do (D14, amended): a wrong amount and an unknown
// currency are reported under total, and a valid Money passes.
func TestANestedDependencyObjectIsValidated(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	root := buildDependencyTypesTree(t)
	const invalid = `{"slug": "a", "total": {"amount": "1", "currency": "gbp"}, "status": "open"}`
	const valid = `{"slug": "a", "total": {"amount": 1, "currency": "usd"}, "status": "open"}`

	t.Run("rust", func(t *testing.T) {
		cargo, err := exec.LookPath("cargo")
		if err != nil {
			t.Skip("cargo not available")
		}
		crate := filepath.Join(root, "types", "rust", "shop-orders")
		ident := strings.ReplaceAll(naming.Default().RustTypesCrate("shop-orders"), "-", "_")
		test := fmt.Sprintf(`use %s::validators::validate_order_view;

#[test]
fn a_nested_dependency_object_is_validated() {
    let errors = validate_order_view(&serde_json::from_str(r#"%s"#).unwrap());
    let errors = serde_json::to_value(&errors).unwrap();
    assert_eq!(errors["total"]["amount"][0]["validator"], "type", "{errors}");
    assert_eq!(errors["total"]["currency"][0]["validator"], "enum", "{errors}");
    assert!(validate_order_view(&serde_json::from_str(r#"%s"#).unwrap()).is_empty());
}
`, ident, invalid, valid)
		writeFile(t, filepath.Join(crate, "tests", "nested.rs"), test)
		cmd := exec.Command(cargo, "test", "--quiet")
		cmd.Dir = crate
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo test: %v\n%s", err, out)
		}
	})

	t.Run("typescript", func(t *testing.T) {
		bun, err := exec.LookPath("bun")
		if err != nil {
			testpaths.RequireOrSkipTS(t, "bun not available for the TypeScript check")
		}
		typesRoot := filepath.Join(root, "types", "typescript")
		install := exec.Command(bun, "install")
		install.Dir = typesRoot
		if out, err := install.CombinedOutput(); err != nil {
			testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed (likely offline): %v\n%s", err, out))
		}
		pkg := filepath.Join(typesRoot, "shop-orders")
		// The package has no Node types, so the check throws on its own.
		writeFile(t, filepath.Join(pkg, "nested.check.ts"), fmt.Sprintf(`import { validateOrderView } from './validators';

const errors = validateOrderView(%s as never);
const total = errors === true ? undefined : (errors as unknown as Record<string, Record<string, { validator: string }[]>>).total;
if (total?.amount?.[0]?.validator !== 'type' || total?.currency?.[0]?.validator !== 'enum') {
  throw new Error('want total.amount type and total.currency enum, got ' + JSON.stringify(errors));
}
if (validateOrderView(%s as never) !== true) {
  throw new Error('a valid OrderView failed');
}
`, invalid, valid))
		for _, args := range [][]string{{"x", "tsc", "--noEmit"}, {"run", "nested.check.ts"}} {
			cmd := exec.Command(bun, args...)
			cmd.Dir = pkg
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bun %s: %v\n%s", strings.Join(args, " "), err, out)
			}
		}
	})
}

// writeFile writes text to path, creating its directory.
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
