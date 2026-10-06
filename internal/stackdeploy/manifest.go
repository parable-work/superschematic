package stackdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// ManifestVersion is the version of the deploy manifest's JSON form.
const ManifestVersion = 1

// Status is where a run's last deploy got to.
type Status string

const (
	// StatusDeploying: a deploy is part-way. The manifest records each
	// step as it finishes, so a deploy that died says how far it got.
	StatusDeploying Status = "deploying"

	// StatusDeployed: the last deploy ran every step.
	StatusDeployed Status = "deployed"

	// StatusFailed: the last deploy stopped at a step that failed.
	StatusFailed Status = "failed"
)

// Manifest is the record of what a run of an environment runs
// (docs/stack-model.md, section 11.2). The target's state store keeps one
// per run. It is the migration baseline, the record of what is running,
// and the starting point for a rollback: the next deploy plans each
// database's migration from the model it records, and carries forward the
// image of each server it is given none for.
type Manifest struct {
	// Version is ManifestVersion.
	Version int `json:"version"`

	// Stack, Environment and Run name the run (registry.Run.Name).
	Stack       string            `json:"stack"`
	Environment string            `json:"environment"`
	Run         string            `json:"run"`
	Parameters  map[string]string `json:"parameters,omitempty"`

	// Status is where the last deploy got to. Step names the last step it
	// finished, or for a failed deploy the step that failed, with Error.
	Status Status `json:"status"`
	Step   string `json:"step,omitempty"`
	Error  string `json:"error,omitempty"`

	// Time is when the manifest was written.
	Time time.Time `json:"time"`

	// Resolved is the resolved environment the deploy applied, before its
	// images were pinned.
	Resolved *ir.ResolvedEnvironment `json:"resolved"`

	// Services holds the IR digest of each service the stack reaches, by
	// name: `sha256:` and the hex SHA-256 of the IR's canonical JSON.
	Services map[string]string `json:"services,omitempty"`

	// Images holds the image each server runs, by server:
	// `<repository>@sha256:<digest>`. A server whose rollout wave did not
	// finish keeps the image the previous deploy recorded.
	Images map[string]string `json:"images,omitempty"`

	// Contexts holds, by server, the digest of the build context each
	// image of Images was built from, when a deploy built it (Context): a
	// deploy builds the server's image again only when its context's
	// digest differs. An image given with --image has none.
	Contexts map[string]string `json:"contexts,omitempty"`

	// Databases holds the schema each database holds, by database
	// deployable and then by the DB service it hosts.
	Databases map[string]map[string]*AppliedSchema `json:"databases,omitempty"`
}

// AppliedSchema is the schema one DB service's database holds, as the
// migration runner recorded it (D27).
type AppliedSchema struct {
	// Dialect is the database's SQL dialect.
	Dialect string `json:"dialect"`

	// Hash is the model's hash, and Model its canonical JSON. After a
	// rollout that failed, it is the model between a plan's phases, which
	// the next deploy plans from (D27, amended).
	Hash  string          `json:"hash"`
	Model json.RawMessage `json:"model"`

	// Pending is a plan whose phase a deploy started and did not finish.
	// The next deploy finishes that phase first, since the runner resumes
	// it and refuses any other plan until it does.
	Pending *PendingMigration `json:"pending,omitempty"`

	// Servers are the servers that connected to the DB service when the
	// migration runner last ran on it, sorted. A runner that owns the
	// database's privileges gave each what it reads and writes (D46), so a
	// deploy runs the expand phase of a DB service whose connecting
	// servers changed even when its plan has no expand steps.
	Servers []string `json:"servers,omitempty"`
}

// PendingMigration is one phase of one plan that did not finish.
type PendingMigration struct {
	Phase ir.MigrationPhase `json:"phase"`
	Plan  *DatabasePlan     `json:"plan"`
}

