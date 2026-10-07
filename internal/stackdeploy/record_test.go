package stackdeploy_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// shopSchemaTS is the shop stack of stacktest.Shop as an engineer writes
// it: Staging declares the project, and Preview extends it.
const shopSchemaTS = `import { ShopApi } from "@schemas/shop-api";
import { ShopDb } from "@schemas/shop-db";
import { ShopOrders } from "@schemas/shop-orders";
import { environment, server, stack } from "@superschematic/stack";

@stack({ deploy: [ShopApi, ShopOrders], expose: [ShopApi] })
export abstract class Shop {}

@server({ serves: [ShopOrders] })
export abstract class Orders {}

// Staging, whose project its previews share.
@environment({
  target: "fake",
  fake: { project: "acme-staging", region: "us-east1" },
  domain: "staging.acme.dev",
  dns: { "fake.dns": { zone: "acme.dev" } },
  settings: [{ of: Orders, env: { FULFILLMENT_REGION: "us" } }]
})
export abstract class Staging {}

@environment({
  target: "fake",
  fake: {
    project: "acme-prod", // production's own
    region: "us-east1",
    production: true,
  },
  domain: "acme.dev",
  settings: [
    { of: ShopDb, tier: "large", highAvailability: true },
    { of: ShopApi, minInstances: 1, env: { LOG_LEVEL: "warn" } },
    { of: ShopOrders, env: { FULFILLMENT_REGION: "us" } }
  ]
})
export abstract class Production {}

@environment({
  parameters: ["pr"],
  settings: [{ of: ShopApi, env: { PREVIEW_ID: { parameter: "pr" } } }]
})
export abstract class Preview extends Staging {}
`

