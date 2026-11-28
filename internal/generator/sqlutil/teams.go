package sqlutil

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// A caller's teams (#735): the teams of the @team table its ids are
// members of, directly or through another team, which the authenticator
// reads on every call and puts on the principal, so an audience that
// names a team admits its members. The read is one recursive statement
// over the @teamMembers table: the teams the starting ids are members of,
// then the teams each of those is a member of, and so on. UNION drops a
// team the read has already found, so a cycle in the membership (a team
// that is, through others, a member of itself) adds nothing and the read
// ends. It answers one column, id, a team's key, once each.
//
// Its one parameter is the ids to start from: on Postgres a UUID[] ($1),
// on SQLite a JSON array of their text (?1), which json_each reads, as an
// audience's effective audience is read there (D182).

// AccessTeamMembers is what the @teamMembers table is to the runtime's
// per-record access (TableModel.Access, #735).
const AccessTeamMembers = "teamMembers"

// TeamsSQL is the statement that reads the teams the ids of its parameter
// are members of, through every nesting, over table, the @teamMembers
// table, on Postgres; SQLiteTeamsSQL is the same on SQLite.
func TeamsSQL(table TableModel) (string, error) {
	return teamsSQL(table, "= ANY($1::uuid[])")
}

// SQLiteTeamsSQL is TeamsSQL on SQLite.
func SQLiteTeamsSQL(table TableModel) (string, error) {
	return teamsSQL(table, "IN (SELECT value FROM json_each(?1))")
}

func teamsSQL(table TableModel, starts string) (string, error) {
	var team, member string
	for _, c := range table.Columns {
		switch c.Field {
		case ir.TeamMemberTeamField:
			team = c.Quoted
		case ir.TeamMemberMemberField:
			member = c.Quoted
		}
	}
	if team == "" || member == "" {
		return "", fmt.Errorf("@teamMembers on %s: table %s has no %s or no %s column", table.Type, table.Name, ir.TeamMemberTeamField, ir.TeamMemberMemberField)
	}
	return fmt.Sprintf("WITH RECURSIVE teams(id) AS (SELECT m.%s FROM %s AS m WHERE m.%s %s UNION SELECT m.%s FROM %s AS m JOIN teams ON m.%s = teams.id) SELECT id FROM teams",
		team, table.Quoted, member, starts, team, table.Quoted, member), nil
}
