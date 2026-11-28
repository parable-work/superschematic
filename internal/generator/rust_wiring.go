package generator

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// The runtime parts of a Rust API's database, which its generated server,
// jobs and workers build themselves (#620, #622, #617, #610; rustrestgen's
// wiring.go): the build reads its database's Rust types for them, leaves
// the workers of their queues out of the implementation's Workers, and
// refuses an implementation that predates them.

// rustWires reports whether the Rust API r builds the runtime parts of its
// database db: db, on Postgres first, has the notifier, the orchestration,
// a @searchable table, a calendar source, grants or an access log. Its
// Rust types then hold the repositories and the queues the parts run over.
func (r run) rustWires(db string) bool {
	if r.Options.LoadDependency == nil || r.rustDatabaseOnSQLite(db) {
		return false
	}
	schema, err := r.LoadDependency(db)
	if err != nil || schema == nil {
		return false
	}
	return rustrestgen.HasRuntimeParts(schema)
}

// rustDatabaseOnSQLite reports whether db's DB service lists sqlite first
// (D116), where no runtime part is built.
func (r run) rustDatabaseOnSQLite(db string) bool {
	if r.Options.DependencyConfig == nil {
		return false
	}
	cfg, ok := r.Options.DependencyConfig(db)
	if !ok {
		return false
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, r.Registry)
	if err != nil {
		return false
	}
	return outputs.SQLDialects()[0] == registry.SQLDialectSQLite
}

// rustRuntimeQueues are the queues of the databases of the Rust API api
// whose @worker a runtime part handles (#620, #622, #617): the
// implementation's Workers has no method for them.
func (r run) rustRuntimeQueues(services []stack.Service, api string) map[string]bool {
	out := map[string]bool{}
	if r.Options.LoadDependency == nil {
		return out
	}
	for _, svc := range services {
		if svc.Name != api {
			continue
		}
		var dbs []string
		if svc.AuthDB != nil {
			dbs = append(dbs, svc.AuthDB.Name)
		}
		for _, dep := range svc.Dependencies {
			if dep.Kind == ir.SchemaKindDB {
				dbs = append(dbs, dep.Name)
			}
		}
		for _, db := range dbs {
			if r.rustDatabaseOnSQLite(db) {
				continue
			}
			schema, err := r.Options.LoadDependency(db)
			if err != nil || schema == nil {
				continue
			}
			for queue := range rustrestgen.RuntimeQueues(schema) {
				out[queue] = true
			}
		}
	}
	return out
}

// checkRustWiring refuses a Rust API whose database has runtime parts and
// whose implementation exists and declares no create_wiring (#620): every
// entrypoint of the API asks it for the Wire that shapes them, and the
// build no longer writes into the implementation, so one that predates
// the parts fails here, saying what to add, rather than when cargo builds
// an entrypoint.
func (r run) checkRustWiring(stackName string, output *rustrestgen.APIOutput) error {
	if !output.HasWiring() {
		return nil
	}
	dir := r.Options.Naming.RustImplementationDir(r.Options.RepositoryRoot, output.SchemaName)
	exists, err := rustrestgen.ImplementationExists(dir)
	if err != nil || !exists {
		return err
	}
	declares, err := rustrestgen.DeclaresFunc(dir, rustrestgen.WiringFunc)
	if err != nil || declares {
		return err
	}
	return fmt.Errorf("stack %s: %s's server builds the runtime parts of %s, its database (#620, #622, #617, #610), and its implementation at %s, which the build no longer writes into, declares no %s in %s; add\n\n"+
		"  pub async fn %s(deps: &WiringDeps) -> Result<Arc<dyn Wire>, BoxError> {\n      Ok(Arc::new(Wiring))\n  }\n\n  pub struct Wiring;\n\n  impl Wire for Wiring {}\n\n"+
		"where Wire, WiringDeps and BoxError are %s's: each method of Wire, which keeps the part it is handed by default, shapes a part (docs/stack-model.md, section 8.6)",
		stackName, output.SchemaName, output.Deps.Database, dir, rustrestgen.WiringFunc, rustrestgen.ImplementationLib, rustrestgen.WiringFunc, r.Options.Naming.RustAPICrate(output.SchemaName))
}
