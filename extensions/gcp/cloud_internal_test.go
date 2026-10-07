package gcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	ir "github.com/parable-work/superschematic/ir"
)

// TestBuildEnded: waitBuild stops at every status but the four of a build
// that has yet to end. A status Cloud Build adds fails here until it is
// placed.
func TestBuildEnded(t *testing.T) {
	running := map[cloudbuildpb.Build_Status]bool{
		cloudbuildpb.Build_STATUS_UNKNOWN: true,
		cloudbuildpb.Build_PENDING:        true,
		cloudbuildpb.Build_QUEUED:         true,
		cloudbuildpb.Build_WORKING:        true,
	}
	ended := map[cloudbuildpb.Build_Status]bool{
		cloudbuildpb.Build_SUCCESS:        true,
		cloudbuildpb.Build_FAILURE:        true,
		cloudbuildpb.Build_INTERNAL_ERROR: true,
		cloudbuildpb.Build_TIMEOUT:        true,
		cloudbuildpb.Build_CANCELLED:      true,
		cloudbuildpb.Build_EXPIRED:        true,
	}
	for value, name := range cloudbuildpb.Build_Status_name {
		status := cloudbuildpb.Build_Status(value)
		if !running[status] && !ended[status] {
			t.Errorf("status %s is neither running nor ended", name)
		}
		if got := buildEnded(status); got != ended[status] {
			t.Errorf("buildEnded(%s) = %v", name, got)
		}
	}
}

// TestTagResource names an image's tag in Artifact Registry.
func TestTagResource(t *testing.T) {
	got, err := tagResource("us-east1-docker.pkg.dev/acme-staging/shop/superschematic-migrate:1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if want := "projects/acme-staging/locations/us-east1/repositories/shop/packages/superschematic-migrate/tags/1.2.3"; got != want {
		t.Errorf("tagResource = %s, want %s", got, want)
	}
	got, err = tagResource("europe-west1-docker.pkg.dev/p/r/a/b:context-ab")
	if err != nil || got != "projects/p/locations/europe-west1/repositories/r/packages/a%2Fb/tags/context-ab" {
		t.Errorf("a nested image: %s, %v", got, err)
	}
	for _, bad := range []string{
		"us-east1-docker.pkg.dev/acme-staging/shop/orders",
		"gcr.io/acme/orders:1",
		"us-east1-docker.pkg.dev/acme-staging/orders:1",
		"localhost:5000/orders",
	} {
		if _, err := tagResource(bad); err == nil {
			t.Errorf("tagResource(%s) is accepted", bad)
		}
	}
}

// TestLiteral puts a run's parameter values in a name.
func TestLiteral(t *testing.T) {
	params := map[string]string{"pr": "7"}
	got, err := literal(ir.Concat{"shop-api-pr", ir.Parameter("pr"), "@acme.iam"}, params)
	if err != nil || got != "shop-api-pr7@acme.iam" {
		t.Errorf("literal = %q, %v", got, err)
	}
	if _, err := literal(ir.Parameter("region"), params); err == nil || !strings.Contains(err.Error(), "region has no value") {
		t.Errorf("a parameter with no value: %v", err)
	}
	if _, err := literal(ir.Output{Resource: "x", Name: "name"}, params); err == nil {
		t.Error("an output is read as a string")
	}
}

// TestReleaseVersion: a test binary is built from a checkout, so it names
// no release.
func TestReleaseVersion(t *testing.T) {
	if v := releaseVersion(); v != "" {
		t.Errorf("releaseVersion = %q in a test binary", v)
	}
}

// fakeOperation is the operation of a job's execution: Wait's result, and
// whether the operation is done after it.
type fakeOperation struct {
	execution *runpb.Execution
	err       error
	done      bool
	meta      *runpb.Execution
}

func (op fakeOperation) Wait(context.Context, ...gax.CallOption) (*runpb.Execution, error) {
	return op.execution, op.err
}

func (op fakeOperation) Done() bool { return op.done }

