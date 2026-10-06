#!/usr/bin/env bash
# Plan the migration of tasks-db from v1/ to v2/ with the core superschematic
# binary, apply it with superschematic-migrate, and check the outputs the
# docs site's schema migrations page quotes.
#
#   examples/migrations/scripts/check.sh            check
#   UPDATE=1 examples/migrations/scripts/check.sh   also rewrite testdata/generated/
#
# Needs the superscalar checkout and archive and the version-graph archive
# (make setup), the pinned Go and sqlite3. With
# SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL set to a Postgres whose role may
# create databases, as the runner's own tests take it, it also applies the
# Postgres plan, with psql, to a database it creates and drops; without it,
# that step is skipped. Everything else goes to a temporary directory.
#
# Asserts, in order:
#   1. without --rename, the plan drops task.title and adds task.summary,
#      and --fail-on destructive stops it, naming the rename;
#   2. with --rename task.title=task.summary, the plans for Postgres and
#      SQLite in sql and markdown; --fail-on destructive stops each on what
#      v2 means to lose, and --allow of each hazard lets it pass;
#   3. SQLite: a database built from v1's create.sql, with rows, adopts v1's
#      model; after apply --phase expand, servers of both versions can
#      write comments, the reminders are numbers and status shows the
#      contract pending; apply --phase contract drops comment.legacy_id;
#      the database is then at v2's model, a plan from it has no steps, a
#      second apply does nothing, and a database with no recorded model
#      refuses the plan;
#   4. Postgres, when the URL is set: step 3's apply on Postgres;
#   5. the copies under testdata/generated/ match this run byte for byte.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(cd "$EXAMPLE_DIR/../.." && pwd)"

OUT="$(mktemp -d)"
PG_ADMIN_URL="${SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL:-}"
PG_DATABASE=""
cleanup() {
  if [[ -n "$PG_DATABASE" ]]; then
    psql "$PG_ADMIN_URL" -q -v ON_ERROR_STOP=1 -c "DROP DATABASE IF EXISTS $PG_DATABASE WITH (FORCE)" >/dev/null || true
  fi
  rm -rf "$OUT"
}
trap cleanup EXIT
mkdir -p "$OUT/bin" "$OUT/quoted/logs"

command -v sqlite3 >/dev/null || { echo "check.sh needs sqlite3" >&2; exit 1; }

export CGO_ENABLED=1
if [[ -z "${CGO_LDFLAGS:-}" ]]; then
  CGO_LDFLAGS="$("$REPO_ROOT/scripts/superscalar-dep.sh" --print) $("$REPO_ROOT/scripts/versiongraph-archive.sh" --print)"
  export CGO_LDFLAGS
fi

echo "==> the core binary and the runner"
# The core with no extension linked, under the name the docs pages run. The
# runner is a Go module of its own and needs no cgo.
(cd "$REPO_ROOT" && go build -o "$OUT/bin/superschematic" ./internal/cmd/superschematic-core)
(cd "$REPO_ROOT/runtime/migrate/go" && CGO_ENABLED=0 go build -o "$OUT/bin/superschematic-migrate" ./cmd/superschematic-migrate)
export PATH="$OUT/bin:$PATH"

# Every command runs from the example directory, as the README runs it.
cd "$EXAMPLE_DIR"
V1=v1/schemas/services/tasks-db
V2=v2/schemas/services/tasks-db
RENAME=(--rename task.title=task.summary)

# The plan from v1 to v2, with the flags given.
plan() { superschematic migrate plan "$V2" --from "$V1" "$@"; }

# Runs a command that must fail and keeps its stderr in the file $1.
fails() {
  local stderr="$1"
  shift
  if "$@" >/dev/null 2>"$stderr"; then
    echo "expected to fail: $*" >&2
    exit 1
  fi
}

# Fails when the file $2 has a line matching $1. (set -e ignores a failed
# "! grep".)
lacks() {
  if grep -q -- "$1" "$2"; then
    echo "$2 has a line matching $1" >&2
    exit 1
  fi
}

# The runner prints times and durations, which change from run to run; the
# kept copies show them as "...".
steady() {
  sed -E -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z/.../g' -e 's/done in [0-9][0-9.hms]*/done in .../'
}

echo "==> without --rename: a drop and an add, which --fail-on destructive stops"
plan >"$OUT/without-rename.sql"
grep -q '^-- Step .*, addColumn table/task/column/summary$' "$OUT/without-rename.sql"
grep -q '^-- Step .*, contract, dropColumn table/task/column/title$' "$OUT/without-rename.sql"
fails "$OUT/quoted/logs/fail-on-without-rename.txt" plan --fail-on destructive
cat "$OUT/quoted/logs/fail-on-without-rename.txt"
grep -q 'plan with --rename task.title=task.summary\.$' "$OUT/quoted/logs/fail-on-without-rename.txt"

