package rustrestgen

import (
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	ir "github.com/parable-work/superschematic/ir"
)

// apiSource is what apigen needs from a test besides the schema: the part
// of a service's config generator.Run reads for its APIOutput.
type apiSource struct {
	public     bool
	upstream   string
	upstreamIR *ir.Schema
}

// generateFrom runs apigen on schema with the core session provider, as
// generator.Run builds the APIOutput every server and SDK shares, and
// Generate on its output.
func generateFrom(schema *ir.Schema, src apiSource, opts Options) (*APIOutput, error) {
	api, err := apigen.Generate(schema, apigen.Options{
		SchemaName:     opts.SchemaName,
		IsPublic:       src.public,
		UpstreamSchema: src.upstream,
		UpstreamIR:     src.upstreamIR,
		Naming:         opts.Naming,
		Provider:       sessionauth.Provider{},
		Clock:          opts.Clock,
	})
	if err != nil {
		return nil, err
	}
	return Generate(api, opts)
}
