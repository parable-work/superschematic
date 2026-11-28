import { app } from "@superschematic/api";

// The back office staff run the warehouse from: a Topcoat app (D78) over
// shop-stock's Topcoat crate, which the extension writes when a binary
// links it (superschematic-topcoat), in apps/backoffice. Its pages, under
// /stock, list the stock and count and receive it through shop-stock's
// operations, over the Postgres database where the stack's checkouts hold
// it. It runs where shop-stock's server runs, a deployable of its own,
// signed in by the sessions shop-api's sign-in starts, as shop-stock's
// callers are.
@app({ extensions: ["topcoat"] })
export abstract class Backoffice {}