echo "==> with --rename: the plans for Postgres and SQLite"
for dialect in postgres sqlite; do
  plan "${RENAME[@]}" --dialect "$dialect" --out "$OUT/plan.$dialect.json" >"$OUT/quoted/plan.$dialect.sql"
  plan "${RENAME[@]}" --dialect "$dialect" --format markdown >"$OUT/quoted/plan.$dialect.md"
  grep -q '^-- Step 1, expand, renameColumn table/task/column/summary$' "$OUT/quoted/plan.$dialect.sql"
  lacks 'dropColumn table/task/column/title' "$OUT/quoted/plan.$dialect.sql"
done
cat "$OUT/quoted/plan.postgres.sql"
# SQLite cannot convert the reminders in place: it rebuilds task, and
# comment with it, since comment references task.
grep -q '^-- Step 2, expand, copyTable table/comment$' "$OUT/quoted/plan.sqlite.sql"

echo "==> --fail-on destructive, then --allow"
# Postgres's cast of the reminders fails on text that is not a number
# (data-dependent); SQLite's never fails, so there it is destructive too.
LEGACY_ID=destructive:table/comment/column/legacy_id
REMINDERS=destructive:table/task/column/remind_before
fails "$OUT/quoted/logs/fail-on.postgres.txt" plan "${RENAME[@]}" --fail-on destructive
fails "$OUT/quoted/logs/fail-on.sqlite.txt" plan "${RENAME[@]}" --dialect sqlite --fail-on destructive
cat "$OUT/quoted/logs/fail-on.sqlite.txt"
grep -q "^  --allow '$LEGACY_ID'$" "$OUT/quoted/logs/fail-on.postgres.txt"
lacks "^  --allow '$REMINDERS'$" "$OUT/quoted/logs/fail-on.postgres.txt"
grep -q "^  --allow '$REMINDERS'$" "$OUT/quoted/logs/fail-on.sqlite.txt"
plan "${RENAME[@]}" --fail-on destructive --allow "$LEGACY_ID" >/dev/null
plan "${RENAME[@]}" --dialect sqlite --fail-on destructive --allow "$LEGACY_ID" --allow "$REMINDERS" >/dev/null

echo "==> v1's create.sql"
superschematic build "$V1" --out "$OUT/v1-dist" >/dev/null

# Rows as v1's servers write them: a project, a task with two reminders
# and a comment. $1 runs one SQL statement and prints rows as a|b, $2 is
# the dialect's false, and $3 the reminders as v1 holds them.
seed() {
  "$1" "INSERT INTO project (name) VALUES ('Launch')"
  "$1" "INSERT INTO task (project_id, title, done, remind_before) SELECT id, 'Write the changelog', $2, '$3' FROM project"
  "$1" "INSERT INTO comment (task_id, body, legacy_id) SELECT id, 'The draft is up', 'TRK-7' FROM task"
}

# Adopts v1's model for the database at $2, applies the plan one phase at
# a time, and checks the rows and the state after each through $1, which
# runs one SQL statement. $3 is the dialect, $4 an empty list and $5 the
# reminders after the plan, as $1 prints them.
apply_plan() {
  local sql="$1" url="$2" dialect="$3" empty="$4" reminders="$5"
  superschematic migrate plan "$V1" --dialect "$dialect" --print-model >"$OUT/v1.$dialect.model.json"
  superschematic-migrate adopt --model "$OUT/v1.$dialect.model.json" --database-url "$url"

  superschematic-migrate apply --plan "$OUT/plan.$dialect.json" --phase expand --database-url "$url" | steady >"$OUT/apply-expand.$dialect.txt"
  cat "$OUT/apply-expand.$dialect.txt"
  superschematic-migrate status --service tasks-db --database-url "$url" | steady >"$OUT/status-expanded.$dialect.txt"
  grep -q '(expand done; contract pending; ' "$OUT/status-expanded.$dialect.txt"
  test "$("$sql" "SELECT summary, remind_before FROM task")" = "Write the changelog|$reminders"
  # Between the phases, a v1 server still writes legacy_id and leaves out
  # mentions, and a v2 server leaves out legacy_id.
  "$sql" "INSERT INTO comment (task_id, body, legacy_id) SELECT id, 'Written by v1', 'TRK-8' FROM task"
  "$sql" "INSERT INTO comment (task_id, body) SELECT id, 'Written by v2' FROM task"
  test "$("$sql" "SELECT count(*) FROM comment WHERE mentions = '$empty'")" = 3

  superschematic-migrate apply --plan "$OUT/plan.$dialect.json" --phase contract --database-url "$url" | steady >"$OUT/apply-contract.$dialect.txt"
  cat "$OUT/apply-contract.$dialect.txt"
  superschematic-migrate status --service tasks-db --database-url "$url" | steady >"$OUT/status.$dialect.txt"
  grep -q '^plan in progress: none$' "$OUT/status.$dialect.txt"
  test "$("$sql" "SELECT count(*) FROM comment")" = 3
  fails "$OUT/legacy-id.$dialect.txt" "$sql" "SELECT legacy_id FROM comment"

  # The database is at v2's model: the model it recorded is the one v2
  # resolves to, and a plan from it has nothing to do.
  superschematic-migrate status --service tasks-db --model --database-url "$url" >"$OUT/applied.$dialect.model.json"
  superschematic migrate plan "$V2" --dialect "$dialect" --print-model | cmp - "$OUT/applied.$dialect.model.json"
  superschematic migrate plan "$V2" --dialect "$dialect" --from "$OUT/applied.$dialect.model.json" >"$OUT/replan.$dialect.sql"
  grep -q '^-- no steps, no hazards$' "$OUT/replan.$dialect.sql"
  superschematic-migrate apply --plan "$OUT/plan.$dialect.json" --database-url "$url" >"$OUT/apply-again.$dialect.txt"
  grep -q ' is already applied: ' "$OUT/apply-again.$dialect.txt"
}

