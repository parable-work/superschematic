package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// MigrateEnv names the environment variable that locates the migration
// runner, `superschematic-migrate` (runtime/migrate/README.md), when it is
// not on PATH as MigrateBinary.
const (
	MigrateEnv    = "SUPERSCHEMATIC_MIGRATE"
	MigrateBinary = "superschematic-migrate"
)

// inheritedVariables are the variables of the provisioner's own
// environment a server process inherits: what a program needs to run on
// the machine. Nothing else leaks in, so a variable a server reads is one
// its environment binds.
var inheritedVariables = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TMP", "TEMP",
	"LANG", "LC_ALL", "LC_CTYPE", "TZ", "SYSTEMROOT",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
}

// Provisioner is the local provisioner (docs/stack-model.md, section 8.3).
// It applies a local environment's graph to the machine, a step of the
// deploy order at a time:
//
//   - infrastructure: it starts the Postgres container, creating it when
//     it is missing and keeping its data when it is stopped, waits until
//     Postgres answers, and creates each database that is missing;
//   - migrate expand: it plans each hosted DB schema's migration from the
//     model its database recorded to the model the deploy wrote into
//     ModelsDir, and applies it with the migration runner, expand and
//     contract back to back, since no server of a previous version runs;
//     migrate contract has nothing left to run;
//   - rollout: it builds each Go server's entrypoint module with `go
//     build`, and installs the output root's Bun workspace once for the
//     TypeScript servers, whose main.ts Bun runs as it is (D51); it starts
//     each server with its environment (its literals, its secrets from the
//     environment's secrets file, its derived variables, and PORT), and
//     waits until it answers ReadinessPath.
//
// Each process's output reaches Out a line at a time, prefixed with its
// name. The processes run until Destroy, which stops them and the
// container, keeping the container's data; Purge removes the container and
// its data too.
//
// The zero value runs commands with os/exec and writes to os.Stdout. A
// Provisioner keeps the processes it started in memory, so the one that
// applied an environment is the one that destroys it.
type Provisioner struct {
	// Runner does what the provisioner does to the machine; nil is
	// os/exec.
	Runner Runner

	// Out takes the provisioner's progress and every process's output;
	// nil is os.Stdout.
	Out io.Writer

	// Migrate is the path of the migration runner; empty is MigrateEnv's
	// value, or MigrateBinary on PATH.
	Migrate string

	// ReadyTimeout bounds the wait for Postgres and for each server's
	// readiness; zero is a minute.
	ReadyTimeout time.Duration

	// StopTimeout is how long a server has to exit after SIGTERM before
	// it is killed; zero is ten seconds.
	StopTimeout time.Duration

	// PollInterval is the wait between two readiness probes; zero is
	// 200ms.
	PollInterval time.Duration

	mu      sync.Mutex
	console *console
	running map[string][]*runningServer

	// installed are the output roots whose Bun workspace this provisioner
	// installed: one `bun install` serves every TypeScript server of every
	// wave.
	installed map[string]bool
}

var _ registry.Provisioner = (*Provisioner)(nil)

// runningServer is a server process the provisioner started.
type runningServer struct {
	server *Server
	proc   Process
	output []*prefixWriter
}

// SetOutput replaces Out: where the provisioner registered with the target
// writes for the command that runs it.
func (p *Provisioner) SetOutput(w io.Writer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Out = w
	p.console = nil
}

func (p *Provisioner) runner() Runner {
	if p.Runner == nil {
		return execRunner{}
	}
	return p.Runner
}

func (p *Provisioner) out() *console {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.console == nil {
		w := p.Out
		if w == nil {
			w = os.Stdout
		}
		p.console = &console{out: w}
	}
	return p.console
}

func (p *Provisioner) printf(format string, args ...any) { p.out().printf(format, args...) }

func (p *Provisioner) readyTimeout() time.Duration {
	if p.ReadyTimeout > 0 {
		return p.ReadyTimeout
	}
	return time.Minute
}

func (p *Provisioner) stopTimeout() time.Duration {
	if p.StopTimeout > 0 {
		return p.StopTimeout
	}
	return 10 * time.Second
}

func (p *Provisioner) pollInterval() time.Duration {
	if p.PollInterval > 0 {
		return p.PollInterval
	}
	return 200 * time.Millisecond
}

// key names an environment among the ones the provisioner runs.
func key(env *ir.ResolvedEnvironment) string { return env.Stack + "/" + env.Environment }

