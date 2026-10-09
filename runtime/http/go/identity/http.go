package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/http/go/apperror"
	"github.com/parable-work/superschematic/runtime/http/go/response"
	"github.com/parable-work/superschematic/runtime/http/go/routing"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// maxBodyBytes bounds an identity route's JSON body.
const maxBodyBytes = 64 << 10

type principalKey struct{}

// WithPrincipal returns a context carrying p, and p's id, name, session id
// and roles through the session package's helpers, so RequireAuth,
// RequirePermissions and every handler that reads them see the caller.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	ctx = context.WithValue(ctx, principalKey{}, p)
	ctx = session.ContextWithPrincipalID(ctx, p.ID)
	ctx = session.ContextWithPrincipalName(ctx, p.Name)
	ctx = session.ContextWithSessionID(ctx, p.SessionID)
	return session.ContextWithRoles(ctx, p.Roles)
}

// PrincipalFromContext returns the principal Middleware put on the
// request, and false when it put none.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Middleware authenticates every request (Authenticate) and puts the
// principal on its context (WithPrincipal). A request it cannot
// authenticate is answered with the problem: 401 unauthorized, or 403
// cross_origin; a refused session cookie is cleared with the 401. It is a
// generated server's AuthMiddleware for the routes that need a caller.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.principal(w, r)
		if !ok {
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// RequirePermissions is the permission check a generated server runs on a
// user model route that needs permissions (an administration route), in
// the route's place for one (after its rate and body limits and its
// service step, before its timeout). It admits the caller when the
// service's matcher gives their roles one of permissions, as the route's
// handler and capabilities do, and answers a refusal with the problem the
// routes' contract names (ir.IdentityOperationErrors): 401 unauthorized
// without a usable session, 403 forbidden otherwise.
func (s *Service) RequirePermissions(permissions ...string) func(http.Handler) http.Handler {
	required := append([]string(nil), permissions...)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := s.principal(w, r)
			if !ok {
				return
			}
			if !s.matcher(p.Roles, required) {
				WriteError(w, r, forbidden("Insufficient permissions", nil))
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// CORS is the credentialed CORS middleware for the config's trusted
// origins (package function CORS).
func (s *Service) CORS() func(http.Handler) http.Handler {
	return CORS(s.cfg.TrustedOrigins)
}

// principal is the request's principal: the one Middleware put on it, or
// one authenticated now. It answers the refusal itself and reports false.
func (s *Service) principal(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if p, ok := PrincipalFromContext(r.Context()); ok {
		return p, true
	}
	p, transport, err := s.authenticate(r)
	if err != nil {
		var appErr *apperror.AppError
		if transport == TransportCookie && errors.As(err, &appErr) && appErr.ErrorCode == CodeUnauthorized {
			http.SetCookie(w, s.cfg.ClearCookie())
		}
		WriteError(w, r, err)
		return Principal{}, false
	}
	return p, true
}

// Operation is one of the user model's routes: its name (an ir IdentityOp
// constant), the set it belongs to, its method and its path under the
// set's path, with {id} and {roleId} for its path parameters.
type Operation struct {
	Name string
	// Administration: the route is @userAdministration's, under its path
	// (ir.DefaultUserAdministrationPath by default); otherwise
	// @userSessions', under ir.DefaultUserSessionsPath.
	Administration bool
	Method         string
	Path           string
}

// Operations are every route of the user model, in ir/identity_routes.go's
// order.
var Operations = []Operation{
	{Name: ir.IdentityOpLogin, Method: http.MethodPost, Path: "login"},
	{Name: ir.IdentityOpLogout, Method: http.MethodPost, Path: "logout"},
	{Name: ir.IdentityOpMe, Method: http.MethodGet, Path: "me"},
	{Name: ir.IdentityOpCapabilities, Method: http.MethodGet, Path: "capabilities"},
	{Name: ir.IdentityOpChangePassword, Method: http.MethodPost, Path: "password"},
	{Name: ir.IdentityOpRegister, Method: http.MethodPost, Path: "register"},
	{Name: ir.IdentityOpCreateUser, Administration: true, Method: http.MethodPost, Path: "users"},
	{Name: ir.IdentityOpListUsers, Administration: true, Method: http.MethodGet, Path: "users"},
	{Name: ir.IdentityOpGetUser, Administration: true, Method: http.MethodGet, Path: "users/{id}"},
	{Name: ir.IdentityOpDisableUser, Administration: true, Method: http.MethodPost, Path: "users/{id}/disable"},
	{Name: ir.IdentityOpEnableUser, Administration: true, Method: http.MethodPost, Path: "users/{id}/enable"},
	{Name: ir.IdentityOpSetUserPassword, Administration: true, Method: http.MethodPut, Path: "users/{id}/password"},
	{Name: ir.IdentityOpListRoles, Administration: true, Method: http.MethodGet, Path: "roles"},
	{Name: ir.IdentityOpCreateRole, Administration: true, Method: http.MethodPost, Path: "roles"},
	{Name: ir.IdentityOpUpdateRole, Administration: true, Method: http.MethodPut, Path: "roles/{id}"},
	{Name: ir.IdentityOpDeleteRole, Administration: true, Method: http.MethodDelete, Path: "roles/{id}"},
	{Name: ir.IdentityOpGrantRole, Administration: true, Method: http.MethodPut, Path: "users/{id}/roles/{roleId}"},
	{Name: ir.IdentityOpRevokeRole, Administration: true, Method: http.MethodDelete, Path: "users/{id}/roles/{roleId}"},
}

// Handler returns the handler of the operation named op (an ir
// IdentityOp constant), and false for a name that is none. A handler of a
// route that needs a caller reads the principal Middleware put on the
// request, or authenticates the request itself when there is none.
// Success is the generated servers' envelope ({"data": ..., "meta":
// {"requestId": ...}}) with 200; logout, changePassword, setUserPassword
// and deleteRole, which the contract types as the boolean true, answer
// {"data": true, ...}. A refusal is the problem WriteError writes.
func (s *Service) Handler(op string) (http.Handler, bool) {
	var h http.HandlerFunc
	switch op {
	case ir.IdentityOpLogin:
		h = s.serveLogin
	case ir.IdentityOpRegister:
		h = s.serveRegister
	case ir.IdentityOpLogout:
		h = s.authed(func(w http.ResponseWriter, r *http.Request, p Principal) {
			if err := s.Logout(r.Context(), p); err != nil {
				WriteError(w, r, err)
				return
			}
			if p.Transport == TransportCookie {
				http.SetCookie(w, s.cfg.ClearCookie())
			}
			respond(w, r, true, nil)
		})
	case ir.IdentityOpMe:
		h = s.authed(func(w http.ResponseWriter, r *http.Request, p Principal) { respond(w, r, s.Me(p), nil) })
	case ir.IdentityOpCapabilities:
		h = s.authed(func(w http.ResponseWriter, r *http.Request, p Principal) { respond(w, r, s.Capabilities(p), nil) })
	case ir.IdentityOpChangePassword:
		h = s.authed(func(w http.ResponseWriter, r *http.Request, p Principal) {
			var in ChangePasswordInput
			if decode(w, r, &in) {
				respondTrue(w, r, s.ChangePassword(r.Context(), p, in))
			}
		})
	case ir.IdentityOpCreateUser:
		h = s.authed(func(w http.ResponseWriter, r *http.Request, p Principal) {
			var in CreateUserInput
			if decode(w, r, &in) {
				out, err := s.CreateUser(r.Context(), p, in)
				respond(w, r, out, err)
			}
		})
	case ir.IdentityOpListUsers:
		h = s.authed(func(w http.ResponseWriter, r *http.Request, p Principal) {
			out, err := s.ListUsers(r.Context(), p)
			respondList(w, r, out, err)
		})
	case ir.IdentityOpGetUser:
		h = s.withID(func(w http.ResponseWriter, r *http.Request, p Principal, id string) {
			out, err := s.GetUser(r.Context(), p, id)
			respond(w, r, out, err)
		})
	case ir.IdentityOpDisableUser:
		h = s.withID(func(w http.ResponseWriter, r *http.Request, p Principal, id string) {
			out, err := s.DisableUser(r.Context(), p, id)
			respond(w, r, out, err)
		})
	case ir.IdentityOpEnableUser:
		h = s.withID(func(w http.ResponseWriter, r *http.Request, p Principal, id string) {
			out, err := s.EnableUser(r.Context(), p, id)
			respond(w, r, out, err)
		})
	case ir.IdentityOpSetUserPassword:
		h = s.withID(func(w http.ResponseWriter, r *http.Request, p Principal, id string) {
			var in SetPasswordInput
			if decode(w, r, &in) {
				respondTrue(w, r, s.SetUserPassword(r.Context(), p, id, in))
			}
		})
	case ir.IdentityOpListRoles:
		h = s.authed(func(w http.ResponseWriter, r *http.Request, p Principal) {
			out, err := s.ListRoles(r.Context(), p)
			respondList(w, r, out, err)
		})
	case ir.IdentityOpCreateRole:
		h = s.authed(func(w http.ResponseWriter, r *http.Request, p Principal) {
			var in RoleInput
			if decode(w, r, &in) {
				out, err := s.CreateRole(r.Context(), p, in)
				respond(w, r, out, err)
			}
		})
	case ir.IdentityOpUpdateRole:
		h = s.withID(func(w http.ResponseWriter, r *http.Request, p Principal, id string) {
			var in RoleInput
			if decode(w, r, &in) {
				out, err := s.UpdateRole(r.Context(), p, id, in)
				respond(w, r, out, err)
			}
		})
	case ir.IdentityOpDeleteRole:
		h = s.withID(func(w http.ResponseWriter, r *http.Request, p Principal, id string) {
			respondTrue(w, r, s.DeleteRole(r.Context(), p, id))
		})
	case ir.IdentityOpGrantRole, ir.IdentityOpRevokeRole:
		change := s.GrantRole
		if op == ir.IdentityOpRevokeRole {
			change = s.RevokeRole
		}
		h = s.withID(func(w http.ResponseWriter, r *http.Request, p Principal, id string) {
			roleID, ok := pathParam(w, r, "roleId")
			if !ok {
				return
			}
			out, err := change(r.Context(), p, id, roleID)
			respond(w, r, out, err)
		})
	default:
		return nil, false
	}
	return h, true
}

// Routes are the routes of the sets an API declares, for routing.Register:
// the session routes when sessions is not nil, under its path (without
// login, logout and changePassword when NoLogin, and with register only
// when Register), and the administration routes when admin is not nil,
// under its path. Paths start with "/"; the caller adds its server's own
// prefix.
func (s *Service) Routes(sessions *ir.UserSessionsConfig, admin *ir.UserAdministrationConfig) []routing.Route {
	var routes []routing.Route
	for _, op := range Operations {
		var base string
		switch {
		case op.Administration && admin != nil:
			base = admin.UserAdministrationPath()
		case op.Administration:
			continue
		case sessions == nil:
			continue
		case sessions.NoLogin && (op.Name == ir.IdentityOpLogin || op.Name == ir.IdentityOpLogout || op.Name == ir.IdentityOpChangePassword || op.Name == ir.IdentityOpRegister):
			continue
		case !sessions.Register && op.Name == ir.IdentityOpRegister:
			continue
		default:
			base = sessions.UserSessionsPath()
		}
		h, _ := s.Handler(op.Name)
		routes = append(routes, routing.Route{Method: op.Method, Path: "/" + strings.Trim(base, "/") + "/" + op.Path, Handler: h})
	}
	return routes
}

func (s *Service) serveLogin(w http.ResponseWriter, r *http.Request) {
	var in LoginInput
	if !decode(w, r, &in) || !s.checkCookieLogin(w, r, in.Session) {
		return
	}
	issued, err := s.Login(r.Context(), in)
	s.respondIssued(w, r, issued, err)
}

func (s *Service) serveRegister(w http.ResponseWriter, r *http.Request) {
	var in RegisterInput
	if !decode(w, r, &in) || !s.checkCookieLogin(w, r, in.Session) {
		return
	}
	issued, err := s.Register(r.Context(), in)
	s.respondIssued(w, r, issued, err)
}

// checkCookieLogin runs the cross-origin check on a login that asks for a
// cookie session, since its cookie would sign the browser in.
func (s *Service) checkCookieLogin(w http.ResponseWriter, r *http.Request, t Transport) bool {
	if t != TransportCookie {
		return true
	}
	if err := s.cop.Check(r); err != nil {
		WriteError(w, r, crossOrigin(err))
		return false
	}
	return true
}

func (s *Service) respondIssued(w http.ResponseWriter, r *http.Request, issued IssuedSession, err error) {
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if issued.Transport == TransportCookie {
		http.SetCookie(w, s.cfg.SessionCookie(issued.Token, s.cfg.SessionTTL()))
	}
	respond(w, r, issued.Result, nil)
}

// authed is a handler of a route that needs a caller.
func (s *Service) authed(h func(http.ResponseWriter, *http.Request, Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p, ok := s.principal(w, r); ok {
			h(w, r, p)
		}
	}
}

// withID is authed with the route's {id}.
func (s *Service) withID(h func(http.ResponseWriter, *http.Request, Principal, string)) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request, p Principal) {
		if id, ok := pathParam(w, r, "id"); ok {
			h(w, r, p, id)
		}
	})
}

