package topcoat

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// identityPagesModel is what identity_pages.tmpl renders: sign-in,
// sign-out, the optional sign-up, the account page, and the users and
// roles pages under the prefix (D44, amended), each calling the identity
// runtime's Service in-process.
type identityPagesModel struct {
	// SignUp reports whether the sign-up page is served:
	// pages.identity.signUp asks for it, or the API's
	// @userSessions({ register: true }) opens registration itself.
	SignUp bool
	// SignIn, Register and ChangePassword are the rate limits the pages
	// that sign in, sign up and change a password meet: the login,
	// register and changePassword routes' (D50).
	SignIn, Register, ChangePassword identityLimit
}

// identityLimit is the rate limit of a page that takes a password, whose
// limiter is in the app's registry (Setup.rate_limits) and counts a
// client by client_key, as a route's does. A page whose API serves the
// route takes the route's limiter, so a client has one budget on both
// ways in. On an API without the route, which the pages still serve (an
// API whose @userSessions has login: false, or none), the page has a
// limiter of its own at the route's rate, keyed by an OperationInfo of the
// crate's that no route has.
type identityLimit struct {
	// Operation is the API's OperationInfo constant of the route, empty
	// when the API does not serve it.
	Operation string
	// Name, Path and Rate are the page's own limiter's: its name in
	// the topcoat namespace, the page's path, and the route's requests per
	// minute (ir.Identity*RateLimit).
	Name, Path string
	Rate       int
}

// Info is the Rust expression of the limit's OperationInfo, the key of its
// limiter in the app's registry.
func (l identityLimit) Info() string {
	if l.Operation != "" {
		return "&crate::api::operations::" + l.Operation
	}
	return fmt.Sprintf("&OperationInfo { namespace: \"topcoat\", name: %s, method: \"POST\", path: %s, requires_auth: false, permissions: &[], require_ownership: false, manual: false, rate_limit: Some(%d), service: None }", rustString(l.Name), rustString(l.Path), l.Rate)
}

// identityPageWords are the first words below the prefix of the identity
// pages' paths, which no resource's pages may take.
var identityPageWords = []string{"sign-in", "sign-out", "sign-up", "account", "users", "roles"}

// identityPagesOf is the identity pages of the crate, nil when it has none:
// its pages are off, its API does not authenticate with the identity
// runtime (D50), pages.identity is false, or a resource's pages take one
// of their paths, which the build log says.
func identityPagesOf(c registry.GenerateContext, api *registry.RustAPI, cfg Config, out *crate) (*identityPagesModel, error) {
	if out.Pages == nil || !out.Identity || !cfg.PagesIdentity() {
		return nil, nil
	}
	prefix := out.Pages.Prefix
	for _, r := range out.Pages.Resources {
		word, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(r.Path, prefix), "/"), "/")
		for _, taken := range identityPageWords {
			if word == taken {
				c.Logf("  - topcoat: no sign-in, account, users or roles pages under %s: the pages of %s are at %s/%s, where the identity pages would be\n", prefix, r.Name, prefix, word)
				return nil, nil
			}
		}
	}
	m := &identityPagesModel{
		SignUp:         cfg.PagesSignUp() || registers(api),
		SignIn:         limitOf(api, ir.IdentityOpLogin, "signIn", prefix+"/sign-in", ir.IdentityLoginRateLimit),
		Register:       limitOf(api, ir.IdentityOpRegister, "signUp", prefix+"/sign-up", ir.IdentityRegisterRateLimit),
		ChangePassword: limitOf(api, ir.IdentityOpChangePassword, "changePassword", prefix+"/account", ir.IdentityChangePasswordRateLimit),
	}
	if !m.SignUp {
		c.Logf("  - topcoat: no sign-up page under %s: neither pages.identity.signUp nor the API's @userSessions({ register: true }) opens registration\n", prefix)
	}
	return m, nil
}

// limitOf is the rate limit of the page at path that does what the user
// model's operation does: the API's route's when it serves the operation
// with a rate limit, else the page's own at rate.
func limitOf(api *registry.RustAPI, operation, name, path string, rate int) identityLimit {
	for _, e := range api.IdentityEndpoints {
		if e.IdentityOperation == operation && e.RateLimit > 0 {
			return identityLimit{Operation: e.ConstName()}
		}
	}
	return identityLimit{Name: name, Path: path, Rate: rate}
}

// registers reports whether the API serves the user model's register
// route, @userSessions({ register: true }).
func registers(api *registry.RustAPI) bool {
	for _, e := range api.IdentityEndpoints {
		if e.IdentityOperation == ir.IdentityOpRegister {
			return true
		}
	}
	return false
}