// Render writes the environment's program, ProgramFile, into dir.
func (p *Provisioner) Render(env *ir.ResolvedEnvironment, dir string) error {
	prog, err := ProgramOf(env)
	if err != nil {
		return err
	}
	data, err := prog.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("local: render: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ProgramFile), data, 0o644); err != nil {
		return fmt.Errorf("local: render: %w", err)
	}
	return nil
}

// Plan returns what applying the environment would start or change: a
// container to create, start or replace, a database to create, a
// migration to apply, and each server to start, or to restart when this
// provisioner runs it already.
func (p *Provisioner) Plan(ctx context.Context, req registry.ProvisionRequest) ([]registry.PlannedChange, error) {
	prog, err := p.program(req)
	if err != nil {
		return nil, err
	}
	var changes []registry.PlannedChange
	running := map[string]bool{}
	if len(prog.Containers) > 0 {
		docker, err := p.lookPath("docker")
		if err != nil {
			return nil, err
		}
		for _, c := range prog.Containers {
			state, err := p.inspect(ctx, docker, c)
			if err != nil {
				return nil, err
			}
			switch {
			case !state.exists:
				changes = append(changes, registry.PlannedChange{Resource: c.ID, Action: "create"})
			case state.mismatch(c) != "":
				changes = append(changes, registry.PlannedChange{Resource: c.ID, Action: "replace"})
			case !state.running:
				changes = append(changes, registry.PlannedChange{Resource: c.ID, Action: "start"})
			default:
				running[c.ID] = p.postgresReady(ctx, docker, c)
			}
		}
		exists := map[string]bool{}
		for _, db := range prog.Databases {
			if running[db.Container] {
				c := prog.container(db.Container)
				if exists[db.ID], err = p.databaseExists(ctx, docker, c, db); err != nil {
					return nil, err
				}
			}
			if !exists[db.ID] {
				changes = append(changes, registry.PlannedChange{Resource: db.ID, Action: "create"})
			}
		}
		for _, m := range prog.Migrations {
			pending := true
			if exists[m.Resource] {
				if pending, err = p.migrationPending(ctx, req, m); err != nil {
					return nil, err
				}
			}
			if pending {
				changes = append(changes, registry.PlannedChange{Resource: m.Resource, Action: "migrate"})
			}
		}
	}
	if len(prog.KeyPairs) > 0 {
		dir, err := stateDirOf(req.Backend)
		if err != nil {
			return nil, fmt.Errorf("local: environment %s has key pairs, but %w", req.Environment.Environment, err)
		}
		for _, k := range prog.KeyPairs {
			if _, err := readKey(dir, k.ID); err != nil {
				changes = append(changes, registry.PlannedChange{Resource: k.ID, Action: "create"})
			}
		}
	}
	started := map[string]bool{}
	p.mu.Lock()
	for _, rs := range p.running[key(req.Environment)] {
		started[rs.server.ID] = true
	}
	p.mu.Unlock()
	for _, s := range prog.Servers {
		action := "start"
		if started[s.ID] {
			action = "restart"
		}
		changes = append(changes, registry.PlannedChange{Resource: s.ID, Action: action})
	}
	return changes, nil
}

