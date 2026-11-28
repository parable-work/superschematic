package verify

import (
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkTeams checks a schema's teams (#735): the @team table, whose rows
// are teams an audience names by their ids, and the @teamMembers class,
// their membership, whose fields the expansion fills. A schema has one of
// each or neither: a team with no membership has no members, and a
// membership names teams of the @team table. The team table is a plain
// table of the database whose key is a UUID, as every id an audience
// holds is. The membership is a table of the database, one per schema,
// which declares no fields of its own (checkAccessTable).
func checkTeams(schema *ir.Schema, in Input, r *Result) {
	var teams []*ir.TypeDef
	for _, name := range sortedTypeNames(schema.Types) {
		if td := schema.Types[name]; td != nil && td.Team != nil {
			teams = append(teams, td)
		}
	}
	if len(teams) > 1 {
		names := make([]string, len(teams))
		for i, td := range teams {
			names[i] = td.Name
		}
		r.errorf(teams[1].Owner, "@team marks %s; a schema has one table of teams", strings.Join(names, " and "))
	}
	members := schema.TeamMembersTable()
	checkAccessTable(schema, in, "@teamMembers", func(td *ir.TypeDef) bool { return td.TeamMembers != nil }, r)
	for _, td := range teams {
		switch {
		case schema.Kind != ir.SchemaKindDB || td.Role != ir.RoleDBTable || td.Projection != nil:
			r.errorf(td.Owner, "@team on %s: the class is a table of a DB service, whose rows are teams", td.Name)
		case keyScalar(td) != "Identity.UUID":
			r.errorf(td.Owner, "@team on %s: its key is %s, and an audience names a team by its id, an Identity.UUID; key the table by an Identity.UUID", td.Name, keyScalarOrNone(td))
		case members == nil:
			r.errorf(td.Owner, "@team on %s: the schema has no @teamMembers class, so the teams have no members; declare one (@teamMembers() export abstract class TeamMember {})", td.Name)
		}
		refuseSQLiteReaders(schema, in, td, "@team", r)
	}
	if members != nil && len(teams) == 0 {
		r.errorf(members.Owner, "@teamMembers on %s: the schema has no @team table, whose teams the membership holds; mark the table of teams @team", members.Name)
	}
}

// keyScalarOrNone is td's key's scalar, or "no key".
func keyScalarOrNone(td *ir.TypeDef) string {
	if scalar := keyScalar(td); scalar != "" {
		return scalar
	}
	return "no key"
}
