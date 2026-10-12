package local_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The local target's jobs (D52, docs/stack-model.md, section 8.7):
// Pinned runs shop-orders' job every minute, with one retry and a timeout
// of five minutes.

const shipOrders = stacktest.ShipOrdersJob

// fakeClock fires each wait a schedule asks for when the test ticks it,
// and each other wait, a run's timeout, when timeouts says so.
type fakeClock struct {
	now      time.Time
	ticks    chan time.Time
	timeouts chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:      time.Date(2026, 10, 7, 12, 0, 30, 0, time.UTC),
		ticks:    make(chan time.Time),
		timeouts: make(chan time.Time),
	}
}

func (c *fakeClock) Now() time.Time { return c.now }

// After hands a schedule's wait, a minute at most, the ticks, and a
// run's timeout, five minutes, the timeouts.
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	if d <= time.Minute {
		return c.ticks
	}
	return c.timeouts
}

// lockedOutput is the provisioner's output, which its runs write while the
// test reads it.
type lockedOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *lockedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *lockedOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// waitFor waits until the output holds want.
func waitFor(t *testing.T, out *lockedOutput, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("the output never held %q:\n%s", want, out)
		}
		time.Sleep(time.Millisecond)
	}
}

// pinnedJobs applies Pinned with a fake clock and output, and returns the
// fixture with Wait running and the function that stops it and returns
// Wait's error.
func pinnedJobs(t *testing.T, exits ...error) (*fixture, *fakeClock, *lockedOutput, func() error) {
	t.Helper()
	f := newFixture(t, "Pinned")
	clock := newFakeClock()
	out := &lockedOutput{}
	f.prov.Clock = clock
	f.prov.SetOutput(out)
	f.runner.exits = map[string][]error{shipOrders: exits}
	f.keys(t)
	for _, step := range f.env.DeployOrder {
		if step.Step == ir.StepRollout {
			if err := f.prov.Apply(context.Background(), f.req, *step); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() { waited <- f.prov.Wait(ctx, f.req) }()
	var once sync.Once
	var waitErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case waitErr = <-waited:
			case <-time.After(5 * time.Second):
				t.Error("Wait did not return")
			}
		})
		return waitErr
	}
	t.Cleanup(func() { _ = stop() })
	return f, clock, out, stop
}

// TestJobRunsOnItsSchedule: the rollout builds the job, and Wait runs it
// each time its schedule comes due, with its environment and its output
// prefixed by its name. A failed run is run again once, its one retry,
// and a run's failure, even its last try's, never stops the environment.
func TestJobRunsOnItsSchedule(t *testing.T) {
	f, clock, out, stop := pinnedJobs(t, errors.New("exit status 1"), nil, errors.New("exit status 1"), errors.New("exit status 1"))
	waitFor(t, out, "job "+shipOrders+" runs on * * * * * (UTC)")

	clock.ticks <- clock.now
	waitFor(t, out, "job "+shipOrders+": the run due at 2026-10-07T12:01:00Z, try 2 of 2 succeeded")
	for _, line := range []string{
		"job " + shipOrders + ": start the run due at 2026-10-07T12:01:00Z, try 1 of 2",
		"job " + shipOrders + ": the run due at 2026-10-07T12:01:00Z, try 1 of 2 failed after 0s: exit status 1",
		"[" + shipOrders + "] listening on \n",
		"[" + shipOrders + "] a line with no end\n",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("the output lacks %q:\n%s", line, out)
		}
	}

	clock.ticks <- clock.now
	waitFor(t, out, "try 2 of 2 failed after 0s: exit status 1")

	var started []string
	var env []string
	for _, cmd := range f.runner.commands {
		if filepath.Base(cmd.Path) == shipOrders {
			started = append(started, cmd.Path)
			env = cmd.Env
		}
	}
	if len(started) != 4 {
		t.Errorf("the job ran %d times, want 4: two runs of two tries", len(started))
	}
	for name, want := range map[string]string{
		"FULFILLMENT_REGION":   "eu",
		"STRIPE_KEY":           "sk_test_123",
		"SHOP_DB_DATABASE_URL": local.DatabaseURL(f.pgPort, "shop_db"),
		"SHOP_API_SERVICE_URL": local.ServerURL(f.apiPort),
		"PORT":                 "",
	} {
		if got := envValue(env, name); got != want {
			t.Errorf("the job's %s = %q, want %q", name, got, want)
		}
	}
	if key := envValue(env, "SHOP_API_SERVICE_CREDENTIAL_KEY"); !strings.Contains(key, `"crv":"Ed25519"`) || !strings.Contains(key, `"d":`) {
		t.Errorf("the job signs its calls with no private key: %q", key)
	}
	if err := stop(); err != nil {
		t.Errorf("Wait = %v, want nil: a job's failure never stops the environment", err)
	}
}

