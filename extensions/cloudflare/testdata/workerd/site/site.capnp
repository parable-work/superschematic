# A site's Worker under workerd (D64, docs/stack-model.md, section 18.8):
# the script the extension embeds (site.js), with the bindings the
# cloudflare.sites platform gives its version, and a stand-in for its
# static assets (assets.js), since workerd has none of its own. "site"
# falls back to index.html and "bare" has no fallback. The tester
# (tester.js) reaches each through a service binding, as Cloudflare hands
# the script a path that names no file. TestSiteScriptUnderWorkerd writes
# the embedded script beside this file and puts the web fixture's Staging
# config in place of $SITE_CONFIG, then runs `workerd test`.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "site", worker = .site),
    (name = "bare", worker = .bare),
    (name = "assets", worker = .assets),
    (name = "tester", worker = .tester),
  ],
);

const site :Workerd.Worker = (
  modules = [(name = "site.js", esModule = embed "site.js")],
  bindings = [
    (name = "ASSETS", service = "assets"),
    (name = "SITE_CONFIG", text = "$SITE_CONFIG"),
    (name = "SITE_FALLBACK", text = "index.html"),
  ],
  compatibilityDate = "2026-09-15",
);

const bare :Workerd.Worker = (
  modules = [(name = "site.js", esModule = embed "site.js")],
  bindings = [
    (name = "ASSETS", service = "assets"),
    (name = "SITE_CONFIG", text = "$SITE_CONFIG"),
  ],
  compatibilityDate = "2026-09-15",
);

const assets :Workerd.Worker = (
  modules = [(name = "assets.js", esModule = embed "assets.js")],
  compatibilityDate = "2026-09-15",
);

const tester :Workerd.Worker = (
  modules = [(name = "tester.js", esModule = embed "tester.js")],
  bindings = [
    (name = "SITE", service = "site"),
    (name = "BARE", service = "bare"),
    (name = "WANT_CONFIG", text = "$SITE_CONFIG"),
  ],
  compatibilityDate = "2026-09-15",
);
