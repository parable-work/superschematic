package generator

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// goServerSQLiteErr refuses, in the Go server of r's API, the features of
// schema, its database db, that a Go server serves on Postgres alone
// (D152, D155, D187), when db's config lists sqlite first: Recurrence, a
// calendar source and a @searchable table, Recurrence and the calendar as
// rustrestgen refuses them in a Rust server (D188). goDeps calls it for
// every Go server over a database, whatever its tables declare; a
// database whose config the build has not read is left unchecked.
func (r run) goServerSQLiteErr(db string, schema *ir.Schema) error {
	if r.Options.DependencyConfig == nil {
		return nil
	}
	cfg, ok := r.Options.DependencyConfig(db)
	if !ok || cfg == nil {
		return nil
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, r.Registry)
	if err != nil {
		return fmt.Errorf("generator: schema config for %s: %w", db, err)
	}
	return verify.GoServerSQLiteErr(r.Schema, schema, outputs.SQLDialects())
}
