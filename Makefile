# superschematic (the schema compiler). Convenience targets for the Go
# modules, the installed binary, the TypeScript authoring packages, the
# schema runtimes, the TypeScript and Rust http runtimes, the version-graph
# core, the engine and the engine's work-queue package.
# Mirrors the CI workflow gates (.github/workflows/ci.yml).
#
#   make setup && make all

# Pins are tools.env (Go, Node, Bun, Python, uv, Rust, golangci-lint) and
# superscalar.pin (the superscalar commit). Both are read here and by CI,
# never inlined.
include tools.env
export GOTOOLCHAIN := go$(GO_VERSION)

# Every crate declares RUST_VERSION as its rust-version, so every cargo run
# here uses that toolchain, whatever rustup's default is, unless
# RUSTUP_TOOLCHAIN already names one: cargo's own, and maturin's when uv
# builds the Python binding. The two archive scripts set it to RUST_VERSION
# regardless (scripts/superscalar-dep.sh says why).
export RUSTUP_TOOLCHAIN ?= $(RUST_VERSION)

# The Go binding of superscalar is cgo against a static archive that
# scripts/superscalar-dep.sh builds under third_party/superscalar. Every go
# command that links a scalar-dependent package needs this in CGO_LDFLAGS.
# The version-graph binding (runtime/versiongraph/go) links the core's static
# archive, which scripts/versiongraph-archive.sh (make versiongraph) stages,
# and the engine's (runtime/engine/go) the engine's, which
# scripts/engine-archive.sh (make engine-archive) stages.
export CGO_LDFLAGS := $(shell scripts/superscalar-dep.sh --print) $(shell scripts/versiongraph-archive.sh --print) $(shell scripts/engine-archive.sh --print)

# Where make engine-go stages the test kit's engine archive, which the corpus
# replay links in place of the core's (it is never shipped).
ENGINE_REPLAY_LIB := $(CURDIR)/runtime/engine/go/lib/testkit/$(shell go env GOOS)_$(shell go env GOARCH)

GO_MODULES := . ir runtime/schema/go runtime/http/go runtime/versiongraph/go runtime/engine/go runtime/migrate/go extensions/gcp extensions/cloudflare extensions/pulumi extensions/topcoat cmd/superschematic

# Two programs, one CLI (docs/stack-model.md, section 13). BIN is the binary
# a release ships and a user installs: cmd/superschematic, a Go module of its
# own, which links the core and the official extensions. CORE_BIN is the core
# with no extension linked, internal/cmd/superschematic-core in the root
# module; it is never shipped. cli-smoke runs it to prove the core works
# alone, and behaviors runs it because it needs only the core.
BIN := bin/superschematic
CORE_BIN := bin/superschematic-core

# build-all keys its cache on a hash of this binary. -trimpath drops the
# checkout's absolute source paths, and -buildvcs=false drops the revision,
# time and dirty bit Go stamps when the checkout's .git is a directory, so a
# commit or an edit that changes no Go source leaves the binary, and its
# cache entries, as they were. release.yml builds with the same two flags.
GO_BUILD_FLAGS := -trimpath -buildvcs=false

.PHONY: all setup build cli test lint fmt vet go-build go-test go-vet go-fmt-check go-lint \
        go-goldens catalog-check schema-file-types schema-file-types-check behaviors behaviors-check \
        gcp-schemas gcp-schemas-check cloudflare-schemas cloudflare-schemas-check generic-schemas generic-schemas-check ts python rust engine-rust acme-engine-rust \
        engine-rust-soak engine-archive engine-go engine-link-check engine-servers \
        versiongraph versiongraph-scenarios versiongraph-scenarios-ts versiongraph-scenarios-rust \
        versiongraph-scenarios-python docs cli-smoke example-migrations scrub versions clean

all: build test lint

# Check out and build the superscalar dependency (Go static archive and
# TypeScript binding, with its browser and workerd builds over the
# WebAssembly core). Needs git, a Rust toolchain, bun and node; it adds the
# wasm32 target and installs wasm-pack at WASM_PACK_VERSION when missing.
setup:
	rustup target add wasm32-unknown-unknown --toolchain $(RUST_VERSION)
	@wasm-pack --version 2>/dev/null | grep -qx 'wasm-pack $(WASM_PACK_VERSION)' || cargo install wasm-pack --version $(WASM_PACK_VERSION) --locked
	scripts/superscalar-dep.sh
	scripts/versiongraph-archive.sh
	cd packages && bun install
	cd runtime/schema/typescript && bun install
	cd runtime/http/typescript && bun install
	cd runtime/versiongraph/typescript && bun install
	cd runtime/engine/typescript && bun install
	cd runtime/engine-workqueue/typescript && bun install
	cd runtime/engine-commerce/typescript && bun install
	cd runtime/schema/python && uv sync
	cd runtime/versiongraph/python && uv sync