// TestJobRunsNeverOverlap: a run that comes due while the previous one
// goes on is skipped, and when Wait returns it stops the run going on.
func TestJobRunsNeverOverlap(t *testing.T) {
	f, clock, out, stop := pinnedJobs(t)
	clock.ticks <- clock.now
	waitFor(t, out, "start the run due at")
	clock.ticks <- clock.now
	waitFor(t, out, "skipped the run due at 2026-10-07T12:01:00Z: the previous run goes on")
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	for _, p := range f.runner.started {
		if filepath.Base(p.cmd.Path) == shipOrders {
			stopped = p.stopped
		}
	}
	if !stopped {
		t.Errorf("Wait left the job's run going")
	}
	waitFor(t, out, "failed after 0s: stopped: context canceled")
}

// TestJobTimesOut: a run that outlives the job's timeout is stopped and
// fails, and its retry runs.
func TestJobTimesOut(t *testing.T) {
	_, clock, out, _ := pinnedJobs(t)
	clock.ticks <- clock.now
	waitFor(t, out, "start the run due at")
	clock.timeouts <- clock.now
	waitFor(t, out, "try 1 of 2 failed after 0s: timed out after 5m0s, the job's timeout")
	waitFor(t, out, "start the run due at 2026-10-07T12:01:00Z, try 2 of 2")
}

// TestRunJob: stack run's run of a job against a running environment
// builds the job and runs it once; it refuses an environment that does not
// answer, a job the environment lacks, and a job whose run from the
// schedule goes on.
func TestRunJob(t *testing.T) {
	f := newFixture(t, "Pinned")
	out := &lockedOutput{}
	f.prov.SetOutput(out)
	f.keys(t)
	running := fmt.Sprintf(`true|postgres:17-alpine|{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"%d"}]}`, f.pgPort)
	f.runner.rules = []rule{{prefix: inspectPrefix, out: running}}
	f.runner.exits = map[string][]error{shipOrders: {nil}}
	if err := f.prov.RunJob(context.Background(), f.req, shipOrders); err != nil {
		t.Fatalf("RunJob = %v\n%s", err, out)
	}
	for _, line := range []string{
		"build " + shipOrders + ": go build server/shop-stack/" + shipOrders,
		"job " + shipOrders + ": start the run stack run asked for, try 1 of 2",
		"job " + shipOrders + ": the run stack run asked for, try 1 of 2 succeeded",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("the output lacks %q:\n%s", line, out)
		}
	}

	if err := f.prov.RunJob(context.Background(), f.req, "shop-orders-expire-carts"); err == nil || !strings.Contains(err.Error(), "has no job shop-orders-expire-carts (its jobs: "+shipOrders+")") {
		t.Errorf("RunJob of a job the environment lacks = %v", err)
	}

	f.runner.rules = []rule{{prefix: inspectPrefix, out: strings.Replace(running, "true", "false", 1)}}
	if err := f.prov.RunJob(context.Background(), f.req, shipOrders); err == nil || !strings.Contains(err.Error(), "environment Pinned is not running: its container superschematic-shop-stack-pinned-postgres does not run; start it with superschematic stack dev") {
		t.Errorf("RunJob with the environment down = %v", err)
	}

	// A run from stack dev's schedule holds the job's lock.
	f.runner.rules = []rule{{prefix: inspectPrefix, out: running}}
	lock, err := os.OpenFile(filepath.Join(stateDir(f), local.JobsDir, shipOrders+".lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := flock(lock); err != nil {
		t.Skipf("no flock here: %v", err)
	}
	if err := f.prov.RunJob(context.Background(), f.req, shipOrders); err == nil || !strings.Contains(err.Error(), "a run of job "+shipOrders+" goes on, from stack dev's schedule") {
		t.Errorf("RunJob while a run goes on = %v", err)
	}
}

// stateDir is the fixture's environment's state directory.
func stateDir(f *fixture) string {
	return filepath.FromSlash(strings.TrimPrefix(f.req.Backend.URL, "file://"))
}
