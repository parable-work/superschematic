#!/usr/bin/env bash
# Deploy smoke-stack to one environment, check it through the site's
# origin, and destroy it (D97): the live proof of one origin (D91) and of
# Hyperdrive, the Cron drainer and sign-in on Workers (D92).
#
#   examples/deploy-smoke/scripts/smoke.sh Dev          stack dev, checked, stopped and its data removed
#   examples/deploy-smoke/scripts/smoke.sh Gcp          deploy, check, destroy
#   examples/deploy-smoke/scripts/smoke.sh Cloudflare   deploy, check, destroy
#
#   --keep            leave the deployment up (Dev: stack dev runs on until Ctrl-C)
#   --destroy         only destroy what an earlier --keep left
#   --bootstrap       run `stack bootstrap` first, once per project or account,
#                     with an owner's credentials
#   --base-url <url>  only check a stack already up at the site's address
#
# The check is scripts/smoke-check.ts, on Bun: the site's page, fallback
# and config; an administrator and a member registered, approved and
# signed in with a cookie on the site's origin; smoke-db's declared query;
# nearest; and a message the worker drains. It prints the sign-ins' and
# the drain's times. SMOKE_ADMIN_PASSWORD is the administrator's password,
# required but for Dev; SMOKE_ADMIN_LOGIN is their login, admin@smoke.test
# unless set.
#
# It runs the checkout's bin/superschematic (make cli), or the binary
# $SUPERSCHEMATIC names, which must link the gcp, cloudflare and pulumi
# extensions, with bin/superschematic-migrate beside it, and needs Bun and,
# for Dev, Docker. The committed stack names placeholder accounts, which
# resolve offline; for a deploy the script writes these in their place and
# puts the placeholders back when it exits:
#
#   Gcp:        SMOKE_GCP_PROJECT, SMOKE_GCP_REGION; application default
#               credentials, the pulumi CLI and gcloud. SUPERSCHEMATIC_MIGRATE_IMAGE
#               when set, else scripts/migrate-dev-image.sh builds the
#               migration runner's image from the checkout.
#   Cloudflare: SMOKE_CLOUDFLARE_ACCOUNT_ID, SMOKE_CLOUDFLARE_WORKERS_SUBDOMAIN,
#               SMOKE_CLOUDFLARE_SECRETS_STORE_ID (the last two recorded by
#               --bootstrap), SMOKE_HYPERDRIVE_ID, the Hyperdrive config of
#               a Postgres the smoke owns, which HYPERDRIVE_ORIGIN_SMOKE_DB
#               names for the migrations; CLOUDFLARE_API_TOKEN,
#               PULUMI_CONFIG_PASSPHRASE and the pulumi CLI. The destroy
#               empties that database too, with psql or, without it,
#               Docker's postgres image, since the next deploy migrates it
#               from empty.
#
# CONTRIBUTING.md, "A deploy to the cloud", has the one-time setup and
# .github/workflows/deploy-smoke.yml runs this by hand on GitHub Actions.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"
STACK="schemas/services/smoke-stack"
SCHEMA="$EXAMPLE_DIR/$STACK/src/stack.schema.ts"
BIN="${SUPERSCHEMATIC:-$REPO_ROOT/bin/superschematic}"

usage() {
  sed -n '2,/unless set\./p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

env_name=""
keep=""
destroy_only=""
bootstrap=""
base_url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep) keep=1 ;;
    --destroy) destroy_only=1 ;;
    --bootstrap) bootstrap=1 ;;
    --base-url)
      [[ $# -ge 2 ]] || usage
      base_url="$2"
      shift
      ;;
    -h | --help) usage ;;
    -*) echo "unknown flag $1" >&2 && usage ;;
    *)
      [[ -z "$env_name" ]] || usage
      env_name="$1"
      ;;
  esac
  shift
done
case "$env_name" in
  Dev | Gcp | Cloudflare) ;;
  *) usage ;;
esac

need() {
  for name in "$@"; do
    [[ -n "${!name:-}" ]] || {
      echo "smoke $env_name needs $name" >&2
      exit 2
    }
  done
}
have() {
  command -v "$1" >/dev/null || {
    echo "smoke $env_name needs $1 on PATH" >&2
    exit 2
  }
}

