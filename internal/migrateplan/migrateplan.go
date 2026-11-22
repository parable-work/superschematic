// Package migrateplan plans a database's migration from schema documents
// and the model the database recorded (D117): the planner a runtime
// environment runs at a publish, with no service directory, no git ref and
// no build. The documents are the data form of one DB service's schemas,
// as `format --to=json` writes them, and the planner is the compiler's: the
// loader reads them, sqlgen's model resolves them and D27's diff plans
// between the models. `migrate plan --documents` and `migrate plan
// --request` call it, and so may a caller in process.
package migrateplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/jsonreader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// sqlGenerator is the core generator that writes create.sql: a kind whose
// pipeline runs it has a database to migrate.
const sqlGenerator = "sql"

// Source is one schema document, as `format --to=json` writes it, and the
// name its errors give it: a file's path, or its place in a request
// ("documents[1]").
type Source struct {
	Name string
	Data []byte
}

// Env is what a plan from documents takes from its caller and not from the
// documents: the registry, whose kinds, decorators, behaviors and
// extensions a document may use, as the binary assembles it, and the
// naming.
type Env struct {
	Registry *registry.Registry
	Naming   naming.Naming
}

// ReadSources reads a stream of schema documents, JSON values one after
// another as `format --to=json --stdout` prints them, and names each
// "<label> #<n>". A stream with no document is refused.
func ReadSources(r io.Reader, label string) ([]Source, error) {
	decoder := json.NewDecoder(r)
	var sources []Source
	for {
		var value json.RawMessage
		err := decoder.Decode(&value)
		if errors.Is(err, io.EOF) {
			break
		}
		name := fmt.Sprintf("%s #%d", label, len(sources)+1)
		if err != nil {
			return nil, fmt.Errorf("%s: not JSON: %w", name, err)
		}
		sources = append(sources, Source{Name: name, Data: value})
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("%s holds no schema document", label)
	}
	return sources, nil
}

// Load reads one DB service's schema documents into its schema, checked
// for dialect as a build of the service checks it for each dialect its
// config lists. Every document names the service and its kind, as `format
// --to=json` writes them, and every one the same: a publish takes a
// database's schemas whole (D117). The kind must be one that has a
// database. A document of the data form is checked against its JSON
// Schema and merged as a service load merges its files; the documents name
// no other service, since nothing beside them resolves one.
func Load(sources []Source, dialect sqlmigrate.Dialect, env Env) (*ir.Schema, error) {
	if len(sources) == 0 {
		return nil, errors.New("no schema documents: a plan from documents takes every schema document of one DB service, as format --to=json writes them")
	}
	reg := env.registry()
	docs := make([]loader.SourcedDocument, 0, len(sources))
	for _, source := range sources {
		doc, err := jsonreader.ReadWith(source.Data, source.Name, reg)
		if err != nil {
			return nil, fmt.Errorf("not a schema document: %w", err)
		}
		if doc.Name == "" || doc.Kind == "" {
			return nil, fmt.Errorf("%s: the document names no service (name) or no kind (kind); a plan from documents reads both from each document, as format --to=json writes them", source.Name)
		}
		if len(doc.Imports) > 0 {
			imp := doc.Imports[0]
			return nil, fmt.Errorf("%s: the document imports %s from %s; a plan from documents takes one DB service's documents and resolves no other service", source.Name, strings.Join(imp.Types, ", "), imp.Package)
		}
		docs = append(docs, loader.SourcedDocument{Source: source.Name, Document: doc})
	}
	first := docs[0].Document
	for _, sourced := range docs[1:] {
		if sourced.Document.Name != first.Name {
			return nil, fmt.Errorf("%s is a document of %s, and %s of %s: a plan from documents takes one DB service's documents", docs[0].Source, first.Name, sourced.Source, sourced.Document.Name)
		}
	}
	if !hasDatabase(reg, string(first.Kind)) {
		return nil, fmt.Errorf("%s is of kind %s, which has no database; plan a DB service's documents", first.Name, first.Kind)
	}
	return loader.LoadDocuments(docs,
		loader.WithRegistry(reg), loader.WithNaming(env.Naming), loader.WithDialects(string(dialect)))
}

// hasDatabase reports whether a schema of kind has a database: whether
// its kind's pipeline runs the sql generator.
func hasDatabase(reg *registry.Registry, kind string) bool {
	return slices.ContainsFunc(reg.Pipeline(kind), func(gen registry.GeneratorSpec) bool { return gen.Name == sqlGenerator })
}

// Model resolves a schema Load read to its model in dialect, with the
// options a build passes sqlgen for a service whose config sets no
// outputs.sql options and names no dependency: a document carries no
// config.
func Model(schema *ir.Schema, dialect sqlmigrate.Dialect, env Env) (*sqlmigrate.Model, error) {
	cfg := &schemaconfig.SchemaConfig{Name: schema.Name, Kind: schema.Kind}
	return sqlmigrate.BuildModel(schema, generator.SQLOptions(cfg, &registry.Outputs{}, nil, env.Naming), dialect)
}

