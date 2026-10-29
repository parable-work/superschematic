import { SmokeApi } from "@smoke/smoke-api";
import { SmokeDb } from "@smoke/smoke-db";
import { SmokeWeb } from "@smoke/smoke-web";
import { environment, stack } from "@superschematic/stack";

// smoke-api and the site that calls it on one origin in every environment
// (D91): the site's own host serves the API under /api/smoke-api, so a
// cookie session needs no CORS, no trusted origin and no cookie domain.
// Nothing exposes smoke-api at an address of its own.
@stack({ deploy: [SmokeApi, SmokeWeb], origins: [{ site: SmokeWeb, apis: [SmokeApi] }] })
export abstract class Smoke {}

// This machine: stack dev runs Postgres with pgvector in a container,
// smoke-api, its worker and its job as processes, and the site behind the
// proxy that serves smoke-api under /api/smoke-api. scripts/smoke.sh Dev
// runs it and checks it.
@environment({ target: "local" })
export abstract class Dev {}

// Google Cloud: Cloud SQL with pgvector, smoke-api on Cloud Run behind the
// site's load balancer, its worker a worker pool, its job a Cloud Run job
// on Cloud Scheduler. The project and region are placeholders, which
// resolve offline: scripts/smoke.sh writes a maintainer's own from
// SMOKE_GCP_PROJECT and SMOKE_GCP_REGION for a deploy and puts these back
// after.
@environment({ target: "gcp", gcp: { project: "superschematic-smoke", region: "us-central1" } })
export abstract class Gcp {}

// Cloudflare: smoke-api, its worker and its job are Workers, the worker
// draining its queue at each Cron firing (D92), and the site's Worker
// forwards /api/smoke-api through a service binding. smoke-db is a
// Postgres outside Cloudflare, reached through a Hyperdrive config made
// once. The account, its subdomain and Secrets Store, and the config are
// placeholders, which scripts/smoke.sh replaces as it does Gcp's.
@environment({
  target: "cloudflare",
  cloudflare: {
    accountId: "0123456789abcdef0123456789abcdef",
    workersSubdomain: "smoke-subdomain",
    secretsStoreId: "fedcba9876543210fedcba9876543210",
  },
  settings: [{ of: SmokeDb, hyperdrive: "00000000000000000000000000000000" }],
})
export abstract class Cloudflare {}
