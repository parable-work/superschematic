package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// writeProvider writes a package.json into dir/name and returns its path.
func writeProvider(t *testing.T, dir, name, packageJSON string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReadRuntimeProviderChecksTheManifest (D159): a provider package's
// package.json declares the interface it implements, which the build
// checks against the interface's contract.
func TestReadRuntimeProviderChecksTheManifest(t *testing.T) {
	dir := t.TempDir()
	good := writeProvider(t, dir, "relay", `{"name": "@example/relay", "superschematic": {"provider": {
		"name": "relay", "implements": "channel", "operations": ["inbound", "send", "health"],
		"config": {"URL": {"description": "where"}, "TOKEN": {"secret": true}}, "role": ["messages.relay"]}}}`)
	p, err := ReadRuntimeProvider(good)
	if err != nil {
		t.Fatal(err)
	}
	want := ir.RuntimeProvider{Name: "relay", Implements: ir.RuntimeChannel, Package: "@example/relay", Operations: []string{"send", "health", "inbound"}, Config: []ir.ProviderConfigKey{{Key: "TOKEN", Secret: true}, {Key: "URL"}}, Role: []string{"messages.relay"}}
	if p.Name != want.Name || p.Package != want.Package || strings.Join(p.Operations, ",") != "send,health,inbound" || len(p.Config) != 2 || p.Config[0] != want.Config[0] || p.Role[0] != "messages.relay" {
		t.Fatalf("ReadRuntimeProvider = %+v, want %+v", p, want)
	}

	for name, tc := range map[string]struct {
		packageJSON string
		want        string
	}{
		"no provider":       {`{"name": "@example/plain"}`, "package @example/plain declares no provider"},
		"an unknown key":    {`{"name": "@example/x", "superschematic": {"provider": {"name": "x", "implements": "model", "kind": "llm"}}}`, `unknown field "kind"`},
		"an unknown option": {`{"name": "@example/x", "superschematic": {"provider": {"name": "x", "implements": "model", "config": {"KEY": {"required": true}}}}}`, `unknown field "required"`},
		"no interface":      {`{"name": "@example/x", "superschematic": {"provider": {"name": "x", "implements": "queue"}}}`, `implements "queue", which is no runtime interface`},
		"a missing operation": {`{"name": "@example/x", "superschematic": {"provider": {"name": "x", "implements": "sandbox", "operations": ["start", "cancel", "status"]}}}`,
			"a sandbox provider implements start, cancel, status, cost, and its operations leave out cost"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadRuntimeProvider(writeProvider(t, t.TempDir(), "p", tc.packageJSON))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ReadRuntimeProvider() = %v, want it to say %q", err, tc.want)
			}
		})
	}
	if _, err := ReadRuntimeProvider(filepath.Join(dir, "absent")); err == nil || !strings.Contains(err.Error(), "no package.json") {
		t.Fatalf("a directory with no package.json: %v", err)
	}
}

// providerSchema is an API whose config, Config, declares slot SMS.
func providerSchema(fields ...*ir.FieldDef) *ir.Schema {
	return &ir.Schema{Name: "notes-api", Kind: ir.SchemaKindAPI, Types: map[string]*ir.TypeDef{
		"Config": {Name: "Config", EnvVars: true, ProviderSlots: map[string]string{"SMS": ir.RuntimeChannel}, Fields: fields},
	}}
}