have bun
cd "$EXAMPLE_DIR"
WORK="$(mktemp -d)"
seconds() { echo $(($(date +%s) - $1)); }

# A stack already up: the check alone.
if [[ -n "$base_url" ]]; then
  [[ "$env_name" == Dev ]] && export SMOKE_ADMIN_PASSWORD="${SMOKE_ADMIN_PASSWORD:-smoke-dev-password}"
  need SMOKE_ADMIN_PASSWORD
  exec bun scripts/smoke-check.ts --base-url "$base_url"
fi

[[ -x "$BIN" ]] || {
  echo "no superschematic at $BIN: run make cli in $REPO_ROOT, or set SUPERSCHEMATIC" >&2
  exit 2
}
export PATH="$(dirname "$BIN"):$PATH"

# Dev: stack dev in the background, until the check is done; its database
# is removed when it stops, unless --keep.
if [[ "$env_name" == Dev ]]; then
  have docker
  export SMOKE_ADMIN_PASSWORD="${SMOKE_ADMIN_PASSWORD:-smoke-dev-password}"
  flags=(--environment Dev)
  [[ -n "$keep" ]] || flags+=(--remove-data)
  log="$WORK/stack-dev.log"
  started=$(date +%s)
  "$BIN" stack dev "$STACK" "${flags[@]}" >"$log" 2>&1 &
  dev=$!
  # stack dev stops its processes and containers on SIGTERM, as on Ctrl-C.
  trap 'kill -TERM "$dev" 2>/dev/null; wait "$dev" 2>/dev/null; rm -rf "$WORK"' EXIT
  until grep -q '^Press Ctrl-C to stop' "$log"; do
    if ! kill -0 "$dev" 2>/dev/null || [[ $(seconds "$started") -gt 900 ]]; then
      cat "$log" >&2
      echo "stack dev did not start" >&2
      exit 1
    fi
    sleep 2
  done
  echo "stack dev is up after $(seconds "$started")s"
  site="$(sed -n 's/^  site  *smoke-web  *\(http[^,]*\),.*/\1/p' "$log")"
  [[ -n "$site" ]] || {
    cat "$log" >&2
    echo "stack dev names no address of site smoke-web" >&2
    exit 1
  }
  bun scripts/smoke-check.ts --base-url "$site" --ready-seconds 60 --drain-seconds 60
  if [[ -n "$keep" ]]; then
    # A job of a script ignores SIGINT, so Ctrl-C reaches stack dev as
    # SIGTERM, through this trap.
    echo "stack dev runs on with smoke-web at $site; Ctrl-C stops it"
    trap 'kill -TERM "$dev" 2>/dev/null' INT TERM
    wait "$dev" || wait "$dev" || true
  fi
  exit 0
fi

# A cloud environment: the account's values in place of the placeholders,
# then deploy, check and destroy.
need SMOKE_ADMIN_PASSWORD
have pulumi
case "$env_name" in
  Gcp)
    need SMOKE_GCP_PROJECT SMOKE_GCP_REGION
    [[ -n "${SUPERSCHEMATIC_MIGRATE_IMAGE:-}" || -n "$destroy_only" ]] || have gcloud
    ;;
  Cloudflare)
    need SMOKE_CLOUDFLARE_ACCOUNT_ID SMOKE_HYPERDRIVE_ID HYPERDRIVE_ORIGIN_SMOKE_DB CLOUDFLARE_API_TOKEN PULUMI_CONFIG_PASSPHRASE
    [[ -n "$bootstrap" ]] || need SMOKE_CLOUDFLARE_WORKERS_SUBDOMAIN SMOKE_CLOUDFLARE_SECRETS_STORE_ID
    ;;
esac

cp "$SCHEMA" "$WORK/stack.schema.ts"
deployed=""
status=0
finish() {
  status=$?
  set +e
  if [[ -n "$deployed" && -z "$keep" ]]; then
    destroy
    status=$((status ? status : $?))
  fi
  cp "$WORK/stack.schema.ts" "$SCHEMA"
  rm -rf "$WORK"
  exit "$status"
}
trap finish EXIT

