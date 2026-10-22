package stacktest

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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

// FakeState is a state store in memory. It keeps each run's deploy lock
// beside its manifest (D46, amended), as a target's store does where it
// can take one atomically.
type FakeState struct {
	mu        sync.Mutex
	manifests map[string][]byte
	locks     map[string]registry.DeployLock

	// LockFail, when set, is the error Lock returns; UnlockFail the error
	// Unlock returns, with the lock left as it is.
	LockFail   error
	UnlockFail error

	// Locked counts the locks taken, and Unlocked the locks released.
	Locked, Unlocked int
}

var (
	_ registry.StateStore  = (*FakeState)(nil)
	_ registry.StateLocker = (*FakeState)(nil)
)

// Lock takes the run's lock, or refuses with the lock another holds.
func (s *FakeState) Lock(_ context.Context, run registry.Run, lock registry.DeployLock) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LockFail != nil {
		return s.LockFail
	}
	if held, ok := s.locks[run.Name()]; ok {
		return &registry.LockHeldError{Lock: held}
	}
	if s.locks == nil {
		s.locks = map[string]registry.DeployLock{}
	}
	s.locks[run.Name()] = lock
	s.Locked++
	return nil
}

// ReadLock returns the run's lock, or nil.
func (s *FakeState) ReadLock(_ context.Context, run registry.Run) (*registry.DeployLock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.locks[run.Name()]
	if !ok {
		return nil, nil
	}
	return &held, nil
}

// Unlock removes the run's lock when it holds id.
func (s *FakeState) Unlock(_ context.Context, run registry.Run, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.UnlockFail != nil {
		return s.UnlockFail
	}
	held, ok := s.locks[run.Name()]
	switch {
	case !ok:
		return nil
	case held.ID != id:
		return &registry.LockHeldError{Lock: held}
	}
	delete(s.locks, run.Name())
	s.Unlocked++
	return nil
}

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

	// Prepared names each run a deploy readied the runner for before its
	// first step, and PrepareFail is the error to return when it does.
	Prepared    []string
	PrepareFail error

	// ReadOutputs makes each phase read the applied graph's outputs
	// through the request, as a runner that reaches a database by its
	// node's output does (D64), and fail when the deploy gives none.
	ReadOutputs bool
}

var (
	_ registry.MigrationRunner   = (*FakeMigrations)(nil)
	_ registry.MigrationPreparer = (*FakeMigrations)(nil)
)

// PrepareMigrations records the run, and returns PrepareFail.
func (m *FakeMigrations) PrepareMigrations(_ context.Context, run registry.Run, _ io.Writer) error {
	m.Prepared = append(m.Prepared, run.Name())
	return m.PrepareFail
}

// Migrate records the phase and the plans' services, and the servers of
// a plan with no steps in the phase.
func (m *FakeMigrations) Migrate(ctx context.Context, req registry.MigrationRequest) error {
	if m.ReadOutputs {
		if req.Outputs == nil {
			return fmt.Errorf("the deploy gives the %s phase on %s no outputs", req.Phase, req.Database)
		}
		if _, err := req.Outputs(ctx); err != nil {
			return err
		}
	}
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

// privilegelessMigrations is the fake runner as one whose databases have
// no roles, which Extension.PrivilegelessMigrations registers.
type privilegelessMigrations struct{ *FakeMigrations }

var _ registry.PrivilegelessMigrations = privilegelessMigrations{}

func (privilegelessMigrations) GrantsNoPrivileges() {}

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
		b.log.Record("build %s: %s", req.Deployable, req.Dockerfile)
	}
	if err := b.Fail[req.Deployable]; err != nil {
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
	b.contexts[req.Deployable] = entries
	b.mu.Unlock()
	return kebab(req.Deployable) + "@" + req.ContextDigest, nil
}

// Context returns the entries of the context the last build of server was
// handed, in archive order: a directory's name ends in a slash.
func (b *FakeBuilder) Context(server string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.contexts[server])
}

// FakeJobs is a job runner that runs nothing: it records each run of a
// job on demand in the provisioner's call log (D52).
type FakeJobs struct {
	log *FakeProvisioner

	// Fail holds the error to return for a job's run.
	Fail map[string]error
}

var _ registry.JobRunner = (*FakeJobs)(nil)

// RunJob records the run, `run job <job>: <image>`, or for a job whose
// artifact is a bundle, `run job <job>: bundle <digest>`.
func (j *FakeJobs) RunJob(_ context.Context, req registry.JobRunRequest) error {
	if err := req.Check(); err != nil {
		return err
	}
	if j.log != nil {
		if req.Bundle != "" {
			j.log.Record("run job %s: bundle %s", req.Job, req.Bundle)
		} else {
			j.log.Record("run job %s: %s", req.Job, req.Image)
		}
	}
	return j.Fail[req.Job]
}

