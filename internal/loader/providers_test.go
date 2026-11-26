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
	if err := loadProviders(dir, schema, &schemaconfig.SchemaConfig{Providers: []string{"relay"}}); err != nil {
		t.Fatal(err)
	}
	if len(schema.Providers) != 1 || schema.Providers[0].Path != "relay" || schema.Provider("relay") == nil {
		t.Fatalf("Providers = %+v", schema.Providers)
	}

	for name, tc := range map[string]struct {
		schema    *ir.Schema
		providers []string
		want      string
	}{
		"two of one name":  {providerSchema(sms), []string{"relay", "relay-copy"}, "providers relay and relay-copy are both named relay"},
		"no field":         {providerSchema(), []string{"relay"}, "slot SMS is no field of the config Config"},
		"a secret field":   {providerSchema(&ir.FieldDef{Name: "SMS", Secret: true, TypeRef: ir.TypeRef{Name: "string"}}), []string{"relay"}, "slot SMS is a Secret<T>"},
		"a number field":   {providerSchema(&ir.FieldDef{Name: "SMS", TypeRef: ir.TypeRef{Name: "int"}}), []string{"relay"}, "slot SMS is a int; it is a string"},
		"no implementer":   {providerSchema(sms), []string{"model"}, "slot SMS takes a channel adapter, and none of the providers the config lists implements channel"},
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
