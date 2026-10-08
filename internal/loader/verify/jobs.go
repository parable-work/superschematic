package verify

import (
	"github.com/parable-work/superschematic/internal/generator/goutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkJobs checks an API service's jobs (D52) in every form: only an API
// declares one, each has a name no other job, type or operation set of the
// schema takes and whose Go method no other job's takes, and each job's
// arguments are what @job takes (registry.CheckJob). The TypeScript reader
// checks the arguments where they are written; a data form states the jobs
// as Schema.Jobs, which this reaches.
func checkJobs(schema *ir.Schema, r *Result) {
	if len(schema.Jobs) == 0 {
		return
	}
	if schema.Kind != ir.SchemaKindAPI {
		r.errorf("", "schema %s is a %s service and declares jobs; only an API service declares one", schema.Name, schema.Kind)
		return
	}
	seen := map[string]bool{}
	methods := map[string]string{}
	sets := map[string]bool{}
	for _, set := range schema.OperationSets {
		if set != nil {
			sets[set.Name] = true
		}
	}
	for _, job := range schema.Jobs {
		if job == nil {
			continue
		}
		if err := registry.CheckJob(job); err != nil {
			r.errorf("", "job %s: %v", job.Name, err)
			continue
		}
		switch {
		case seen[job.Name]:
			r.errorf("", "job %s is declared twice", job.Name)
			continue
		case schema.Types[job.Name] != nil:
			r.errorf("", "job %s takes the name of a type of the schema; a job's class is no type, so give one of them another name", job.Name)
		case sets[job.Name]:
			r.errorf("", "job %s takes the name of an operation set of the schema; give one of them another name", job.Name)
		}
		seen[job.Name] = true
		method := goutil.GoPublicIdentifier(job.Name)
		if other, clash := methods[method]; clash {
			r.errorf("", "jobs %s and %s are both the method %s of the API's Jobs interface; give one of them another name", other, job.Name, method)
			continue
		}
		methods[method] = job.Name
	}
}
