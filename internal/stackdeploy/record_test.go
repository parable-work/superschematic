package stackdeploy_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// shopSchemaTS stands for the shop stack's TypeScript schema file. The
// fake edit appends to it and reads nothing of it; the TypeScript reader's
// edit has its own tests, and cli's bootstrap test runs it on the real
// file.
const shopSchemaTS = "// The shop stack: Staging declares the project, and Preview extends it.\n"

// fakeEdit stands in for the TypeScript reader's edit, which the CLI hands
// bootstrap. It keeps each class's values in memory and, for each value it
// writes, appends a line `// <Class>.<key> = <value>` to the file, so a
// test reads which environment bootstrap wrote to, and that a matching
// value writes nothing. It records each call.
type fakeEdit struct {
	values map[string]string
	calls  []stackdeploy.EnvironmentValue
	fail   error
}

func (e *fakeEdit) edit(_ string, src []byte, v stackdeploy.EnvironmentValue) ([]byte, string, bool, error) {
	e.calls = append(e.calls, v)
	if e.fail != nil {
		return nil, "", false, e.fail
	}
	key := v.Class + "." + v.Key
	previous, found := e.values[key]
	if found && previous == v.Value {
		return src, previous, true, nil
	}
	if e.values == nil {
		e.values = map[string]string{}
	}
	e.values[key] = v.Value
	return append(slices.Clone(src), fmt.Sprintf("// %s = %s\n", key, v.Value)...), previous, found, nil
}

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
// the project's number is written, through the edit, on Staging, which
// declares the project; a second run finds it and leaves the file alone; a
// new number updates it; Production declares its own project.
func TestBootstrapRecordsValues(t *testing.T) {
	f := newFixture(t)
	edit := &fakeEdit{}
	src := shopSource(t, "src/stack.schema.ts", shopSchemaTS)
	src.EditTypeScript = edit.edit
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
	want := stackdeploy.EnvironmentValue{Class: "Staging", Target: stacktest.Target, Key: "projectNumber", Value: "123456789012", Beside: "project"}
	if !slices.Equal(edit.calls, []stackdeploy.EnvironmentValue{want}) {
		t.Errorf("edit calls %+v, want %+v", edit.calls, want)
	}
	written := shopSchemaTS + "// Staging.projectNumber = 123456789012\n"
	if got := read(); got != written {
		t.Fatalf("the schema file is\n%s\nwant\n%s", got, written)
	}

	// A second bootstrap finds the number and writes nothing: the file is
	// read-only, and a write would fail.
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
	if got := read(); got != written {
		t.Errorf("a matching bootstrap changed the file:\n%s", got)
	}

	// A project whose number differs from the schema's is written again,
	// and said so.
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
	written += "// Staging.projectNumber = 999999999999\n"
	if got := read(); got != written {
		t.Errorf("the updated file is\n%s\nwant\n%s", got, written)
	}

	// Production declares its own project, so its number goes there.
	recorded, err = f.bootstrapWith(t, "Production", src, projectNumber("210987654321"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[0].String(), "projectNumber 210987654321: recorded on Production in src/stack.schema.ts"; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	if got, want := read(), written+"// Production.projectNumber = 210987654321\n"; got != want {
		t.Errorf("the file is\n%s\nwant\n%s", got, want)
	}
}

// TestBootstrapValuesByHand covers the values bootstrap does not write: a
// JSON or YAML schema's, which it says how to add or change, a TypeScript
// file the edit refuses or that no edit was handed in for, and a bootstrap
// with no schema source. An environment between that sets the value too is
// named.
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

	// The edit's refusal is the line, and the file is unchanged.
	src = shopSource(t, "src/stack.schema.ts", shopSchemaTS)
	refusal := `src/stack.schema.ts:15:3: the fake values of class Staging are not an object literal; set projectNumber: "123456789012" in the fake values of Staging by hand`
	src.EditTypeScript = (&fakeEdit{fail: errors.New(refusal)}).edit
	recorded, err = f.bootstrapWith(t, "Staging", src, projectNumber("123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[0].String(), "projectNumber 123456789012: not recorded: "+refusal; got != want {
		t.Errorf("line %q, want %q", got, want)
	}

	// Without an edit, a TypeScript schema is changed by hand too.
	src.EditTypeScript = nil
	recorded, err = f.bootstrapWith(t, "Preview", src, projectNumber("123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[0].String(), `projectNumber 123456789012: not recorded: bootstrap was handed no TypeScript edit; in src/stack.schema.ts, add projectNumber: "123456789012" beside project in the fake values of Staging`; got != want {
		t.Errorf("line %q, want %q", got, want)
	}
	if data, _ := os.ReadFile(filepath.Join(src.Dir, "src", "stack.schema.ts")); string(data) != shopSchemaTS {
		t.Errorf("bootstrap changed the file:\n%s", data)
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
