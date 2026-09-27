package ext_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/loader"
	"github.com/parable-work/superschematic/registry"

	"example.com/acme/schematic/ext"
)

// shopRatings is a General service whose one type composes acme.Rating in
// the JSON data form.
const shopRatings = "testdata/services/shop-ratings"

// TestRatingBehavior: acme declares acme.Rating with no core edit, a
// General schema composes it and the IR carries it; the generators refuse
// the schema until they render behaviors; the core registry does not know
// the behavior.
func TestRatingBehavior(t *testing.T) {
	reg, names := assemble(t)
	rating, ok := reg.Behavior(ext.RatingBehavior)
	if !ok {
		t.Fatalf("%s is not registered (behaviors: %v)", ext.RatingBehavior, reg.BehaviorNames())
	}
	if rating.Extension != ext.Name || len(rating.Operations) != 2 || rating.Operations[0].InvocationPolicy != ext.ConfirmAlways {
		t.Fatalf("%s = %+v", ext.RatingBehavior, rating)
	}

	schema, cfg, err := loader.LoadServiceWithConfig(shopRatings, loader.WithRegistry(reg), loader.WithNaming(names))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	refs := schema.Types["Product"].Behaviors
	if len(refs) != 1 || refs[0].Name != ext.RatingBehavior || string(refs[0].Config) != `{"maxStars":5}` {
		t.Fatalf("Product behaviors = %+v", refs)
	}
	if _, err := registry.Generate(schema, cfg, registry.Options{OutputRoot: t.TempDir(), ServicePath: shopRatings, Naming: names, Registry: reg}); err == nil ||
		!strings.Contains(err.Error(), "types does not render behaviors yet: type Product composes behavior acme.Rating") {
		t.Fatalf("Generate: err = %v, want the types generator's refusal", err)
	}

	// A config the declaration rejects fails the load, naming the type and
	// the behavior.
	dir := t.TempDir()
	for _, file := range []string{"schema.config.json", "src/product.schema.json"} {
		data, err := os.ReadFile(filepath.Join(shopRatings, file))
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.Replace(string(data), `"maxStars": 5`, `"maxStars": 20`, 1))
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loader.LoadService(dir, loader.WithRegistry(reg), loader.WithNaming(names)); err == nil ||
		!strings.Contains(err.Error(), `type "Product": behavior acme.Rating config: `) {
		t.Fatalf("maxStars 20: err = %v", err)
	}

	core, err := registry.Assemble(registry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadService(shopRatings, loader.WithRegistry(core)); err == nil ||
		!strings.Contains(err.Error(), `behavior "acme.Rating" on type "Product" is not a registered behavior (none are registered)`) {
		t.Fatalf("core registry: err = %v", err)
	}
}