// FakeQueues is a queue runner that reaches no database (D68, amended):
// it records each operation in the provisioner's call log, `queue
// <database>: <service>.<queue> <operation>`, keeps each document it was
// handed, and returns the result Results holds for the operation, else
// one that found no dead message, or Fail.
type FakeQueues struct {
	log *FakeProvisioner

	mu sync.Mutex

	// Results holds the result to return for an operation (`dead`), and
	// Fail the error to return for every one.
	Results map[string][]byte
	Fail    error

	documents [][]byte
}

var _ registry.QueueRunner = (*FakeQueues)(nil)

// RunQueue records the operation and returns its result.
func (q *FakeQueues) RunQueue(_ context.Context, req registry.QueueRequest) ([]byte, error) {
	if err := req.Check(); err != nil {
		return nil, err
	}
	var doc struct {
		Version   int    `json:"version"`
		Service   string `json:"service"`
		Queue     string `json:"queue"`
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(req.Document, &doc); err != nil {
		return nil, fmt.Errorf("the queue document: %w", err)
	}
	q.mu.Lock()
	q.documents = append(q.documents, slices.Clone(req.Document))
	q.mu.Unlock()
	if q.log != nil {
		q.log.Record("queue %s: %s.%s %s", req.Database, doc.Service, doc.Queue, doc.Operation)
	}
	if q.Fail != nil {
		return nil, q.Fail
	}
	if result, ok := q.Results[doc.Operation]; ok {
		return slices.Clone(result), nil
	}
	head := fmt.Sprintf(`{"version": %d, "service": %q, "queue": %q, "operation": %q, `, doc.Version, doc.Service, doc.Queue, doc.Operation)
	if doc.Operation == "dead" {
		return []byte(head + `"total": 0, "messages": []}` + "\n"), nil
	}
	return []byte(head + `"ids": []}` + "\n"), nil
}

// Documents returns the documents the runner was handed, in order.
func (q *FakeQueues) Documents() [][]byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.documents)
}

// FakeSites is a site publisher in memory (D55): it records each publish
// in the provisioner's call log, `publish site <site>: <digest>`, with
// `upload <n> files` when it uploads and `config <json>` when it writes
// the config, and keeps the files of each digest it was given. As an
// ArtifactFetcher it writes them back, `fetch site <site>: <digest>`, so
// a deploy on the fake target places each site's files beside the program
// (D64).
type FakeSites struct {
	log *FakeProvisioner

	mu    sync.Mutex
	files map[string]map[string]storedFile
	// configs holds the last config written for each site, by digest.
	configs map[string]map[string]string

	// Fail holds the error to return for a site's publish.
	Fail map[string]error
}

var (
	_ registry.SitePublisher   = (*FakeSites)(nil)
	_ registry.ArtifactFetcher = (*FakeSites)(nil)
)

// Publish records the publish, keeps the files of req.Dir under its
// digest, and refuses a digest with no files when it is given none.
func (s *FakeSites) Publish(_ context.Context, req registry.SitePublishRequest) error {
	if err := req.Check(); err != nil {
		return err
	}
	if err := s.Fail[req.Site]; err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.Site + "@" + req.Digest
	line := fmt.Sprintf("publish site %s: %s", req.Site, req.Digest)
	switch _, known := s.files[key]; {
	case req.Dir != "":
		files, err := readFiles(req.Dir)
		if err != nil {
			return err
		}
		if s.files == nil {
			s.files = map[string]map[string]storedFile{}
		}
		s.files[key] = files
		line += fmt.Sprintf(", upload %d files", len(files))
	case !known:
		return fmt.Errorf("site %s: no files under %s, and the publish gives none", req.Site, req.Digest)
	}
	if req.Config != nil {
		if s.configs == nil {
			s.configs = map[string]map[string]string{}
		}
		if s.configs[req.Site] == nil {
			s.configs[req.Site] = map[string]string{}
		}
		s.configs[req.Site][req.Digest] = string(req.Config)
		line += ", config " + string(req.Config)
	}
	if s.log != nil {
		s.log.Record("%s", line)
	}
	return nil
}

// Fetch records the fetch and writes the files kept for a site under
// digest into dest.
func (s *FakeSites) Fetch(_ context.Context, run registry.Run, site, digest, dest string) error {
	if err := run.Check(); err != nil {
		return err
	}
	s.mu.Lock()
	files, ok := s.files[site+"@"+digest]
	s.mu.Unlock()
	if s.log != nil {
		s.log.Record("fetch site %s: %s", site, digest)
	}
	if !ok {
		return fmt.Errorf("site %s: no files under %s", site, digest)
	}
	return writeFiles(dest, files)
}

// Files returns the files published for a site under digest, sorted.
func (s *FakeSites) Files(site, digest string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedNames(s.files[site+"@"+digest])
}

// Config returns the config last written for a site under digest.
func (s *FakeSites) Config(site, digest string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configs[site][digest]
}

// FakeBundles is a bundle store in memory (D64): it records each publish
// and each fetch in the provisioner's call log, `publish bundle
// <deployable>: <digest>, upload <n> files` and `fetch bundle
// <deployable>: <digest>`, keeps the files of each digest it was given,
// and writes them back on Fetch.
type FakeBundles struct {
	log *FakeProvisioner

	mu    sync.Mutex
	files map[string]map[string]storedFile

	// Fail holds the error to return for a deployable's publish.
	Fail map[string]error
}

