# The storefront fixture: two Workers, storefront and pricing, configured
# as the cloudflare target configures them (section 18.2 of
# docs/stack-model.md): each literal and derived variable a text binding,
# the D1 database, the R2 bucket and the Secrets Store secret wrapped
# stand-ins (standins.js), and pricing reached through a service binding
# alone. scripts/workerd-test.mjs writes the bundles beside this file in
# out/app/, and puts the edge's key pair, which it generates, in place of
# $CALLER_KEY_PEM and $CALLER_PUBLIC_PEM.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "storefront", worker = .storefront),
    (name = "pricing", worker = .pricing),
  ],
  extensions = [(
    modules = [(name = "superschematic-fixture:standins", esModule = embed "standins.js", internal = true)],
  )],
);

const storefront :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "worker.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "SHOP_DB_DATABASE_D1_BINDING", text = "SHOP_DB_DATABASE"),
    (name = "SHOP_DB_DATABASE_D1_DATABASE", text = "1c3e5a7b-9d2f-4b6a-8c0e-2f4a6b8c0d1e"),
    (name = "SHOP_DB_DATABASE", wrapped = (moduleName = "superschematic-fixture:standins", entrypoint = "d1")),
    (name = "SHOP_MEDIA_BUCKET_NAME", text = "shop-media"),
    (name = "SHOP_MEDIA_BUCKET_BINDING", text = "SHOP_MEDIA_BUCKET"),
    (name = "SHOP_MEDIA_BUCKET_R2_ACCOUNT", text = "0123456789abcdef0123456789abcdef"),
    (name = "SHOP_MEDIA_BUCKET_R2_ACCESS_KEY_ID", text = "8f2b4d6e0a1c3e5f7b9d1f3a5c7e9b0d"),
    (name = "SHOP_MEDIA_BUCKET_R2_TOKEN", text = "r2-token-value"),
    (name = "SHOP_MEDIA_BUCKET", wrapped = (moduleName = "superschematic-fixture:standins", entrypoint = "r2")),
    (name = "PRICING_SERVICE_URL", text = "https://pricing"),
    (name = "PRICING_SERVICE_BINDING_NAME", text = "PRICING_SERVICE"),
    (name = "PRICING_SERVICE_BINDING_WORKER", text = "pricing"),
    (name = "PRICING_SERVICE_CREDENTIAL_SOURCE", text = "signed-token"),
    (name = "PRICING_SERVICE_CREDENTIAL_AUDIENCE", text = "pricing"),
    (name = "PRICING_SERVICE_CREDENTIAL_ISSUER", text = "Storefront"),
    (name = "PRICING_SERVICE_CREDENTIAL_KEY", text = "$CALLER_KEY_PEM"),
    (name = "PRICING_SERVICE", service = "pricing"),
    (name = "STOREFRONT_API_KEY", wrapped = (
      moduleName = "superschematic-fixture:standins",
      entrypoint = "secret",
      innerBindings = [(name = "value", text = "sk_test")],
    )),
  ],
  compatibilityDate = "2026-09-01",
);

const pricing :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "pricing.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "PRICING_CALLERS_ISSUERS", text = "1"),
    (name = "PRICING_CALLERS_ISSUERS_0_ISSUER", text = "Storefront"),
    (name = "PRICING_CALLERS_ISSUERS_0_AUDIENCE", text = "pricing"),
    (name = "PRICING_CALLERS_ISSUERS_0_ALGORITHMS", text = "EdDSA"),
    (name = "PRICING_CALLERS_ISSUERS_0_KEYS", text = "1"),
    (name = "PRICING_CALLERS_ISSUERS_0_KEYS_0_PEM", text = "$CALLER_PUBLIC_PEM"),
    (name = "PRICING_CALLERS_ISSUERS_0_MAX_LIFETIME_SECONDS", text = "300"),
    (name = "PRICING_CALLERS_ISSUERS_0_CALLERS", text = "1"),
    (name = "PRICING_CALLERS_ISSUERS_0_CALLERS_0_SUBJECT", text = "Storefront"),
    (name = "PRICING_CALLERS_ISSUERS_0_CALLERS_0_DEPLOYABLE", text = "Storefront"),
    (name = "PRICING_CALLERS_ISSUERS_0_CALLERS_0_SERVES", text = "shop-storefront"),
  ],
  compatibilityDate = "2026-09-01",
);
