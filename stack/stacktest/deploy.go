package stacktest

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The fake target's deploy seams (docs/stack-model.md, sections 7.3 and
// 11): a state store, a secret store, a migration runner, an image builder
// and a bootstrap that keep everything in memory and record each call in the
// provisioner's call log, so a test reads the order a deploy ran in.

// FakeState is a state store in memory.
type FakeState struct {
	mu        sync.Mutex
	manifests map[string][]byte
}

var _ registry.StateStore = (*FakeState)(nil)

// Backend is a fake backend named after the environment's stack.
func (s *FakeState) Backend(_ context.Context, env *ir.ResolvedEnvironment) (registry.StateBackend, error) {
	return registry.StateBackend{URL: "fake://" + env.Stack, SecretsProvider: "fake"}, nil
}

// ReadManifest returns the run's manifest.
func (s *FakeState) ReadManifest(_ context.Context, run registry.Run) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.manifests[run.Name()]
	if !ok {
		return nil, fmt.Errorf("run %s: %w", run.Name(), registry.ErrNoManifest)
	}
	return slices.Clone(data), nil
}

// WriteManifest keeps the run's manifest.
func (s *FakeState) WriteManifest(_ context.Context, run registry.Run, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.manifests == nil {
		s.manifests = map[string][]byte{}
	}
	s.manifests[run.Name()] = slices.Clone(data)
	return nil
}

// DeleteManifest drops the run's manifest.
func (s *FakeState) DeleteManifest(_ context.Context, run registry.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.manifests, run.Name())
	return nil
}

// FakeSecrets is a secret store in memory, keyed by the stack and the
// secret's ID.
type FakeSecrets struct {
	mu     sync.Mutex
	values map[string][]byte

	// Uncreated lists IDs whose storage does not exist: Set refuses them
	// with registry.ErrSecretNotCreated.
	Uncreated []string
}

var _ registry.SecretStore = (*FakeSecrets)(nil)

func secretKey(env *ir.ResolvedEnvironment, id string) string { return env.Stack + "/" + id }

// Exists reports whether the secret has a value.
func (s *FakeSecrets) Exists(_ context.Context, env *ir.ResolvedEnvironment, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.values[secretKey(env, id)]
	return ok, nil
}

// List returns the IDs of the stack's secrets that have a value.
func (s *FakeSecrets) List(_ context.Context, env *ir.ResolvedEnvironment) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for key := range s.values {
		if id, ok := strings.CutPrefix(key, env.Stack+"/"); ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// Set stores a value.
func (s *FakeSecrets) Set(_ context.Context, env *ir.ResolvedEnvironment, id string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.Contains(s.Uncreated, id) {
		return fmt.Errorf("secret %s: %w", id, registry.ErrSecretNotCreated)
	}
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[secretKey(env, id)] = slices.Clone(value)
	return nil
}

// Get returns a value.
func (s *FakeSecrets) Get(_ context.Context, env *ir.ResolvedEnvironment, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[secretKey(env, id)]
	if !ok {
		return nil, fmt.Errorf("secret %s has no value", id)
	}
	return slices.Clone(value), nil
}

// FakeMigrations is a migration runner that runs nothing: it records each
// phase it is asked to run in the provisioner's call log.
type FakeMigrations struct {
	log *FakeProvisioner

	// Fail holds the error to return for a phase on a database, keyed
	// `<phase> <database>` (`contract shop-db`).
	Fail map[string]error
}

var _ registry.MigrationRunner = (*FakeMigrations)(nil)

// Migrate records the phase and the plans' services, and the servers of
// a plan with no steps in the phase.
func (m *FakeMigrations) Migrate(_ context.Context, req registry.MigrationRequest) error {
	var services []string
	for _, plan := range req.Plans {
		if plan.Steps == 0 {
			// Handed only for the servers that connect, which changed.
			services = append(services, plan.Service+" (no steps; servers "+strings.Join(plan.Servers, ", ")+")")
			continue
		}
		services = append(services, plan.Service)
	}
	if m.log != nil {
		m.log.Record("migrate %s %s: %s", req.Phase, req.Database, strings.Join(services, ", "))
	}
	return m.Fail[string(req.Phase)+" "+req.Database]
}

// FakeBuilder is an image builder that builds nothing: it records each
// build in the provisioner's call log, keeps the entries of each context
// it was handed, and returns the image the fake platform's repository
// names (the server in kebab case) at the context's digest.
type FakeBuilder struct {
	log *FakeProvisioner

	mu       sync.Mutex
	contexts map[string][]string

	// Fail holds the error to return for a server's build.
	Fail map[string]error
}

var _ registry.ImageBuilder = (*FakeBuilder)(nil)

// Build records the build and returns `<server in kebab case>@<context
// digest>`.
func (b *FakeBuilder) Build(_ context.Context, req registry.BuildRequest) (string, error) {
	if err := req.Check(); err != nil {
		return "", err
	}
	if b.log != nil {
		b.log.Record("build %s: %s", req.Server, req.Dockerfile)
	}
	if err := b.Fail[req.Server]; err != nil {
		return "", err
	}
	entries, err := archiveEntries(req.Context)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	if b.contexts == nil {
		b.contexts = map[string][]string{}
	}
	b.contexts[req.Server] = entries
	b.mu.Unlock()
	return kebab(req.Server) + "@" + req.ContextDigest, nil
}

// Context returns the entries of the context the last build of server was
// handed, in archive order: a directory's name ends in a slash.
func (b *FakeBuilder) Context(server string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.contexts[server])
}

// archiveEntries lists a gzipped tarball's entries.
func archiveEntries(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	var out []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, hdr.Name)
	}
}

// FakeBootstrap is a bootstrap that creates nothing: it records the
// request in the provisioner's call log.
type FakeBootstrap struct {
	log *FakeProvisioner
}

var _ registry.Bootstrapper = (*FakeBootstrap)(nil)

// Bootstrap records the environment, the repository and the credentials'
// secrets.
func (b *FakeBootstrap) Bootstrap(_ context.Context, req registry.BootstrapRequest) error {
	var secrets []string
	for _, c := range req.Credentials {
		secrets = append(secrets, c.Secret)
	}
	if b.log != nil {
		b.log.Record("bootstrap %s: repository %s, credentials %s", req.Environment.Environment, req.Repository, strings.Join(secrets, ", "))
	}
	return nil
}
