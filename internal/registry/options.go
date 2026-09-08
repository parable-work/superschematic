package registry

import (
	"io"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/profile"
	ir "github.com/parable-work/superschematic/ir"
)

// Naming is the set of names generated artifacts carry, loaded from
// superschematic.toml at the schemas root.
type Naming = naming.Naming

// Options configures a generator run.
type Options struct {
	// OutputRoot is the shadow output root (e.g., platform-schemas/dist).
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
