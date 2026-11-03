# A site's Worker serving its origin under workerd (D91,
# docs/stack-model.md, section 18.8): the script the extension embeds
# (site.js), with the bindings the cloudflare.sites platform gives the
# origin web fixture's Staging version, a stand-in for its static assets
# (../site/assets.js), and for shop-storefront, the API of its origin, a
# Worker (api.js) that runs the TypeScript identity runtime's own
# cross-origin check and session cookie over its defaults, bound as
# SITE_API_SHOP_STOREFRONT. SITE_ORIGIN also names shop-pricing at a public
# address of another provider, which the site's fetch reaches through its
# globalOutbound, a stand-in for the internet (upstream.js). The tester
# (tester.js) reaches the site through a service binding, as a browser's
# request reaches its Worker. TestSiteOriginUnderWorkerd writes the embedded
# script and the bundled API beside this file and puts the fixture's
# config, origin and public address in place of $SITE_CONFIG, $SITE_ORIGIN
# and $SITE_URL, then runs `workerd test`.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "site", worker = .site),
    (name = "assets", worker = .assets),
    (name = "api", worker = .api),
    (name = "upstream", worker = .upstream),
    (name = "tester", worker = .tester),
  ],
);

const site :Workerd.Worker = (
  modules = [(name = "site.js", esModule = embed "site.js")],
  bindings = [
    (name = "ASSETS", service = "assets"),
    (name = "SITE_CONFIG", text = "$SITE_CONFIG"),
    (name = "SITE_FALLBACK", text = "index.html"),
    (name = "SITE_ORIGIN", text = "$SITE_ORIGIN"),
    (name = "SITE_API_SHOP_STOREFRONT", service = "api"),
  ],
  globalOutbound = "upstream",
  compatibilityDate = "2026-09-15",
);

const assets :Workerd.Worker = (
  modules = [(name = "assets.js", esModule = embed "assets.js")],
  compatibilityDate = "2026-09-15",
);

const api :Workerd.Worker = (
  modules = [(name = "api.js", esModule = embed "api.js")],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_compat"],
);

const upstream :Workerd.Worker = (
  modules = [(name = "upstream.js", esModule = embed "upstream.js")],
  compatibilityDate = "2026-09-15",
);

const tester :Workerd.Worker = (
  modules = [(name = "tester.js", esModule = embed "tester.js")],
  bindings = [
    (name = "SITE", service = "site"),
    (name = "SITE_URL", text = "$SITE_URL"),
    (name = "WANT_CONFIG", text = "$SITE_CONFIG"),
  ],
  compatibilityDate = "2026-09-15",
);