echo "==> SQLite: adopt v1, apply expand, then contract"
DB="$OUT/tasks.db"
sqlite_sql() { sqlite3 -bail "$DB" "$1"; }
sqlite3 -bail "$DB" <"$OUT/v1-dist/sql/tasks-db/sqlite/create.sql"
seed sqlite_sql 0 '["15","60"]'
apply_plan sqlite_sql "$DB" sqlite '[]' '[15,60]'
cp "$OUT/apply-expand.sqlite.txt" "$OUT/status-expanded.sqlite.txt" \
  "$OUT/apply-contract.sqlite.txt" "$OUT/status.sqlite.txt" "$OUT/quoted/logs/"

echo "==> SQLite: a database with no recorded model refuses the plan"
FRESH="$OUT/fresh.db"
sqlite3 -bail "$FRESH" <"$OUT/v1-dist/sql/tasks-db/sqlite/create.sql"
fails "$OUT/quoted/logs/apply-without-adopt.sqlite.txt" superschematic-migrate apply --plan "$OUT/plan.sqlite.json" --database-url "$FRESH"
cat "$OUT/quoted/logs/apply-without-adopt.sqlite.txt"
grep -q 'has no applied model' "$OUT/quoted/logs/apply-without-adopt.sqlite.txt"

if [[ -n "$PG_ADMIN_URL" ]]; then
  echo "==> Postgres: adopt v1, apply expand, then contract"
  command -v psql >/dev/null || { echo "check.sh needs psql for SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL" >&2; exit 1; }
  PG_DATABASE="migrations_example_$$"
  psql "$PG_ADMIN_URL" -q -v ON_ERROR_STOP=1 -c "CREATE DATABASE $PG_DATABASE" >/dev/null
  # The admin URL with its database replaced by the new one.
  query=""
  if [[ "$PG_ADMIN_URL" == *\?* ]]; then query="?${PG_ADMIN_URL#*\?}"; fi
  base="${PG_ADMIN_URL%%\?*}"
  scheme="${base%%://*}"
  authority="${base#*://}"
  authority="${authority%%/*}"
  PG_URL="$scheme://$authority/$PG_DATABASE$query"
  postgres_sql() { psql "$PG_URL" -q -At -v ON_ERROR_STOP=1 -c "$1"; }
  psql "$PG_URL" -q -v ON_ERROR_STOP=1 -f "$OUT/v1-dist/sql/tasks-db/create.sql" >/dev/null
  seed postgres_sql false '{15,60}'
  apply_plan postgres_sql "$PG_URL" postgres '{}' '{15,60}'
else
  echo "==> Postgres: skipped; set SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL to run it"
fi

echo "==> testdata/generated/ matches this run"
# The docs site builds without Go, so it reads these committed copies. A
# change that alters one fails here until the copy is refreshed with
# UPDATE=1.
COPIES="$EXAMPLE_DIR/testdata/generated"
if [[ "${UPDATE:-}" == 1 ]]; then
  rm -rf "$COPIES"
  mkdir -p "$(dirname "$COPIES")"
  cp -R "$OUT/quoted" "$COPIES"
elif ! diff -r "$OUT/quoted" "$COPIES"; then
  echo "testdata/generated/ is stale: rerun with UPDATE=1, then check the docs pages that quote it" >&2
  exit 1
fi

echo "migrations: ok"