// Apply applies one step of the environment's deploy order.
func (p *Provisioner) Apply(ctx context.Context, req registry.ProvisionRequest, step ir.DeployStep) error {
	prog, err := p.program(req)
	if err != nil {
		return err
	}
	if step.Step == ir.StepMigrate {
		if step.Migration == ir.MigrationContract {
			if len(step.Deployables) > 0 {
				p.printf("migrate contract: %s ran its contract steps with its expand steps", strings.Join(step.Deployables, ", "))
			}
			return nil
		}
		for _, m := range prog.Migrations {
			if slices.Contains(step.Deployables, m.Database) {
				if err := p.migrate(ctx, req, m); err != nil {
					return err
				}
			}
		}
		return nil
	}
	var containers []*Container
	var databases []*Database
	var keyPairs []*KeyPair
	var servers []*Server
	for _, id := range step.Resources {
		if c := prog.container(id); c != nil {
			containers = append(containers, c)
		} else if db := prog.database(id); db != nil {
			databases = append(databases, db)
		} else if k := prog.keyPair(id); k != nil {
			keyPairs = append(keyPairs, k)
		} else if s := prog.server(id); s != nil {
			servers = append(servers, s)
		} else if prog.job(id) != nil {
			// A job's entrypoint is not built yet, so stack dev runs none.
		} else {
			return fmt.Errorf("local: step %s applies %s, which is not in the program", step.Step, id)
		}
	}
	if len(keyPairs) > 0 {
		dir, err := stateDirOf(req.Backend)
		if err != nil {
			return fmt.Errorf("local: environment %s has key pairs to keep, but %w", req.Environment.Environment, err)
		}
		for _, k := range keyPairs {
			_, created, err := ensureKey(dir, k.ID, nil)
			if err != nil {
				return err
			}
			if created {
				p.printf("create key pair %s: %s signs its calls to %s with it", k.ID, k.Caller, k.Callee)
			}
		}
	}
	if len(containers) > 0 || len(databases) > 0 {
		docker, err := p.lookPath("docker")
		if err != nil {
			return err
		}
		ready := map[string]bool{}
		for _, c := range containers {
			if err := p.ensureContainer(ctx, docker, c); err != nil {
				return err
			}
		}
		for _, db := range databases {
			c := prog.container(db.Container)
			if !ready[c.ID] {
				if err := p.waitPostgres(ctx, docker, c); err != nil {
					return err
				}
				ready[c.ID] = true
			}
			if err := p.ensureDatabase(ctx, docker, c, db); err != nil {
				return err
			}
		}
	}
	if len(servers) > 0 {
		return p.startServers(ctx, req, prog, servers)
	}
	return nil
}

// Destroy stops the environment's servers, callers first, then its
// container, which keeps its data for the next run.
func (p *Provisioner) Destroy(ctx context.Context, req registry.ProvisionRequest) error {
	return p.destroy(ctx, req, false)
}

// Purge stops the environment's servers, and removes its container with
// its data.
func (p *Provisioner) Purge(ctx context.Context, req registry.ProvisionRequest) error {
	return p.destroy(ctx, req, true)
}

func (p *Provisioner) destroy(ctx context.Context, req registry.ProvisionRequest, purge bool) error {
	if req.Environment == nil {
		return errors.New("local: no environment to destroy")
	}
	prog, err := ProgramOf(req.Environment)
	if err != nil {
		return err
	}
	errs := []error{p.stopServers(key(req.Environment))}
	if len(prog.Containers) > 0 {
		docker, err := p.lookPath("docker")
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, c := range prog.Containers {
			state, err := p.inspect(ctx, docker, c)
			switch {
			case err != nil:
				errs = append(errs, err)
			case !state.exists, !purge && !state.running:
			case purge:
				if _, err := p.runner().Run(ctx, Command{Path: docker, Args: []string{"rm", "--force", "--volumes", c.Name}}); err != nil {
					errs = append(errs, fmt.Errorf("local: remove container %s: %w", c.Name, err))
				} else {
					p.printf("removed container %s and its data", c.Name)
				}
			default:
				if _, err := p.runner().Run(ctx, Command{Path: docker, Args: []string{"stop", c.Name}}); err != nil {
					errs = append(errs, fmt.Errorf("local: stop container %s: %w", c.Name, err))
				} else {
					p.printf("stopped container %s; its data stays for the next run", c.Name)
				}
			}
		}
	}
	return errors.Join(errs...)
}

// Outputs returns each node's outputs: a container's name, host and port,
// a database's name and URL, and a server's URL and port.
func (p *Provisioner) Outputs(_ context.Context, req registry.ProvisionRequest) (map[string]map[string]any, error) {
	if req.Environment == nil {
		return nil, errors.New("local: no environment")
	}
	prog, err := ProgramOf(req.Environment)
	if err != nil {
		return nil, err
	}
	out := prog.outputs()
	if dir, err := stateDirOf(req.Backend); err == nil {
		for _, k := range prog.KeyPairs {
			if key, err := readKey(dir, k.ID); err == nil {
				public, err := json.Marshal(key.Public())
				if err != nil {
					return nil, err
				}
				out[k.ID] = map[string]any{"kid": key.Kid, "publicJwk": string(public)}
			}
		}
	}
	return out, nil
}

func (prog *Program) outputs() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, c := range prog.Containers {
		out[c.ID] = map[string]any{"name": c.Name, "host": c.Host, "port": c.HostPort}
	}
	for _, db := range prog.Databases {
		out[db.ID] = map[string]any{"name": db.Name, "url": db.URL}
	}
	for _, s := range prog.Servers {
		out[s.ID] = map[string]any{"url": s.URL, "port": s.Port}
	}
	return out
}

