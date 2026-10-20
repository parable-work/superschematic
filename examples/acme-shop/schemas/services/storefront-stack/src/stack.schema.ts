import { ShopStorefront } from "@acme/shop-storefront";
import { environment, stack } from "@superschematic/stack";

// The storefront alone, exposed, on its TypeScript server: on Bun on this
// machine, and on Cloudflare Workers in Staging (D64). It is the first
// deployable the cloudflare target runs, at workers.dev and then under a
// domain (docs/stack-model.md, section 18).
@stack({
  deploy: [ShopStorefront],
  expose: [ShopStorefront]
})
export abstract class Storefront {}

// This machine: `superschematic stack dev schemas/services/storefront-stack`
// runs the storefront's main.ts on Bun, as shop-stack's Dev does.
@environment({
  target: "local"
})
export abstract class Dev {}

// A Cloudflare account, with no domain, so the storefront is reached at
// https://shop-storefront.<workersSubdomain>.workers.dev. The account is the
// docs' placeholder, and stack bootstrap records the account's
// workers.dev subdomain and its Secrets Store beside it; replace them with
// your account's, or run `superschematic stack init --cloudflare-account`.
// Its server is a Worker that runs the bundle its bundle.ts writes, and its
// carts live per isolate.
@environment({
  target: "cloudflare",
  cloudflare: {
    accountId: "0123456789abcdef0123456789abcdef",
    workersSubdomain: "acme"
  }
})
export abstract class Staging {}
