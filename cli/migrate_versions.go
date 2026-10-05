package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// sqlGenerator is the core generator that writes create.sql. A kind whose
// pipeline runs it has a database to migrate.
const sqlGenerator = "sql"

// schemaVersion is one version of a schemas root: its services, found
// with the discovery build-all uses and loaded as build --with-deps loads
// them (the schema catalog, the shared TypeScript program), with the
// naming and registry of that root. Each service loads once.
type schemaVersion struct {
	servicesRoot string
	names        naming.Naming
	reg          *registry.Registry
	services     []buildplan.Service
	loadOpts     []loader.Option
	programs     *tsreader.ProgramCache
	schemas      map[string]*ir.Schema
	configs      map[string]*schemaconfig.SchemaConfig
}

func openSchemaVersion(servicesRoot string, names naming.Naming, reg *registry.Registry) (*schemaVersion, error) {
	// A config may import a sibling's sentinel (D34), so the sentinels are
	// written first; a --from-ref version may predate one. Discovery
	// computes each service's output directories under an output root;
	// migrate writes nothing there.
	if err := buildplan.EnsureSentinels(servicesRoot, reg, nil); err != nil {
		return nil, err
	}
	services, err := buildplan.DiscoverWith(servicesRoot, filepath.Join(filepath.Dir(servicesRoot), "dist"), reg)
	if err != nil {
		return nil, err
	}
	catalog := make(map[string]registry.SchemaCatalogEntry, len(services))
	for _, service := range services {
		catalog[service.Name] = registry.SchemaCatalogEntry{Kind: string(service.Config.Kind), AuthDB: service.Config.AuthDB}
	}
	v := &schemaVersion{
		servicesRoot: servicesRoot,
		names:        names,
		reg:          reg,
		services:     services,
		loadOpts:     []loader.Option{loader.WithSchemaCatalog(catalog), loader.WithNaming(names), loader.WithRegistry(reg)},
		schemas:      map[string]*ir.Schema{},
		configs:      map[string]*schemaconfig.SchemaConfig{},
	}
	if dirs := tsServiceDirectories(services); len(dirs) > 0 {
		v.programs = tsreader.NewProgramCache(dirs)
	}
	return v, nil
}

// openVersionAt opens the schemas root above servicesRoot with its own
// naming file, or, when it has none, the file --naming names, else the
// defaults.
func openVersionAt(a *app, servicesRoot, namingPath string) (*schemaVersion, error) {
	names, err := versionNaming(filepath.Dir(servicesRoot), namingPath)
	if err != nil {
		return nil, err
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return nil, err
	}
	return openSchemaVersion(servicesRoot, names, reg)
}

func versionNaming(schemasRoot, namingPath string) (naming.Naming, error) {
	own := filepath.Join(schemasRoot, naming.FileName)
	if _, err := os.Stat(own); err == nil {
		return naming.LoadFile(own)
	}
	if namingPath != "" {
		return naming.LoadFile(namingPath)
	}
	return naming.Default(), nil
}

func (v *schemaVersion) close() {
	if v != nil && v.programs != nil {
		v.programs.Close()
	}
}

// serviceAt returns the service whose directory is dir, an absolute path.
func (v *schemaVersion) serviceAt(dir string) (buildplan.Service, error) {
	for _, service := range v.services {
		if filepath.Clean(service.Dir) == dir {
			return service, nil
		}
	}
	return buildplan.Service{}, fmt.Errorf("%s is not a schema service under %s", dir, v.servicesRoot)
}

func (v *schemaVersion) service(name string) (buildplan.Service, bool) {
	for _, service := range v.services {
		if service.Name == name {
			return service, true
		}
	}
	return buildplan.Service{}, false
}

// requireDatabase refuses a service whose kind does not generate SQL: it
// has no database to migrate.
func (v *schemaVersion) requireDatabase(service buildplan.Service) error {
	kind := string(service.Config.Kind)
	for _, gen := range v.reg.Pipeline(kind) {
		if gen.Name == sqlGenerator {
			return nil
		}
	}
	return fmt.Errorf("migrate plan: %s is of kind %s, which has no database; plan a DB service", service.Name, kind)
}

// requireDialect refuses to plan the named service for a dialect its
// outputs.sql.dialects does not list: no build of it writes that
// dialect's create.sql, so no database of it is in that dialect.
func (v *schemaVersion) requireDialect(name string, dialect sqlmigrate.Dialect) error {
	built, dialects, err := v.builtFor(name, dialect)
	if err != nil {
		return err
	}
	if !built {
		return fmt.Errorf("migrate plan: %s is built for %s, not %s: add %s to its outputs.sql.dialects to plan for it",
			name, strings.Join(dialects, ", "), dialect, dialect)
	}
	return nil
}