func (op fakeOperation) Metadata() (*runpb.Execution, error) { return op.meta, nil }

// TestWaitExecution: an operation that ends with an error is a failed
// execution, with Cloud Run's message; an error while it is not done is
// the wait's, and no JobRun.
func TestWaitExecution(t *testing.T) {
	const job = "projects/p/locations/r/jobs/shop-migrate"
	created := time.Date(2026, 10, 7, 18, 2, 0, 0, time.UTC)
	execution := &runpb.Execution{
		Name:       job + "/executions/shop-migrate-x7k2p",
		LogUri:     "https://console.cloud.google.com/logs/x",
		CreateTime: timestamppb.New(created),
	}
	ctx := context.Background()

	done := proto.Clone(execution).(*runpb.Execution)
	done.SucceededCount = 1
	run, err := waitExecution(ctx, job, fakeOperation{execution: done, done: true})
	if err != nil || !run.Succeeded || run.Name != execution.Name || !run.Created.Equal(created) {
		t.Errorf("a succeeded execution: %+v, %v", run, err)
	}

	failed := proto.Clone(execution).(*runpb.Execution)
	failed.FailedCount = 1
	failed.Conditions = []*runpb.Condition{{Type: "Completed", Message: "Task shop-migrate-x7k2p-task0 failed with exit code: 1"}}
	run, err = waitExecution(ctx, job, fakeOperation{execution: failed, done: true})
	if err != nil || run.Succeeded || run.Message != "Task shop-migrate-x7k2p-task0 failed with exit code: 1" {
		t.Errorf("an execution that failed in its response: %+v, %v", run, err)
	}

	aborted := status.Error(codes.Aborted, "Task shop-migrate-x7k2p-task0 failed with exit code: 1 and message: The container exited with an error.")
	run, err = waitExecution(ctx, job, fakeOperation{err: aborted, done: true, meta: execution})
	if err != nil || run.Succeeded || run.Name != execution.Name || run.LogURI != execution.LogUri || !run.Created.Equal(created) ||
		run.Message != "Task shop-migrate-x7k2p-task0 failed with exit code: 1 and message: The container exited with an error." {
		t.Errorf("an operation that ended with an error: %+v, %v", run, err)
	}

	unavailable := status.Error(codes.Unavailable, "connection reset by peer")
	run, err = waitExecution(ctx, job, fakeOperation{err: unavailable, meta: execution})
	if run != nil || err == nil || !errors.Is(err, unavailable) ||
		err.Error() != "gcp: job "+job+": waiting for execution "+execution.Name+", which may still be running: rpc error: code = Unavailable desc = connection reset by peer" {
		t.Errorf("a wait that failed: %+v, %v", run, err)
	}
	if _, err = waitExecution(ctx, job, fakeOperation{err: context.Canceled}); !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "waiting for its execution") {
		t.Errorf("a wait canceled before the first poll: %v", err)
	}
}

// TestStderrFilter: an execution's stderr is its job's stderr log,
// labeled with its name, since a minute before it was created.
func TestStderrFilter(t *testing.T) {
	run := &JobRun{
		Name:    "projects/acme-staging/locations/us-east1/jobs/shop-migrate/executions/shop-migrate-x7k2p",
		Created: time.Date(2026, 10, 7, 18, 2, 30, 0, time.FixedZone("EDT", -4*3600)),
	}
	project, filter, err := stderrFilter(run)
	if err != nil {
		t.Fatal(err)
	}
	want := `logName="projects/acme-staging/logs/run.googleapis.com%2Fstderr" AND resource.type="cloud_run_job" AND ` +
		`resource.labels.location="us-east1" AND resource.labels.job_name="shop-migrate" AND ` +
		`labels."run.googleapis.com/execution_name"="shop-migrate-x7k2p" AND timestamp>="2026-10-07T22:01:30Z"`
	if project != "acme-staging" || filter != want {
		t.Errorf("stderrFilter = %s, %s\nwant acme-staging, %s", project, filter, want)
	}
	run.Created = time.Time{}
	if _, filter, _ := stderrFilter(run); strings.Contains(filter, "timestamp") {
		t.Errorf("an execution with no creation time: %s", filter)
	}
	for _, bad := range []string{"", "projects/p/locations/r/jobs/j", "projects/p/locations/r/jobs//executions/e", "projects/p/locations/r/services/s/revisions/x"} {
		if _, _, err := stderrFilter(&JobRun{Name: bad}); err == nil {
			t.Errorf("stderrFilter(%q) is accepted", bad)
		}
	}
}