var (
	_ registry.BundlePublisher = (*FakeBundles)(nil)
	_ registry.ArtifactFetcher = (*FakeBundles)(nil)
)

// Publish records the publish and keeps the files of dir under digest,
// unless they are there already.
func (b *FakeBundles) Publish(_ context.Context, run registry.Run, deployable, dir, digest string) error {
	if err := run.Check(); err != nil {
		return err
	}
	if d := run.Environment.Deployable(deployable); d == nil || !d.HasBundle() {
		return fmt.Errorf("publish: environment %s has no deployable %s that runs a bundle", run.Environment.Environment, deployable)
	}
	if err := b.Fail[deployable]; err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := deployable + "@" + digest
	line := fmt.Sprintf("publish bundle %s: %s", deployable, digest)
	if _, known := b.files[key]; known {
		line += ", there already"
	} else {
		files, err := readFiles(dir)
		if err != nil {
			return err
		}
		if b.files == nil {
			b.files = map[string]map[string]storedFile{}
		}
		b.files[key] = files
		line += fmt.Sprintf(", upload %d files", len(files))
	}
	if b.log != nil {
		b.log.Record("%s", line)
	}
	return nil
}

// Fetch records the fetch and writes the files kept for a deployable under
// digest into dest.
func (b *FakeBundles) Fetch(_ context.Context, run registry.Run, deployable, digest, dest string) error {
	if err := run.Check(); err != nil {
		return err
	}
	b.mu.Lock()
	files, ok := b.files[deployable+"@"+digest]
	b.mu.Unlock()
	if b.log != nil {
		b.log.Record("fetch bundle %s: %s", deployable, digest)
	}
	if !ok {
		return fmt.Errorf("bundle %s: no files under %s", deployable, digest)
	}
	return writeFiles(dest, files)
}

// Files returns the files kept for a deployable's bundle under digest,
// sorted.
func (b *FakeBundles) Files(deployable, digest string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return sortedNames(b.files[deployable+"@"+digest])
}

// FakePreDestroy readies nothing for a destroy (D64): it records each run
// it is asked to ready in the provisioner's call log, `prepare destroy
// <run>`, and returns Fail.
type FakePreDestroy struct {
	log *FakeProvisioner

	// Fail is the error PrepareDestroy returns.
	Fail error
}

var _ registry.DestroyPreparer = (*FakePreDestroy)(nil)

// PrepareDestroy records the run, and returns Fail.
func (p *FakePreDestroy) PrepareDestroy(_ context.Context, run registry.Run, _ io.Writer) error {
	if p.log != nil {
		p.log.Record("prepare destroy %s", run.Name())
	}
	return p.Fail
}

// storedFile is a file a fake store keeps: its content, and whether it is
// executable, which a digest covers.
type storedFile struct {
	data       []byte
	executable bool
}

// readFiles reads every regular file under dir, by its slash-separated
// path.
func readFiles(dir string) (map[string]storedFile, error) {
	files := map[string]storedFile{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = storedFile{data: data, executable: info.Mode()&0o111 != 0}
		return nil
	})
	return files, err
}

// writeFiles writes files under dest.
func writeFiles(dest string, files map[string]storedFile) error {
	for name, f := range files {
		path := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if f.executable {
			mode = 0o755
		}
		if err := os.WriteFile(path, f.data, mode); err != nil {
			return err
		}
	}
	return nil
}

// sortedNames returns the names of files, sorted.
func sortedNames(files map[string]storedFile) []string {
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
// request in the provisioner's call log, and returns Values for the core
// to record in the schema.
type FakeBootstrap struct {
	log *FakeProvisioner

	// Values are what each bootstrap returns: nothing when empty, else
	// values such as gcp's project number (D47), which the fake target's
	// values take as `projectNumber`, beside `project`.
	Values []registry.BootstrapValue

	// Requests are the requests each bootstrap was given, in order.
	Requests []registry.BootstrapRequest
}

var _ registry.Bootstrapper = (*FakeBootstrap)(nil)

// Bootstrap records the environment, the repository, the credentials'
// secrets and, when it was asked to store the CI's secrets, `github`,
// keeps the request in Requests, and returns Values.
func (b *FakeBootstrap) Bootstrap(_ context.Context, req registry.BootstrapRequest) (*registry.BootstrapResult, error) {
	b.Requests = append(b.Requests, req)
	var secrets []string
	for _, c := range req.Credentials {
		secrets = append(secrets, c.Secret)
	}
	if b.log != nil {
		github := ""
		if req.GitHub {
			github = ", github"
		}
		b.log.Record("bootstrap %s: repository %s, credentials %s%s", req.Environment.Environment, req.Repository, strings.Join(secrets, ", "), github)
	}
	if len(b.Values) == 0 {
		return nil, nil
	}
	return &registry.BootstrapResult{Values: slices.Clone(b.Values)}, nil
}
