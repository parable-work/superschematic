package gcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/parable-work/superschematic/registry"
)

// An operator's operation on a queue's dead messages in a deployed run
// (`superschematic stack queue`, docs/stack-model.md, section 8.8, D68,
// amended) runs as one execution of the stack's migration job, which
// reaches Cloud SQL as the migrator, as a deploy's migrations do:
//
//  1. the runner writes the queue document under the migration documents,
//     superschematic/migrations/<stack>/<run>/queue/<id>.json, which the
//     migrator reads;
//  2. it runs the job once, as the last deploy left it, with
//     `superschematic-migrate queue`, the database by its Cloud SQL
//     instance and name, and --result naming
//     superschematic/queue/<stack>/<run>/<id>.json, where the runner
//     writes what it did instead of to stdout: a listing holds the dead
//     messages' bodies, which may hold personal data, and the job's
//     stdout and stderr go to Cloud Logging, which every holder of Logs
//     Viewer reads. The runner first creates an empty marker,
//     <id>.json.reserved, so a migrator that may not write the result
//     fails before the queue changes;
//  3. it reads the result once and deletes its version, and the marker's,
//     so the bucket, which keeps versions, holds no copy.
//
// It never changes the job. The job is shared with the deploys of every
// run of the stack in the project, and a deploy updates its image between
// its EnsureJob and its RunJob, holding only its own run's lock; a queue
// operation that updated the job to its CLI's release could put that
// release's runner under a deploy's migration phase. A job a deploy of an
// older release left, whose runner does not take --cloud-sql or --result,
// fails with a usage error before it reads anything, and the error says
// to deploy with this release.
//
// <id> is 128 random bits, so no one can name the object of another run
// before it exists. The migrator may create objects under its stack's
// queuePrefix and nothing else there (BootstrapEnvironment): it reads
// none, and replaces none, since the runner writes only where no object
// is. The state bucket's queueLifecycle rule deletes a result a day after
// it was written, so one a crashed command never read is gone by then.

// queuePrefix is where the runner writes each queue operation's result.
const queuePrefix = "superschematic/queue/"

// queueRetentionDays is how long the state bucket keeps a result the
// command did not delete (queueLifecycle).
const queueRetentionDays = 1

// queueLifecycle is the state bucket's rule that deletes each queue
// operation's result, live and noncurrent, a day after it was written.
func queueLifecycle() LifecycleRule {
	return LifecycleRule{Prefixes: []string{queuePrefix}, AgeDays: queueRetentionDays}
}

// queueResultPrefix is where the results of a stack's queue operations
// sit, which the stack's migrator may create objects under.
func queueResultPrefix(stack string) string { return queuePrefix + kebab(stack) + "/" }

var _ registry.QueueRunner = migrationRunner{}

