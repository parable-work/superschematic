package gcp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// runtimeDir writes one runtime publish's documents as runtime mode's job
// runner writes them (rules-runtime's JobRunner with a Cloud SQL
// database), into a directory named after the plan's hash.
func runtimeDir(t *testing.T, job map[string]any) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "superschematic", "migrations", "runtime", "tasks-db", "5e1f")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"job.json":    doc,
		"plan.json":   []byte(`{"version": 1, "service": "tasks-db"}`),
		"record.json": []byte(`{"version": 1, "service": "tasks-db", "modelHash": "abc"}`),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runtimeJob() map[string]any {
	return map[string]any{
		"version": 1, "phase": "all",
		"cloudSql": map[string]any{"instance": "acme-staging:us-east1:shop-db", "user": "shop-migrator@acme-staging.iam"},
		"databases": []any{map[string]any{
			"service": "tasks-db", "database": "tasks", "plan": "plan.json", "record": "record.json",
			"privileges": map[string]any{"readWrite": []any{"shop-api@acme-staging.iam"}, "readState": true},
		}},
	}
}

// TestRuntimeJobRunsThePublishInTheMigrationJob: a runtime publish's
// documents go to the state bucket, beside one another, and one execution
// of the stack's migration job runs the job document from there; a failed
// execution fails with the runner's error from its logs, and a job
// document that names its plan or record elsewhere is refused before
// anything is written.
func TestRuntimeJobRunsThePublishInTheMigrationJob(t *testing.T) {
	f := newFakeCloud()
	f.jobs["acme-staging/us-east1/shop-migrate"] = gcp.JobSpec{Name: "shop-migrate"}
	ext := gcp.Extension{Cloud: f}
	ctx := context.Background()
	job := gcp.RuntimeJob{Project: "acme-staging", Region: "us-east1", Stack: "Shop", Dir: runtimeDir(t, runtimeJob())}

	run, err := ext.RunRuntimeJob(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Succeeded {
		t.Fatalf("the execution %+v", run)
	}
	prefix := stateBucket + "/superschematic/migrations/runtime/shop/tasks-db/5e1f/"
	for _, name := range []string{"job.json", "plan.json", "record.json"} {
		if _, ok := f.objects[prefix+name]; !ok {
			t.Errorf("no %s%s in %v", prefix, name, keys(f.objects))
		}
	}
	want := []string{"job", "--job", "gs://" + prefix + "job.json"}
	if len(f.runs) != 1 || !slices.Equal(f.runs[0], want) {
		t.Fatalf("the job ran with %q, want %q", f.runs, want)
	}

	f.failRun["runtime/shop/tasks-db"] = fakeFailure{
		message: cloudRunExited,
		stderr:  []string{`superschematic-migrate: service tasks-db: the record is of model abc and the plan ends at model def`},
	}
	_, err = ext.RunRuntimeJob(ctx, job, nil)
	if err == nil || !strings.Contains(err.Error(), "superschematic-migrate: service tasks-db: the record is of model abc") ||
		!strings.Contains(err.Error(), "shop-migrate/executions/2") {
		t.Fatalf("a failed execution = %v", err)
	}

	elsewhere := runtimeJob()
	elsewhere["databases"].([]any)[0].(map[string]any)["plan"] = "gs://other/plan.json"
	before := len(f.objects)
	_, err = ext.RunRuntimeJob(ctx, gcp.RuntimeJob{Project: "acme-staging", Region: "us-east1", Stack: "Shop", Dir: runtimeDir(t, elsewhere)}, nil)
	if err == nil || !strings.Contains(err.Error(), "want plan.json and record.json beside it") || len(f.objects) != before {
		t.Fatalf("a plan elsewhere = %v, %d objects written", err, len(f.objects)-before)
	}
	noSQL := runtimeJob()
	delete(noSQL, "cloudSql")
	if _, err := ext.RunRuntimeJob(ctx, gcp.RuntimeJob{Project: "acme-staging", Region: "us-east1", Stack: "Shop", Dir: runtimeDir(t, noSQL)}, nil); err == nil ||
		!strings.Contains(err.Error(), "Cloud SQL instance") {
		t.Fatalf("a job with no Cloud SQL instance = %v", err)
	}

	// A stack no deploy made the job of.
	if _, err := ext.RunRuntimeJob(ctx, gcp.RuntimeJob{Project: "acme-staging", Region: "us-east1", Stack: "Other", Dir: job.Dir}, nil); err == nil {
		t.Fatal("a stack with no migration job ran one")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