// Documents reads the sources (Load) and resolves them to their model in
// dialect (Model).
func Documents(sources []Source, dialect sqlmigrate.Dialect, env Env) (*ir.Schema, *sqlmigrate.Model, error) {
	schema, err := Load(sources, dialect, env)
	if err != nil {
		return nil, nil, err
	}
	if dialect == sqlmigrate.Postgres && len(schema.Queries()) > 0 {
		return nil, nil, fmt.Errorf("%s declares queries (@query), which a Postgres plan prepares on the new model first (D86) and a plan from documents does not: plan the service directory", schema.Name)
	}
	model, err := Model(schema, dialect, env)
	if err != nil {
		return nil, nil, err
	}
	return schema, model, nil
}

// CheckModel checks that from, the model a database recorded, is a model
// this compiler plans from, of service in dialect.
func CheckModel(from *sqlmigrate.Model, service string, dialect sqlmigrate.Dialect) error {
	switch {
	case from.Version != sqlmigrate.ModelVersion:
		return fmt.Errorf("model version %d; this compiler reads version %d", from.Version, sqlmigrate.ModelVersion)
	case from.Service != service:
		return fmt.Errorf("the model is %s's, not %s's", from.Service, service)
	case from.Dialect != dialect:
		return fmt.Errorf("the model is for %s, not %s", from.Dialect, dialect)
	}
	return nil
}

// ReadModel decodes a model's JSON, as `superschematic-migrate status
// --model` prints it, refusing a member a model does not have.
func ReadModel(data []byte) (*sqlmigrate.Model, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var model sqlmigrate.Model
	if err := decoder.Decode(&model); err != nil {
		return nil, fmt.Errorf("not a model (%w)", err)
	}
	return &model, nil
}

// Plan plans the migration from the model a database recorded, from, to
// the model the documents resolve to; a nil from is an empty database. It
// is a plan with no readers: the documents are one DB service's, and the
// services that read its tables are not among them.
func Plan(sources []Source, from *sqlmigrate.Model, dialect sqlmigrate.Dialect, renames []sqlmigrate.Rename, env Env) (*sqlmigrate.Plan, error) {
	_, planned, err := planDocuments(sources, from, dialect, renames, env)
	return planned, err
}

// planDocuments is Plan with the schema the documents load as.
func planDocuments(sources []Source, from *sqlmigrate.Model, dialect sqlmigrate.Dialect, renames []sqlmigrate.Rename, env Env) (*ir.Schema, *sqlmigrate.Plan, error) {
	schema, to, err := Documents(sources, dialect, env)
	if err != nil {
		return nil, nil, err
	}
	if from != nil {
		if err := CheckModel(from, schema.Name, dialect); err != nil {
			return nil, nil, fmt.Errorf("from: %w", err)
		}
	}
	diffed, err := sqlmigrate.Diff(from, to, sqlmigrate.Options{Renames: renames})
	if err != nil {
		return nil, nil, err
	}
	return schema, diffed, nil
}

// Layout is the layout document of the tables of a schema Load read, as a
// runtime handler serves them (behaviorlayout.RuntimeOf, D117), in
// canonical JSON: what a runtime environment records next to the model a
// publish takes its database to, so that no part of it derives the layout
// again.
func Layout(schema *ir.Schema, env Env) (json.RawMessage, error) {
	layout, err := behaviorlayout.RuntimeOf(schema, env.registry())
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(layout)
	if err != nil {
		return nil, err
	}
	return ir.CanonicalJSON(data)
}

// ParseHazardClasses reads hazard class names, or all for every class, in
// the order of sqlmigrate.HazardClasses, each once.
func ParseHazardClasses(values []string) ([]sqlmigrate.HazardClass, error) {
	want := map[sqlmigrate.HazardClass]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "all" {
			for _, class := range sqlmigrate.HazardClasses {
				want[class] = true
			}
			continue
		}
		class := sqlmigrate.HazardClass(value)
		if !slices.Contains(sqlmigrate.HazardClasses, class) {
			names := make([]string, 0, len(sqlmigrate.HazardClasses))
			for _, class := range sqlmigrate.HazardClasses {
				names = append(names, string(class))
			}
			return nil, fmt.Errorf("%q is not a hazard class: want %s, or all", value, strings.Join(names, ", "))
		}
		want[class] = true
	}
	var classes []sqlmigrate.HazardClass
	for _, class := range sqlmigrate.HazardClasses {
		if want[class] {
			classes = append(classes, class)
		}
	}
	return classes, nil
}

// CheckHazardID refuses an id that is not <class>:<subject> of a known
// class, as a plan lists its hazards.
func CheckHazardID(id string) error {
	class, _, ok := strings.Cut(id, ":")
	if !ok || !slices.Contains(sqlmigrate.HazardClasses, sqlmigrate.HazardClass(class)) {
		return fmt.Errorf("%q is not a hazard id: want <class>:<subject>, as the plan lists it", id)
	}
	return nil
}

// registry returns the env's registry, or the core's for its naming.
func (env Env) registry() *registry.Registry {
	if env.Registry != nil {
		return env.Registry
	}
	return generator.CoreRegistry(env.Naming.OrDefault())
}
