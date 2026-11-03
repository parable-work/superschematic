package verify

import (
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkDerivers checks an API service's derivers (D74) in every form: only
// an API declares one, each has a name no other deriver, worker, job, type
// or operation set of the schema takes and whose member of the generated
// Derivers no other deriver's takes, and each deriver's arguments are what
// @deriver takes
// (registry.CheckDeriver). Whether each schema is an engine type of the
// API's database that composes Derived is the API's build's to check, and
// resolution's: each reads that database's schema.
func checkDerivers(schema *ir.Schema, r *Result) {
	if len(schema.Derivers) == 0 {
		return
	}
	if schema.Kind != ir.SchemaKindAPI {
		r.errorf("", "schema %s is a %s service and declares derivers; only an API service declares one", schema.Name, schema.Kind)
		return
	}
	sets := map[string]bool{}
	for _, set := range schema.OperationSets {
		if set != nil {
			sets[set.Name] = true
		}
	}
	seen := map[string]bool{}
	members := map[string]string{}
	for _, deriver := range schema.Derivers {
		if deriver == nil {
			continue
		}
		if err := registry.CheckDeriver(deriver); err != nil {
			r.errorf("", "deriver %s: %v", deriver.Name, err)
			continue
		}
		switch {
		case seen[deriver.Name]:
			r.errorf("", "deriver %s is declared twice", deriver.Name)
			continue
		case schema.Types[deriver.Name] != nil:
			r.errorf("", "deriver %s takes the name of a type of the schema; a deriver's class is no type, so give one of them another name", deriver.Name)
		case sets[deriver.Name]:
			r.errorf("", "deriver %s takes the name of an operation set of the schema; give one of them another name", deriver.Name)
		case schema.Job(deriver.Name) != nil:
			r.errorf("", "deriver %s takes the name of a job of the schema, and both would be the deployable %s; give one of them another name", deriver.Name, ir.DeriverDeployableName(schema.Name, deriver.Name))
		case schema.Worker(deriver.Name) != nil:
			r.errorf("", "deriver %s takes the name of a worker of the schema, and both would be the deployable %s; give one of them another name", deriver.Name, ir.DeriverDeployableName(schema.Name, deriver.Name))
		}
		seen[deriver.Name] = true
		member := tsutil.ToCamelCase(deriver.Name)
		if other, clash := members[member]; clash {
			r.errorf("", "derivers %s and %s are both the member %s of the API's Derivers; give one of them another name", other, deriver.Name, member)
			continue
		}
		members[member] = deriver.Name
	}
}
