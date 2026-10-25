package topcoat

import "github.com/parable-work/superschematic/registry"

// identityPagesModel is what identity_pages.tmpl renders: sign-in,
// sign-out, the optional sign-up, the account page, and the users and
// roles pages under the prefix (D44, amended), each calling the identity
// runtime's Service in-process.
type identityPagesModel struct{}

// identityPagesOf is the identity pages of the crate, nil when it has none:
// its pages are off, its API does not authenticate with the identity
// runtime (D50), or pages.identity is false. The identity pages are not
// built yet, so the crate has none, and the build log says so where it
// would.
func identityPagesOf(c registry.GenerateContext, api *registry.RustAPI, cfg Config, out *crate) (*identityPagesModel, error) {
	if out.Pages == nil || !out.Identity || !cfg.PagesIdentity() {
		return nil, nil
	}
	c.Logf("  - topcoat: no sign-in, account, users or roles pages under %s: the identity pages are not built yet\n", out.Pages.Prefix)
	return nil, nil
}