// TestLoadProvidersChecksTheSlots (D159): the loader reads the packages
// the config lists into the schema, refuses two of one name, and holds
// each slot to a string field of the config that some listed provider
// fills.
func TestLoadProvidersChecksTheSlots(t *testing.T) {
	dir := t.TempDir()
	writeProvider(t, dir, "relay", `{"name": "@example/relay", "superschematic": {"provider": {"name": "relay", "implements": "channel"}}}`)
	writeProvider(t, dir, "relay-copy", `{"name": "@example/relay-copy", "superschematic": {"provider": {"name": "relay", "implements": "channel"}}}`)
	writeProvider(t, dir, "model", `{"name": "@example/model", "superschematic": {"provider": {"name": "model", "implements": "model"}}}`)
	sms := &ir.FieldDef{Name: "SMS", TypeRef: ir.TypeRef{Name: "string"}}

	schema := providerSchema(sms)
	if err := loadProviders(dir, schema, &schemaconfig.SchemaConfig{Providers: []string{"./relay"}}); err != nil {
		t.Fatal(err)
	}
	if len(schema.Providers) != 1 || schema.Providers[0].Path != "./relay" || schema.Provider("relay") == nil {
		t.Fatalf("Providers = %+v", schema.Providers)
	}

	for name, tc := range map[string]struct {
		schema    *ir.Schema
		providers []string
		want      string
	}{
		"two of one name":  {providerSchema(sms), []string{"./relay", "./relay-copy"}, "providers ./relay and ./relay-copy are both named relay"},
		"no field":         {providerSchema(), []string{"./relay"}, "slot SMS is no field of the config Config"},
		"a secret field":   {providerSchema(&ir.FieldDef{Name: "SMS", Secret: true, TypeRef: ir.TypeRef{Name: "string"}}), []string{"./relay"}, "slot SMS is a Secret<T>"},
		"a number field":   {providerSchema(&ir.FieldDef{Name: "SMS", TypeRef: ir.TypeRef{Name: "int"}}), []string{"./relay"}, "slot SMS is a int; it is a string"},
		"no implementer":   {providerSchema(sms), []string{"./model"}, "slot SMS takes a channel adapter, and none of the providers the config lists implements channel"},
		"no listed at all": {providerSchema(sms), nil, "none of the providers the config lists implements channel"},
	} {
		t.Run(name, func(t *testing.T) {
			err := loadProviders(dir, tc.schema, &schemaconfig.SchemaConfig{Providers: tc.providers})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loadProviders() = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// TestLoadProvidersResolvesAnNpmName (D159, #608): a provider the config
// lists by its npm name resolves from the first node_modules in the
// service's directory or one above it, as Node.js resolves it, and the
// schema records the name a TypeScript server imports it by. A package no
// node_modules holds is refused, naming the entry and every place looked
// in, and so is one whose package.json gives another name.
func TestLoadProvidersResolvesAnNpmName(t *testing.T) {
	root := t.TempDir()
	service := filepath.Join(root, "schemas", "services", "notes-api")
	if err := os.MkdirAll(service, 0o755); err != nil {
		t.Fatal(err)
	}
	modules := filepath.Join(root, "schemas", "node_modules")
	writeProvider(t, modules, "@example/sms-relay", `{"name": "@example/sms-relay", "superschematic": {"provider": {"name": "sms-relay", "implements": "channel"}}}`)
	writeProvider(t, modules, "model-stub", `{"name": "model-stub", "superschematic": {"provider": {"name": "model-stub", "implements": "model"}}}`)
	writeProvider(t, modules, "renamed", `{"name": "@example/other", "superschematic": {"provider": {"name": "renamed", "implements": "model"}}}`)
	writeProvider(t, root, "relays/local", `{"name": "@example/local", "superschematic": {"provider": {"name": "local", "implements": "channel"}}}`)
	sms := &ir.FieldDef{Name: "SMS", TypeRef: ir.TypeRef{Name: "string"}}

	schema := providerSchema(sms)
	if err := loadProviders(service, schema, &schemaconfig.SchemaConfig{Providers: []string{"@example/sms-relay", "model-stub", "../../../relays/local"}}); err != nil {
		t.Fatal(err)
	}
	got := map[string]*ir.RuntimeProvider{}
	for _, p := range schema.Providers {
		got[p.Name] = p
	}
	if p := got["sms-relay"]; p == nil || p.Npm != "@example/sms-relay" || p.Path != "@example/sms-relay" || p.Dir != filepath.Join(modules, "@example", "sms-relay") {
		t.Errorf("sms-relay = %+v, want npm @example/sms-relay from %s", p, modules)
	}
	if p := got["model-stub"]; p == nil || p.Npm != "model-stub" {
		t.Errorf("model-stub = %+v, want npm model-stub", p)
	}
	if p := got["local"]; p == nil || p.Npm != "" || p.Path != "../../../relays/local" || p.Dir != filepath.Join(root, "relays", "local") {
		t.Errorf("local = %+v, want the directory ../../../relays/local", p)
	}

	for name, tc := range map[string]struct {
		entry string
		want  []string
	}{
		"a missing package": {"@example/absent", []string{
			"providers: @example/absent: no package @example/absent in the service's node_modules or one above it",
			filepath.Join(service, "node_modules"), filepath.Join(root, "schemas", "node_modules"), filepath.Join(root, "node_modules"),
		}},
		"another name": {"renamed", []string{`holds package "@example/other", whose package.json names it other than renamed`}},
	} {
		t.Run(name, func(t *testing.T) {
			err := loadProviders(service, providerSchema(sms), &schemaconfig.SchemaConfig{Providers: []string{"@example/sms-relay", tc.entry}})
			if err == nil {
				t.Fatal("loadProviders() = nil")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("loadProviders() = %v, want it to say %q", err, want)
				}
			}
		})
	}
}

// TestReadRuntimeProviderReadsACrate (D185): a provider crate's Cargo.toml
// declares the interface it implements under
// [package.metadata.superschematic.provider], which the build checks as
// it checks a package.json, and a directory that holds both declares one
// provider in each.
func TestReadRuntimeProviderReadsACrate(t *testing.T) {
	const crate = `[package]
name = "relay-a"
version = "0.0.0"

[package.metadata.superschematic.provider]
name = "relay"
implements = "channel"
operations = ["send", "health", "inbound"]
role = ["messages.relay"]

[package.metadata.superschematic.provider.config]
URL = { description = "where" }
TOKEN = { secret = true }
`
	write := func(dir, name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	write(dir, "Cargo.toml", crate)
	p, err := ReadRuntimeProvider(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "relay" || p.Crate != "relay-a" || p.Package != "" || strings.Join(p.Operations, ",") != "send,health,inbound" || len(p.Config) != 2 || p.Config[0] != (ir.ProviderConfigKey{Key: "TOKEN", Secret: true}) {
		t.Fatalf("ReadRuntimeProvider = %+v", p)
	}
	// The same provider in a package.json beside it: one provider, of
	// both.
	write(dir, "package.json", `{"name": "@example/relay", "superschematic": {"provider": {
		"name": "relay", "implements": "channel", "operations": ["inbound", "send", "health"],
		"config": {"URL": {}, "TOKEN": {"secret": true}}, "role": ["messages.relay"]}}}`)
	if p, err = ReadRuntimeProvider(dir); err != nil || p.Package != "@example/relay" || p.Crate != "relay-a" {
		t.Fatalf("ReadRuntimeProvider of both = %+v, %v", p, err)
	}
	// A package.json of another provider is refused.
	write(dir, "package.json", `{"name": "@example/relay", "superschematic": {"provider": {"name": "relay", "implements": "channel", "role": ["messages.send"]}}}`)
	if _, err := ReadRuntimeProvider(dir); err == nil || !strings.Contains(err.Error(), "declare different providers") {
		t.Fatalf("two providers in one directory: %v", err)
	}
	for name, tc := range map[string]struct{ cargo, want string }{
		"no provider":    {"[package]\nname = \"plain\"\n", "crate plain declares no provider"},
		"an unknown key": {"[package]\nname = \"x\"\n[package.metadata.superschematic.provider]\nname = \"x\"\nimplements = \"model\"\nkind = \"llm\"\n", "unknown key package.metadata.superschematic.provider.kind"},
		"no interface":   {"[package]\nname = \"x\"\n[package.metadata.superschematic.provider]\nname = \"x\"\nimplements = \"queue\"\n", `implements "queue", which is no runtime interface`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(dir, "Cargo.toml", tc.cargo)
			if _, err := ReadRuntimeProvider(dir); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ReadRuntimeProvider() = %v, want it to say %q", err, tc.want)
			}
		})
	}
}