// Wait blocks until ctx is done, and returns nil, or until a server of the
// environment exits, and returns an error that names it.
func (p *Provisioner) Wait(ctx context.Context, req registry.ProvisionRequest) error {
	if req.Environment == nil {
		return errors.New("local: no environment")
	}
	p.mu.Lock()
	servers := slices.Clone(p.running[key(req.Environment)])
	p.mu.Unlock()
	exited := make(chan *runningServer, len(servers))
	for _, rs := range servers {
		go func() {
			select {
			case <-rs.proc.Done():
				exited <- rs
			case <-ctx.Done():
			}
		}()
	}
	select {
	case <-ctx.Done():
		return nil
	case rs := <-exited:
		return fmt.Errorf("local: server %s exited: %v", rs.server.Deployable, exitReason(rs.proc.Err()))
	}
}

func exitReason(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// program reads the program Render wrote for the request's environment.
func (p *Provisioner) program(req registry.ProvisionRequest) (*Program, error) {
	if req.Environment == nil {
		return nil, errors.New("local: no environment")
	}
	for _, param := range req.Environment.Parameters {
		if _, ok := req.Parameters[param]; !ok {
			return nil, fmt.Errorf("local: environment %s needs a value for parameter %s", req.Environment.Environment, param)
		}
	}
	return readProgram(req.Environment, req.Dir)
}

func (p *Provisioner) lookPath(name string) (string, error) {
	path, err := p.runner().LookPath(name)
	if err != nil {
		switch name {
		case "docker":
			return "", fmt.Errorf("local: docker is not on PATH; the local target runs Postgres in a Docker container: %w", err)
		case "go":
			return "", fmt.Errorf("local: go is not on PATH; the local target builds each Go server with go build: %w", err)
		case "bun":
			return "", fmt.Errorf("local: bun is not on PATH; the local target runs each TypeScript server on Bun (https://bun.sh): %w", err)
		}
		return "", fmt.Errorf("local: %s is not on PATH: %w", name, err)
	}
	return path, nil
}

// noSuchContainer reports whether a docker command failed because the
// container does not exist.
func noSuchContainer(err error) bool {
	return stderrContains(err, "no such container") || stderrContains(err, "no such object")
}

// containerState is what `docker container inspect` says of a container.
type containerState struct {
	exists   bool
	running  bool
	image    string
	bindings map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}
}

// mismatch describes how the container differs from c, or is empty.
func (s containerState) mismatch(c *Container) string {
	var diffs []string
	if s.image != c.Image {
		diffs = append(diffs, fmt.Sprintf("it runs %s, not %s", s.image, c.Image))
	}
	published := false
	for _, b := range s.bindings[fmt.Sprintf("%d/tcp", c.ContainerPort)] {
		if b.HostPort == strconv.Itoa(c.HostPort) && (b.HostIP == c.Host || (c.Host == Loopback && b.HostIP == "")) {
			published = true
		}
	}
	if !published {
		diffs = append(diffs, fmt.Sprintf("it does not publish port %d on %s:%d", c.ContainerPort, c.Host, c.HostPort))
	}
	return strings.Join(diffs, ", and ")
}

// postgresReady reports whether Postgres in a running container answers.
func (p *Provisioner) postgresReady(ctx context.Context, docker string, c *Container) bool {
	_, err := p.runner().Run(ctx, pgIsReady(docker, c))
	return err == nil
}

const inspectFormat = `{{.State.Running}}|{{.Config.Image}}|{{json .HostConfig.PortBindings}}`