// pathParam reads a path parameter: chi's, decoded once as
// routing.PathParam does, or the standard mux's.
func pathParam(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	var value string
	if chi.RouteContext(r.Context()) != nil {
		v, err := routing.PathParam(r, name)
		if err != nil {
			WriteError(w, r, apperror.BadRequestError(name+" must be percent-encoded UTF-8", err).WithCode(response.ValidationCode))
			return "", false
		}
		value = v
	} else {
		value = r.PathValue(name)
	}
	if value == "" {
		WriteError(w, r, apperror.BadRequestError(name+" is required", nil).WithCode(response.ValidationCode))
		return "", false
	}
	return value, true
}

// decode reads a JSON body into v, refusing unknown members, a second
// value and a body over maxBodyBytes with 400 bad_request.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err = dec.Decode(v); err == nil && dec.More() {
			err = errors.New("a second JSON value follows the body")
		}
	}
	if err != nil {
		WriteError(w, r, apperror.BadRequestError("The body is not the operation's JSON input: "+err.Error(), err).WithCode(response.ValidationCode))
		return false
	}
	return true
}

func meta(r *http.Request) *response.Meta {
	return &response.Meta{RequestID: chimiddleware.GetReqID(r.Context())}
}

func respond(w http.ResponseWriter, r *http.Request, data any, err error) {
	if err != nil {
		WriteError(w, r, err)
		return
	}
	response.JSONEnvelope(w, http.StatusOK, data, meta(r), nil)
}

func respondList(w http.ResponseWriter, r *http.Request, data any, err error) {
	if err != nil {
		WriteError(w, r, err)
		return
	}
	response.CollectionEnvelope(w, http.StatusOK, data, meta(r), nil)
}

// respondTrue answers an operation the contract types as the boolean
// true: {"data": true, ...} with 200, as every generated server answers
// one, or the problem err is.
func respondTrue(w http.ResponseWriter, r *http.Request, err error) {
	respond(w, r, true, err)
}
