package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestEnvVarsTypesMakeOneConfig: the documented pair, @envVars FieldKeys
// and @envVars FinancesConfig extends FieldKeys, is one config; two
// @envVars classes neither of which extends the other are one error, at
// the file of the first by name, naming both (D94). A field FinancesConfig
// inherits from FieldKeys that collides with a derived field is reported
// once, at FieldKeys, which declares it.
func TestEnvVarsTypesMakeOneConfig(t *testing.T) {
	schema := ir.NewSchema("finances-api", ir.SchemaKindAPI)
	schema.AuthDB = "finances-db"
	schema.Types["FieldKeys"] = &ir.TypeDef{Name: "FieldKeys", Owner: "src/keys.schema.ts", EnvVars: true, Fields: []*ir.FieldDef{
		{Name: "FINANCES_DB_DATABASE_URL"},
	}}
	schema.Types["FinancesConfig"] = &ir.TypeDef{Name: "FinancesConfig", Owner: "src/config.schema.ts", EnvVars: true, Extends: "FieldKeys", Fields: []*ir.FieldDef{
		{Name: "FINANCES_DB_DATABASE_URL", InheritedFrom: "FieldKeys"},
		{Name: "EMBEDDING_MODEL"},
	}}
	r := Run(schema, Input{})
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Error())
	}
	want := []string{"@envVars field FINANCES_DB_DATABASE_URL of FieldKeys collides with FINANCES_DB_DATABASE, the config field the authDb finances-db derives; a stack's platform sets it, so rename the setting"}
	if !slices.Equal(got, want) {
		t.Errorf("errors = %q, want %q", got, want)
	}

	schema.Types["FieldKeys"].Fields = []*ir.FieldDef{{Name: "FINANCES_DB_FIELD_KEYS"}}
	schema.Types["FinancesConfig"].Extends = ""
	schema.Types["FinancesConfig"].Fields = []*ir.FieldDef{{Name: "EMBEDDING_MODEL"}}
	r = &Result{}
	checkEnvVarsTypes(schema, r)
	want = []string{"src/keys.schema.ts: finances-api has two @envVars classes, FieldKeys (src/keys.schema.ts) and FinancesConfig (src/config.schema.ts), and neither extends the other; a service's config is the one @envVars class no other extends, which holds the fields of every class it extends, so make one of them extend the other, or remove @envVars from one (D94)"}
	got = nil
	for _, d := range r.Errors {
		got = append(got, d.Error())
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors = %q, want %q", got, want)
	}
}
