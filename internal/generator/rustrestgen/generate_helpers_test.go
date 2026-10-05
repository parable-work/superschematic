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

// apiOutputOf runs apigen on schema with the core session provider, as
// generator.Run builds the APIOutput every server and SDK shares.
func apiOutputOf(schema *ir.Schema, src apiSource, opts Options) (*apigen.APIOutput, error) {
	return apigen.Generate(schema, apigen.Options{
		SchemaName:     opts.SchemaName,
		IsPublic:       src.public,
		UpstreamSchema: src.upstream,
		UpstreamIR:     src.upstreamIR,
		Naming:         opts.Naming,
		Provider:       sessionauth.Provider{},
		Clock:          opts.Clock,
	})
}

// generateFrom runs Generate on schema's APIOutput (apiOutputOf).
func generateFrom(schema *ir.Schema, src apiSource, opts Options) (*APIOutput, error) {
	api, err := apiOutputOf(schema, src, opts)
	if err != nil {
		return nil, err
	}
	if src.upstreamIR != nil {
		opts.Dependencies = map[string]*ir.Schema{src.upstream: src.upstreamIR}
	}
	return Generate(schema, api, opts)
}
