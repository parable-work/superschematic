package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// queueDocument is a listing of shop-db's OrderPlaced, as `stack queue`
// writes one (internal/queueops).
const queueDocument = `{"version": 1, "service": "shop-db", "queue": "OrderPlaced", "operation": "dead", "limit": 10, "statements": {}}` + "\n"

// queueResult is what the runner writes for it: a dead message whose body
// holds personal data, which no log may hold.
const (
	privateNote = "leave it with Mrs Example at 1 Main St"
	queueResult = `{"version": 1, "service": "shop-db", "queue": "OrderPlaced", "operation": "dead", "total": 1, "messages": [` +
		`{"id": "00000000-0000-4000-8000-000000000001", "attempts": 4, "lastError": "the card was refused", "diedAt": "2026-10-20T09:00:00Z", "message": {"note": "` + privateNote + `"}}]}` + "\n"
)

// queueArgs matches the arguments of a queue operation's run of the
// migration job, and captures its id.
var queueArgs = regexp.MustCompile(`^queue --queue gs://` + stateBucket + `/superschematic/migrations/shop/(Staging|Preview\.pr-7)/queue/([0-9a-f]{32})\.json ` +
	`--cloud-sql acme-staging:us-east1:shop-db --cloud-sql-user shop-migrator@acme-staging\.iam --database (shop_db|shop_db_pr7) ` +
	`--result gs://` + stateBucket + `/superschematic/queue/shop/(Staging|Preview\.pr-7)/([0-9a-f]{32})\.json$`)

// writesResult stands in for the runner: a run of the queue command
// writes result where --result names it.
func writesResult(result string) func(args []string) map[string][]byte {
	return func(args []string) map[string][]byte {
		if len(args) == 0 || args[0] != "queue" {
			return nil
		}
		i := slices.Index(args, "--result")
		object := strings.TrimPrefix(args[i+1], "gs://")
		return map[string][]byte{object + ".reserved": {}, object: []byte(result)}
	}
}

// deployedRunner is the runner's image a deploy left the migration job
// running, another than the one the extension names.
var deployedRunner = migrateRepo + "@sha256:" + strings.Repeat("d", 64)

// queueFixture is the gcp target over the fake cloud, with an image of
// the runner, and its queue runner.
func queueFixture(t *testing.T) (*deployFixture, registry.QueueRunner) {
	t.Helper()
	f := newJobFixture(t, gcp.Extension{MigrateImage: migrateRepo + "@sha256:" + strings.Repeat("c", 64)})
	f.cloud.buckets[stateBucket] = "us-east1"
	// The job a deploy made, which a queue operation runs as it is.
	f.cloud.jobs[jobKey] = gcp.JobSpec{Name: "shop-migrate", Container: "migrate", Image: deployedRunner,
		ServiceAccount: "shop-migrator@acme-staging.iam.gserviceaccount.com", Timeout: time.Hour}
	target, _ := f.reg.Target(gcp.Target)
	if target.Queues == nil {
		t.Fatal("the gcp target has no queue runner")
	}
	return f, target.Queues
}

