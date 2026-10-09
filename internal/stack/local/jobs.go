package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
)

// The jobs of a local environment (docs/stack-model.md, section 8.7, D52).
// A rollout step builds each job's entrypoint module with go build, as it
// builds a server's. While stack dev waits on the environment, each job
// with a schedule runs on it, in its time zone: never two runs of one job
// at once, each line of a run's output with the job's name in front, a run
// stopped at the job's timeout and run again up to its retries when it
// fails. A run's end, success or failure, is logged and never stops the
// environment. RunJob runs one job once, for stack run, against the
// environment stack dev runs.
//
// A lock file per job in the environment's state directory keeps a run
// stack run starts and one stack dev's schedule starts apart: the schedule
// skips its run while stack run's goes on, and stack run refuses to start
// while the schedule's does.

// JobsDir is the directory, in a local environment's state directory, that
// holds each job's lock file.
const JobsDir = "jobs"

// builtJob is a job a rollout built: its binary, its module directory and
// its whole environment.
type builtJob struct {
	job    *Job
	binary string
	module string
	env    []string
}

// Clock is the time a schedule reads; the provisioner takes the system's
// unless one is set, and its tests take a fake.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (p *Provisioner) clock() Clock {
	if p.Clock == nil {
		return systemClock{}
	}
	return p.Clock
}

// jobEnviron resolves a job's env: the variables it inherits and each of
// its entries, with the environment's secrets, the run's parameters and
// the outputs, key pairs included.
func (p *Provisioner) jobEnviron(req registry.ProvisionRequest, prog *Program, j *Job) ([]string, error) {
	secrets, err := p.secretsFor(req, [][]EnvVar{j.Env})
	if err != nil {
		return nil, err
	}
	outputs, err := p.runOutputs(req, prog)
	if err != nil {
		return nil, err
	}
	env, err := processEnviron(j.Env, secrets, req.Parameters, outputs)
	if err != nil {
		return nil, fmt.Errorf("local: job %s: %w", j.Deployable, err)
	}
	return env, nil
}

// buildJob builds a job's entrypoint module into the program's bin
// directory and returns it with its environment.
func (p *Provisioner) buildJob(ctx context.Context, req registry.ProvisionRequest, prog *Program, j *Job) (*builtJob, error) {
	if req.OutputRoot == "" {
		return nil, errors.New("local: the request names no output root, where the build wrote each job's entrypoint module")
	}
	module := filepath.Join(req.OutputRoot, filepath.FromSlash(j.Module))
	if info, err := os.Stat(module); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("local: job %s: no entrypoint module at %s; the stack's build writes it (docs/stack-model.md, section 8.7)", j.Deployable, module)
	}
	binary, err := filepath.Abs(filepath.Join(req.Dir, filepath.FromSlash(j.Binary)))
	if err != nil {
		return nil, err
	}
	env, err := p.jobEnviron(req, prog, j)
	if err != nil {
		return nil, err
	}
	goTool, err := p.lookPath("go")
	if err != nil {
		return nil, err
	}
	p.printf("build %s: go build %s", j.Deployable, j.Module)
	if _, err := p.runner().Run(ctx, Command{Path: goTool, Args: []string{"build", "-o", binary, "."}, Dir: module, Env: buildEnv()}); err != nil {
		return nil, fmt.Errorf("local: build job %s: %w", j.Deployable, err)
	}
	return &builtJob{job: j, binary: binary, module: module, env: env}, nil
}

// buildJobs builds each job a rollout step rolls out, and keeps them for
// Wait to run on their schedules. A job's schedule is printed, or that it
// runs only on demand.
func (p *Provisioner) buildJobs(ctx context.Context, req registry.ProvisionRequest, prog *Program, jobs []*Job) error {
	k := key(req.Environment)
	for _, j := range jobs {
		b, err := p.buildJob(ctx, req, prog, j)
		if err != nil {
			return err
		}
		p.mu.Lock()
		if p.jobs == nil {
			p.jobs = map[string][]*builtJob{}
		}
		p.jobs[k] = append(slices.DeleteFunc(p.jobs[k], func(other *builtJob) bool { return other.job.ID == j.ID }), b)
		p.mu.Unlock()
		if j.Schedule == "" {
			p.printf("job %s runs only on demand: superschematic stack run %s %s", j.Deployable, req.Environment.Environment, j.Deployable)
			continue
		}
		p.printf("job %s runs on %s (%s)", j.Deployable, j.Schedule, j.TimeZone)
	}
	return nil
}