// TestEntryLine reads a stderr line from a log entry: its text, or the
// message of a JSON line.
func TestEntryLine(t *testing.T) {
	text := &loggingpb.LogEntry{Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "superschematic-migrate: refused\n"}}
	if got := entryLine(text); got != "superschematic-migrate: refused" {
		t.Errorf("a text line: %q", got)
	}
	message, _ := structpb.NewStruct(map[string]any{"message": "connected", "severity": "INFO"})
	if got := entryLine(&loggingpb.LogEntry{Payload: &loggingpb.LogEntry_JsonPayload{JsonPayload: message}}); got != "connected" {
		t.Errorf("a JSON line with a message: %q", got)
	}
	other, _ := structpb.NewStruct(map[string]any{"msg": "connected"})
	if got := entryLine(&loggingpb.LogEntry{Payload: &loggingpb.LogEntry_JsonPayload{JsonPayload: other}}); !strings.Contains(got, `"msg"`) || !strings.Contains(got, "connected") {
		t.Errorf("a JSON line without a message: %q", got)
	}
	if got := entryLine(&loggingpb.LogEntry{}); got != "" {
		t.Errorf("an entry with no payload: %q", got)
	}
}

// TestUntilEnabled: bootstrap retries a call that an API it has just
// enabled refuses, in the words the KMS client and the provisioner used on
// a fresh project, and returns any other error, or the refusal once the
// propagation window has passed, at once.
func TestUntilEnabled(t *testing.T) {
	defer func(p, r time.Duration) { apiPropagation, apiRetry = p, r }(apiPropagation, apiRetry)
	apiPropagation, apiRetry = time.Second, time.Millisecond
	ctx := context.Background()
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	for _, refusal := range []string{
		"gcp: key ring projects/p/locations/r/keyRings/k: rpc error: code = PermissionDenied desc = Google Cloud KMS API has not been used in project 100000 before or it is disabled. Enable it by visiting https://console.developers.google.com/apis/api/cloudkms.googleapis.com/overview?project=100000 then retry.",
		"pulumi: up p, step infrastructure: googleapi: Error 403: Google Cloud KMS API has not been used in project 100000 before or it is disabled., forbidden",
		"rpc error: code = PermissionDenied desc = reason: SERVICE_DISABLED",
	} {
		calls := 0
		err := untilEnabled(ctx, logf, "the state key", func() error {
			if calls++; calls < 3 {
				return errors.New(refusal)
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Errorf("%q: err %v after %d calls, want success on the third", refusal, err, calls)
		}
	}
	if len(logged) != 6 || !strings.Contains(logged[0], "the state key") {
		t.Errorf("logged %q, want a line per retry naming the call", logged)
	}

	calls := 0
	other := errors.New("rpc error: code = PermissionDenied desc = the caller does not have permission")
	if err := untilEnabled(ctx, logf, "x", func() error { calls++; return other }); err != other || calls != 1 {
		t.Errorf("another error: %v after %d calls, want it at once", err, calls)
	}

	apiPropagation = 0
	calls = 0
	disabled := errors.New("the API has not been used in project 100000 before or it is disabled")
	if err := untilEnabled(ctx, logf, "x", func() error { calls++; return disabled }); err != disabled || calls != 1 {
		t.Errorf("past the window: %v after %d calls, want the refusal at once", err, calls)
	}
}
