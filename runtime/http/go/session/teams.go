package session

import (
	"net/http"

	"github.com/parable-work/superschematic/runtime/http/go/response"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// TeamAuthenticator is the auth step of a database's teams (#735): auth,
// the step that establishes a request's caller, then the caller's teams,
// which authority reads on every call, put on the caller (Caller.Teams),
// so an audience that names a team admits each of its members and a
// member removed from a team is refused its rows at the next call. A
// request auth establishes no caller for stays anonymous; a caller in no
// team gets none. A read that fails answers 500, since a caller is never
// held to fewer ids than it has without saying so. A nil auth is the step
// that establishes no caller.
func TeamAuthenticator(authority access.TeamAuthority, auth func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		withTeams := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := GetCaller(r.Context())
			subjects := access.TeamSubjects(caller)
			if len(subjects) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			teams, err := authority.Teams(r.Context(), subjects)
			if err != nil {
				response.Error(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			if len(teams) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			caller.Teams = teams
			next.ServeHTTP(w, r.WithContext(ContextWithCaller(r.Context(), caller)))
		})
		if auth == nil {
			return withTeams
		}
		return auth(withTeams)
	}
}