func (p *Provisioner) inspect(ctx context.Context, docker string, c *Container) (containerState, error) {
	out, err := p.runner().Run(ctx, Command{Path: docker, Args: []string{"container", "inspect", "--format", inspectFormat, c.Name}})
	if err != nil {
		if noSuchContainer(err) {
			return containerState{}, nil
		}
		return containerState{}, fmt.Errorf("local: inspect container %s: %w", c.Name, err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(parts) != 3 {
		return containerState{}, fmt.Errorf("local: inspect container %s: unexpected output %q", c.Name, out)
	}
	state := containerState{exists: true, running: parts[0] == "true", image: parts[1]}
	if parts[2] != "" && parts[2] != "null" {
		if err := json.Unmarshal([]byte(parts[2]), &state.bindings); err != nil {
			return containerState{}, fmt.Errorf("local: inspect container %s: port bindings: %w", c.Name, err)
		}
	}
	return state, nil
}

// ensureContainer creates the container when it is missing and starts it
// when it is stopped. It refuses one that runs another image or publishes
// another port, whose data it would otherwise have to throw away.
func (p *Provisioner) ensureContainer(ctx context.Context, docker string, c *Container) error {
	state, err := p.inspect(ctx, docker, c)
	if err != nil {
		return err
	}
	if !state.exists {
		args := []string{"run", "--detach", "--name", c.Name}
		labels := make([]string, 0, len(c.Labels))
		for k, v := range c.Labels {
			labels = append(labels, k+"="+v)
		}
		sort.Strings(labels)
		for _, label := range labels {
			args = append(args, "--label", label)
		}
		for _, v := range c.Env {
			args = append(args, "--env", v.Name+"="+v.Value.(string))
		}
		args = append(args, "--publish", fmt.Sprintf("%s:%d:%d", c.Host, c.HostPort, c.ContainerPort), c.Image)
		p.printf("create container %s from %s on %s:%d", c.Name, c.Image, c.Host, c.HostPort)
		if _, err := p.runner().Run(ctx, Command{Path: docker, Args: args}); err != nil {
			return fmt.Errorf("local: create container %s: %w", c.Name, err)
		}
		return nil
	}
	if diff := state.mismatch(c); diff != "" {
		return fmt.Errorf("local: container %s exists, but %s; remove it (docker rm --force --volumes %s, which deletes its data) and run again", c.Name, diff, c.Name)
	}
	if state.running {
		p.printf("container %s runs", c.Name)
		return nil
	}
	p.printf("start container %s", c.Name)
	if _, err := p.runner().Run(ctx, Command{Path: docker, Args: []string{"start", c.Name}}); err != nil {
		return fmt.Errorf("local: start container %s: %w", c.Name, err)
	}
	return nil
}

func pgIsReady(docker string, c *Container) Command {
	return Command{Path: docker, Args: []string{
		"exec", c.Name, "pg_isready", "--host", Loopback, "--port", strconv.Itoa(c.ContainerPort), "--username", PostgresUser, "--quiet",
	}}
}

// waitPostgres waits until Postgres in the container answers on TCP,
// which it does only once the image's first-run initialization is done,
// and until the port the container publishes takes connections, which
// Docker's port proxy may do a moment later.
func (p *Provisioner) waitPostgres(ctx context.Context, docker string, c *Container) error {
	deadline := time.Now().Add(p.readyTimeout())
	for {
		_, err := p.runner().Run(ctx, pgIsReady(docker, c))
		if err == nil {
			if p.runner().PortInUse(c.HostPort) {
				return nil
			}
			err = fmt.Errorf("nothing takes connections on %s:%d", c.Host, c.HostPort)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("local: Postgres in container %s is not ready after %s: %w", c.Name, p.readyTimeout(), err)
		}
		if err := sleep(ctx, p.pollInterval()); err != nil {
			return err
		}
	}
}

// databaseNamePattern is the shape of a database's name, checked again
// before it reaches a command.
var databaseNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func psql(docker string, c *Container, sql string) Command {
	return Command{Path: docker, Args: []string{
		"exec", c.Name, "psql", "--host", Loopback, "--port", strconv.Itoa(c.ContainerPort), "--username", PostgresUser,
		"--dbname", "postgres", "--tuples-only", "--no-align", "--command", sql,
	}}
}

func (p *Provisioner) databaseExists(ctx context.Context, docker string, c *Container, db *Database) (bool, error) {
	if !databaseNamePattern.MatchString(db.Name) {
		return false, fmt.Errorf("local: database name %q is not a lower-case identifier", db.Name)
	}
	out, err := p.runner().Run(ctx, psql(docker, c, "SELECT 1 FROM pg_database WHERE datname = '"+db.Name+"'"))
	if err != nil {
		return false, fmt.Errorf("local: look for database %s: %w", db.Name, err)
	}
	return strings.TrimSpace(string(out)) == "1", nil
}

// ensureDatabase creates the database when it is missing.
func (p *Provisioner) ensureDatabase(ctx context.Context, docker string, c *Container, db *Database) error {
	exists, err := p.databaseExists(ctx, docker, c, db)
	if err != nil {
		return err
	}
	if exists {
		p.printf("database %s exists", db.Name)
		return nil
	}
	p.printf("create database %s for %s", db.Name, db.Service)
	if _, err := p.runner().Run(ctx, Command{Path: docker, Args: []string{
		"exec", c.Name, "createdb", "--host", Loopback, "--port", strconv.Itoa(c.ContainerPort), "--username", PostgresUser, db.Name,
	}}); err != nil {
		return fmt.Errorf("local: create database %s: %w", db.Name, err)
	}
	return nil
}

// startServers builds each Go server's entrypoint module, and installs
// the output root's Bun workspace once for the TypeScript servers, then
// starts each server, and waits until every one is ready. A server this
// provisioner already runs is stopped first.
func (p *Provisioner) startServers(ctx context.Context, req registry.ProvisionRequest, prog *Program, servers []*Server) error {
	if req.OutputRoot == "" {
		return errors.New("local: the request names no output root, where the build wrote each server's entrypoint module")
	}
	secrets, err := p.secretsFor(req, servers)
	if err != nil {
		return err
	}
	tools := map[string]string{}
	for _, s := range servers {
		tool := "go"
		if s.Language == LanguageTypeScript {
			tool = "bun"
		}
		if tools[s.Language] == "" {
			if tools[s.Language], err = p.lookPath(tool); err != nil {
				return err
			}
		}
	}
	type built struct {
		server *Server
		cmd    Command
	}
	var builds []built
	outputs := prog.outputs()
	keys, err := p.keysFor(req, prog)
	if err != nil {
		return err
	}
	for id, key := range keys {
		private, err := json.Marshal(key)
		if err != nil {
			return err
		}
		public, err := json.Marshal(key.Public())
		if err != nil {
			return err
		}
		outputs[id] = map[string]any{"kid": key.Kid, "publicJwk": string(public), "privateJwk": string(private)}
	}
	for _, s := range servers {
		module := filepath.Join(req.OutputRoot, filepath.FromSlash(s.Module))
		if info, err := os.Stat(module); err != nil || !info.IsDir() {
			return fmt.Errorf("local: server %s: no entrypoint module at %s; the stack's build writes it (docs/stack-model.md, section 8.1)", s.Deployable, module)
		}
		env, err := serverEnv(s, secrets, req.Parameters, outputs)
		if err != nil {
			return fmt.Errorf("local: server %s: %w", s.Deployable, err)
		}
		if s.Language == LanguageTypeScript {
			if err := p.installWorkspace(ctx, req.OutputRoot, tools[s.Language]); err != nil {
				return err
			}
			builds = append(builds, built{server: s, cmd: Command{Path: tools[s.Language], Args: []string{TypeScriptEntrypoint}, Dir: module, Env: env}})
			continue
		}
		binary, err := filepath.Abs(filepath.Join(req.Dir, filepath.FromSlash(s.Binary)))
		if err != nil {
			return err
		}
		p.printf("build %s: go build %s", s.Deployable, s.Module)
		if _, err := p.runner().Run(ctx, Command{Path: tools[s.Language], Args: []string{"build", "-o", binary, "."}, Dir: module, Env: buildEnv()}); err != nil {
			return fmt.Errorf("local: build server %s: %w", s.Deployable, err)
		}
		builds = append(builds, built{server: s, cmd: Command{Path: binary, Dir: module, Env: env}})
	}
	k := key(req.Environment)
	for _, b := range builds {
		if err := p.stopServer(k, b.server.ID); err != nil {
			return err
		}
		if p.runner().PortInUse(b.server.Port) {
			return fmt.Errorf("local: server %s: port %d is in use, by another program or a server an earlier run left behind; stop it, or give %s another port with its port setting", b.server.Deployable, b.server.Port, b.server.Deployable)
		}
		c := p.out()
		stdout, stderr := c.writer(b.server.Deployable), c.writer(b.server.Deployable)
		cmd := b.cmd
		cmd.Stdout, cmd.Stderr = stdout, stderr
		proc, err := p.runner().Start(cmd)
		if err != nil {
			return fmt.Errorf("local: start server %s: %w", b.server.Deployable, err)
		}
		p.mu.Lock()
		if p.running == nil {
			p.running = map[string][]*runningServer{}
		}
		p.running[k] = append(p.running[k], &runningServer{server: b.server, proc: proc, output: []*prefixWriter{stdout, stderr}})
		p.mu.Unlock()
		p.printf("start %s on %s", b.server.Deployable, b.server.URL)
	}
	for _, b := range builds {
		if err := p.waitReady(ctx, k, b.server); err != nil {
			return err
		}
		p.printf("%s is ready at %s", b.server.Deployable, b.server.URL)
	}
	return nil
}

// TypeScriptEntrypoint is the file of a TypeScript server's entrypoint
// module that Bun runs (docs/stack-model.md, section 8.6).
const TypeScriptEntrypoint = "main.ts"

// installWorkspace installs the Bun workspace whose root is the output
// root, which links each TypeScript server to the generated packages and
// the implementations it imports, unless this provisioner installed it
// already. The build writes the root's package.json; the install writes
// the lockfile beside it, or brings the one there up to date, as an
// engineer's own bun install does, so the lockfile the project commits
// (D51, amended) follows the schemas through stack dev. It is not frozen:
// the generated CI's frozen install is what refuses a stale lockfile.
func (p *Provisioner) installWorkspace(ctx context.Context, outputRoot, bun string) error {
	p.mu.Lock()
	done := p.installed[outputRoot]
	p.mu.Unlock()
	if done {
		return nil
	}
	if _, err := os.Stat(filepath.Join(outputRoot, "package.json")); err != nil {
		return fmt.Errorf("local: no Bun workspace at %s, whose package.json the stack's build writes for its TypeScript servers (docs/stack-model.md, section 8.6): %w", outputRoot, err)
	}
	p.printf("install the TypeScript workspace: bun install in %s", outputRoot)
	if _, err := p.runner().Run(ctx, Command{Path: bun, Args: []string{"install"}, Dir: outputRoot, Env: os.Environ()}); err != nil {
		return fmt.Errorf("local: install the TypeScript workspace at %s: %w", outputRoot, err)
	}
	p.mu.Lock()
	if p.installed == nil {
		p.installed = map[string]bool{}
	}
	p.installed[outputRoot] = true
	p.mu.Unlock()
	return nil
}

// buildEnv is the environment `go build` runs a server module in. Each
// module stands alone, so a go.work above the output root, which does not
// list it, is off. The build writes no go.sum, so the module's own go.mod
// and go.sum may change: -mod=mod replaces any -mod in GOFLAGS. Only the
// server module's files change, never the implementation packages it
// requires.
func buildEnv() []string {
	var flags []string
	for _, flag := range strings.Fields(os.Getenv("GOFLAGS")) {
		if !strings.HasPrefix(flag, "-mod=") {
			flags = append(flags, flag)
		}
	}
	flags = append(flags, "-mod=mod")
	return append(os.Environ(), "GOWORK=off", "GOFLAGS="+strings.Join(flags, " "))
}

// waitReady probes a server's readiness path until it answers 200.
func (p *Provisioner) waitReady(ctx context.Context, k string, s *Server) error {
	rs := p.find(k, s.ID)
	if rs == nil {
		return fmt.Errorf("local: server %s is not running", s.Deployable)
	}
	url := s.URL + s.Readiness
	deadline := time.Now().Add(p.readyTimeout())
	for {
		select {
		case <-rs.proc.Done():
			return fmt.Errorf("local: server %s exited before it was ready: %s", s.Deployable, exitReason(rs.proc.Err()))
		default:
		}
		code, err := p.runner().Get(ctx, url)
		if err == nil && code == 200 {
			return nil
		}
		if time.Now().After(deadline) {
			reason := fmt.Sprintf("status %d", code)
			if err != nil {
				reason = err.Error()
			}
			return fmt.Errorf("local: server %s is not ready at %s after %s: %s", s.Deployable, url, p.readyTimeout(), reason)
		}
		select {
		case <-rs.proc.Done():
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.pollInterval()):
		}
	}
}

func (p *Provisioner) find(k, id string) *runningServer {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, rs := range p.running[k] {
		if rs.server.ID == id {
			return rs
		}
	}
	return nil
}

// stopServer stops the server this provisioner runs as id, if it runs one.
func (p *Provisioner) stopServer(k, id string) error {
	p.mu.Lock()
	var rs *runningServer
	list := p.running[k]
	for i, candidate := range list {
		if candidate.server.ID == id {
			rs = candidate
			p.running[k] = slices.Delete(slices.Clone(list), i, i+1)
			break
		}
	}
	p.mu.Unlock()
	if rs == nil {
		return nil
	}
	return p.stop(rs)
}

// stopServers stops every server of an environment, the last started
// first, so callers stop before their callees.
func (p *Provisioner) stopServers(k string) error {
	p.mu.Lock()
	list := p.running[k]
	delete(p.running, k)
	p.mu.Unlock()
	var errs []error
	for i := len(list) - 1; i >= 0; i-- {
		errs = append(errs, p.stop(list[i]))
	}
	return errors.Join(errs...)
}

func (p *Provisioner) stop(rs *runningServer) error {
	select {
	case <-rs.proc.Done():
	default:
		p.printf("stop %s", rs.server.Deployable)
	}
	err := rs.proc.Stop(p.stopTimeout())
	for _, w := range rs.output {
		w.Flush()
	}
	if err != nil {
		return fmt.Errorf("local: stop server %s: %w", rs.server.Deployable, err)
	}
	return nil
}

// keysFor reads every key pair of the environment from its state
// directory, where applying its infrastructure generated them.
func (p *Provisioner) keysFor(req registry.ProvisionRequest, prog *Program) (map[string]JWK, error) {
	if len(prog.KeyPairs) == 0 {
		return nil, nil
	}
	dir, err := stateDirOf(req.Backend)
	if err != nil {
		return nil, fmt.Errorf("local: environment %s has key pairs, but %w", req.Environment.Environment, err)
	}
	keys := map[string]JWK{}
	for _, k := range prog.KeyPairs {
		key, err := readKey(dir, k.ID)
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("local: no key pair %s in %s; apply the environment's infrastructure first", k.ID, filepath.Join(dir, KeysDir))
		}
		if err != nil {
			return nil, err
		}
		keys[k.ID] = key
	}
	return keys, nil
}