// builtFor reports whether the named service's outputs.sql.dialects lists
// dialect, with the dialects it lists.
func (v *schemaVersion) builtFor(name string, dialect sqlmigrate.Dialect) (bool, []string, error) {
	_, cfg, err := v.load(name)
	if err != nil {
		return false, nil, err
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, v.reg)
	if err != nil {
		return false, nil, fmt.Errorf("schema config for %s: %w", name, err)
	}
	return outputs.SQLDialect(string(dialect)), outputs.SQLDialects(), nil
}

// readsSources reports whether service's kind may hold @source views,
// which make it a reader: API and General in the core.
func (v *schemaVersion) readsSources(service buildplan.Service) bool {
	kind, ok := v.reg.Kind(string(service.Config.Kind))
	return ok && kind.SourceProjectionRole != ""
}

func (v *schemaVersion) load(name string) (*ir.Schema, *schemaconfig.SchemaConfig, error) {
	if schema, ok := v.schemas[name]; ok {
		return schema, v.configs[name], nil
	}
	service, ok := v.service(name)
	if !ok {
		return nil, nil, fmt.Errorf("schema service %s not found under %s", name, v.servicesRoot)
	}
	opts := append([]loader.Option(nil), v.loadOpts...)
	if v.programs != nil {
		opts = append(opts, loader.WithTSProgramCache(v.programs))
	}
	schema, cfg, err := loader.LoadServiceWithConfig(service.Dir, opts...)
	if err != nil {
		return nil, nil, err
	}
	v.schemas[name] = schema
	v.configs[name] = cfg
	return schema, cfg, nil
}

// model resolves the named DB service to its model with the sqlgen
// options a build of this version passes it.
func (v *schemaVersion) model(name string, dialect sqlmigrate.Dialect) (*sqlmigrate.Model, error) {
	schema, cfg, err := v.load(name)
	if err != nil {
		return nil, err
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, v.reg)
	if err != nil {
		return nil, fmt.Errorf("schema config for %s: %w", name, err)
	}
	var deps map[string]*ir.Schema
	for _, dep := range cfg.Dependencies {
		depSchema, _, err := v.load(dep.Name)
		if err != nil {
			return nil, fmt.Errorf("load dependency %s of %s: %w", dep.Name, name, err)
		}
		if deps == nil {
			deps = make(map[string]*ir.Schema, len(cfg.Dependencies))
		}
		deps[dep.Name] = depSchema
	}
	return sqlmigrate.BuildModel(schema, generator.SQLOptions(cfg, outputs, deps, v.names), dialect)
}

// reads returns the columns of database's tables, as model has them, that
// the readers of this version read. A nil model is an empty database,
// which no one reads.
func (v *schemaVersion) reads(database string, model *sqlmigrate.Model) ([]sqlmigrate.Read, error) {
	if model == nil {
		return nil, nil
	}
	var sets [][]sqlmigrate.Read
	for _, service := range v.services {
		if !v.readsSources(service) {
			continue
		}
		schema, _, err := v.load(service.Name)
		if err != nil {
			return nil, fmt.Errorf("reader %s: %w", service.Name, err)
		}
		reads, err := sqlmigrate.SourceReads(schema, database, model)
		if err != nil {
			return nil, fmt.Errorf("reader %s: %w", service.Name, err)
		}
		sets = append(sets, reads)
	}
	return sqlmigrate.MergeReads(sets...), nil
}

// previousVersion is the version a plan starts from.
type previousVersion struct {
	// label names it in messages ("--from-ref origin/main").
	label string
	// version is its schemas root; nil for a model file and an empty
	// database.
	version *schemaVersion
	// found is true when version holds the service.
	found bool
	// model is the model a --from file holds.
	model *sqlmigrate.Model
	// cleanup removes what --from-ref extracted.
	cleanup func()
}

func (p *previousVersion) close() {
	p.version.close()
	if p.cleanup != nil {
		p.cleanup()
	}
}

