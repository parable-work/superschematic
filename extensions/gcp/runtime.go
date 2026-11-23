package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A runtime environment's publish on gcp (D117's runtime mode): its plan
// is applied by the stack's migration job, `<stack>-migrate` (D46), never
// over a serving connection. Runtime mode's job runner
// (runtime/engine/rust/rules-runtime, JobRunner) writes one publish's
// documents into a directory: the plan, runtime mode's record of the
// publish, and a job document naming both beside it, the database on the
// stack's Cloud SQL instance and the servers' IAM database users. The gcp
// half uploads them to the state bucket under
// superschematic/migrations/runtime/<stack>/<service>/<publish>/, where the
// migrator reads them, and runs one execution of the job with the job
// document's gs:// URL; the job document's references stay relative, so
// the job reads the plan and the record beside it. The job writes the
// record and the servers' privileges in the transaction that finishes the
// plan, and the publish waits on the model the database records.
//
// The job itself is the deploy's: RunRuntimeJob runs it as it is, and
// refuses a stack whose job a deploy has not made.

// runtimePrefix is where a runtime publish's documents sit in the state
// bucket, under the migrator's prefix.
const runtimePrefix = migrationPrefix + "runtime/"

// The documents of one runtime publish, as runtime mode's job runner names
// them.
var runtimeDocuments = []string{"job.json", "plan.json", "record.json"}

// RuntimeJob is one runtime publish's job: the stack's project, region
// and name, and the directory runtime mode's job runner wrote its
// documents into.
type RuntimeJob struct {
	Project, Region, Stack string
	Dir                    string
}

// runtimeJobDocument is what RunRuntimeJob reads of a job document.
type runtimeJobDocument struct {
	Version  int `json:"version"`
	CloudSQL *struct {
		Instance string `json:"instance"`
	} `json:"cloudSql"`
	Databases []struct {
		Service string `json:"service"`
		Plan    string `json:"plan"`
		Record  string `json:"record"`
	} `json:"databases"`
}

// RunRuntimeJob uploads a runtime publish's documents to the state bucket
// and runs one execution of the stack's migration job with them, waiting
// for it to finish. A failed execution's error is the runner's, read from
// Cloud Logging, with the execution's name and its logs' URL.
func (e Extension) RunRuntimeJob(ctx context.Context, job RuntimeJob, log io.Writer) (*JobRun, error) {
	if log == nil {
		log = io.Discard
	}
	docs := map[string][]byte{}
	for _, name := range runtimeDocuments {
		data, err := os.ReadFile(filepath.Join(job.Dir, name))
		if err != nil {
			return nil, fmt.Errorf("gcp: a runtime publish's %s: %w", name, err)
		}
		docs[name] = data
	}
	var doc runtimeJobDocument
	decoder := json.NewDecoder(bytes.NewReader(docs["job.json"]))
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("gcp: a runtime publish's job document: %w", err)
	}
	switch {
	case doc.Version != 1:
		return nil, fmt.Errorf("gcp: a runtime publish's job document is version %d; want 1", doc.Version)
	case doc.CloudSQL == nil || doc.CloudSQL.Instance == "":
		return nil, fmt.Errorf("gcp: a runtime publish's job reaches its database on the stack's Cloud SQL instance; its job document names none")
	case len(doc.Databases) != 1:
		return nil, fmt.Errorf("gcp: a runtime publish migrates one database; its job document names %d", len(doc.Databases))
	}
	database := doc.Databases[0]
	if database.Plan != "plan.json" || database.Record != "record.json" {
		return nil, fmt.Errorf("gcp: a runtime publish's job document names its plan %q and its record %q; want plan.json and record.json beside it", database.Plan, database.Record)
	}
	if database.Service == "" || strings.ContainsAny(database.Service, "/\\") {
		return nil, fmt.Errorf("gcp: a runtime publish's job document names service %q", database.Service)
	}
	cloud := e.cloud()
	bucket := stateBucket(job.Project)
	dir := runtimePrefix + kebab(job.Stack) + "/" + database.Service + "/" + filepath.Base(job.Dir) + "/"
	for _, name := range runtimeDocuments {
		if err := cloud.WriteObject(ctx, bucket, dir+name, docs[name]); err != nil {
			return nil, err
		}
	}
	name := migrationJobName(job.Stack)
	ref := "gs://" + bucket + "/" + dir + "job.json"
	_, _ = fmt.Fprintf(log, "  run job %s: %s\n", name, ref)
	run, err := cloud.RunJob(ctx, job.Project, job.Region, name, []string{"job", "--job", ref})
	if err != nil {
		return nil, err
	}
	if !run.Succeeded {
		return run, fmt.Errorf("gcp: the runtime publish of %s failed in job %s, execution %s: %s", database.Service, name, run.Name, failure(ctx, cloud, run, "", runnerError))
	}
	_, _ = fmt.Fprintf(log, "  execution %s finished\n", run.Name)
	return run, nil
}