// scheduleJobs runs each built job of the environment with a schedule on
// it until ctx ends, then stops the runs going and returns.
func (p *Provisioner) scheduleJobs(ctx context.Context, req registry.ProvisionRequest) {
	p.mu.Lock()
	jobs := slices.Clone(p.jobs[key(req.Environment)])
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, b := range jobs {
		if b.job.Schedule == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.schedule(ctx, req, b)
		}()
	}
	wg.Wait()
}

// schedule runs one job on its schedule until ctx ends. A run that comes
// due while the job's previous one goes on, from the schedule or from
// stack run, is skipped. When ctx ends, the run going on is stopped.
func (p *Provisioner) schedule(ctx context.Context, req registry.ProvisionRequest, b *builtJob) {
	j := b.job
	loc, err := time.LoadLocation(j.TimeZone)
	if err == nil {
		var sched interface{ Next(time.Time) time.Time }
		if sched, err = registry.ParseSchedule(j.Schedule, loc); err == nil {
			p.runSchedule(ctx, req, b, sched.Next)
			return
		}
	}
	p.printf("job %s: no schedule runs: %v", j.Deployable, err)
}

func (p *Provisioner) runSchedule(ctx context.Context, req registry.ProvisionRequest, b *builtJob, next func(time.Time) time.Time) {
	j := b.job
	clock := p.clock()
	var running chan struct{}
	runCtx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		if running != nil {
			<-running
		}
	}()
	for {
		now := clock.Now()
		due := next(now)
		select {
		case <-ctx.Done():
			return
		case <-clock.After(due.Sub(now)):
		}
		if running != nil {
			select {
			case <-running:
				running = nil
			default:
				p.printf("job %s: skipped the run due at %s: the previous run goes on", j.Deployable, due.Format(time.RFC3339))
				continue
			}
		}
		lock, held, err := p.lockJob(req, j)
		if err != nil {
			p.printf("job %s: skipped the run due at %s: %v", j.Deployable, due.Format(time.RFC3339), err)
			continue
		}
		if held {
			p.printf("job %s: skipped the run due at %s: a run of it goes on, from stack run", j.Deployable, due.Format(time.RFC3339))
			continue
		}
		done := make(chan struct{})
		running = done
		go func() {
			defer close(done)
			defer unlock(lock)
			_ = p.runJob(runCtx, b, fmt.Sprintf("the run due at %s", due.Format(time.RFC3339)))
		}()
	}
}

