#!/usr/bin/env bash
# The Go modules' build, vet and tests in shards, for CI's go job.
#
# One job that built, vetted and tested the eleven modules in turn took
# most of an hour, the root module's tests most of that: go test runs a
# module's packages a few at a time, and one package, internal/sqlmigrate,
# ran on alone for a quarter of an hour after the rest. The go job runs
# the shards in jobs of their own instead, and this script says what a
# shard runs, so that the shards together run what the one loop ran
# (`make go-build go-vet go-test` still runs it whole).
#
# Usage:
#   scripts/go-test-shard.sh <shard>          # run the shard
#   scripts/go-test-shard.sh --print <shard>  # print the packages it tests
#   scripts/go-test-shard.sh --shards         # print the shards' names
#
# The shards:
#   sqlmigrate, loader, generators
#             the root module's tests under the directories named below;
#   modules   the tests of the ten other modules;
#   rest      go build and go vet of every module, then the tests of every
#             root package no other shard names. A new package lands here,
#             and none is left out. It builds every module, so its build
#             cache is the one CI saves for the others to start from.
#
# A named shard whose patterns match no package fails the script: the
# directory moved, and its tests would run in rest unnoticed.
#
# GO_TEST_TIMEOUT (go test's default when unset) and GO_TEST_SHORT (false)
# are the go job's. CGO_LDFLAGS is the caller's to set (the Makefile and
# the go job say how).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# The modules beside the root one, as the Makefile's GO_MODULES has them
# less the three that link an archive of their own, which have jobs of
# their own (versiongraph, engine-go, rules-go).
MODULES="ir runtime/schema/go runtime/http/go runtime/migrate/go extensions/gcp extensions/cloudflare extensions/embedded extensions/resolvers extensions/pulumi extensions/topcoat cmd/superschematic"

NAMED="sqlmigrate loader generators"
SHARDS="$NAMED modules rest"

# patterns prints a named shard's package patterns in the root module.
# generators has cli with it: both run cargo on generated crates in the
# full tier, and wait on one target directory together.
patterns() {
  case "$1" in
    sqlmigrate) echo "./internal/sqlmigrate/..." ;;
    loader) echo "./internal/loader/... ./internal/registry/..." ;;
    generators) echo "./internal/generator/... ./cli/..." ;;
    *) return 1 ;;
  esac
}

# named_packages prints a named shard's packages, and fails when one of
# its patterns matches none.
named_packages() {
  local pattern found
  for pattern in $(patterns "$1"); do
    found="$(go list "$pattern" 2>/dev/null || true)"
    if [ -z "$found" ]; then
      echo "go-test-shard: shard $1 names $pattern, which matches no package" >&2
      return 1
    fi
    echo "$found"
  done
}

# rest_packages prints the root packages no named shard has.
rest_packages() {
  local shard named
  named="$(for shard in $NAMED; do named_packages "$shard"; done)"
  comm -23 <(go list ./... | LC_ALL=C sort -u) <(echo "$named" | LC_ALL=C sort -u)
}

packages() {
  case "$1" in
    rest) rest_packages ;;
    modules)
      local m
      for m in $MODULES; do (cd "$m" && go list ./...); done
      ;;
    *) named_packages "$1" ;;
  esac
}

go_test() {
  go test -count=1 ${GO_TEST_TIMEOUT:+-timeout "$GO_TEST_TIMEOUT"} -short="${GO_TEST_SHORT:-false}" "$@"
}

print=0
if [ "${1:-}" = "--shards" ]; then
  for shard in $SHARDS; do echo "$shard"; done
  exit 0
fi
if [ "${1:-}" = "--print" ]; then
  print=1
  shift
fi
shard="${1:-}"
case " $SHARDS " in
  *" $shard "*) ;;
  *)
    echo "usage: scripts/go-test-shard.sh [--print] <shard>, a shard one of: $SHARDS" >&2
    exit 2
    ;;
esac

if [ "$print" = "1" ]; then
  packages "$shard"
  exit 0
fi

case "$shard" in
  modules)
    for m in $MODULES; do
      echo "==> go test $m"
      (cd "$m" && go_test ./...)
    done
    ;;
  rest)
    for m in . $MODULES; do
      echo "==> go build, go vet $m"
      (cd "$m" && go build ./... && go vet ./...)
    done
    echo "==> go test . (the packages no other shard names)"
    # shellcheck disable=SC2046 # one argument a package
    go_test $(rest_packages)
    ;;
  *)
    echo "==> go test . ($(patterns "$shard"))"
    # shellcheck disable=SC2046 # one argument a package
    go_test $(named_packages "$shard")
    ;;
esac