build: go-build $(BIN)

# The CLI alone, for a checkout without Rust (D82): bin/superschematic and
# the migration runner bin/superschematic-migrate, over superscalar's static
# archive, the one thing the CLI links, which scripts/superscalar-dep.sh
# --archive builds, or downloads prebuilt where cargo is missing. The
# migration runner needs no cgo.
cli:
	scripts/superscalar-dep.sh --archive >/dev/null
	cd cmd/superschematic && go build $(GO_BUILD_FLAGS) -o $(CURDIR)/$(BIN) .
	cd runtime/migrate/go && CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -o $(CURDIR)/bin/superschematic-migrate ./cmd/superschematic-migrate

$(BIN): FORCE
	cd cmd/superschematic && go build $(GO_BUILD_FLAGS) -o $(CURDIR)/$(BIN) .

$(CORE_BIN): FORCE
	go build $(GO_BUILD_FLAGS) -o $(CORE_BIN) ./internal/cmd/superschematic-core

FORCE:

go-build: versiongraph engine-archive
	@for m in $(GO_MODULES); do echo "==> go build $$m"; (cd $$m && go build ./...) || exit 1; done

go-vet:
	@for m in $(GO_MODULES); do echo "==> go vet $$m"; (cd $$m && go vet ./...) || exit 1; done

go-test: versiongraph engine-archive
	@for m in $(GO_MODULES); do echo "==> go test $$m"; (cd $$m && go test -count=1 ./...) || exit 1; done

go-fmt-check:
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$out" ]; then echo "gofmt: files need formatting:"; echo "$$out"; exit 1; fi

go-lint:
	@for m in $(GO_MODULES); do echo "==> golangci-lint $$m"; (cd $$m && golangci-lint run ./...) || exit 1; done

