package registry

import (
	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

type (
	// VerifyReporter is what KindSpec.Verify and CheckSpec.Verify report
	// through; see internal/registry.VerifyReporter.
	VerifyReporter = registry.VerifyReporter

	// SchemaConfig is a loaded schema.config; GenerateContext.Config and
	// loader.LoadServiceWithConfig hand one out.
	SchemaConfig = schemaconfig.SchemaConfig
)

// Generate resolves schema's kind in opts.Registry and runs the kind's
// generator pipeline; see internal/generator.Run. Extension tests use it to
// exercise their generators against a loaded fixture.
func Generate(schema *ir.Schema, cfg *SchemaConfig, opts Options) (*Result, error) {
	return generator.Run(schema, cfg, opts)
}