// TestQueueOperationOnGCP runs a listing on Staging, deployed, as `stack
// queue` does (D68, amended): the document goes under the migration
// documents, the state bucket gets the rule that deletes results after a
// day, and the migration job runs `superschematic-migrate queue` as the
// migrator, on the database by its Cloud SQL instance and name, with
// --result naming an object under the stack's results by an id of 128
// random bits, the document's too. The result comes back as the runner
// wrote it, its version and its marker's are deleted, and the log holds
// nothing of it. A
// second run takes another id. A member of Preview reaches its own
// database, under its run's name.
func TestQueueOperationOnGCP(t *testing.T) {
	f, queues := queueFixture(t)
	delete(f.cloud.jobs, jobKey)
	f.ready(t)
	f.cloud.runner = writesResult(queueResult)
	ctx := context.Background()
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
	var log bytes.Buffer
	o := stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir(), Log: &log}
	if _, err := stack.Deploy(ctx, stack.DeployOptions{Options: o, Images: shopImages("acme-staging", 1), Planner: firstPlanner}); err != nil {
		t.Fatal(err)
	}
	runs := len(f.cloud.runs)
	log.Reset()
	out, err := stack.RunQueue(ctx, stack.RunQueueOptions{Options: o, Database: "shop-db", Service: "shop-db", Document: []byte(queueDocument)})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != queueResult {
		t.Errorf("the result is %s", out)
	}
	if len(f.cloud.runs) != runs+1 {
		t.Fatalf("ran %d executions", len(f.cloud.runs)-runs)
	}
	m := queueArgs.FindStringSubmatch(strings.Join(f.cloud.runs[runs], " "))
	if m == nil || m[1] != "Staging" || m[3] != "shop_db" || m[4] != "Staging" || m[2] != m[5] {
		t.Fatalf("the job ran with %q", f.cloud.runs[runs])
	}
	if got := string(f.object(t, "superschematic/migrations/shop/Staging/queue/"+m[2]+".json")); got != queueDocument {
		t.Errorf("the document is %s", got)
	}
	result := "superschematic/queue/shop/Staging/" + m[5] + ".json"
	if _, ok := f.cloud.objects[stateBucket+"/"+result]; ok {
		t.Error("the result is left in the state bucket")
	}
	if len(f.cloud.deletes) != 2 || !strings.HasPrefix(f.cloud.deletes[0], "gs://"+stateBucket+"/"+result+"#") ||
		!strings.HasPrefix(f.cloud.deletes[1], "gs://"+stateBucket+"/"+result+".reserved#") {
		t.Errorf("deleted %q", f.cloud.deletes)
	}
	if _, ok := f.cloud.objects[stateBucket+"/"+result+".reserved"]; ok {
		t.Error("the result's marker is left in the state bucket")
	}
	if rules := f.cloud.lifecycle[stateBucket]; !slices.ContainsFunc(rules, func(r gcp.LifecycleRule) bool {
		return slices.Equal(r.Prefixes, []string{"superschematic/queue/"}) && r.AgeDays == 1
	}) {
		t.Errorf("the state bucket's rules are %+v", rules)
	}
	if job := f.cloud.jobs[jobKey]; job.ServiceAccount != "shop-migrator@acme-staging.iam.gserviceaccount.com" {
		t.Errorf("the job runs as %s", job.ServiceAccount)
	}
	for _, private := range []string{privateNote, "the card was refused"} {
		if strings.Contains(log.String(), private) {
			t.Errorf("the log holds %q:\n%s", private, log.String())
		}
	}
	if !strings.Contains(log.String(), "run job shop-migrate ("+migrateRepo+"@sha256:"+strings.Repeat("c", 64)+"): the queue operation of gs://"+stateBucket+"/superschematic/migrations/shop/Staging/queue/"+m[2]+".json on shop-db") {
		t.Errorf("the log:\n%s", log.String())
	}

	if _, err := stack.RunQueue(ctx, stack.RunQueueOptions{Options: o, Database: "shop-db", Service: "shop-db", Document: []byte(queueDocument)}); err != nil {
		t.Fatal(err)
	}
	if again := queueArgs.FindStringSubmatch(strings.Join(f.cloud.runs[runs+1], " ")); again == nil || again[2] == m[2] {
		t.Errorf("the second run ran with %q", f.cloud.runs[runs+1])
	}

	preview := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Preview")
	if _, err := queues.RunQueue(ctx, registry.QueueRequest{
		Run: registry.Run{Environment: preview, Parameters: map[string]string{"pr": "7"}}, Database: "shop-db", Service: "shop-db", Document: []byte(queueDocument),
	}); err != nil {
		t.Fatal(err)
	}
	if m := queueArgs.FindStringSubmatch(strings.Join(f.cloud.runs[len(f.cloud.runs)-1], " ")); m == nil || m[1] != "Preview.pr-7" || m[3] != "shop_db_pr7" || m[4] != "Preview.pr-7" {
		t.Errorf("the member's run ran with %q", f.cloud.runs[len(f.cloud.runs)-1])
	}
}

