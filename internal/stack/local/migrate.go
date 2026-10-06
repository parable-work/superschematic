package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// noAppliedModel is what `superschematic-migrate status --model` says of a
// database no plan has finished on.
const noAppliedModel = "has no applied model"

// migrateBinary locates the migration runner. It runs as a binary of its
// own because it holds the database drivers, which stay out of the
// compiler's module (D27, Apply).
func (p *Provisioner) migrateBinary() (string, error) {
	if p.Migrate != "" {
		return p.Migrate, nil
	}
	if path := os.Getenv(MigrateEnv); path != "" {
		return path, nil
	}
	path, err := p.runner().LookPath(MigrateBinary)
	if err != nil {
		return "", fmt.Errorf("local: %s is not on PATH, and %s names no runner; the local target applies migrations with it: "+
			"install it from a release, or build it (cd runtime/migrate/go && go build -o \"$(go env GOPATH)/bin/%s\" ./cmd/%s), "+
			"as runtime/migrate/README.md says: %w", MigrateBinary, MigrateEnv, MigrateBinary, MigrateBinary, err)
	}
	return path, nil
}

// targetModel reads the model the deploy wrote for a migration.
func targetModel(req registry.ProvisionRequest, m *Migration) (*sqlmigrate.Model, error) {
	path := filepath.Join(req.Dir, filepath.FromSlash(m.Model))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("local: migrate %s: no model to migrate to (the deploy writes it before it applies): %w", m.Service, err)
	}
	model, err := decodeModel(data)
	if err != nil {
		return nil, fmt.Errorf("local: migrate %s: %s: %w", m.Service, path, err)
	}
	if model.Service != m.Service || model.Dialect != sqlmigrate.Postgres {
		return nil, fmt.Errorf("local: migrate %s: %s is the %s model of %s", m.Service, path, model.Dialect, model.Service)
	}
	return model, nil
}

func decodeModel(data []byte) (*sqlmigrate.Model, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var model sqlmigrate.Model
	if err := decoder.Decode(&model); err != nil {
		return nil, fmt.Errorf("not a model: %w", err)
	}
	if model.Version != sqlmigrate.ModelVersion {
		return nil, fmt.Errorf("model version %d; this compiler reads version %d", model.Version, sqlmigrate.ModelVersion)
	}
	return &model, nil
}

// appliedModel asks the runner for the model the database recorded; nil is
// none, an empty database.
func (p *Provisioner) appliedModel(ctx context.Context, runner string, m *Migration) (*sqlmigrate.Model, error) {
	out, err := p.runner().Run(ctx, Command{Path: runner, Args: []string{"status", "--service", m.Service, "--model", "--database-url", m.URL}})
	if err != nil {
		if stderrContains(err, noAppliedModel) {
			return nil, nil
		}
		return nil, fmt.Errorf("local: migrate %s: read the applied model: %w", m.Service, err)
	}
	model, err := decodeModel(out)
	if err != nil {
		return nil, fmt.Errorf("local: migrate %s: the applied model: %w", m.Service, err)
	}
	return model, nil
}

// migrationPending reports whether the database's applied model is not the
// one the deploy wrote.
func (p *Provisioner) migrationPending(ctx context.Context, req registry.ProvisionRequest, m *Migration) (bool, error) {
	to, err := targetModel(req, m)
	if err != nil {
		return false, err
	}
	runner, err := p.migrateBinary()
	if err != nil {
		return false, err
	}
	from, err := p.appliedModel(ctx, runner, m)
	if err != nil || from == nil {
		return true, err
	}
	return !sameModel(from, to), nil
}

func sameModel(a, b *sqlmigrate.Model) bool {
	ha, errA := a.Hash()
	hb, errB := b.Hash()
	return errA == nil && errB == nil && ha == hb
}

// migrate plans a DB schema's migration from the model its database
// recorded to the one the deploy wrote, writes the plan into
// MigrationsDir, and applies both phases back to back. Locally no server of
// the previous version runs during the migration, so nothing needs the
// contract to wait.
func (p *Provisioner) migrate(ctx context.Context, req registry.ProvisionRequest, m *Migration) error {
	to, err := targetModel(req, m)
	if err != nil {
		return err
	}
	runner, err := p.migrateBinary()
	if err != nil {
		return err
	}
	from, err := p.appliedModel(ctx, runner, m)
	if err != nil {
		return err
	}
	toHash, err := to.Hash()
	if err != nil {
		return err
	}
	if from != nil && sameModel(from, to) {
		p.printf("migrate %s: up to date at model %s", m.Service, short(toHash))
		return nil
	}
	plan, err := sqlmigrate.Diff(from, to, sqlmigrate.Options{})
	if err != nil {
		return fmt.Errorf("local: migrate %s: plan: %w", m.Service, err)
	}
	data, err := plan.CanonicalJSON()
	if err != nil {
		return err
	}
	path, err := filepath.Abs(filepath.Join(req.Dir, filepath.FromSlash(m.Plan)))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("local: migrate %s: %w", m.Service, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("local: migrate %s: %w", m.Service, err)
	}
	start := "an empty database"
	if from != nil {
		fromHash, _ := from.Hash()
		start = "model " + short(fromHash)
	}
	p.printf("migrate %s: %d steps from %s to model %s, expand and contract back to back (%s)", m.Service, len(plan.Steps), start, short(toHash), path)
	if hazards := plan.Hazards(); len(hazards) > 0 {
		ids := make([]string, len(hazards))
		for i, h := range hazards {
			ids[i] = h.ID
		}
		p.printf("migrate %s: hazards: %s", m.Service, strings.Join(ids, ", "))
	}
	output := p.out().writer("migrate " + m.Service)
	defer output.Flush()
	if _, err := p.runner().Run(ctx, Command{
		Path:   runner,
		Args:   []string{"apply", "--plan", path, "--phase", "all", "--database-url", m.URL},
		Stdout: output,
		Stderr: output,
	}); err != nil {
		return fmt.Errorf("local: migrate %s: %w", m.Service, err)
	}
	return nil
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