# Rewrite every golden file from the generators, and the schema-file JSON
# Schema and TypeScript types. Review the diff by eye. Each package's tests
# run from the Go module that holds it, with an hour each: with the
# TypeScript gates on, extensions/topcoat's alone outrun go test's ten
# minutes.
go-goldens: schema-file-types
	@for p in $$(grep -rl 'flag.Bool("update"' --include='*_test.go' . | grep -v '^./third_party/' | xargs -n1 dirname | sort -u); do \
		p=./$${p#./}; m=$$p; while [ ! -f $$m/go.mod ]; do m=$$(dirname $$m); done; \
		(cd $$m && go test -count=1 -timeout 60m .$${p#$$m} -update) || exit 1; done

# The TypeScript and Python scalar catalogs are written from the superscalar
# Go package, the TypeScript one with each scalar's value class from the
# graph descriptor's rule, and the docs' scalar page with the table
# `superschematic catalog scalars` prints. CI fails when a committed
# catalog differs.
catalog-check:
	go run ./internal/tools/scalarcatalog -check

# The schema-file JSON Schema and TypeScript types in ir/typescript, and the
# engine's Rust schema reader's copy of the JSON Schema, are written from
# the IR structs. CI fails when a committed file differs.
schema-file-types:
	go run ./internal/tools/schemafiletypes

schema-file-types-check:
	go run ./internal/tools/schemafiletypes -check

# The gcp target's pinned provider schemas (extensions/gcp/schemas) are
# extracted from the pulumi-gcp release extensions/gcp/schemas/pulumi-gcp.json
# pins, whose upstream files are fetched once into the user cache. CI fails
# when a committed file differs. Move the pin with
# `cd extensions/gcp && go run ./internal/tools/providerschemas -version X.Y.Z`
# and update gcp.ProviderVersion to match.
gcp-schemas:
	cd extensions/gcp && go run ./internal/tools/providerschemas

gcp-schemas-check:
	cd extensions/gcp && go run ./internal/tools/providerschemas -check

# The Cloudflare extension's pinned provider schemas (extensions/cloudflare/
# schemas), from the pulumi-cloudflare release its pulumi-cloudflare.json
# pins, by the same tool (stack/providerschema/pintool). Move the pin with
# `cd extensions/cloudflare && go run ./internal/tools/providerschemas
# -version X.Y.Z` and update cloudflare.ProviderVersion to match.
cloudflare-schemas:
	cd extensions/cloudflare && go run ./internal/tools/providerschemas

cloudflare-schemas-check:
	cd extensions/cloudflare && go run ./internal/tools/providerschemas -check

# The core's generic connector's pinned provider schema (internal/stack/
# generic/schemas, D63), from the pulumi-tls release its pulumi-tls.json
# pins, by the same tool, from the root module. Move the pin with
# `go run ./internal/tools/providerschemas -version X.Y.Z` and update
# generic.ProviderVersion to match.
generic-schemas:
	go run ./internal/tools/providerschemas

generic-schemas-check:
	go run ./internal/tools/providerschemas -check

# The engine's behaviors crate, the work queue's crate (D58) and the
# commerce crate (D70) implement the core's behaviors over a copy of each
# declaration (internal/registry/behaviors), which the core with no
# extension linked
# writes into the crate that implements it, by the npm package that ships
# it (--package): each behavior embeds its copy with include_str!. CI fails
# when a committed copy differs.
ENGINE_RUST_DECLARATIONS := runtime/engine/rust/behaviors/declarations
WORKQUEUE_RUST_DECLARATIONS := runtime/engine-workqueue/rust/declarations
COMMERCE_RUST_DECLARATIONS := runtime/engine-commerce/rust/declarations

behaviors:
	go run ./internal/cmd/superschematic-core behaviors --package @superschematic/engine --out $(ENGINE_RUST_DECLARATIONS)
	go run ./internal/cmd/superschematic-core behaviors --package @superschematic/engine-workqueue --out $(WORKQUEUE_RUST_DECLARATIONS)
	go run ./internal/cmd/superschematic-core behaviors --package @superschematic/engine-commerce --out $(COMMERCE_RUST_DECLARATIONS)

behaviors-check:
	go run ./internal/cmd/superschematic-core behaviors --package @superschematic/engine --out $(ENGINE_RUST_DECLARATIONS) --check
	go run ./internal/cmd/superschematic-core behaviors --package @superschematic/engine-workqueue --out $(WORKQUEUE_RUST_DECLARATIONS) --check
	go run ./internal/cmd/superschematic-core behaviors --package @superschematic/engine-commerce --out $(COMMERCE_RUST_DECLARATIONS) --check

ts:
	cd packages && bun install --frozen-lockfile && bun run typecheck && bun test
	cd runtime/schema/typescript && bun install --frozen-lockfile && bun run typecheck && bun run build && bun run test
	cd runtime/http/typescript && bun install --frozen-lockfile && bun run build && bun run test
	cd runtime/versiongraph/typescript && bun install --frozen-lockfile && bun run typecheck && bun run test
	cd runtime/engine/typescript && bun install --frozen-lockfile
	scripts/engine-napi.sh --js-behaviors >/dev/null
	cd runtime/engine/typescript && bun run typecheck && bun run build && bun run test
	cd runtime/engine-workqueue/typescript && bun install --frozen-lockfile && bun run typecheck && bun run test
	cd runtime/engine-commerce/typescript && bun install --frozen-lockfile && bun run typecheck && bun run test
	examples/engine-notes/scripts/check.sh
	examples/engine-jobs/scripts/check.sh

# The version-graph core's Python binding: cargo test runs the binding's own
# unit tests, uv builds the PyO3 extension with maturin into the package's
# environment, then pytest runs every core vector through it and the
# engine's tests that need no database, under the default Python and under
# 3.9, the floor its pyproject.toml declares. The Postgres tests skip here;
# versiongraph-scenarios-python runs them.
python:
	cd runtime/schema/python && uv run pytest -q
	cd runtime/versiongraph/python && cargo fmt --check && cargo clippy --all-targets -- -D warnings \
		&& cargo test && uv run pytest -q && uv run --python 3.9 --isolated pytest -q

# The version-graph crates' tests run again with serde_json's preserve_order
# on, which superscalar turns on and Cargo unifies into every crate of a
# build that uses it: a content hash and a canonical row must not depend on
# the order a serde_json map keeps. The Rust engine's tests run with its
# rusqlite feature on, so its SQLite adapter's tests and scenario pass run
# too, and its lints run with and without each of its features. The schema
# runtime's run again with arbitrary_precision too, which superscalar's
# default lossless-json feature turns on: an error map and a number check
# must not depend on either. The engine's schema reader (rust-ir) links
# superscalar's core from the checkout `make setup` stands up, so
# preserve_order is always on there; its lints and tests run with its
# schemafile feature and without, and again with arbitrary_precision. The
# http runtime's run again with its optional http-client feature, with its
# identity features, whose store and route tests run on SQLite (and on the
# Postgres SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL names, when it is set),
# and with a stack server's features (D57): postgres, whose tests run on the
# Postgres SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL names, when it is set,
# logging, and the two at once, as a worker runs (D65); and gcs, a bucket on
# GCS (D54), whose tests run against fakes, and against the fake-gcs-server
# SUPERSCHEMATIC_TEST_GCS_EMULATOR names, when it is set, and beside a
# worker's two, as a worker with a bucket builds; memorystore-redis, a memory
# store on Redis (D75), whose tests run on the Valkey
# SUPERSCHEMATIC_TEST_MEMORY_STORE names, when it is set, and beside a
# worker's two; and otel, a deployable's
# trace export (D67), alone and with postgres, as a worker builds it.
rust:
	cd runtime/http/rust && cargo fmt --check && cargo clippy --all-targets -- -D warnings \
		&& cargo clippy --all-targets --features serde_json/arbitrary_precision,serde_json/preserve_order -- -D warnings \
		&& cargo test && cargo test --features serde_json/arbitrary_precision,serde_json/preserve_order \
		&& cargo clippy --all-targets --features http-client -- -D warnings && cargo test --features http-client \
		&& cargo clippy --all-targets --features identity -- -D warnings \
		&& cargo clippy --all-targets --features identity-postgres,identity-sqlite -- -D warnings \
		&& cargo test --features identity-postgres,identity-sqlite \
		&& cargo clippy --all-targets --features postgres -- -D warnings \
		&& cargo clippy --all-targets --features postgres,identity-postgres,identity-sqlite -- -D warnings \
		&& cargo test --features postgres,identity-postgres,identity-sqlite \
		&& cargo clippy --all-targets --features logging -- -D warnings && cargo test --features logging \
		&& cargo clippy --all-targets --features postgres,logging -- -D warnings \
		&& cargo test --features postgres,logging \
		&& cargo clippy --all-targets --features gcs -- -D warnings && cargo test --features gcs \
		&& cargo clippy --all-targets --features gcs,postgres,logging -- -D warnings \
		&& cargo clippy --all-targets --features memorystore-redis -- -D warnings \
		&& cargo test --features memorystore-redis \
		&& cargo clippy --all-targets --features memorystore-redis,postgres,logging -- -D warnings \
		&& cargo clippy --all-targets --features otel -- -D warnings && cargo test --features otel \
		&& cargo clippy --all-targets --features postgres,otel -- -D warnings \
		&& cargo test --features postgres,otel \
		&& cargo clippy --all-targets --features sqlite -- -D warnings && cargo test --features sqlite \
		&& cargo clippy --all-targets --features postgres,sqlite,logging -- -D warnings \
		&& cargo test --features postgres,sqlite,logging
	cd runtime/schema/rust && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test \
		&& cargo test --features serde_json/arbitrary_precision,serde_json/preserve_order
	cd runtime/schema/rust-ir && cargo fmt --check && cargo clippy --all-targets -- -D warnings \
		&& cargo clippy --all-targets --all-features -- -D warnings \
		&& cargo clippy --all-targets --all-features --features serde_json/arbitrary_precision -- -D warnings \
		&& cargo test && cargo test --all-features \
		&& cargo test --all-features --features serde_json/arbitrary_precision
	cd runtime/versiongraph/rust && cargo fmt --check && cargo clippy --all-targets -- -D warnings \
		&& cargo clippy --all-targets --no-default-features -- -D warnings \
		&& cargo clippy --target wasm32-unknown-unknown -- -D warnings && cargo test \
		&& cargo test --no-default-features && cargo test --features serde_json/preserve_order
	cd runtime/versiongraph/rust-engine && cargo fmt --check && cargo clippy --all-targets -- -D warnings \
		&& cargo clippy --all-targets --all-features -- -D warnings \
		&& cargo clippy --no-default-features -- -D warnings \
		&& cargo clippy --no-default-features --features rusqlite -- -D warnings \
		&& cargo test --features rusqlite && cargo test --features rusqlite,serde_json/preserve_order

# The Rust engine's workspace (D58): every crate's format, its lints on
# every feature combination of the databases (both, neither, and each
# alone) and on the axum router over Postgres, as a Rust server builds it,
# and its tests, under the toolchain RUSTUP_TOOLCHAIN names. The work
# queue's crate (runtime/engine-workqueue/rust) and the commerce crate
# (runtime/engine-commerce/rust) are members, so they build, lint and test
# here with the rest. The
# engine reads schemas through runtime/schema/rust-ir, which needs the
# superscalar checkout (scripts/superscalar-dep.sh --checkout). The engine's
# and the storage's tests run on SQLite, and on Postgres as well when
# SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL names one (say a container of your
# own on a spare port, postgres://postgres:<password>@localhost:<port>/postgres?sslmode=disable),
# each in a schema it creates and drops; without it they skip that pass.
# The test kit replays the scenario corpus
# (runtime/engine/testdata/scenarios, or the directory ENGINE_CORPUS_DIR
# names) on the same databases, and the HTTP corpus through the engine's
# routes and its axum router, and fails when a corpus is missing.
# CI runs the same commands as the engine-rust job, against its Postgres.
engine-rust:
	cd runtime/engine/rust && cargo fmt --all --check \
		&& cargo clippy --workspace --all-targets --all-features -- -D warnings \
		&& cargo clippy --workspace --all-targets -- -D warnings \
		&& cargo clippy --workspace --all-targets --no-default-features -- -D warnings \
		&& cargo clippy --workspace --all-targets --no-default-features --features superschematic-engine/sqlite -- -D warnings \
		&& cargo clippy --workspace --all-targets --no-default-features --features superschematic-engine/postgres -- -D warnings \
		&& cargo clippy --workspace --all-targets --no-default-features --features superschematic-engine/postgres,superschematic-engine/axum,superschematic-engine-testkit/axum -- -D warnings \
		&& cargo test --workspace --all-features

# The engine's static archive (D58), the core's C ABI over the core's
# behaviors, staged where its Go binding links it
# (runtime/engine/go/lib/<goos>_<goarch>).
engine-archive:
	scripts/engine-archive.sh >/dev/null

# The engine's Go binding (runtime/engine/go), whose own tests go-test runs
# over the core's archive, replays the HTTP corpus
# (runtime/engine/testdata/http) through enginehttp, and the engine's own
# scenario files through Engine.Do, over the test kit's archive, which has
# the test behaviors and the recorded policies the corpora need, on SQLite
# and on the Postgres SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL names, when it
# is set.
engine-go: engine-archive
	scripts/engine-archive.sh --crate runtime/engine/rust/testkit-ffi --out $(ENGINE_REPLAY_LIB) >/dev/null
	cd runtime/engine/go && CGO_LDFLAGS="-L$(ENGINE_REPLAY_LIB) $(CGO_LDFLAGS)" ENGINE_REPLAY_ARCHIVE=1 \
		go test -count=1 -run Replay -v ./enginehttp/
	scripts/engine-archive.sh >/dev/null

# Generated servers over the engine (D58), as CI's engine-servers job runs
# them: servergen's engine entrypoint tests and the script three servers,
# Go, Rust and TypeScript, answer alike over one engine database
# (TestEngineServersAnswerOneScriptAlike), and the commerce fixture's
# servers in each language (TestCommerceEngineEntrypointServes*, D70),
# which three answer one commerce script alike
# (TestCommerceServersAnswerOneScriptAlike) and oversell nothing when
# 1,000 claims race for the last units and one slot
# (TestCommerceServersOversellNothing), over the engine's archive, its
# package and its Node-API addon, which this builds. The holds-a-second
# measurement, TestCommerceHoldsLoad, runs only with
# SUPERSCHEMATIC_COMMERCE_LOAD=1. They run offline, and
# on the Postgres SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL names when it is
# set; SUPERSCHEMATIC_REQUIRE_ENGINE_SERVERS=1 makes a missing toolchain or
# database a failure rather than a skip.
ENGINE_SERVER_TESTS := ^(TestEngineEntrypointServes|TestTheEngineReachesCloudSQLThroughASocket|TestEngineProjectArchiveServes|TestRustEngineEntrypointServes|TestTypeScriptEngineEntrypointServes|TestTypeScriptEngineProjectAddonServes|TestEngineServersAnswerOneScriptAlike|TestCommerceEngineEntrypointServes|TestCommerceEngineEntrypointServesRust|TestCommerceEngineEntrypointServesTypeScript|TestCommerceServersAnswerOneScriptAlike|TestCommerceServersOversellNothing)$$
engine-servers: versiongraph engine-archive
	cd runtime/http/typescript && bun install --frozen-lockfile && bun run build
	cd runtime/engine/typescript && bun install --frozen-lockfile && bun run build
	scripts/engine-napi.sh
	go test -count=1 -timeout 80m -run '$(ENGINE_SERVER_TESTS)' ./internal/generator/servergen/

# One Go binary that links superscalar's, the version graph's and the
# engine's archives together and calls each (scripts/engine-link-check.sh),
# after checking that each archive defines its own C functions alone.
engine-link-check:
	scripts/engine-link-check.sh

# acme.Rating in the Rust engine (D58): examples/acme-schematic/crates/behaviors
# is an extension's crate, a workspace of its own beside the engine's, checked
# as engine-rust checks the engine: fmt, clippy, and its tests on SQLite, and
# on Postgres too when SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL names one. The
# tests read the meta-schema the acme binary writes (ACME_META_SCHEMA). acme's
# smoke runs the same, and builds the engine crate the binary writes for a
# schema that composes acme.Rating.
ACME_CRATE := examples/acme-schematic/crates/behaviors
acme-engine-rust:
	@mkdir -p /tmp/superschematic-acme-engine
	cd examples/acme-schematic && go run ./cmd/acme-schematic json-schema >/tmp/superschematic-acme-engine/acme-schema-file.json
	cd $(ACME_CRATE) && cargo fmt --check \
		&& cargo clippy --locked --all-targets -- -D warnings \
		&& ACME_META_SCHEMA=/tmp/superschematic-acme-engine/acme-schema-file.json cargo test --locked

# The Rust engine's soaks on Postgres (D58), which CI does not run: two
# engines on one database racing long, with more threads than either pool
# has connections, then the invariants the CI tests check (no lost update,
# no repeated seq, cursor or number, no over-committed budget, no job
# claimed twice); event streams that see every event once while their
# listener's session is ended every 200 ms; and reactions handled once
# each while the runner's lease is taken from its holder every 300 ms
# (testkit/tests/replicas_*.rs). They need
# SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL; ENGINE_SOAK_CALLS sets the calls
# each thread makes, and the orders the handover soak creates (200 unless
# set).
engine-rust-soak:
	@test -n "$$SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL" || \
		{ echo "set SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL to the Postgres the soaks run against" >&2; exit 1; }
	cd runtime/engine/rust && cargo test -p superschematic-engine-testkit --all-features --test 'replicas_*' -- --ignored --nocapture

# The version-graph core's static archive, staged where the Go binding links
# it (runtime/versiongraph/go/lib/<goos>_<goarch>).
versiongraph:
	scripts/versiongraph-archive.sh >/dev/null

# Every version-graph scenario (runtime/versiongraph/testdata/scenarios)
# through the Go engine. First on SQLite (D32): the SQLite adapter, with its
# own tests and the SQLite vectors, which need no database server and run
# with or without a Postgres URL. Then through its Postgres adapter,
# against the Postgres that SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL
# names; that pass fails without it.
versiongraph-scenarios: versiongraph
	cd runtime/versiongraph/go && go test -count=1 -v -run 'OnSQLite$$' ./engine/ && go test -count=1 ./sqlite/
	@test -n "$$SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL" || \
		{ echo "set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to the Postgres the scenarios run against (the SQLite pass above needs none)" >&2; exit 1; }
	cd runtime/versiongraph/go && go test -count=1 -v -run '^TestScenarios$$' ./engine/

# Every version-graph scenario through the TypeScript engine. First on SQLite
# (D32): SyncEngine over the SQLite adapter, with the adapter's own tests, the
# SQLite vectors and a kind that gains a column, which need no database
# server and run with or without a Postgres URL (the URL is unset for them,
# so the Postgres tests skip here and run once below). Then on Postgres: the
# Postgres adapter, each operation replayed through SyncEngine, with the
# canonical vectors checked against Postgres, the adapter's, the sweeper's
# and the facade's own tests, and the gained column compared across both
# backends, against the Postgres that
# SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names; that pass fails
# without it.
versiongraph-scenarios-ts:
	cd runtime/versiongraph/typescript && bun install --frozen-lockfile && bun run build && \
		env -u SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL bun test test/scenarios.test.ts test/sqlite.test.ts \
		test/sqlite-vectors.test.ts test/gained-column.test.ts
	@test -n "$$SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL" || \
		{ echo "set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to the Postgres the scenarios run against (the SQLite pass above needs none)" >&2; exit 1; }
	cd runtime/versiongraph/typescript && \
		bun test test/scenarios.test.ts test/canonical.test.ts test/adapter.test.ts test/sweeper.test.ts \
		test/facade.test.ts test/gained-column.test.ts

# Every version-graph scenario through the Rust engine. First on SQLite
# (D32): the SQLite adapter over rusqlite (the rusqlite feature), and its
# fixed layout inside a host's transaction over plain rusqlite (D58), with
# the adapter's own tests, every canonical vector as a round trip and the
# SQLite vectors, which need no database server and run with or without a
# Postgres URL (the URL is unset for them, so the Postgres tests skip here
# and run below). Then on Postgres: the Postgres adapter, the fixed layout
# on Postgres inside a host's transaction over the synchronous postgres
# client, and the crate's other Postgres tests (every canonical vector's
# rendering, the adapter, the sweeper), against the Postgres that
# SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names; that pass fails
# without it. It runs every test target with the feature on, so the SQLite
# tests run again there, and one build serves both passes.
versiongraph-scenarios-rust:
	cd runtime/versiongraph/rust-engine && env -u SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL \
		cargo test --features rusqlite --test scenarios --test sqlite --test host -- --nocapture
	@test -n "$$SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL" || \
		{ echo "set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to the Postgres the scenarios run against (the SQLite pass above needs none)" >&2; exit 1; }
	cd runtime/versiongraph/rust-engine && cargo test --tests --features rusqlite -- --nocapture

# Every version-graph scenario through the Python engine. First on SQLite
# (D32): the SQLite adapter over the standard library's sqlite3, with the
# adapter's own tests and the shared SQLite vectors (testdata/sqlite), which
# need no database server and run with or without
# a Postgres URL (the URL is unset for them, so the Postgres tests skip here
# and run once below). Then on Postgres: the Postgres adapter, with the
# package's other Postgres tests (every canonical vector's rendering, the
# adapter, the sweeper, the facade), against the Postgres that
# SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names; that pass fails
# without it.
versiongraph-scenarios-python:
	cd runtime/versiongraph/python && \
		env -u SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL uv run pytest -v -rs tests/test_scenarios.py tests/test_sqlite.py \
		tests/test_sqlite_vectors.py
	@test -n "$$SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL" || \
		{ echo "set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to the Postgres the scenarios run against (the SQLite pass above needs none)" >&2; exit 1; }
	cd runtime/versiongraph/python && uv run pytest -v -rs tests/test_scenarios.py tests/test_canonical.py \
		tests/test_adapter.py tests/test_sweeper.py tests/test_facade.py

# Starlight site. CI runs this as the docs job (D9); release.yml deploys it.
docs:
	cd docs && npm ci && npm run build

# The core with no extension linked (CORE_BIN) builds a DB, an API and a
# General service from the fixture corpus, and loads fixture-behaviors-json,
# fixture-cross-instance-json, fixture-rollups-json, fixture-search-json,
# fixture-reactions-json, fixture-variants-json, fixture-branches-json,
# fixture-staterules-json, fixture-numbering-json, fixture-approvals-json,
# fixture-timers-json, fixture-workqueue-json, fixture-commerce-json and
# fixture-derived-json, whose types compose the core's behaviors (D10):
# --emit-ir carries all twenty-eight and json-schema admits them; fixture-commerce-currency-json
# holds a commerce currency in a Finance.Currency own field (D70). The
# engine runs the first eleven documents with its own behaviors in
# runtime/engine/typescript/test/core-behaviors.test.ts, the work-queue
# package runs fixture-workqueue-json in
# runtime/engine-workqueue/typescript/test/package.test.ts, and the
# commerce package's suites (runtime/engine-commerce/typescript) run its
# behaviors on the addon. It builds
# fixture-behaviors-json, a General schema, and fixture-engine-db-json,
# whose engine types the sql generator leaves out and whose engine
# document the build writes (D58).
cli-smoke: $(CORE_BIN)
	@rm -rf /tmp/superschematic-cli-smoke
	@for s in fixture-db fixture-api fixture-general; do \
		$(CORE_BIN) build internal/loader/tsreader/testdata/services/$$s --out /tmp/superschematic-cli-smoke || exit 1; done
	@for s in fixture-db-json fixture-general-yaml fixture-engine-db-json fixture-behaviors-json; do \
		$(CORE_BIN) build internal/loader/testdata/services/$$s --out /tmp/superschematic-cli-smoke || exit 1; done
	@grep -q '"namespace": "fixture-engine-db"' /tmp/superschematic-cli-smoke/types/go/fixture-engine-db/engine/fixture-engine-db.json \
		|| { echo "cli-smoke: the engine fixture's build wrote no engine document"; exit 1; }
	@! grep -qi 'ticket' /tmp/superschematic-cli-smoke/sql/fixture-engine-db/create.sql \
		|| { echo "cli-smoke: sql wrote a table for an engine type"; exit 1; }
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-behaviors-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-cross-instance-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-rollups-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-search-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-reactions-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-variants-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-branches-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-staterules-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-approvals-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-numbering-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-timers-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-workqueue-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-commerce-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-derived-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(CORE_BIN) build internal/loader/testdata/services/fixture-commerce-currency-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>/tmp/superschematic-cli-smoke/commerce-currency-ir.json
	@grep -q '"name": "Finance.Currency"' /tmp/superschematic-cli-smoke/commerce-currency-ir.json \
		|| { echo "cli-smoke: the commerce currency fixture's IR names no Finance.Currency"; exit 1; }
	@$(CORE_BIN) json-schema >/tmp/superschematic-cli-smoke/schema-file.json
	@for b in Workflow Comments Revisions Dependencies Links Rollups Search Reactions Constants Variants Branches StateRules Approvals Numbering Timers Derived Provenance \
		Lease Assignment Queue Presence Blueprint Budget Retries Stock Calendar Holds Payments; do \
		grep -q "\"name\": \"$$b\"" /tmp/superschematic-cli-smoke/behaviors-ir.json && grep -q "\"const\": \"$$b\"" /tmp/superschematic-cli-smoke/schema-file.json \
			|| { echo "cli-smoke: the core binary does not carry behavior $$b"; exit 1; }; done

# examples/migrations: plans its DB service from v1 to v2 for Postgres and
# SQLite with the core alone, gates the plans on their hazards, and applies
# them with superschematic-migrate to a SQLite file, and to a database of
# its own on the Postgres SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL names
# when it is set. Needs sqlite3, and psql for Postgres. UPDATE=1 rewrites
# the outputs the docs site quotes (testdata/generated/ there).
example-migrations:
	examples/migrations/scripts/check.sh

# The extraction scrub: the only allowed maintainer mentions are the license
# holder, the GitHub org in module paths and publisher registrations, and the
# maintainer lines; source-tree identifiers, planning ids (wave, review and
# phase numbers) and schema-kind names fail it, and so do the field
# directives D18 moved out of the core IR.
scrub:
	scripts/scrub-check.sh

# Every version site agrees with versions.env, and bump_version.py, which
# writes them, passes its tests. CI runs both in the scrub job.
versions:
	python3 scripts/bump_version.py check
	python3 -m unittest discover -s scripts -p 'test_*.py'

test: go-test catalog-check schema-file-types-check behaviors-check ts python rust engine-rust engine-go engine-link-check cli-smoke example-migrations versions

lint: go-vet go-fmt-check go-lint scrub

vet: go-vet

fmt:
	gofmt -w $$(git ls-files '*.go')
	cd runtime/http/rust && cargo fmt
	cd runtime/versiongraph/rust && cargo fmt
	cd runtime/versiongraph/rust-engine && cargo fmt
	cd runtime/versiongraph/python && cargo fmt
	cd runtime/engine/rust && cargo fmt --all

clean:
	rm -rf bin