// TestQueueOperationFailsOnGCP: an execution that fails reports the
// runner's error, which it wrote to stderr, read from Cloud Logging as a
// failed migration's is; one that succeeds and wrote no result says so; a
// wait that fails says the execution may still write its result, which
// the bucket deletes a day later; a result that cannot be deleted comes
// back all the same, and the log says the bucket deletes it.
func TestQueueOperationFailsOnGCP(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, f *deployFixture, queues registry.QueueRunner, log io.Writer) ([]byte, error) {
		t.Helper()
		env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
		return queues.RunQueue(ctx, registry.QueueRequest{Run: registry.Run{Environment: env}, Database: "shop-db", Service: "shop-db", Document: []byte(queueDocument), Log: log})
	}

	t.Run("the runner's error", func(t *testing.T) {
		f, queues := queueFixture(t)
		f.cloud.failRun["--result"] = fakeFailure{message: cloudRunExited, stderr: []string{
			"superschematic-migrate: queue OrderPlaced of shop-db: 1 of the 1 message(s) named are not dead, so none was retried: 00000000-0000-4000-8000-000000000001 is ready",
		}}
		_, err := run(t, f, queues, nil)
		want := "gcp: the queue operation on shop-db failed in job shop-migrate, execution " + jobKey + "/executions/1: " +
			"superschematic-migrate: queue OrderPlaced of shop-db: 1 of the 1 message(s) named are not dead, so none was retried: 00000000-0000-4000-8000-000000000001 is ready (logs: https://console.cloud.google.com/logs/x)"
		if err == nil || err.Error() != want {
			t.Fatalf("RunQueue = %v\nwant %s", err, want)
		}
		if len(f.cloud.stderrReads) != 1 {
			t.Errorf("stderr reads %+v", f.cloud.stderrReads)
		}
	})

	t.Run("no result", func(t *testing.T) {
		f, queues := queueFixture(t)
		_, err := run(t, f, queues, nil)
		if err == nil || !regexp.MustCompile(`^gcp: execution `+jobKey+`/executions/1 of job shop-migrate succeeded and wrote no result to gs://`+stateBucket+`/superschematic/queue/shop/Staging/[0-9a-f]{32}\.json \(logs: `).MatchString(err.Error()) {
			t.Fatalf("RunQueue = %v", err)
		}
	})

	t.Run("the wait fails", func(t *testing.T) {
		f, queues := queueFixture(t)
		f.cloud.failWait = context.DeadlineExceeded
		_, err := run(t, f, queues, nil)
		if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "; an execution that still runs writes its result to gs://"+stateBucket+"/superschematic/queue/shop/Staging/") ||
			!strings.HasSuffix(err.Error(), ".json, which the state bucket deletes a day later") {
			t.Fatalf("RunQueue = %v", err)
		}
	})

	t.Run("the delete fails", func(t *testing.T) {
		f, queues := queueFixture(t)
		f.cloud.runner = writesResult(queueResult)
		f.cloud.failDelete = errors.New("403 Forbidden")
		var log bytes.Buffer
		out, err := run(t, f, queues, &log)
		if err != nil || string(out) != queueResult {
			t.Fatalf("RunQueue = %s, %v", out, err)
		}
		if !strings.Contains(log.String(), "was read and not deleted (403 Forbidden); the state bucket deletes it a day after it was written") || strings.Contains(log.String(), privateNote) {
			t.Errorf("the log:\n%s", log.String())
		}
	})

	t.Run("no lifecycle rule", func(t *testing.T) {
		f, queues := queueFixture(t)
		delete(f.cloud.buckets, stateBucket)
		_, err := run(t, f, queues, nil)
		if err == nil || !strings.Contains(err.Error(), "the state bucket's rule that deletes what is under superschematic/queue/ after a day") || len(f.cloud.runs) > 0 {
			t.Fatalf("RunQueue = %v, %d run(s)", err, len(f.cloud.runs))
		}
	})
}

