package registry

import (
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

func coreOutputRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := New(naming.Default())
	for _, spec := range []GeneratorSpec{
		{Name: "types", OutputKey: "types"},
		{Name: "sql"},
		{Name: "orm"},
		{Name: "api", OutputKey: "api"},
		{Name: "sdks", OutputKey: "sdk"},
		{Name: "envConfig"},
	} {
		spec.Generate = noopGenerate
		if err := reg.RegisterGenerator(spec); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

func TestParseOutputsRejectsUnknownKeyWithTodaysMessage(t *testing.T) {
	_, err := ParseOutputs(map[string]any{"graphql": map[string]any{"enabled": true}}, coreOutputRegistry(t))
	if err == nil {
		t.Fatal("expected error for unknown outputs key")
	}
	want := `outputs block has unknown key "graphql" (expected types, api, sdk)`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestParseOutputsAcceptsExtensionKeysAndKeepsRaw(t *testing.T) {
	reg := coreOutputRegistry(t)
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "catalog", Extension: "acme", OutputKey: "catalog", Generate: noopGenerate}); err != nil {
		t.Fatal(err)
	}
	outputs, err := ParseOutputs(map[string]any{
		"types":   map[string]any{"go": map[string]any{"enabled": true}},
		"catalog": map[string]any{"enabled": true, "region": "eu"},
	}, reg)
	if err != nil {
		t.Fatalf("ParseOutputs: %v", err)
	}
	if got := outputs.EnabledTypeLanguages(); !reflect.DeepEqual(got, []string{"go"}) {
		t.Errorf("EnabledTypeLanguages = %v", got)
	}

	var catalog struct {
		Enabled bool   `json:"enabled"`
		Region  string `json:"region"`
	}
	if err := DecodeOutput(outputs, "catalog", &catalog); err != nil {
		t.Fatalf("DecodeOutput: %v", err)
	}
	if !catalog.Enabled || catalog.Region != "eu" {
		t.Errorf("decoded catalog section = %+v", catalog)
	}
	var missing struct{ Enabled bool }
	if err := DecodeOutput(outputs, "absent", &missing); err != nil || missing.Enabled {
		t.Errorf("DecodeOutput on a missing key: err=%v, v=%+v", err, missing)
	}
	var wrongShape string
	if err := DecodeOutput(outputs, "types", &wrongShape); err == nil || !strings.Contains(err.Error(), "decoding outputs.types") {
		t.Errorf("DecodeOutput into the wrong shape: err=%v", err)
	}
}

func TestParseOutputsEmptyBlock(t *testing.T) {
	outputs, err := ParseOutputs(nil, coreOutputRegistry(t))
	if err != nil {
		t.Fatalf("ParseOutputs(nil): %v", err)
	}
	if outputs.APIEnabled() || len(outputs.EnabledTypeLanguages()) != 0 || len(outputs.Raw) != 0 {
		t.Errorf("empty outputs should enable nothing: %+v", outputs)
	}
}

// An OutputSchema is compiled when the generator registers, so a malformed
// one fails assembly, and ParseOutputs checks the claimed section against it.
func TestParseOutputsValidatesSectionsAgainstOutputSchema(t *testing.T) {
	generate := func(GenerateContext) error { return nil }
	reg := New(naming.Default())
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "broken", OutputKey: "broken", OutputSchema: []byte(`{"type": 3}`), Generate: generate}); err == nil {
		t.Error("a malformed OutputSchema registered")
	}
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "keyless", OutputSchema: []byte(`{}`), Generate: generate}); err == nil {
		t.Error("an OutputSchema without an OutputKey registered")
	}
	if err := reg.RegisterGenerator(GeneratorSpec{
		Name: "catalog", OutputKey: "catalog", Generate: generate,
		OutputSchema: []byte(`{"type": "object", "properties": {"enabled": {"type": "boolean"}}, "additionalProperties": false}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseOutputs(map[string]any{"catalog": map[string]any{"enabled": true}}, reg); err != nil {
		t.Errorf("a valid section: %v", err)
	}
	for _, section := range []any{
		map[string]any{"enabled": "yes"},
		map[string]any{"enabled": true, "shelves": 3},
		true,
	} {
		_, err := ParseOutputs(map[string]any{"catalog": section}, reg)
		if err == nil || !strings.HasPrefix(err.Error(), "outputs.catalog: ") {
			t.Errorf("section %v: %v", section, err)
		}
	}
}

func TestParseOutputsReadsTheSQLSectionStrictly(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "sql", OutputKey: "sql", Generate: func(GenerateContext) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	outputs, err := ParseOutputs(map[string]any{"sql": map[string]any{"migrationsDir": "db/migrations", "viewOwner": "app_view_owner"}}, reg)
	if err != nil {
		t.Fatal(err)
	}
	if outputs.SQLMigrationsDir() != "db/migrations" || outputs.SQLViewOwner() != "app_view_owner" {
		t.Errorf("sql section = %+v", outputs.SQL)
	}
	if (&Outputs{}).SQLMigrationsDir() != "" || (*Outputs)(nil).SQLViewOwner() != "" {
		t.Error("an absent sql section must leave both unset")
	}
	_, err = ParseOutputs(map[string]any{"sql": map[string]any{"migrationDir": "db"}}, reg)
	if err == nil || !strings.Contains(err.Error(), `outputs.sql: json: unknown field "migrationDir"`) {
		t.Fatalf("a misspelt outputs.sql key: %v", err)
	}
}
