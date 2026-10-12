package rustrestgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// apiSource is what apigen needs from a test besides the schema: the part
// of a service's config generator.Run reads for its APIOutput.
type apiSource struct {
	public     bool
	upstream   string
	upstreamIR *ir.Schema
	// authDB is the schema the API's authDb names, which the crate reads
	// the user model's tables from (D50); nil without one.
	authDB *ir.Schema
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
	opts.AuthDB = src.authDB
	return Generate(schema, api, opts)
}

// authDBOf loads the fixture schema's authDb as a build does, for the user
// model's tables (D50); nil when it names none, or one the fixtures do not
// have.
func authDBOf(t *testing.T, schema *ir.Schema) *ir.Schema {
	t.Helper()
	if schema.AuthDB == "" {
		return nil
	}
	dir := filepath.Join(fixturesDir, schema.AuthDB)
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	authDB, err := loader.LoadService(dir)
	if err != nil {
		t.Fatalf("load %s, the authDb of %s: %v", schema.AuthDB, schema.Name, err)
	}
	return authDB
}
