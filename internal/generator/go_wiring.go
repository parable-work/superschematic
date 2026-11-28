package generator

import (
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// checkGoWiring refuses a Go API whose Deps holds the runtime's wiring
// options (D190, D192, D187; apigen.DepsWiring.Constructs) and whose
// implementation, at dir, exists and declares no NewWiring: every
// entrypoint of the API builds the notifier, the dispatcher and the
// inbound path, the calendar and search with it, and the build no longer
// writes into the implementation, so one that predates its database's
// notifier, calendar or search fails here, saying what to add, rather
// than when go builds main.go.
func (r run) checkGoWiring(stackName string, output *apigen.APIOutput, dir string) error {
	w := output.Deps.Wiring
	if !w.Constructs() {
		return nil
	}
	declares, err := apigen.DeclaresFunc(dir, apigen.WiringFunc)
	if err != nil || declares {
		return err
	}
	example := "\treturn api.Wiring{}, nil"
	if w.Agents != "" {
		example = "\treturn api.Wiring{Agents: agents.Options{Endpoint: \"https://<this server>/api/agents\"}}, nil"
	}
	return fmt.Errorf("stack %s: %s's database %s has the runtime the generated server wires (its notifier, calendar or search), and its implementation at %s, which the build no longer writes into, declares no %s; add\n\n"+
		"\tfunc %s(deps api.Deps) (api.Wiring, error) {\n\t%s\n\t}\n\n"+
		"where api is %s, whose api.WiringConstructor is %s's signature: the options the notifier, the agent dispatcher, the inbound path, the calendar and search are built with beside what the wiring gives them (docs/stack-model.md, section 8.5)",
		stackName, output.SchemaName, output.Deps.Database, dir, apigen.WiringFunc, apigen.WiringFunc, example, output.ModulePath, apigen.WiringFunc)
}

// goRuntimeQueues are the queues of the Go API api's database whose
// @worker the runtime handles (apigen.DepsWiring.RuntimeOf): the
// notifier's, the dispatcher's, the run requests' and each @searchable
// declaration's. Its implementation declares no Workers method for one.
func (r run) goRuntimeQueues(services []stack.Service, api string) map[string]bool {
	out := map[string]bool{}
	if r.Options.LoadDependency == nil {
		return out
	}
	for _, svc := range services {
		if svc.Name != api {
			continue
		}
		db := ""
		if svc.AuthDB != nil {
			db = svc.AuthDB.Name
		}
		for _, dep := range svc.Dependencies {
			if db == "" && dep.Kind == ir.SchemaKindDB {
				db = dep.Name
			}
		}
		if db == "" {
			continue
		}
		schema, err := r.Options.LoadDependency(db)
		if err != nil || schema == nil {
			continue
		}
		w := apigen.WiringOf(schema, false)
		for _, worker := range svc.Workers {
			if worker.BehaviorWorker == nil && w.RuntimeOf(worker.Queue) != "" {
				out[worker.Queue] = true
			}
		}
	}
	return out
}

// goImplementedWorkers are the workers of implemented that the Go
// implementation runs: those whose queue the runtime does not handle.
func goImplementedWorkers(implemented []ir.Worker, runtime map[string]bool) []ir.Worker {
	return slices.DeleteFunc(slices.Clone(implemented), func(w ir.Worker) bool {
		return w.BehaviorWorker == nil && runtime[w.Queue]
	})
}
