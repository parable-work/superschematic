package topcoat

import (
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
	m := &identityPagesModel{SignUp: cfg.PagesSignUp() || registers(api)}
	if !m.SignUp {
		c.Logf("  - topcoat: no sign-up page under %s: neither pages.identity.signUp nor the API's @userSessions({ register: true }) opens registration\n", prefix)
	}
	return m, nil
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
