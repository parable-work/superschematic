package tsreader

import (
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestAppsAreNoTypes: each @app class of an API is an app of it, in
// declaration order, with its comment and the extensions it names, and no
// type of the schema (D78).
func TestAppsAreNoTypes(t *testing.T) {
	schema, _, err := LoadService("testdata/services/fixture-app-api")
	if err != nil {
		t.Fatal(err)
	}
	want := []*ir.App{
		{Name: "Backoffice", Comment: "The staff's console over the stock, with its pages.", Extensions: []string{"topcoat"}},
		{Name: "Kiosk", Comment: "A kiosk of the project's own."},
	}
	if !reflect.DeepEqual(schema.Apps, want) {
		t.Errorf("Apps = %+v, want %+v", schema.Apps, want)
	}
	for _, name := range []string{"Backoffice", "Kiosk"} {
		if schema.Types[name] != nil {
			t.Errorf("app %s is a type", name)
		}
	}
}

// TestAppRefusals: what @app refuses, where it is written, and that only
// an API declares one.
func TestAppRefusals(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { app } from "@superschematic/api";

@app({ extensions: ["topcoat", "topcoat"] })
export abstract class Twice {}

@app({ extensions: ["topcoat.pages"] })
export abstract class Dotted {}

@app({ pages: true } as never)
export abstract class Unknown {}

@app()
export abstract class WithFields {
  port: number;
}
`})
	_, _, err := LoadService(dir)
	if err == nil {
		t.Fatal("the load passed")
	}
	for _, want := range []string{
		`@app Twice: names extension topcoat twice`,
		`@app Dotted: names extension "topcoat.pages", and an extension's name has no dot`,
		`@app has no option "pages"; it takes extensions`,
		`@app class WithFields has fields`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%s", want, err)
		}
	}
	dir = decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { app } from "@superschematic/api";

@app()
export abstract class Backoffice {}
`})
	if _, _, err := LoadService(dir); err == nil || !strings.Contains(err.Error(), "@app is only allowed in API schemas (this service is kind DB)") {
		t.Fatalf("load = %v, want @app refused in a DB schema", err)
	}
}