// Marshal encodes the manifest as indented JSON.
func (m *Manifest) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// UnmarshalManifest decodes a manifest and checks its version.
func UnmarshalManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("deploy manifest: %w", err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("deploy manifest version %d; this binary reads version %d", m.Version, ManifestVersion)
	}
	// Marshal indents the models and plan documents it embeds; read them
	// back in their canonical form, which their hashes are over.
	for _, database := range m.Databases {
		for _, applied := range database {
			if err := canonicalize(&applied.Model); err != nil {
				return nil, fmt.Errorf("deploy manifest: %w", err)
			}
			if p := applied.Pending; p != nil && p.Plan != nil {
				for _, raw := range []*json.RawMessage{&p.Plan.ToModel, &p.Plan.ExpandedModel, &p.Plan.Document} {
					if err := canonicalize(raw); err != nil {
						return nil, fmt.Errorf("deploy manifest: %w", err)
					}
				}
			}
		}
	}
	return &m, nil
}

// canonicalize replaces a JSON value with its canonical form; an empty one
// stays empty.
func canonicalize(raw *json.RawMessage) error {
	if len(*raw) == 0 {
		return nil
	}
	canonical, err := ir.CanonicalJSON(*raw)
	if err != nil {
		return err
	}
	*raw = canonical
	return nil
}

// applied returns the schema the manifest records for a DB service, or
// nil.
func (m *Manifest) applied(database, service string) *AppliedSchema {
	if m == nil {
		return nil
	}
	return m.Databases[database][service]
}

// setApplied records the schema a DB service's database holds.
func (m *Manifest) setApplied(database, service string, applied *AppliedSchema) {
	if m.Databases == nil {
		m.Databases = map[string]map[string]*AppliedSchema{}
	}
	if m.Databases[database] == nil {
		m.Databases[database] = map[string]*AppliedSchema{}
	}
	m.Databases[database][service] = applied
}

// setImage records the image a server runs, and the context a deploy
// built it from, or none.
func (m *Manifest) setImage(server, image, context string) {
	if m.Images == nil {
		m.Images = map[string]string{}
	}
	m.Images[server] = image
	if context == "" {
		delete(m.Contexts, server)
		return
	}
	if m.Contexts == nil {
		m.Contexts = map[string]string{}
	}
	m.Contexts[server] = context
}

// readManifest returns the run's manifest, or nil when the run was never
// deployed. It refuses a manifest of another run.
func readManifest(ctx context.Context, store registry.StateStore, run registry.Run) (*Manifest, error) {
	data, err := store.ReadManifest(ctx, run)
	if errors.Is(err, registry.ErrNoManifest) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the deploy manifest of %s: %w", run.Name(), err)
	}
	m, err := UnmarshalManifest(data)
	if err != nil {
		return nil, fmt.Errorf("the deploy manifest of %s: %w", run.Name(), err)
	}
	if m.Stack != run.Environment.Stack || m.Run != run.Name() {
		return nil, fmt.Errorf("the deploy manifest the state store holds for %s is %s of stack %s", run.Name(), m.Run, m.Stack)
	}
	return m, nil
}

// nextManifest starts the manifest of a deploy from the previous one: the
// images and the databases' schemas carry forward until a step changes
// them. Only the deployables env still has are carried.
func nextManifest(prev *Manifest, run registry.Run, digests map[string]string) *Manifest {
	env := run.Environment
	m := &Manifest{
		Version:     ManifestVersion,
		Stack:       env.Stack,
		Environment: env.Environment,
		Run:         run.Name(),
		Parameters:  maps.Clone(run.Parameters),
		Status:      StatusDeploying,
		Resolved:    env,
		Services:    maps.Clone(digests),
	}
	if prev == nil {
		return m
	}
	for _, d := range env.Deployables {
		switch d.Kind {
		case ir.DeployableServer:
			if image, ok := prev.Images[d.Name]; ok {
				m.setImage(d.Name, image, prev.Contexts[d.Name])
			}
		case ir.DeployableDatabase:
			for _, svc := range d.Services {
				if applied := prev.applied(d.Name, svc.Name); applied != nil {
					copied := *applied
					m.setApplied(d.Name, svc.Name, &copied)
				}
			}
		}
	}
	return m
}
