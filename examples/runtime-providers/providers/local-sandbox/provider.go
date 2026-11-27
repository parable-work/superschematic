// Package localsandbox is local-sandbox in Go (D159, D184): a sandbox
// provider that runs each run's instance as a local process, a stub for
// tests and stack dev. It isolates nothing; a provider for a container
// platform or a VM service implements the same four operations. The
// command runs with the spec's env and secrets as its environment, and no
// secret is logged. Cancel kills the process; Cost is its wall time and
// the CPU time the platform reports once it exits.
package localsandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
)

// Definition is the provider a Go server links.
func Definition() providers.Definition {
	return providers.MustDefine(providers.Definition{
		Name:       "local-sandbox",
		Implements: providers.Sandbox,
		Config: map[string]providers.SettingSpec{
			"WORKDIR": {Optional: true, Description: "The directory each process starts in; the server's own when unset."},
		},
		Create: func(_ context.Context, settings providers.Settings, pc providers.Context) (any, error) {
			dir, _, err := settings.OptionalText("WORKDIR")
			if err != nil {
				return nil, err
			}
			return &sandbox{dir: dir, log: pc.Logger, instances: map[string]*instance{}}, nil
		},
	})
}

type instance struct {
	cmd       *exec.Cmd
	startedAt time.Time
	state     providers.SandboxState
	endedAt   time.Time
	exitCode  *int
	cpu       float64
	done      chan struct{}
}

type sandbox struct {
	dir       string
	log       *zap.Logger
	mu        sync.Mutex
	next      int
	instances map[string]*instance
}

func (s *sandbox) instanceOf(id string) (*instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[id]
	if !ok {
		return nil, fmt.Errorf("local-sandbox: no instance %s", id)
	}
	return in, nil
}

func (s *sandbox) Start(_ context.Context, spec providers.SandboxSpec) (providers.SandboxInstance, error) {
	if len(spec.Command) == 0 {
		return providers.SandboxInstance{}, errors.New("local-sandbox: a spec names the command to run")
	}
	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Dir = s.dir
	env := []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	names := make([]string, 0, len(spec.Secrets))
	for k, v := range spec.Secrets {
		env = append(env, k+"="+v.Reveal())
		names = append(names, k)
	}
	sort.Strings(names)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return providers.SandboxInstance{}, fmt.Errorf("local-sandbox: %w", err)
	}
	s.mu.Lock()
	s.next++
	id := fmt.Sprintf("local-%d-%s", s.next, spec.RunID)
	in := &instance{cmd: cmd, startedAt: time.Now(), state: providers.SandboxRunning, done: make(chan struct{})}
	s.instances[id] = in
	s.mu.Unlock()
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		defer s.mu.Unlock()
		in.endedAt = time.Now()
		if code := cmd.ProcessState.ExitCode(); code >= 0 {
			in.exitCode = &code
		}
		if in.state != providers.SandboxCancelled {
			if err == nil {
				in.state = providers.SandboxSucceeded
			} else {
				in.state = providers.SandboxFailed
			}
		}
		in.cpu = (cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()).Seconds()
		s.log.Info("sandbox instance ended", zap.String("id", id), zap.String("state", string(in.state)))
		close(in.done)
	}()
	s.log.Info("sandbox instance started", zap.String("id", id), zap.String("runId", spec.RunID), zap.Strings("secrets", names))
	return providers.SandboxInstance{ID: id, StartedAt: in.startedAt}, nil
}

func (s *sandbox) Cancel(_ context.Context, id string) error {
	in, err := s.instanceOf(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if !in.endedAt.IsZero() {
		s.mu.Unlock()
		return nil
	}
	in.state = providers.SandboxCancelled
	s.mu.Unlock()
	_ = in.cmd.Process.Kill()
	<-in.done
	return nil
}

func (s *sandbox) Status(_ context.Context, id string) (providers.SandboxStatus, error) {
	in, err := s.instanceOf(id)
	if err != nil {
		return providers.SandboxStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return providers.SandboxStatus{ID: id, State: in.state, ExitCode: in.exitCode}, nil
}

func (s *sandbox) Cost(_ context.Context, id string) (providers.SandboxCost, error) {
	in, err := s.instanceOf(id)
	if err != nil {
		return providers.SandboxCost{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	end := in.endedAt
	if end.IsZero() {
		end = time.Now()
	}
	return providers.SandboxCost{ID: id, WallSeconds: end.Sub(in.startedAt).Seconds(), CPUSeconds: in.cpu}, nil
}

// Settled waits for the instance id of a provider this package made to
// end, for tests.
func Settled(ctx context.Context, provider providers.SandboxProvider, id string, timeout time.Duration) error {
	until := time.Now().Add(timeout)
	for {
		status, err := provider.Status(ctx, id)
		if err != nil {
			return err
		}
		if status.State != providers.SandboxRunning && status.State != providers.SandboxStarting {
			return nil
		}
		if time.Now().After(until) {
			return fmt.Errorf("local-sandbox: %s still %s", id, status.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
