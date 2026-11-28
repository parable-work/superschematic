package ir

import "fmt"

// A team in an audience (#735). A DB table marked @team holds teams: its
// rows are principals an @audience row's audienceNamed may name by their
// ids, as it names users. The class marked @teamMembers is the membership
// table, whose fields the loader fills ([TeamMemberFields]): each row puts
// a member, a user's or another team's id, in a team. Teams nest: a
// caller's ids are its own and those of every team it belongs to, directly
// or through another team, which the authenticator resolves onto the
// principal (its teams) on every call, with one recursive read that a
// cycle in the membership ends. A read of an @audience table keeps to the
// rows whose effective audience holds one of the caller's ids, so a team
// named there admits each of its members, and a member removed from the
// team is refused at its next call.

// TeamDef is the @team declaration of a DB table (#735): its rows are
// teams. A schema has at most one, beside its @teamMembers class.
type TeamDef struct{}

// TeamMembersDef is the @teamMembers declaration of a DB class (#735):
// the class is the membership of the @team table's teams, whose fields
// the loader fills. A schema has at most one.
type TeamMembersDef struct{}

// The fields of a @teamMembers table, which the loader adds.
const (
	// TeamMemberTeamField is the team, a row of the @team table, by its key.
	TeamMemberTeamField = "team"
	// TeamMemberMemberField is the member: a user's id, or another team's,
	// which nests that team in this one.
	TeamMemberMemberField = "member"
	// TeamMemberCreatedAtField is when the member joined.
	TeamMemberCreatedAtField = "createdAt"
)

// TeamMemberFields are the fields of a @teamMembers table beside its key,
// in order.
var TeamMemberFields = []string{TeamMemberTeamField, TeamMemberMemberField, TeamMemberCreatedAtField}

// TeamTable returns the schema's @team table, or nil.
func (s *Schema) TeamTable() *TypeDef {
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.Team != nil {
			return td
		}
	}
	return nil
}

// TeamMembersTable returns the schema's @teamMembers table, or nil.
func (s *Schema) TeamMembersTable() *TypeDef {
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.TeamMembers != nil {
			return td
		}
	}
	return nil
}

// validateTeams checks what td's @team or @teamMembers alone decides: a
// class is one of the two at most, and neither is an access table's own.
func validateTeams(td *TypeDef) []error {
	var errs []error
	if td.Team != nil && td.TeamMembers != nil {
		errs = append(errs, fmt.Errorf("%s: @team marks the table of teams and @teamMembers their membership; a class is one of them", td.Name))
	}
	if (td.Team != nil || td.TeamMembers != nil) && (td.AccessGrants != nil || td.AccessLog != nil) {
		errs = append(errs, fmt.Errorf("%s: @team and @teamMembers mark tables of their own, and %s is an access table's", td.Name, td.Name))
	}
	if td.TeamMembers != nil && td.Audience != nil {
		errs = append(errs, fmt.Errorf("%s: @teamMembers is the membership the authenticator reads, and takes no @audience", td.Name))
	}
	return errs
}
