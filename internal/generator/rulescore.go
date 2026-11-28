package generator

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/rulescore"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	ir "github.com/parable-work/superschematic/ir"
)

// rulescoreGenerator is the name of the core generator of a DB schema's
// project rules crate (D103's step 5).
const rulescoreGenerator = "rulescore"

// tableDerivers are the run's API's derivers, each of which fills tables
// that implement Derived (D146), sorted by name: nil when it declares none.
// An API with a deriver whose database the build cannot find, or whose
// database has no table that implements Derived, is refused.
func (r run) tableDerivers(deps map[string]*ir.Schema) ([]apigen.DeriverInfo, error) {
	if len(r.Schema.Derivers) == 0 {
		return nil, nil
	}
	db, schema := r.databaseSchema(deps)
	var tableTypes []*ir.TypeDef
	if schema != nil {
		tableTypes = codegen.DerivedTableTypes(schema)
	}
	if len(tableTypes) == 0 {
		return nil, fmt.Errorf("generator: %s declares deriver %s, which fills the Derived fields of tables of its database, and %s connects to no database with a table that implements Derived; set its authDb to the DB service whose tables implement Derived (D146)", r.Config.Name, r.Schema.Derivers[0].Name, r.Config.Name)
	}
	return deriversOf(r.Schema, db, tableTypes)
}

// databaseSchema loads the DB service the run's API connects to, its
// authDb or its one DB-kind dependency, from deps or, for one its config
// names as its authDb alone, through the run's loader. The schema is nil
// when the API connects to none, the build cannot load it, or it is no DB
// service.
func (r run) databaseSchema(deps map[string]*ir.Schema) (string, *ir.Schema) {
	db, _, ok := r.Schema.Database()
	if !ok {
		return "", nil
	}
	schema := deps[db]
	if schema == nil {
		if r.Options.LoadDependency == nil || r.Config.AuthDB != db {
			return db, nil
		}
		loaded, err := r.LoadDependency(db)
		if err != nil {
			return db, nil
		}
		schema = loaded
	}
	if kind, ok := r.Registry.Kind(string(schema.Kind)); !ok || kind.StructRole != ir.RoleDBTable {
		return db, nil
	}
	return db, schema
}

// generateRulesCore writes the project's rules crate of a DB schema whose
// tables compose a behavior a class or an extension declares with the Rust
// crate that runs it on the core (D103's step 5) into rules/rust/<schema>
// (internal/generator/rulescore), and removes the one an earlier build
// wrote when the schema needs none now. A composed behavior whose crate
// the project does not hold leaves no core that can run the schema, and
// the log says so.
func (r run) generateRulesCore() error {
	if r.Schema.Kind != ir.SchemaKindDB {
		return nil
	}
	dir := RulesDir(r.Options.OutputRoot, LangRust, r.Config.Name)
	crate, why, err := rulescore.Build(r.Schema, rulescore.Options{
		SchemaName:     r.Config.Name,
		Registry:       r.Registry,
		Naming:         r.Options.Naming,
		RepositoryRoot: r.Options.RepositoryRoot,
		Paths:          r.Options.Paths,
		Dir:            dir,
		RustVersion:    servergen.RustVersion,
	})
	if err != nil {
		return fmt.Errorf("generator: rules crate for %s: %w", r.Config.Name, err)
	}
	if crate == nil {
		if why != "" {
			r.Logf("  - rules-rust: %s\n", why)
		}
		return rulescore.RemoveStale(dir)
	}
	if err := crate.Write(dir); err != nil {
		return fmt.Errorf("generator: rules crate for %s: %w", r.Config.Name, err)
	}
	r.Done("rules-rust", dir)
	return nil
}