// requireDialect refuses a previous version whose outputs.sql.dialects
// does not list dialect: no build of it wrote that dialect's create.sql,
// so its model in that dialect is not what a database holds. A model file
// is checked by its own dialect instead (readModelFile).
func (p *previousVersion) requireDialect(name string, dialect sqlmigrate.Dialect) error {
	built, dialects, err := p.version.builtFor(name, dialect)
	if err != nil {
		return fmt.Errorf("%s: %w", p.label, err)
	}
	if !built {
		return fmt.Errorf("migrate plan: %s: the previous version of %s was not built for %s (its outputs.sql.dialects lists %s); "+
			"plan from the model the database recorded (--from <model.json>, as superschematic-migrate status --model prints it), "+
			"or from an empty database with neither --from nor --from-ref",
			p.label, name, dialect, strings.Join(dialects, ", "))
	}
	return nil
}

// openPrevious resolves --from or --from-ref, or an empty database when
// neither is given. name is the service the plan migrates.
func openPrevious(cmd *cobra.Command, a *app, flags *migratePlanFlags, current *schemaVersion, name string, dialect sqlmigrate.Dialect) (*previousVersion, error) {
	stderr := cmd.ErrOrStderr()
	switch {
	case flags.from != "":
		label := "--from " + flags.from
		info, err := os.Stat(flags.from)
		if err != nil {
			return nil, fmt.Errorf("--from: %w", err)
		}
		if !info.IsDir() {
			model, err := readModelFile(flags.from, name, dialect)
			if err != nil {
				return nil, err
			}
			return &previousVersion{label: label, model: model}, nil
		}
		dir, err := filepath.Abs(flags.from)
		if err != nil {
			return nil, fmt.Errorf("--from: %w", err)
		}
		version, err := openVersionAt(a, filepath.Dir(dir), flags.namingPath)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		service, err := version.serviceAt(dir)
		if err == nil && service.Name != name {
			err = fmt.Errorf("it is service %s, not %s", service.Name, name)
		}
		if err != nil {
			version.close()
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		prev := &previousVersion{label: label, version: version, found: true}
		if err := prev.requireDialect(name, dialect); err != nil {
			prev.close()
			return nil, err
		}
		return prev, nil

	case flags.fromRef != "":
		label := "--from-ref " + flags.fromRef
		schemasRoot := filepath.Dir(current.servicesRoot)
		dir, found, cleanup, err := extractSchemasRoot(cmd.Context(), schemasRoot, flags.fromRef)
		if err != nil {
			return nil, err
		}
		prev := &previousVersion{label: label, cleanup: cleanup}
		servicesRoot := filepath.Join(dir, filepath.Base(current.servicesRoot))
		if found {
			info, err := os.Stat(servicesRoot)
			found = err == nil && info.IsDir()
		}
		if !found {
			_, _ = fmt.Fprintf(stderr, "migrate plan: at %s the schemas root has no %s directory; planning from an empty database\n", flags.fromRef, filepath.Base(current.servicesRoot))
			return prev, nil
		}
		version, err := openVersionAt(a, servicesRoot, flags.namingPath)
		if err != nil {
			prev.close()
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		prev.version = version
		if _, prev.found = version.service(name); !prev.found {
			_, _ = fmt.Fprintf(stderr, "migrate plan: at %s there is no service %s; planning from an empty database\n", flags.fromRef, name)
		} else if err := prev.requireDialect(name, dialect); err != nil {
			prev.close()
			return nil, err
		}
		return prev, nil

	default:
		_, _ = fmt.Fprintln(stderr, "migrate plan: no previous version (--from, --from-ref); planning from an empty database")
		return &previousVersion{label: "empty database"}, nil
	}
}

// openReaders loads each --reader service, with its dependencies resolved
// from its own schemas root.
func openReaders(a *app, flags *migratePlanFlags, current *schemaVersion) ([]*ir.Schema, error) {
	var readers []*ir.Schema
	for _, path := range flags.readers {
		schema, err := loadReader(a, flags.namingPath, current, path)
		if err != nil {
			return nil, fmt.Errorf("--reader %s: %w", path, err)
		}
		readers = append(readers, schema)
	}
	return readers, nil
}

func loadReader(a *app, namingPath string, current *schemaVersion, path string) (*ir.Schema, error) {
	dir, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("service directory not found: %s", path)
	}
	version := current
	if servicesRoot := filepath.Dir(dir); servicesRoot != current.servicesRoot {
		if version, err = openVersionAt(a, servicesRoot, namingPath); err != nil {
			return nil, err
		}
		defer version.close()
	}
	service, err := version.serviceAt(dir)
	if err != nil {
		return nil, err
	}
	if !version.readsSources(service) {
		return nil, fmt.Errorf("%s is of kind %s; a reader is an API or General service", service.Name, service.Config.Kind)
	}
	schema, _, err := version.load(service.Name)
	return schema, err
}
