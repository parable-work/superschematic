package verify

import (
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkApps checks an API service's apps (D78) in every form: only an API
// declares one, each has a name no other app, job, worker, deriver, type
// or operation set of the schema takes, and each app's arguments are what
// @app takes (registry.CheckApp). Whether the API is written in Rust is
// the stack's resolution's to check, which reads the API's config, and
// whether the binary links each extension the stack's build's.
func checkApps(schema *ir.Schema, r *Result) {
	if len(schema.Apps) == 0 {
		return
	}
	if schema.Kind != ir.SchemaKindAPI {
		r.errorf("", "schema %s is a %s service and declares apps; only an API service declares one", schema.Name, schema.Kind)
		return
	}
	sets := map[string]bool{}
	for _, set := range schema.OperationSets {
		if set != nil {
			sets[set.Name] = true
		}
	}
	seen := map[string]bool{}
	deployables := map[string]string{}
	for _, app := range schema.Apps {
		if app == nil {
			continue
		}
		if err := registry.CheckApp(app); err != nil {
			r.errorf("", "app %s: %v", app.Name, err)
			continue
		}
		deployable := ir.AppDeployableName(schema.Name, app.Name)
		switch {
		case seen[app.Name]:
			r.errorf("", "app %s is declared twice", app.Name)
			continue
		case schema.Types[app.Name] != nil:
			r.errorf("", "app %s takes the name of a type of the schema; an app's class is no type, so give one of them another name", app.Name)
		case sets[app.Name]:
			r.errorf("", "app %s takes the name of an operation set of the schema; give one of them another name", app.Name)
		case schema.Job(app.Name) != nil:
			r.errorf("", "app %s takes the name of a job of the schema, and both would be the deployable %s; give one of them another name", app.Name, deployable)
		case schema.Worker(app.Name) != nil:
			r.errorf("", "app %s takes the name of a worker of the schema, and both would be the deployable %s; give one of them another name", app.Name, deployable)
		case schema.Deriver(app.Name) != nil:
			r.errorf("", "app %s takes the name of a deriver of the schema, and both would be the deployable %s; give one of them another name", app.Name, deployable)
		}
		seen[app.Name] = true
		if other, clash := deployables[deployable]; clash {
			r.errorf("", "apps %s and %s would both be the deployable %s; give one of them another name", other, app.Name, deployable)
			continue
		}
		deployables[deployable] = app.Name
	}
}
