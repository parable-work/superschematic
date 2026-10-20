import { ShopWishlist } from "@acme/shop-wishlist";
import { environment, stack } from "@superschematic/stack";

// The wishlist alone, exposed, on its TypeScript server, over
// shop-wishlist-db, which lists sqlite first: on Bun with a SQLite file on
// this machine, and on Cloudflare Workers with D1 in Staging (D64). It is
// the D1-backed API the cloudflare target runs live: a deploy, a schema
// change through expand and contract, a rollback and a destroy
// (docs/stack-model.md, section 18.6).
@stack({
  deploy: [ShopWishlist],
  expose: [ShopWishlist]
})
export abstract class Wishlist {}

// This machine: `superschematic stack dev schemas/services/wishlist-stack`
// runs the wishlist's main.ts on Bun, over a SQLite file under the
// environment's state directory, which the migration runner migrates. It
// needs no Docker.
@environment({
  target: "local"
})
export abstract class Dev {}

// A Cloudflare account, with no domain, so the wishlist is reached at
// https://shop-wishlist.<workersSubdomain>.workers.dev, its database the D1
// database wishlist-stack-shop-wishlist-db, which the deploy migrates from
// the deploying machine. The account is the docs' placeholder: replace it,
// and the workers.dev subdomain stack bootstrap records, with your
// account's.
@environment({
  target: "cloudflare",
  cloudflare: {
    accountId: "0123456789abcdef0123456789abcdef",
    workersSubdomain: "acme"
  }
})
export abstract class Staging {}
