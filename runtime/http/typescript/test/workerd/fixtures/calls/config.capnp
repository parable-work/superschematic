# The calls fixture: two Workers, shop-storefront and shop-pricing, with
# the bindings the cloudflare target gives them in extensions/cloudflare's
# calls-stack Staging (its golden environment.json, section 18.2 of
# docs/stack-model.md), each output the graph holds in place of its value:
# shop-storefront reaches the internal shop-pricing through a service
# binding alone, signing a token with the edge's key (section 18.10), and
# the bucket shop-media through an r2_bucket binding, here a stand-in
# (standins.js), with the edge's R2 credential. extensions/cloudflare's
# TestWorkerdCallsFixtureHoldsTheGoldenBindings holds these bindings to
# the golden's. scripts/workerd-test.mjs writes the bundles beside this
# file in out/calls/, and puts the edge's key pair, which it generates, in
# place of $CALLER_KEY_PEM and $CALLER_PUBLIC_PEM.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "shop-storefront", worker = .storefront),
    (name = "shop-pricing", worker = .pricing),
  ],
  extensions = [(
    modules = [(name = "superschematic-fixture:calls-standins", esModule = embed "standins.js", internal = true)],
  )],
);

const storefront :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "storefront.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "LOG_LEVEL", text = "info"),
    (name = "SHOP_MEDIA_BUCKET", wrapped = (moduleName = "superschematic-fixture:calls-standins", entrypoint = "r2")),
    (name = "SHOP_MEDIA_BUCKET_BINDING", text = "SHOP_MEDIA_BUCKET"),
    (name = "SHOP_MEDIA_BUCKET_NAME", text = "calls-stack-shop-media"),
    (name = "SHOP_MEDIA_BUCKET_R2_ACCESS_KEY_ID", text = "8f2b4d6e0a1c3e5f7b9d1f3a5c7e9b0d"),
    (name = "SHOP_MEDIA_BUCKET_R2_ACCOUNT", text = "0123456789abcdef0123456789abcdef"),
    (name = "SHOP_MEDIA_BUCKET_R2_TOKEN", text = "r2-token-value"),
    (name = "SHOP_PRICING_SERVICE", service = "shop-pricing"),
    (name = "SHOP_PRICING_SERVICE_BINDING_NAME", text = "SHOP_PRICING_SERVICE"),
    (name = "SHOP_PRICING_SERVICE_BINDING_WORKER", text = "shop-pricing"),
    (name = "SHOP_PRICING_SERVICE_CREDENTIAL_AUDIENCE", text = "shop-pricing"),
    (name = "SHOP_PRICING_SERVICE_CREDENTIAL_ISSUER", text = "shop-storefront"),
    (name = "SHOP_PRICING_SERVICE_CREDENTIAL_KEY", text = "$CALLER_KEY_PEM"),
    (name = "SHOP_PRICING_SERVICE_CREDENTIAL_SOURCE", text = "signed-token"),
    (name = "SHOP_PRICING_SERVICE_URL", text = "https://shop-pricing"),
  ],
  compatibilityDate = "2026-09-15",
);

const pricing :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "pricing.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "SHOP_PRICING_CALLERS_ISSUERS", text = "1"),
    (name = "SHOP_PRICING_CALLERS_ISSUERS_0_ALGORITHMS", text = "EdDSA"),
    (name = "SHOP_PRICING_CALLERS_ISSUERS_0_AUDIENCE", text = "shop-pricing"),
    (name = "SHOP_PRICING_CALLERS_ISSUERS_0_CALLERS", text = "1"),
    (name = "SHOP_PRICING_CALLERS_ISSUERS_0_CALLERS_0_DEPLOYABLE", text = "shop-storefront"),
    (name = "SHOP_PRICING_CALLERS_ISSUERS_0_CALLERS_0_SERVES", text = "shop-storefront"),
    (name = "SHOP_PRICING_CALLERS_ISSUERS_0_CALLERS_0_SUBJECT", text = "shop-storefront"),
    (name = "SHOP_PRICING_CALLERS_ISSUERS_0_ISSUER", text = "shop-storefront"),
    (name = "SHOP_PRICING_CALLERS_ISSUERS_0_KEYS", text = "1"),
    (name = "SHOP_PRICING_CALLERS_ISSUERS_0_KEYS_0_PEM", text = "$CALLER_PUBLIC_PEM"),
    (name = "SHOP_PRICING_CALLERS_ISSUERS_0_MAX_LIFETIME_SECONDS", text = "300"),
  ],
  compatibilityDate = "2026-09-15",
);
