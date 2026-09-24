package schemadeps

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A generated TypeScript API package (dist/api/<service>/package.json, the
// Hono server) is a publishable package like the type and SDK packages: it
// joins the graph as <service>-api with its scoped peers as edges. The Go and
// Rust servers share the api/ directory and carry no package.json, so they
// stay invisible to the TypeScript collector.
func TestCollectTypeScriptAPIPackage(t *testing.T) {
	dist := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dist, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("types/typescript/orders-api/package.json", `{"name":"@schemas/orders-api-types","dependencies":{"superscalar":"*"}}`)
	write("api/orders-api/package.json", `{"name":"@schemas/orders-api-api","peerDependencies":{"@schemas/orders-api-types":"*","@superschematic/http-runtime":"*","hono":"4.13.8"}}`)
	write("api/billing-api/go.mod", "module example.com/schemas/api/billing-api\n")

	graph, err := CollectFromDist(dist, map[string]string{
		"types/typescript/orders-api": "orders-api",
		"api/orders-api":              "orders-api",
		"api/billing-api":             "billing-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := graph.ByLanguage("typescript")
	api, ok := ts["orders-api-api"]
	if !ok {
		t.Fatalf("typescript api package not collected: %v", ts)
	}
	if api.Kind != "api" || api.Path != "api/orders-api" || api.Name != "@schemas/orders-api-api" || api.Service != "orders-api" {
		t.Errorf("api package = %+v", api)
	}
	if !reflect.DeepEqual(api.Deps, []string{"orders-api-types"}) {
		t.Errorf("api deps = %v, want [orders-api-types]", api.Deps)
	}
	if _, ok := ts["billing-api-api"]; ok {
		t.Error("the Go api module must not appear as a TypeScript package")
	}
	if closure, err := graph.Closure("typescript", []string{"orders-api-api"}); err != nil || !reflect.DeepEqual(closure, []string{"orders-api-types", "orders-api-api"}) {
		t.Errorf("closure = %v (%v)", closure, err)
	}
}
