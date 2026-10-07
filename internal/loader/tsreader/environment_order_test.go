package tsreader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// environmentOrderService lays out a temp Stack service whose tsconfig
// extends stackgen's fixtures', which resolves the authoring packages,
// including @superschematic/stack, and the sentinels of shop-api, shop-db
// and shop-orders. files are paths relative to the service directory.
func environmentOrderService(t *testing.T, files map[string]string) string {
	t.Helper()
	base, err := filepath.Abs("../../generator/stackgen/testdata/tsconfig.base.json")
	if err != nil {
		t.Fatal(err)
	}
	tsconfig, err := json.Marshal(map[string]any{"extends": filepath.ToSlash(base), "include": []string{"src/**/*.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	all := map[string]string{
		"package.json":       `{"private": true}`,
		"schema.config.json": `{"name": "shop-stack", "kind": "Stack", "outputs": {}}`,
		"tsconfig.json":      string(tsconfig),
	}
	for rel, contents := range files {
		all[rel] = contents
	}
	for rel, contents := range all {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// loadEnvironmentOrders loads a stack service and returns each
// environment's order and the environments as StackOf lists them.
func loadEnvironmentOrders(t *testing.T, dir string) (map[string]int, []string) {
	t.Helper()
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	orders := map[string]int{}
	for name, td := range schema.Types {
		if td.Environment != nil {
			orders[name] = td.Environment.Order
		}
	}
	st := ir.StackOf(schema)
	if st == nil {
		t.Fatal("StackOf = nil")
	}
	var names []string
	for _, env := range st.Environments {
		names = append(names, env.Name)
	}
	return orders, names
}

// TestEnvironmentsAreNumberedInSourceOrder: the reader numbers the
// @environment classes of a file from 1 in source order, which is not
// their names' order, and counts no other class.
func TestEnvironmentsAreNumberedInSourceOrder(t *testing.T) {
	dir := environmentOrderService(t, map[string]string{
		"src/stack.schema.ts": `import { ShopApi } from "@schemas/shop-api";
import { ShopOrders } from "@schemas/shop-orders";
import { environment, server, stack } from "@superschematic/stack";

@environment({ target: "local", local: { postgresPort: 5433 } })
export abstract class Staging {}

@stack({ deploy: [ShopApi, ShopOrders] })
export abstract class Shop {}

@environment({ target: "local", local: { postgresPort: 5434 } })
export abstract class Production {}

@server({ serves: [ShopOrders] })
export abstract class Orders {}

@environment({ parameters: ["pr"] })
export abstract class Preview extends Staging {}
`,
	})
	orders, names := loadEnvironmentOrders(t, dir)
	if want := map[string]int{"Staging": 1, "Production": 2, "Preview": 3}; !reflect.DeepEqual(orders, want) {
		t.Errorf("orders = %v, want %v", orders, want)
	}
	if want := []string{"Staging", "Production", "Preview"}; !reflect.DeepEqual(names, want) {
		t.Errorf("StackOf environments = %v, want %v", names, want)
	}
}

// TestEnvironmentsAreNumberedAcrossFilesInPathOrder: the count runs over
// the service's schema files in path order, so a file's environments come
// after those of every file before it, whatever the classes' names.
func TestEnvironmentsAreNumberedAcrossFilesInPathOrder(t *testing.T) {
	dir := environmentOrderService(t, map[string]string{
		"src/a-stack.schema.ts": `import { ShopApi } from "@schemas/shop-api";
import { environment, stack } from "@superschematic/stack";

@stack({ deploy: [ShopApi] })
export abstract class Shop {}

@environment({ target: "local" })
export abstract class Staging {}
`,
		"src/b-production.schema.ts": `import { environment } from "@superschematic/stack";

@environment({ target: "local" })
export abstract class Production {}

@environment({ target: "local" })
export abstract class Canary {}
`,
		"src/environments/dev.schema.ts": `import { environment } from "@superschematic/stack";

@environment({ target: "local" })
export abstract class Dev {}
`,
	})
	orders, names := loadEnvironmentOrders(t, dir)
	if want := map[string]int{"Staging": 1, "Production": 2, "Canary": 3, "Dev": 4}; !reflect.DeepEqual(orders, want) {
		t.Errorf("orders = %v, want %v", orders, want)
	}
	if want := []string{"Staging", "Production", "Canary", "Dev"}; !reflect.DeepEqual(names, want) {
		t.Errorf("StackOf environments = %v, want %v", names, want)
	}
}
