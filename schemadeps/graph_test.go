package schemadeps

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectFromDist_TypeScriptOmitsWebDBFromAPI(t *testing.T) {
	dist := t.TempDir()
	writeTSPackage(t, dist, "types/typescript/web-api", "@schemas/web-api-types", map[string]string{
		"@schemas/enums-types": "workspace:*",
		"superscalar":          "file:../../../../../third_party/superscalar/bindings/typescript",
	})
	writeTSPackage(t, dist, "types/typescript/enums", "@schemas/enums-types", nil)
	writeTSPackage(t, dist, "types/typescript/web-db", "@schemas/web-db-types", map[string]string{
		"@schemas/enums-types": "workspace:*",
	})
	writeTSPackage(t, dist, "sdk/typescript/web-api", "@schemas/web-api-sdk", map[string]string{
		"axios": "1.0.0",
	})

	g, err := CollectFromDist(dist, nil)
	if err != nil {
		t.Fatalf("CollectFromDist: %v", err)
	}

	api := mustPkg(t, g, "typescript", "web-api-types")
	for _, dep := range api.Deps {
		if dep == "web-db-types" {
			t.Fatalf("web-api-types must not depend on web-db-types, deps=%v", api.Deps)
		}
	}
	if len(api.Deps) != 1 || api.Deps[0] != "enums-types" {
		t.Fatalf("web-api-types deps = %v, want [enums-types]", api.Deps)
	}

	sdk := mustPkg(t, g, "typescript", "web-api-sdk")
	foundTypes := false
	for _, dep := range sdk.Deps {
		if dep == "web-api-types" {
			foundTypes = true
		}
	}
	if !foundTypes {
		t.Fatalf("web-api-sdk should depend on web-api-types, deps=%v", sdk.Deps)
	}
}

func TestCollectFromDist_AnnotatesProducingService(t *testing.T) {
	dist := t.TempDir()
	writeTSPackage(t, dist, "types/typescript/orders-api", "@schemas/orders-api-types", map[string]string{
		"@schemas/enums-types": "workspace:*",
	})
	writeTSPackage(t, dist, "types/typescript/enums", "@schemas/enums-types", nil)

	g, err := CollectFromDist(dist, map[string]string{
		"types/typescript/orders-api": "orders-api",
		"types/typescript/enums":      "enums",
		// Output dirs that hold no package (SQL, extension output) are in
		// the map too and must be ignored.
		"sql/orders-api": "orders-api",
	})
	if err != nil {
		t.Fatalf("CollectFromDist: %v", err)
	}
	if got := mustPkg(t, g, "typescript", "orders-api-types").Service; got != "orders-api" {
		t.Fatalf("orders-api-types service = %q, want orders-api", got)
	}
	if got := mustPkg(t, g, "typescript", "enums-types").Service; got != "enums" {
		t.Fatalf("enums-types service = %q, want enums", got)
	}

	path := filepath.Join(t.TempDir(), DepsFileName)
	if err := Write(path, g); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"service": "orders-api"`) {
		t.Fatalf("written graph lacks the service field:\n%s", data)
	}
}

func TestCollectFromDist_NilProducersOmitService(t *testing.T) {
	dist := t.TempDir()
	writeTSPackage(t, dist, "types/typescript/enums", "@schemas/enums-types", nil)

	g, err := CollectFromDist(dist, nil)
	if err != nil {
		t.Fatalf("CollectFromDist: %v", err)
	}
	path := filepath.Join(t.TempDir(), DepsFileName)
	if err := Write(path, g); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"service"`) {
		t.Fatalf("service must be omitted when no producers are given:\n%s", data)
	}
}

func TestCollectFromDist_PackageWithoutProducerIsAnError(t *testing.T) {
	dist := t.TempDir()
	writeTSPackage(t, dist, "types/typescript/enums", "@schemas/enums-types", nil)
	writeTSPackage(t, dist, "types/typescript/removed", "@schemas/removed-types", nil)

	_, err := CollectFromDist(dist, map[string]string{"types/typescript/enums": "enums"})
	if err == nil {
		t.Fatal("expected an error for a package no schema service produced")
	}
	for _, want := range []string{"1 package(s)", "typescript/removed-types at types/typescript/removed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should contain %q, got: %v", want, err)
		}
	}
}

func TestClosureAndWrite(t *testing.T) {
	g := &Graph{
		Version: 1,
		Packages: []Package{
			{ID: "enums-types", Language: "typescript", Kind: "types", Name: "@schemas/enums-types", Path: "types/typescript/enums"},
			{ID: "web-api-types", Language: "typescript", Kind: "types", Name: "@schemas/web-api-types", Path: "types/typescript/web-api", Deps: []string{"enums-types"}},
			{ID: "web-api-sdk", Language: "typescript", Kind: "sdk", Name: "@schemas/web-api-sdk", Path: "sdk/typescript/web-api", Deps: []string{"web-api-types"}},
		},
	}
	ids, err := g.Closure("typescript", []string{"web-api-sdk"})
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}
	want := map[string]bool{"enums-types": true, "web-api-types": true, "web-api-sdk": true}
	if len(ids) != 3 {
		t.Fatalf("Closure len=%d ids=%v", len(ids), ids)
	}
	for _, id := range ids {
		if !want[id] {
			t.Fatalf("unexpected id %q", id)
		}
	}

	path := filepath.Join(t.TempDir(), ".deps.json")
	if err := Write(path, g); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Packages) != 3 {
		t.Fatalf("Read packages=%d", len(got.Packages))
	}
}

func writeTSPackage(t *testing.T, dist, rel, name string, deps map[string]string) {
	t.Helper()
	dir := filepath.Join(dist, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"name": name}
	if deps != nil {
		manifest["dependencies"] = deps
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustPkg(t *testing.T, g *Graph, language, id string) Package {
	t.Helper()
	for _, p := range g.Packages {
		if p.Language == language && p.ID == id {
			return p
		}
	}
	t.Fatalf("missing package %s/%s", language, id)
	return Package{}
}