// RunQueue runs a queue document on req's database as an execution of the
// stack's migration job, and returns the result the execution wrote to the
// state bucket.
func (r migrationRunner) RunQueue(ctx context.Context, req registry.QueueRequest) ([]byte, error) {
	if err := req.Check(); err != nil {
		return nil, err
	}
	env := req.Run.Environment
	v, err := envValues(env)
	if err != nil {
		return nil, err
	}
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(log, "  "+format+"\n", args...) }
	cloud := r.ext.cloud()
	bucket := stateBucket(v.project)
	params := req.Run.Parameters
	instance, err := nodeString(env, req.Database+".instance", "name", params)
	if err != nil {
		return nil, err
	}
	database, err := nodeString(env, req.Database+".database."+req.Service, "name", params)
	if err != nil {
		return nil, err
	}

	name := migrationJobName(env.Stack)
	image, err := cloud.JobImage(ctx, v.project, v.region, name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("gcp: the stack's migration job %s does not exist in %s; deploy %s first, which creates it", name, v.project, req.Run.Name())
	case err != nil:
		return nil, err
	}

	// The rule that deletes a result nobody read is in place before
	// anything can write one; bootstrap sets it too. Another writer of the
	// bucket's lifecycle in between, a bootstrap or a second queue
	// operation, is read again.
	if err := ensureLifecycleRule(ctx, cloud, bucket, queueLifecycle()); err != nil {
		return nil, fmt.Errorf("gcp: the state bucket's rule that deletes what is under %s after a day, which a queue operation needs before it writes a result there: %w", queuePrefix, err)
	}
	id, err := queueRunID()
	if err != nil {
		return nil, err
	}
	docObject := migrationPrefix + kebab(env.Stack) + "/" + req.Run.Name() + "/queue/" + id + ".json"
	resultObject := queueResultPrefix(env.Stack) + req.Run.Name() + "/" + id + ".json"
	if err := cloud.WriteObject(ctx, bucket, docObject, req.Document); err != nil {
		return nil, err
	}
	resultRef := "gs://" + bucket + "/" + resultObject
	logf("run job %s (%s): the queue operation of gs://%s/%s on %s", name, image, bucket, docObject, req.Service)
	run, err := cloud.RunJob(ctx, v.project, v.region, name, []string{
		"queue", "--queue", "gs://" + bucket + "/" + docObject,
		"--cloud-sql", v.project + ":" + v.region + ":" + instance,
		"--cloud-sql-user", migratorUser(env.Stack, v.project),
		"--database", database,
		"--result", resultRef,
	})
	if err != nil {
		return nil, fmt.Errorf("%w; an execution that still runs writes its result to %s, which the state bucket deletes a day later", err, resultRef)
	}
	defer r.removeResult(ctx, bucket, resultObject+reservedSuffix, logf)
	// The runner writes its result last, so a failed execution holds
	// none; this deletes one all the same.
	if !run.Succeeded {
		r.removeResult(ctx, bucket, resultObject, logf)
		why := failure(ctx, cloud, run, "", runnerError)
		switch {
		case strings.Contains(why, "flag provided but not defined: -cloud-sql"), strings.Contains(why, "flag provided but not defined: -result"):
			why += fmt.Sprintf("; the job runs %s, a runner from before stack queue reached Cloud SQL, and stack queue does not change it: deploy %s with this release, which updates the job, then run this again", image, req.Run.Name())
		case strings.Contains(why, "the queue was not changed") && strings.Contains(why, "403"):
			why += "; the migrator may not create the result: run superschematic stack bootstrap again, which grants it, then run this again"
		}
		return nil, fmt.Errorf("gcp: the queue operation on %s failed in job %s, execution %s: %s", req.Service, name, run.Name, why)
	}
	data, generation, err := cloud.ReadObjectGeneration(ctx, bucket, resultObject)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("gcp: execution %s of job %s succeeded and wrote no result to %s (logs: %s)", run.Name, name, resultRef, run.LogURI)
	case err != nil:
		return nil, fmt.Errorf("gcp: the result of execution %s: %w; the state bucket deletes it a day after it was written", run.Name, err)
	}
	if err := cloud.DeleteObjectVersion(context.WithoutCancel(ctx), bucket, resultObject, generation); err != nil {
		logf("the result %s was read and not deleted (%v); the state bucket deletes it a day after it was written", resultRef, err)
	}
	logf("execution %s finished", run.Name)
	return data, nil
}

// removeResult deletes a result an execution left, if any, and says so
// when it cannot.
func (r migrationRunner) removeResult(ctx context.Context, bucket, object string, logf func(string, ...any)) {
	ctx = context.WithoutCancel(ctx)
	cloud := r.ext.cloud()
	_, generation, err := cloud.ReadObjectGeneration(ctx, bucket, object)
	if err == nil {
		err = cloud.DeleteObjectVersion(ctx, bucket, object, generation)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		logf("a result at gs://%s/%s may be left (%v); the state bucket deletes it a day after it was written", bucket, object, err)
	}
}

// reservedSuffix ends the name of the empty marker the runner writes
// beside a result's object before the operation runs (runtime/migrate,
// "Queues"), which only the reader of the result can delete.
const reservedSuffix = ".reserved"

// lifecycleAttempts is how many times ensureLifecycleRule reads the
// bucket's lifecycle again after another writer changed the bucket.
const lifecycleAttempts = 5

// ensureLifecycleRule gives the bucket rule, reading its lifecycle again
// when another writer changed the bucket between the read and the write.
func ensureLifecycleRule(ctx context.Context, cloud Cloud, bucket string, rule LifecycleRule) error {
	var err error
	for range lifecycleAttempts {
		if _, err = cloud.EnsureLifecycleRule(ctx, bucket, rule); !errors.Is(err, ErrBucketChanged) {
			return err
		}
	}
	return err
}

// queueRunID returns 128 random bits in hex: the name of one queue
// operation's document and result.
func queueRunID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("gcp: a queue operation's id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
