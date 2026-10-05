package registry

import (
	"io"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/profile"
	ir "github.com/parable-work/superschematic/ir"
)

// Naming is the set of names generated artifacts carry, loaded from
// superschematic.toml at the schemas root.
type Naming = naming.Naming

// Options configures a generator run.
type Options struct {
	// OutputRoot is the shadow output root (e.g., schemas/dist).
	OutputRoot string

	// ServicePath is the service directory the schema was loaded from.
	// Relative output paths (e.g., API scaffoldsOutputDir) resolve against it.
	ServicePath string

	// Paths locates the runtime modules in the repository for generated
	// manifests to point path dependencies at; the CLI resolves it from the
	// [paths] table. An unset entry emits no path dependency.
	Paths naming.LocalPaths

	// Naming supplies every module, package and crate name the generators
	// emit. Empty fields fall back to naming.Default() (today's names); the
	// CLI fills it from superschematic.toml at the schemas root.
	Naming Naming

	// Registry supplies the kinds and generators Run dispatches on. Nil
	// selects the core registry built from Naming.
	Registry *Registry

	// LoadDependency loads the IR schema of a declared service dependency by
	// name. Generators that alias imported definitions require it whenever
	// the schema config declares dependencies.
	LoadDependency func(name string) (*ir.Schema, error)

	// DependencyConfig returns the config of a service dependency by name,
	// false when the build does not know it. Run uses it to check that each
	// dependency whose types this schema's type libraries import generates
	// its own types in the same languages. build-all and build --with-deps
	// set it; nil (a single build) makes Run log the check it skipped.
	DependencyConfig func(name string) (*schemaconfig.SchemaConfig, bool)

	// LoadDependencyConfig reads the config of a service by name, from where
	// LoadDependency loads the service. Every build sets it, a single build
	// included: the Stack kind's generator reads the outputs of each service
	// a stack reaches from it, the API's language and the database's
	// dialects (docs/stack-model.md, section 6.10).
	LoadDependencyConfig func(name string) (*schemaconfig.SchemaConfig, error)

	// Clock stamps generated file headers. Defaults to the wall clock.
	Clock codegen.Clock

	// Log receives human-readable progress output. Nil silences it.
	Log io.Writer

	// Profile receives opt-in phase timing. Nil disables profiling.
	Profile *profile.Profiler

	// SkipFormat bypasses developer-friendly formatting for generated files.
	// Generated files remain syntactically valid, but may not be gofmt/prettier-like.
	SkipFormat bool
}

// Result reports what a generator run produced.
type Result struct {
	// Outputs maps output keys (e.g., "types-go", "sql", "api") to the
	// directory each was written to.
	Outputs map[string]string

	// Skipped lists requested outputs that did not produce anything: an
	// unknown target language, or an output the schema cannot back (e.g. an
	// SDK for a schema that declares no operations).
	Skipped []string
}
