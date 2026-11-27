package session

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/response"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// Caller is the principal a request runs as, whole (D156, D180): its id,
// its kind, whom it acts for, the grants it calls through and the
// credential it came with. It is access.Principal, which the generated
// ORM holds an operation's reads and writes to, so a generated API's
// auth-provider hook callerPrincipal answers one and passes it to the
// ORM as it is.
type Caller = access.Principal

type callerKey struct{}

// ContextWithCaller returns ctx carrying caller whole, and its id as the
// principal's ID (ContextWithPrincipalID), so a route's permission check
// and every reader of the ID see the same principal. An auth step that
// knows more of its principal than its id, its kind or its grants, puts
// it with this.
func ContextWithCaller(ctx context.Context, caller Caller) context.Context {
	ctx = ContextWithPrincipalID(ctx, caller.Subject)
	return context.WithValue(ctx, callerKey{}, caller)
}

// GetCaller is the principal the auth step put on ctx, whole: the one
// ContextWithCaller put, while its id is still the principal's ID; else a
// user whose id is the principal's ID; else the anonymous caller, the
// zero Caller. It is the core's answer to the callerPrincipal hook.
func GetCaller(ctx context.Context) Caller {
	id := GetPrincipalID(ctx)
	if caller, ok := ctx.Value(callerKey{}).(Caller); ok && caller.Subject == id {
		return caller
	}
	if id == "" {
		return Caller{}
	}
	return Caller{Subject: id, Kind: access.KindUser}
}

// GrantAuthenticator is the auth step of grants (D156, D180): a request
// whose Authorization is "Bearer grant_..." runs as the grant's holder, a
// party or any other holder but an agent run, whose token is refused here
// and resolved by the agent dispatcher's authenticator (agents'
// Authenticator, D192), with its role's permissions as its one role and
// the grant's reach, which authority establishes from the database on
// every call; every other request is inner's to establish, or has no
// principal when inner is nil. A token authority refuses, unknown, ended
// or revoked, is answered 401 with the refusal's message, so a grant that
// ended is refused at the very next call. now is the clock grants are
// held to; nil reads the system's.
func GrantAuthenticator(authority access.GrantAuthority, inner func(http.Handler) http.Handler, now func() time.Time) func(http.Handler) http.Handler {
	if now == nil {
		now = time.Now
	}
	return func(next http.Handler) http.Handler {
		other := next
		if inner != nil {
			other = inner(next)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := BearerToken(r.Header)
			if !ok || !strings.HasPrefix(token, access.GrantTokenPrefix) {
				other.ServeHTTP(w, r)
				return
			}
			found, err := authority.AuthenticateGrant(r.Context(), token, now())
			if err != nil {
				if errors.Is(err, access.ErrGrantRefused) {
					response.Error(w, http.StatusUnauthorized, refusalMessage(err))
					return
				}
				response.Error(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			// A grant an agent run holds is the run's token, which the agent
			// dispatcher's authenticator resolves (agents.Authenticator, D192):
			// as its grant's holder it would act with the agent role's
			// permissions, narrowed to no one it acts for and held to no
			// autonomy.
			if found.Principal.Kind == access.KindAgent {
				response.Error(w, http.StatusUnauthorized, "An agent run's token is authenticated by the agent dispatcher")
				return
			}
			ctx := ContextWithCaller(r.Context(), found.Principal)
			ctx = ContextWithRoles(ctx, []Role{{Name: found.Role, Permissions: found.Permissions}})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SecretMask is what a generated table operation shows of a value the
// caller may not reveal (D156), access.SecretMask.
const SecretMask = access.SecretMask

// RevealEntry is the access-log entry of a value the caller of ctx opened
// under a reveal permission (D156): the operation, the row by its table's
// type and key, and the field.
func RevealEntry(ctx context.Context, operation, targetType, targetID, field string) access.LogEntry {
	return access.RevealEntry(GetCaller(ctx), operation, targetType, targetID, field)
}

// AccessLogged records the call of operation in log (D156, D180), once
// the auth step established its caller and before the route's permission
// check, so a refused call is recorded too: an agent run's GET or HEAD,
// and any call made with an API key or a grant's token. A nil log records
// nothing. A log that fails answers 500, since a sensitive call is never
// served unrecorded.
func AccessLogged(log access.Log, operation string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if log == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if entry, ok := access.CallEntry(GetCaller(r.Context()), operation, r.Method); ok {
				if err := log.Record(r.Context(), entry); err != nil {
					response.Error(w, http.StatusInternalServerError, "Internal server error")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// refusalMessage is the message a refused grant is answered with: the
// text of the error that wraps access.ErrGrantRefused, before it.
func refusalMessage(err error) string {
	message := strings.TrimSuffix(strings.TrimSuffix(err.Error(), access.ErrGrantRefused.Error()), ": ")
	if message == "" {
		return "Authentication required"
	}
	return message
}