// TestQueueOperationLeavesTheJob: a queue operation runs the migration job
// as the last deploy left it, another release's runner here, and changes
// nothing of it, builds nothing and names the image in the log; a stack
// with no job yet is refused before anything is written; a job whose
// runner predates --cloud-sql, and a migrator that may not reserve the
// result, are reported with what to do.
func TestQueueOperationLeavesTheJob(t *testing.T) {
	ctx := context.Background()
	request := func(t *testing.T, f *deployFixture, log io.Writer) registry.QueueRequest {
		env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
		return registry.QueueRequest{Run: registry.Run{Environment: env}, Database: "shop-db", Service: "shop-db", Document: []byte(queueDocument), Log: log}
	}

	f, queues := queueFixture(t)
	f.cloud.runner = writesResult(queueResult)
	before := f.cloud.jobs[jobKey]
	var log bytes.Buffer
	if _, err := queues.RunQueue(ctx, request(t, f, &log)); err != nil {
		t.Fatal(err)
	}
	if after := f.cloud.jobs[jobKey]; after.Image != deployedRunner || after.Image != before.Image || len(f.cloud.builds) > 0 {
		t.Errorf("the queue operation changed the job to %s, or built %d image(s)", after.Image, len(f.cloud.builds))
	}
	if slices.ContainsFunc(f.cloud.Changes(), func(c string) bool { return strings.HasPrefix(c, "job ") }) {
		t.Errorf("the queue operation changed %v", f.cloud.Changes())
	}
	if !strings.Contains(log.String(), "run job shop-migrate ("+deployedRunner+")") {
		t.Errorf("the log:\n%s", log.String())
	}

	none, queues := queueFixture(t)
	delete(none.cloud.jobs, jobKey)
	_, err := queues.RunQueue(ctx, request(t, none, nil))
	if err == nil || err.Error() != "gcp: the stack's migration job shop-migrate does not exist in acme-staging; deploy Staging first, which creates it" {
		t.Errorf("RunQueue with no job = %v", err)
	}
	if len(none.cloud.runs) > 0 || slices.ContainsFunc(none.cloud.Changes(), func(c string) bool { return strings.HasPrefix(c, "lifecycle") }) {
		t.Errorf("a refused operation ran %d job(s) and changed %v", len(none.cloud.runs), none.cloud.Changes())
	}

	old, queues := queueFixture(t)
	old.cloud.failRun["--result"] = fakeFailure{message: cloudRunExited, stderr: []string{
		"flag provided but not defined: -cloud-sql",
		"superschematic-migrate: flag provided but not defined: -cloud-sql",
	}}
	_, err = queues.RunQueue(ctx, request(t, old, nil))
	if err == nil || !strings.HasSuffix(err.Error(), "; the job runs "+deployedRunner+", a runner from before stack queue reached Cloud SQL, and stack queue does not change it: deploy Staging with this release, which updates the job, then run this again") {
		t.Errorf("RunQueue on an older runner = %v", err)
	}

	unbootstrapped, queues := queueFixture(t)
	unbootstrapped.cloud.failRun["--result"] = fakeFailure{message: cloudRunExited, stderr: []string{
		"superschematic-migrate: queue OrderPlaced of shop-db: reserve gs://" + stateBucket + "/superschematic/queue/shop/Staging/x.json for the result: " +
			"gs://" + stateBucket + "/superschematic/queue/shop/Staging/x.json.reserved: 403 Forbidden: denied; the queue was not changed",
	}}
	_, err = queues.RunQueue(ctx, request(t, unbootstrapped, nil))
	if err == nil || !strings.Contains(err.Error(), "the queue was not changed (logs: ") ||
		!strings.HasSuffix(err.Error(), "; the migrator may not create the result: run superschematic stack bootstrap again, which grants it, then run this again") {
		t.Errorf("RunQueue without the grant = %v", err)
	}
}

// TestQueueOperationRereadsTheLifecycle: a lifecycle write that another
// writer's change to the bucket refused is read and written again, up to
// five times.
func TestQueueOperationRereadsTheLifecycle(t *testing.T) {
	ctx := context.Background()
	f, queues := queueFixture(t)
	f.cloud.runner = writesResult(queueResult)
	f.cloud.lifecycleConflicts = 4
	env := resolve(t, f.reg, shop(), stacktest.AcmeShop(), "Staging")
	req := registry.QueueRequest{Run: registry.Run{Environment: env}, Database: "shop-db", Service: "shop-db", Document: []byte(queueDocument)}
	if _, err := queues.RunQueue(ctx, req); err != nil {
		t.Fatal(err)
	}
	f, queues = queueFixture(t)
	f.cloud.lifecycleConflicts = 5
	if _, err := queues.RunQueue(ctx, req); err == nil || !errors.Is(err, gcp.ErrBucketChanged) || len(f.cloud.runs) > 0 {
		t.Errorf("RunQueue after five conflicts = %v, %d run(s)", err, len(f.cloud.runs))
	}
}