// secretsFor reads the environment's secrets file when a server reads a
// secret, and refuses a secret that has no value.
func (p *Provisioner) secretsFor(req registry.ProvisionRequest, servers []*Server) (map[string]string, error) {
	var ids []string
	for _, s := range servers {
		for _, v := range s.Env {
			if v.Secret != "" && !slices.Contains(ids, v.Secret) {
				ids = append(ids, v.Secret)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	dir, err := stateDirOf(req.Backend)
	if err != nil {
		return nil, fmt.Errorf("local: environment %s reads secrets, but %w", req.Environment.Environment, err)
	}
	path := filepath.Join(dir, SecretsFile)
	secrets, err := ReadSecrets(path)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, id := range ids {
		if _, ok := secrets[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("local: environment %s has no value for secret %s: set each in %s, a line <Type>.<FIELD>=<value> per secret (%s=<value>)",
			req.Environment.Environment, strings.Join(missing, ", "), path, missing[0])
	}
	return secrets, nil
}

// serverEnv is a server process's whole environment: the variables it
// inherits, each of its env entries, and PORT.
func serverEnv(s *Server, secrets, params map[string]string, outputs map[string]map[string]any) ([]string, error) {
	var env []string
	for _, name := range inheritedVariables {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	for _, v := range s.Env {
		if v.Secret != "" {
			env = append(env, v.Name+"="+secrets[v.Secret])
			continue
		}
		value, err := resolveValue(v.Value, params, outputs)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", v.Name, err)
		}
		env = append(env, v.Name+"="+value)
	}
	return append(env, PortVariable+"="+strconv.Itoa(s.Port)), nil
}

// resolveValue renders a value as an environment variable's text: a string
// as it is, a number and a boolean as JSON writes them, a parameter as the
// run's value, an output as the program's, and a concatenation joined.
func resolveValue(v any, params map[string]string, outputs map[string]map[string]any) (string, error) {
	switch v := v.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(v), nil
	case ir.Parameter:
		value, ok := params[string(v)]
		if !ok {
			return "", fmt.Errorf("the run gives no value for parameter %s", v)
		}
		return value, nil
	case ir.Output:
		value, ok := outputs[v.Resource][v.Name]
		if !ok {
			return "", fmt.Errorf("resource %s has no output %s", v.Resource, v.Name)
		}
		return resolveValue(value, params, outputs)
	case ir.Concat:
		var b strings.Builder
		for _, part := range v {
			s, err := resolveValue(part, params, outputs)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		return b.String(), nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
