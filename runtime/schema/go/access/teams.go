package access

import "context"

// TeamAuthority reads the teams a principal belongs to (#735): the
// generated ORM of a database with a @teamMembers table implements it, on
// Postgres and on SQLite. Teams answers the teams ids are members of,
// directly or through another team, once each, read on every call, so a
// member removed from a team is refused the team's rows at its next call;
// a cycle in the membership ends the read.
type TeamAuthority interface {
	Teams(ctx context.Context, ids []string) ([]string, error)
}

// TeamSubjects are the ids p's teams are read from: its own and, for an
// agent run, whom it acts for, whose audience it sees too. None for the
// anonymous caller.
func TeamSubjects(p Principal) []string {
	if p.Subject == "" {
		return nil
	}
	ids := []string{p.Subject}
	if p.KindOrUser() == KindAgent && p.ActingFor != nil && p.ActingFor.Subject != "" {
		ids = append(ids, p.ActingFor.Subject)
	}
	return ids
}

// AudienceSubjects are the ids an audience compares p with (D156, #735):
// its own, whom an agent run acts for, and the teams it belongs to.
func AudienceSubjects(p Principal) []string {
	subjects := []string{p.Subject}
	if p.Kind == KindAgent && p.ActingFor != nil {
		subjects = append(subjects, p.ActingFor.Subject)
	}
	return append(subjects, p.Teams...)
}