// shopSource writes content as the shop stack's schema file, at file
// under a stack directory of its own, and returns the source the loader
// would read from it: stacktest.Shop's declarations, each class declared
// in file.
func shopSource(t *testing.T, file, content string) *stackdeploy.SchemaSource {
	t.Helper()
	dir := t.TempDir()
	if content != "" {
		path := filepath.Join(dir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st := stacktest.Shop()
	schema := &ir.Schema{Name: st.Name, Kind: ir.SchemaKindStack, Types: map[string]*ir.TypeDef{
		"Shop": {Name: "Shop", Owner: file, Stack: &ir.StackDecl{Deploy: st.Deploy, Expose: st.Expose}},
	}}
	for _, d := range st.Deployables {
		schema.Types[d.Name] = &ir.TypeDef{Name: d.Name, Owner: file, Server: &ir.ServerDecl{Serves: d.Serves}}
	}
	for _, env := range st.Environments {
		schema.Types[env.Name] = &ir.TypeDef{Name: env.Name, Owner: file, Extends: env.Extends, Environment: &ir.EnvironmentDecl{
			Target: env.Target, Values: env.Values, Domain: env.Domain, DNS: env.DNS, Settings: env.Settings, Parameters: env.Parameters,
		}}
	}
	return &stackdeploy.SchemaSource{Schema: schema, Dir: dir}
}

// projectNumber is the value the fake target's bootstrap returns.
func projectNumber(value string) registry.BootstrapValue {
	return registry.BootstrapValue{Key: "projectNumber", Value: value, Beside: "project"}
}

// bootstrapWith runs a bootstrap of the environment named name whose
// target returns values, over src.
func (f *fixture) bootstrapWith(t *testing.T, name string, src *stackdeploy.SchemaSource, values ...registry.BootstrapValue) ([]stackdeploy.RecordedValue, error) {
	t.Helper()
	env := f.env(t, name)
	f.setSecret(t, env, dnsToken.Secret, "dns-token")
	f.ext.Bootstrap.Values = values
	return stackdeploy.Bootstrap(context.Background(), stackdeploy.BootstrapOptions{Options: f.options(t, env, nil), Source: src})
}

// TestBootstrapRecordsValues bootstraps Preview, which extends Staging:
// the project's number is recorded on Staging, which declares the project,
// and in the TypeScript file nothing else changes; a second run finds it
// and leaves the file alone; a new number updates it.
func TestBootstrapRecordsValues(t *testing.T) {
	f := newFixture(t)
	src := shopSource(t, "src/stack.schema.ts", shopSchemaTS)
	path := filepath.Join(src.Dir, "src", "stack.schema.ts")
	read := func() string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	recorded, err := f.bootstrapWith(t, "Preview", src, projectNumber("123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || recorded[0].Outcome != stackdeploy.ValueRecorded || recorded[0].Environment != "Staging" || recorded[0].File != "src/stack.schema.ts" {
		t.Fatalf("recorded %+v", recorded)
	}
	if got, want := recorded[0].String(), "projectNumber 123456789012: recorded on Staging in src/stack.schema.ts"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	staging := `fake: { project: "acme-staging", region: "us-east1" },`
	want := strings.Replace(shopSchemaTS, staging, `fake: { project: "acme-staging", projectNumber: "123456789012", region: "us-east1" },`, 1)
	if got := read(); got != want {
		t.Fatalf("the schema file is\n%s\nwant\n%s", got, want)
	}

	// The schema, read again, holds the number. A second bootstrap finds it
	// and writes nothing: the file is read-only, and a write would fail.
	src.Schema.Types["Staging"].Environment.Values = map[string]any{"project": "acme-staging", "projectNumber": "123456789012", "region": "us-east1"}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	recorded, err = f.bootstrapWith(t, "Preview", src, projectNumber("123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[0].String(), "projectNumber 123456789012: matches Staging in src/stack.schema.ts"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	if got := read(); got != want {
		t.Errorf("a matching bootstrap changed the file:\n%s", got)
	}

	// A project whose number differs from the schema's is written again,
	// in the literal's text alone, and said so.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	recorded, err = f.bootstrapWith(t, "Staging", src, projectNumber("999999999999"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[0].String(), "projectNumber 999999999999: updated from 123456789012 on Staging in src/stack.schema.ts"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	if got, want := read(), strings.Replace(want, `"123456789012"`, `"999999999999"`, 1); got != want {
		t.Errorf("the updated file is\n%s\nwant\n%s", got, want)
	}

	// Production declares its own project, and its number is written on
	// its own line there.
	before := read()
	recorded, err = f.bootstrapWith(t, "Production", src, projectNumber("210987654321"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[0].String(), "projectNumber 210987654321: recorded on Production in src/stack.schema.ts"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	prod := `    project: "acme-prod", // production's own` + "\n"
	if got, want := read(), strings.Replace(before, prod, prod+`    projectNumber: "210987654321",`+"\n", 1); got != want {
		t.Errorf("the file is\n%s\nwant\n%s", got, want)
	}
}

// TestBootstrapValuesByHand covers the values bootstrap does not write: a
// JSON or YAML schema's, which it says how to add or change, a TypeScript
// file the edit refuses, and a bootstrap with no schema source. An
// environment between that sets the value too is named.
func TestBootstrapValuesByHand(t *testing.T) {
	f := newFixture(t)

	src := shopSource(t, "src/stack.schema.json", "")
	recorded, err := f.bootstrapWith(t, "Preview", src, projectNumber("123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[0].String(), `projectNumber 123456789012: not recorded: bootstrap edits no JSON schema; in src/stack.schema.json, add "projectNumber": "123456789012" beside "project" in the environment values of Staging`; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	if entries, _ := os.ReadDir(src.Dir); len(entries) != 0 {
		t.Errorf("bootstrap wrote %v", entries)
	}

	src = shopSource(t, "src/stack.schema.yaml", "")
	src.Schema.Types["Staging"].Environment.Values = map[string]any{"project": "acme-staging", "projectNumber": "111111111111", "region": "us-east1"}
	src.Schema.Types["Preview"].Environment.Target = stacktest.Target
	src.Schema.Types["Preview"].Environment.Values = map[string]any{"projectNumber": "1"}
	recorded, err = f.bootstrapWith(t, "Preview", src, projectNumber("123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[0].String(), `projectNumber 123456789012: not recorded: bootstrap edits no YAML schema; in src/stack.schema.yaml, change projectNumber in the environment values of Staging from 111111111111 to "123456789012"; Preview sets projectNumber too, which overrides it`; got != want {
		t.Errorf("line %q, want %q", got, want)
	}

	src = shopSource(t, "src/stack.schema.yaml", "")
	src.Schema.Types["Staging"].Environment.Values = map[string]any{"project": "acme-staging", "projectNumber": "123456789012", "region": "us-east1"}
	recorded, err = f.bootstrapWith(t, "Staging", src, projectNumber("123456789012"))
	if err != nil || recorded[0].Outcome != stackdeploy.ValueMatches {
		t.Errorf("a YAML schema that holds the number: %+v, %v", recorded, err)
	}

	src = shopSource(t, "src/stack.schema.ts", strings.Replace(shopSchemaTS, `fake: { project: "acme-staging", region: "us-east1" },`, `fake: stagingValues,`, 1))
	recorded, err = f.bootstrapWith(t, "Staging", src, projectNumber("123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[0].String(), `projectNumber 123456789012: not recorded: src/stack.schema.ts:15:3: the fake values of class Staging are not an object literal; set projectNumber: "123456789012" in the fake values of Staging by hand`; got != want {
		t.Errorf("line %q, want %q", got, want)
	}

	recorded, err = f.bootstrapWith(t, "Preview", nil, projectNumber("123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[0].String(), `projectNumber 123456789012: not recorded: no schema source to record it in; add projectNumber: "123456789012" beside project in the fake values of Preview, or of the environment it inherits project from`; got != want {
		t.Errorf("line %q, want %q", got, want)
	}

	// A value without a key, or a key returned twice, is the target's
	// mistake.
	if _, err := f.bootstrapWith(t, "Staging", src, registry.BootstrapValue{Value: "1"}); err == nil || !strings.Contains(err.Error(), "a value names both") {
		t.Errorf("a value without a key: %v", err)
	}
	if _, err := f.bootstrapWith(t, "Staging", src, projectNumber("1"), projectNumber("2")); err == nil || !strings.Contains(err.Error(), "returned projectNumber twice") {
		t.Errorf("a key twice: %v", err)
	}
}
