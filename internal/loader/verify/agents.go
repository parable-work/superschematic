package verify

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/loader/agents"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkAgents checks a schema's agent orchestration classes (D158), once
// the agents expansion added their parts' fields.
//
// Each part is declared once: the requests part on a @queue class and
// every other on a table. The definitions are @versioned, since a run
// records the version it ran. A schema that declares any part declares
// them all. A field a class declares under a name its part's fields use
// keeps the part's scalar, but for the runs' status, which may be the
// Workflow that Lease and Retries move (D104). The orchestration is built
// for Postgres alone: its dispatcher enqueues and claims through the
// Postgres queue client, and its tokens are D156's grants.
func checkAgents(schema *ir.Schema, in Input, r *Result) {
	if !schema.UsesAgents() {
		return
	}
	var first *ir.TypeDef
	for _, name := range sortedTypeNames(schema.Types) {
		if td := schema.Types[name]; td != nil && td.Agents != nil {
			first = td
			break
		}
	}
	if schema.Kind != ir.SchemaKindDB {
		r.errorf(first.Owner, "%s: the agent orchestration's tables are a DB service's, and %s is a %s service (D158)", first.Name, schema.Name, schema.Kind)
		return
	}
	if slices.Contains(in.Dialects, registry.SQLDialectSQLite) {
		r.errorf(first.Owner, "%s: %s lists the sqlite dialect, and the agent dispatcher enqueues and claims through the Postgres queue client alone so far; build %s for postgres alone (outputs.sql.dialects: [\"postgres\"]) (D158)", first.Name, schema.Name, schema.Name)
	}
	parts := map[string]*ir.TypeDef{}
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || td.Agents == nil {
			continue
		}
		decorator := `@agents("` + td.Agents.Part + `")`
		if other := parts[td.Agents.Part]; other != nil {
			r.errorf(td.Owner, "%s marks %s and %s; a schema has one", decorator, other.Name, td.Name)
			continue
		}
		parts[td.Agents.Part] = td
		queue := td.Agents.Part == ir.AgentRequests
		switch {
		case queue && td.Queue == nil:
			r.errorf(td.Owner, "%s on %s: the requests part is the dispatcher's queue; mark the class @queue() too", decorator, td.Name)
		case !queue && (td.Role != ir.RoleDBTable || td.Queue != nil):
			r.errorf(td.Owner, "%s on %s: the class is a table of the database, and no queue", decorator, td.Name)
		case td.Agents.Part == ir.AgentDefinitions && !td.Versioned:
			r.errorf(td.Owner, "%s on %s: agent definitions are @versioned, since a run records the version of the definition it ran; mark the class @versioned", decorator, td.Name)
		}
		checkAgentFields(td, decorator, r)
	}
	var missing []string
	for _, part := range ir.AgentParts {
		if parts[part] == nil {
			missing = append(missing, `@agents("`+part+`")`)
		}
	}
	if len(missing) > 0 {
		r.errorf(first.Owner, "%s uses the agent orchestration (D158), and declares no %s: the dispatcher reads and writes each; declare a class for each", schema.Name, strings.Join(missing, ", "))
	}
}

// checkAgentFields holds a field td declares under one of its part's names
// to the part's scalar and requiredness.
func checkAgentFields(td *ir.TypeDef, decorator string, r *Result) {
	expected := map[string]*ir.FieldDef{}
	fields, _ := agents.PartFields(td.Agents.Part)
	for _, fd := range fields {
		expected[fd.Name] = fd
	}
	for _, fd := range td.Fields {
		if fd == nil || fd.Origin == ir.OriginAgents || fd.Key {
			continue
		}
		want := expected[fd.Name]
		if want == nil {
			continue
		}
		if td.Agents.Part == ir.AgentRuns && fd.Name == "status" {
			continue
		}
		if fd.TypeRef.Name != want.TypeRef.Name || fd.TypeRef.IsArray != want.TypeRef.IsArray {
			r.errorf(td.Owner, "%s on %s: %s is the orchestration's field, a %s; declare it as one or leave it to the build", decorator, td.Name, fd.Name, want.TypeRef.Name)
		}
	}
}