# Each placeholder of the committed stack, and the variable that replaces it.
SMOKE_SCHEMA="$SCHEMA" bun -e '
const path = process.env.SMOKE_SCHEMA;
let text = await Bun.file(path).text();
for (const [placeholder, name] of [
  ["superschematic-smoke", "SMOKE_GCP_PROJECT"],
  ["us-central1", "SMOKE_GCP_REGION"],
  ["0123456789abcdef0123456789abcdef", "SMOKE_CLOUDFLARE_ACCOUNT_ID"],
  ["smoke-subdomain", "SMOKE_CLOUDFLARE_WORKERS_SUBDOMAIN"],
  ["fedcba9876543210fedcba9876543210", "SMOKE_CLOUDFLARE_SECRETS_STORE_ID"],
  ["00000000000000000000000000000000", "SMOKE_HYPERDRIVE_ID"],
]) {
  const value = process.env[name];
  if (value) text = text.replaceAll(JSON.stringify(placeholder), JSON.stringify(value));
}
await Bun.write(path, text);
'

# The origin's tables, emptied after a destroy on Cloudflare: the next
# deploy has no manifest and migrates the database from empty (D92).
reset_origin() {
  local sql="$WORK/reset.sql"
  {
    cat schemas/dist/sql/smoke-db/drop.sql
    echo 'DROP TABLE IF EXISTS superschematic_migrations, superschematic_schema_state, superschematic_lock CASCADE;'
  } >"$sql"
  echo "empty the origin's smoke-db"
  export PGOPTIONS="-c client_min_messages=warning"
  if command -v psql >/dev/null; then
    psql "$HYPERDRIVE_ORIGIN_SMOKE_DB" --quiet --set ON_ERROR_STOP=1 --file "$sql"
  else
    docker run --rm -i -e HYPERDRIVE_ORIGIN_SMOKE_DB -e PGOPTIONS postgres:16-alpine \
      sh -c 'psql "$HYPERDRIVE_ORIGIN_SMOKE_DB" --quiet --set ON_ERROR_STOP=1' <"$sql"
  fi
}

destroy() {
  local started
  started=$(date +%s)
  echo "destroy $env_name"
  "$BIN" stack destroy "$env_name" --stack "$STACK" --yes || return 1
  echo "destroyed $env_name in $(seconds "$started")s"
  if [[ "$env_name" == Cloudflare ]]; then
    reset_origin || return 1
  fi
}

echo "build the services"
"$BIN" build-all schemas/services >"$WORK/build.log" 2>&1 || {
  cat "$WORK/build.log" >&2
  exit 1
}

if [[ -n "$destroy_only" ]]; then
  deployed=1
  keep=""
  exit 0
fi

# Bootstrap admits the git remote's repository through Workload Identity
# Federation on gcp, or SMOKE_GITHUB_REPOSITORY's, or none when it is "".
if [[ -n "$bootstrap" ]]; then
  repository=()
  [[ -z "${SMOKE_GITHUB_REPOSITORY+set}" ]] || repository=(--repository "$SMOKE_GITHUB_REPOSITORY")
  "$BIN" stack bootstrap "$env_name" --stack "$STACK" "${repository[@]}"
fi
if [[ "$env_name" == Gcp && -z "${SUPERSCHEMATIC_MIGRATE_IMAGE:-}" ]]; then
  echo "build the migration runner's image"
  SUPERSCHEMATIC_MIGRATE_IMAGE="$("$REPO_ROOT/scripts/migrate-dev-image.sh" "$SMOKE_GCP_PROJECT" "$SMOKE_GCP_REGION" smoke-stack)"
  export SUPERSCHEMATIC_MIGRATE_IMAGE
fi

started=$(date +%s)
deployed=1
"$BIN" stack deploy "$env_name" --stack "$STACK"
echo "deployed $env_name in $(seconds "$started")s"
"$BIN" stack outputs "$env_name" --stack "$STACK" --out "$WORK/outputs.json"
bun scripts/smoke-check.ts \
  --environment "schemas/dist/stack/smoke-stack/$env_name/environment.json" \
  --outputs "$WORK/outputs.json"
if [[ -n "$keep" ]]; then
  echo "$env_name stays up: scripts/smoke.sh $env_name --destroy removes it"
fi