// lockJob takes the job's lock file in the environment's state directory.
// held reports that another process holds it; then lock is nil. With no
// state directory there is nothing to lock, and lock is nil too.
func (p *Provisioner) lockJob(req registry.ProvisionRequest, j *Job) (lock *os.File, held bool, err error) {
	dir, err := stateDirOf(req.Backend)
	if err != nil {
		return nil, false, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, JobsDir), 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(dir, JobsDir, j.Deployable+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	ok, err := tryLock(f)
	if err != nil || !ok {
		_ = f.Close()
		return nil, !ok && err == nil, err
	}
	return f, false, nil
}

func unlock(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

// runJob runs a built job until it succeeds or its tries run out: one,
// and one more per retry. A try that outlives the job's timeout is
// stopped, SIGTERM first, and fails. Each line of a try's output is
// printed with the job's name in front, and each try's end is printed. It
// returns the last try's error, or nil when one succeeded. When ctx ends,
// the try going on is stopped and no other starts.
func (p *Provisioner) runJob(ctx context.Context, b *builtJob, what string) error {
	j := b.job
	tries := j.Retries + 1
	var err error
	for try := 1; try <= tries; try++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		label := what
		if tries > 1 {
			label = fmt.Sprintf("%s, try %d of %d", what, try, tries)
		}
		p.printf("job %s: start %s", j.Deployable, label)
		started := p.clock().Now()
		err = p.runOnce(ctx, b)
		took := p.clock().Now().Sub(started).Round(time.Millisecond)
		if err == nil {
			p.printf("job %s: %s succeeded in %s", j.Deployable, label, took)
			return nil
		}
		p.printf("job %s: %s failed after %s: %v", j.Deployable, label, took, err)
	}
	return err
}

// runOnce runs the job's binary once with its environment.
func (p *Provisioner) runOnce(ctx context.Context, b *builtJob) error {
	j := b.job
	c := p.out()
	stdout, stderr := c.writer(j.Deployable), c.writer(j.Deployable)
	defer stdout.Flush()
	defer stderr.Flush()
	proc, err := p.runner().Start(Command{Path: b.binary, Dir: b.module, Env: b.env, Stdout: stdout, Stderr: stderr})
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	timeout := time.Duration(j.TimeoutSeconds) * time.Second
	select {
	case <-proc.Done():
		return proc.Err()
	case <-ctx.Done():
		if err := proc.Stop(p.stopTimeout()); err != nil {
			return err
		}
		return fmt.Errorf("stopped: %w", ctx.Err())
	case <-p.clock().After(timeout):
		if err := proc.Stop(p.stopTimeout()); err != nil {
			return err
		}
		return fmt.Errorf("timed out after %s, the job's timeout", timeout)
	}
}

// RunJob runs a job of the environment once, as `superschematic stack run`
// does locally (D52): against the environment stack dev runs, whose
// program, keys and secrets it reads. It refuses an environment whose
// Postgres container or servers do not answer, since the job would reach
// nothing, and a job whose run from stack dev's schedule goes on. It
// builds the job's entrypoint module, as the rollout did, runs it with its
// timeout and retries, and returns its error.
func (p *Provisioner) RunJob(ctx context.Context, req registry.ProvisionRequest, job string) error {
	prog, err := p.program(req)
	if err != nil {
		return fmt.Errorf("%w (a job runs on demand against the program stack dev rendered from the last build: start the environment with superschematic stack dev)", err)
	}
	j := prog.JobOf(job)
	if j == nil {
		var names []string
		for _, other := range prog.Jobs {
			names = append(names, other.Deployable)
		}
		if len(names) == 0 {
			return fmt.Errorf("local: environment %s has no job %s: it has none", req.Environment.Environment, job)
		}
		return fmt.Errorf("local: environment %s has no job %s (its jobs: %s)", req.Environment.Environment, job, strings.Join(names, ", "))
	}
	if err := p.checkRunning(ctx, prog); err != nil {
		return fmt.Errorf("local: environment %s is not running: %w; start it with superschematic stack dev, then run the job from another terminal", req.Environment.Environment, err)
	}
	lock, held, err := p.lockJob(req, j)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("local: a run of job %s goes on, from stack dev's schedule; run it again once that run ends", j.Deployable)
	}
	defer unlock(lock)
	b, err := p.buildJob(ctx, req, prog, j)
	if err != nil {
		return err
	}
	if err := p.runJob(ctx, b, "the run stack run asked for"); err != nil {
		return fmt.Errorf("local: job %s failed: %w", j.Deployable, err)
	}
	return nil
}

// checkRunning reports what of the program does not answer: a Postgres
// container that is not running or not ready, or a server that does not
// answer its readiness path. A worker has no port to ask (D53), and a job
// does not need one, so it is not asked.
func (p *Provisioner) checkRunning(ctx context.Context, prog *Program) error {
	if len(prog.Containers) > 0 {
		docker, err := p.lookPath("docker")
		if err != nil {
			return err
		}
		for _, c := range prog.Containers {
			state, err := p.inspect(ctx, docker, c)
			if err != nil {
				return err
			}
			if !state.running || !p.postgresReady(ctx, docker, c) {
				return fmt.Errorf("its Postgres container %s does not run", c.Name)
			}
		}
	}
	for _, s := range prog.Servers {
		if s.URL == "" {
			continue
		}
		if code, err := p.runner().Get(ctx, s.URL+s.Readiness); err != nil || code != 200 {
			return fmt.Errorf("server %s does not answer %s", s.Deployable, s.URL+s.Readiness)
		}
	}
	return nil
}
