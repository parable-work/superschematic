package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// AuthenticatorOptions are what Authenticator takes beside inner.
type AuthenticatorOptions struct {
	// OperationOf names a call to the run's autonomy policy: the
	// operation, its class and its mode when no policy row names it. The
	// method and the path, a read for GET and HEAD and an internal write
	// for any other, unless set; an app that knows its routes' operations
	// and their @autonomy names them here.
	OperationOf func(r *http.Request) (operation string, opts GateOptions)
	// Now is the clock the grants of a token that is no run's are held to.
	Now func() time.Time
}

// runPrincipalKey is the context key of a run's principal Authenticator
// established.
type runPrincipalKey struct{}

// RunPrincipalOf is the run's principal Authenticator established for the
// request of ctx; nil for any other caller.
func RunPrincipalOf(ctx context.Context) *RunPrincipal {
	p, _ := ctx.Value(runPrincipalKey{}).(*RunPrincipal)
	return p
}

// Authenticator is the auth step of a run's token on an app's own routes
// (D158, D183, D192), TypeScript's agents.authenticator and Rust's
// Agents::authenticator: a request whose bearer is a running run's token
// acts as the run's principal (PrincipalOf), its permissions the agent
// role's narrowed to whom it acts for holds as its one role, its reach the
// run's narrowed to a party's paths, and the read-only rows the
// repositories hold an agent run to; and each call is held to the run's
// autonomy before it runs (Gate): one that asks answers 428
// approval_required, one denied 403, and one approved runs once. Any other
// grant's token is session.GrantAuthenticator's over authority, which
// refuses a run's token as its grant's holder; every other request is
// inner's. Unlike TypeScript's and Rust's, whose routes are held to a run's
// autonomy only where the implementation asks the gate, every route the
// run calls is.
func (a *Agents) Authenticator(authority access.GrantAuthority, inner func(http.Handler) http.Handler, opts AuthenticatorOptions) func(http.Handler) http.Handler {
	grants := session.GrantAuthenticator(authority, inner, opts.Now)
	return func(next http.Handler) http.Handler {
		other := grants(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := session.BearerToken(r.Header)
			if !ok || !strings.HasPrefix(token, access.GrantTokenPrefix) {
				other.ServeHTTP(w, r)
				return
			}
			ctx := context.WithoutCancel(r.Context())
			principal, err := a.PrincipalOf(ctx, token)
			if err != nil {
				status, body := a.failure(err)
				writeJSON(w, status, body)
				return
			}
			if principal == nil {
				// A grant's token that is no running run's is any other
				// grant's; an ended run's is refused there.
				other.ServeHTTP(w, r)
				return
			}
			operation, gate := a.operationOf(r, opts)
			args, err := argsOf(r)
			if err != nil {
				refusal(w, 400, "invalid_body", "the body is no JSON object")
				return
			}
			gated, err := a.Gate(ctx, *principal, operation, args, gate)
			if err != nil {
				status, body := a.failure(err)
				writeJSON(w, status, body)
				return
			}
			if !gated.Run {
				writeJSON(w, gated.Status, map[string]any{"type": "about:blank", "title": gated.Code, "status": gated.Status, "code": gated.Code, "detail": gated.Detail, "approval": gated.Approval})
				return
			}
			called := session.ContextWithCaller(r.Context(), principal.Principal.Principal)
			called = session.ContextWithRoles(called, []session.Role{{Name: "agent-run", Permissions: principal.Permissions}})
			called = context.WithValue(called, runPrincipalKey{}, principal)
			// The run's Agent, which the core counts an Approvals sign-off
			// as (D158, D200).
			called = session.ContextWithAgent(called, principal.Agent)
			next.ServeHTTP(w, r.WithContext(called))
		})
	}
}

func (a *Agents) operationOf(r *http.Request, opts AuthenticatorOptions) (string, GateOptions) {
	if opts.OperationOf != nil {
		return opts.OperationOf(r)
	}
	class := InternalWrite
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		class = Read
	}
	return r.Method + " " + r.URL.Path, GateOptions{Class: class}
}

// argsOf are a call's parameters, which an approval is bound to: its JSON
// body's members and its query's, the body read and put back.
func argsOf(r *http.Request) (map[string]any, error) {
	args := map[string]any{}
	for name, values := range r.URL.Query() {
		if len(values) == 1 {
			args[name] = values[0]
		} else {
			args[name] = values
		}
	}
	if r.Body == nil {
		return args, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	if len(bytes.TrimSpace(data)) == 0 {
		return args, nil
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	for k, v := range body {
		args[k] = v
	}
	return args, nil
}
