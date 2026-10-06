package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner is what the provisioner does to the machine: it runs commands to
// completion, starts processes, probes a URL and checks a port. Provisioner
// takes the exec runner unless one is set, and its tests take a fake.
type Runner interface {
	// LookPath finds an executable on PATH, as exec.LookPath does.
	LookPath(name string) (string, error)

	// Run runs cmd to completion and returns its standard output. A
	// command that fails returns a *RunError.
	Run(ctx context.Context, cmd Command) ([]byte, error)

	// Start starts cmd and returns once it runs. Its output goes to
	// cmd.Stdout and cmd.Stderr.
	Start(cmd Command) (Process, error)

	// Get requests url and returns the response's status code.
	Get(ctx context.Context, url string) (int, error)

	// PortInUse reports whether something listens on port on loopback.
	PortInUse(port int) bool
}

// Command is one command a Runner runs.
type Command struct {
	// Path is the executable; Args its arguments, without the executable.
	Path string
	Args []string

	// Dir is the working directory; empty is the provisioner's.
	Dir string

	// Env is the whole environment; nil inherits the provisioner's.
	Env []string

	// Stdout and Stderr take a started process's output.
	Stdout io.Writer
	Stderr io.Writer
}

// String is the command line, for messages.
func (c Command) String() string {
	return strings.Join(append([]string{c.Path}, c.Args...), " ")
}

// RunError is a command that failed: how, and what it wrote to standard
// error.
type RunError struct {
	Command string
	Err     error
	Stderr  string
}

func (e *RunError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("%s: %v", e.Command, e.Err)
	}
	return fmt.Sprintf("%s: %v: %s", e.Command, e.Err, e.Stderr)
}

func (e *RunError) Unwrap() error { return e.Err }

// stderrContains reports whether err is a *RunError whose standard error
// contains s, in any case.
func stderrContains(err error, s string) bool {
	var runErr *RunError
	return errors.As(err, &runErr) && strings.Contains(strings.ToLower(runErr.Stderr), strings.ToLower(s))
}

// Process is a process a Runner started.
type Process interface {
	// Done is closed once the process has exited.
	Done() <-chan struct{}

	// Err is how the process exited, once Done is closed.
	Err() error

	// Stop asks the process and its children to stop (SIGTERM to its
	// process group), waits up to timeout, then kills them, and returns
	// once the process has exited.
	Stop(timeout time.Duration) error
}

// execRunner runs commands with os/exec.
type execRunner struct{}

func (execRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (execRunner) Run(ctx context.Context, c Command) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if c.Stdout != nil {
		cmd.Stdout = io.MultiWriter(&stdout, c.Stdout)
	}
	if c.Stderr != nil {
		cmd.Stderr = io.MultiWriter(&stderr, c.Stderr)
	}
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &RunError{Command: c.String(), Err: err, Stderr: strings.TrimSpace(stderr.String())}
	}
	return stdout.Bytes(), nil
}

func (execRunner) Start(c Command) (Process, error) {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	// A child that keeps the output pipes open after the process exits
	// does not hold Wait up for long.
	cmd.WaitDelay = 2 * time.Second
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, &RunError{Command: c.String(), Err: err}
	}
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (execRunner) Get(ctx context.Context, url string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// PortInUse dials the port on loopback, which a listener on loopback or on
// every address answers, and failing that tries to listen there. It never
// listens on every address, which a desktop firewall may ask about.
func (execRunner) PortInUse(port int) bool {
	addr := net.JoinHostPort(Loopback, strconv.Itoa(port))
	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		return true
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	_ = l.Close()
	return false
}

// execProcess is a process execRunner started.
type execProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *execProcess) Done() <-chan struct{} { return p.done }

func (p *execProcess) Err() error {
	<-p.done
	return p.err
}

func (p *execProcess) Stop(timeout time.Duration) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := terminate(p.cmd); err != nil {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(timeout):
	}
	if err := kill(p.cmd); err != nil {
		return err
	}
	<-p.done
	return nil
}
