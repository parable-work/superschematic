# superschematic (the schema compiler). Convenience targets for the Go
# modules, the TypeScript authoring packages, the schema runtimes, the
# TypeScript and Rust http runtimes, the version-graph core, the engine and
# the engine's work-queue package.
# Mirrors the CI workflow gates (.github/workflows/ci.yml).
#
#   make setup && make all

# Pins are tools.env (Go, Node, Bun, Python, uv, Rust, golangci-lint) and
# superscalar.pin (the superscalar commit). Both are read here and by CI,
# never inlined.
include tools.env
export GOTOOLCHAIN := go$(GO_VERSION)

# The Go binding of superscalar is cgo against a static archive that
# scripts/superscalar-dep.sh builds under third_party/superscalar. Every go
# command that links a scalar-dependent package needs this in CGO_LDFLAGS.
# The version-graph binding (runtime/versiongraph/go) links the core's static
# archive, which scripts/versiongraph-archive.sh (make versiongraph) stages.
export CGO_LDFLAGS := $(shell scripts/superscalar-dep.sh --print) $(shell scripts/versiongraph-archive.sh --print)

GO_MODULES := . ir runtime/schema/go runtime/http/go runtime/versiongraph/go runtime/migrate/go
BIN := bin/superschematic

# build-all keys its cache on a hash of this binary. -trimpath drops the
# checkout's absolute source paths, and -buildvcs=false drops the revision,
# time and dirty bit Go stamps when the checkout's .git is a directory, so a
# commit or an edit that changes no Go source leaves the binary, and its
# cache entries, as they were. release.yml builds with the same two flags.
GO_BUILD_FLAGS := -trimpath -buildvcs=false

.PHONY: all setup build test lint fmt vet go-build go-test go-vet go-fmt-check go-lint \
        go-goldens catalog-check schema-file-types schema-file-types-check behaviors behaviors-check ts python rust \
        versiongraph versiongraph-scenarios versiongraph-scenarios-ts versiongraph-scenarios-rust \
        versiongraph-scenarios-python docs cli-smoke scrub versions clean

all: build test lint

# Check out and build the superscalar dependency (Go static archive and
# TypeScript binding). Needs git, a Rust toolchain, bun and node.
setup:
	scripts/superscalar-dep.sh
	scripts/versiongraph-archive.sh
	cd packages && bun install
	cd runtime/schema/typescript && bun install
	cd runtime/http/typescript && bun install
	cd runtime/versiongraph/typescript && bun install
	cd runtime/engine/typescript && bun install
	cd runtime/engine-workqueue/typescript && bun install
	cd runtime/schema/python && uv sync
	cd runtime/versiongraph/python && uv sync

build: go-build $(BIN)

$(BIN): FORCE
	go build $(GO_BUILD_FLAGS) -o $(BIN) ./cmd/superschematic

FORCE:

go-build: versiongraph
	@for m in $(GO_MODULES); do echo "==> go build $$m"; (cd $$m && go build ./...) || exit 1; done

go-vet:
	@for m in $(GO_MODULES); do echo "==> go vet $$m"; (cd $$m && go vet ./...) || exit 1; done

go-test: versiongraph
	@for m in $(GO_MODULES); do echo "==> go test $$m"; (cd $$m && go test -count=1 ./...) || exit 1; done

go-fmt-check:
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$out" ]; then echo "gofmt: files need formatting:"; echo "$$out"; exit 1; fi

go-lint:
	@for m in $(GO_MODULES); do echo "==> golangci-lint $$m"; (cd $$m && golangci-lint run ./...) || exit 1; done

# Rewrite every golden file from the generators, and the schema-file JSON
# Schema and TypeScript types. Review the diff by eye.
go-goldens: schema-file-types
	@for p in $$(grep -rl 'flag.Bool("update' --include='*_test.go' . | xargs -n1 dirname | sort -u); do \
		go test -count=1 $$p -update || exit 1; done

# The TypeScript and Python scalar catalogs are written from the superscalar
# Go package. CI fails when a committed catalog differs.
catalog-check:
	go run ./internal/tools/scalarcatalog -check

# The schema-file JSON Schema and TypeScript types in ir/typescript are
# written from the IR structs. CI fails when a committed file differs.
schema-file-types:
	go run ./internal/tools/schemafiletypes

schema-file-types-check:
	go run ./internal/tools/schemafiletypes -check

# The engine and the work-queue package implement the core's behaviors over
# a copy of each declaration (internal/registry/behaviors), which the core
# binary writes into the package that implements it (--package). CI fails
# when a committed copy differs.
ENGINE_DECLARATIONS := runtime/engine/typescript/src/behaviors/core/declarations
WORKQUEUE_DECLARATIONS := runtime/engine-workqueue/typescript/src/declarations

behaviors:
	go run ./cmd/superschematic behaviors --package @superschematic/engine --out $(ENGINE_DECLARATIONS)
	go run ./cmd/superschematic behaviors --package @superschematic/engine-workqueue --out $(WORKQUEUE_DECLARATIONS)

behaviors-check:
	go run ./cmd/superschematic behaviors --package @superschematic/engine --out $(ENGINE_DECLARATIONS) --check
	go run ./cmd/superschematic behaviors --package @superschematic/engine-workqueue --out $(WORKQUEUE_DECLARATIONS) --check

ts:
	cd packages && bun install --frozen-lockfile && bun run typecheck && bun test
	cd runtime/schema/typescript && bun install --frozen-lockfile && bun run typecheck && bun run build && bun run test
	cd runtime/http/typescript && bun install --frozen-lockfile && bun run build && bun run test
	cd runtime/versiongraph/typescript && bun install --frozen-lockfile && bun run typecheck && bun run test
	cd runtime/engine/typescript && bun install --frozen-lockfile && bun run typecheck && bun run build && bun run test
	cd runtime/engine-workqueue/typescript && bun install --frozen-lockfile && bun run typecheck && bun run test
	examples/engine-notes/scripts/check.sh

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
# the order a serde_json map keeps. The schema runtime's run again with
# arbitrary_precision too, which superscalar's default lossless-json feature
# turns on: an error map and a number check must not depend on either.
rust:
	cd runtime/http/rust && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test
	cd runtime/schema/rust && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test \
		&& cargo test --features serde_json/arbitrary_precision,serde_json/preserve_order
	cd runtime/versiongraph/rust && cargo fmt --check && cargo clippy --all-targets -- -D warnings \
		&& cargo clippy --target wasm32-unknown-unknown -- -D warnings && cargo test \
		&& cargo test --features serde_json/preserve_order
	cd runtime/versiongraph/rust-engine && cargo fmt --check && cargo clippy --all-targets -- -D warnings \
		&& cargo clippy --no-default-features -- -D warnings && cargo test \
		&& cargo test --features serde_json/preserve_order

# The version-graph core's static archive, staged where the Go binding links
# it (runtime/versiongraph/go/lib/<goos>_<goarch>).
versiongraph:
	scripts/versiongraph-archive.sh >/dev/null

# Every version-graph scenario (runtime/versiongraph/testdata/scenarios)
# through the Go engine and its Postgres adapter, against the Postgres that
# SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names.
versiongraph-scenarios: versiongraph
	@test -n "$$SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL" || \
		{ echo "set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to the Postgres the scenarios run against" >&2; exit 1; }
	cd runtime/versiongraph/go && go test -count=1 -v -run '^TestScenarios$$' ./engine/

# Every version-graph scenario through the TypeScript engine and its Postgres
# adapter, with the canonical vectors checked against Postgres and the
# adapter's, the sweeper's and the facade's own tests, against the Postgres
# that SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names.
versiongraph-scenarios-ts:
	@test -n "$$SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL" || \
		{ echo "set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to the Postgres the scenarios run against" >&2; exit 1; }
	cd runtime/versiongraph/typescript && bun install --frozen-lockfile && bun run build && \
		bun test test/scenarios.test.ts test/canonical.test.ts test/adapter.test.ts test/sweeper.test.ts \
		test/facade.test.ts

# Every version-graph scenario through the Rust engine and its Postgres
# adapter, and the crate's other Postgres tests (every canonical vector's
# rendering, the adapter, the sweeper), against the Postgres that
# SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names.
versiongraph-scenarios-rust:
	@test -n "$$SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL" || \
		{ echo "set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to the Postgres the scenarios run against" >&2; exit 1; }
	cd runtime/versiongraph/rust-engine && cargo test --tests -- --nocapture

# Every version-graph scenario through the Python engine and its Postgres
# adapter, and the package's other Postgres tests (every canonical vector's
# rendering, the adapter, the sweeper, the facade), against the Postgres that
# SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names.
versiongraph-scenarios-python:
	@test -n "$$SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL" || \
		{ echo "set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to the Postgres the scenarios run against" >&2; exit 1; }
	cd runtime/versiongraph/python && uv run pytest -v -rs tests/test_scenarios.py tests/test_canonical.py \
		tests/test_adapter.py tests/test_sweeper.py tests/test_facade.py

# Starlight site. CI runs this as the docs job (D9); release.yml deploys it.
docs:
	cd docs && npm ci && npm run build

# The binary with no extension linked builds a DB, an API and a General
# service from the fixture corpus, and loads fixture-behaviors-json,
# fixture-cross-instance-json, fixture-rollups-json, fixture-search-json,
# fixture-reactions-json and fixture-workqueue-json, whose types compose
# the core's behaviors (D10): --emit-ir carries all fifteen and json-schema
# admits them. The engine runs the first five documents with its own
# behaviors in runtime/engine/typescript/test/core-behaviors.test.ts, and
# the work-queue package runs the last in
# runtime/engine-workqueue/typescript/test/package.test.ts.
cli-smoke: $(BIN)
	@rm -rf /tmp/superschematic-cli-smoke
	@for s in fixture-db fixture-api fixture-general; do \
		$(BIN) build internal/loader/tsreader/testdata/services/$$s --out /tmp/superschematic-cli-smoke || exit 1; done
	@for s in fixture-db-json fixture-general-yaml; do \
		$(BIN) build internal/loader/testdata/services/$$s --out /tmp/superschematic-cli-smoke || exit 1; done
	@$(BIN) build internal/loader/testdata/services/fixture-behaviors-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(BIN) build internal/loader/testdata/services/fixture-cross-instance-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(BIN) build internal/loader/testdata/services/fixture-rollups-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(BIN) build internal/loader/testdata/services/fixture-search-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(BIN) build internal/loader/testdata/services/fixture-reactions-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(BIN) build internal/loader/testdata/services/fixture-workqueue-json --emit-ir --out /tmp/superschematic-cli-smoke \
		>>/tmp/superschematic-cli-smoke/behaviors-ir.json
	@$(BIN) json-schema >/tmp/superschematic-cli-smoke/schema-file.json
	@for b in Workflow Comments Revisions Dependencies Links Rollups Search Reactions Lease Assignment Queue Presence Blueprint Budget Retries; do \
		grep -q "\"name\": \"$$b\"" /tmp/superschematic-cli-smoke/behaviors-ir.json && grep -q "\"const\": \"$$b\"" /tmp/superschematic-cli-smoke/schema-file.json \
			|| { echo "cli-smoke: the core binary does not carry behavior $$b"; exit 1; }; done

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

test: go-test catalog-check schema-file-types-check behaviors-check ts python rust cli-smoke versions

lint: go-vet go-fmt-check go-lint scrub

vet: go-vet

fmt:
	gofmt -w $$(git ls-files '*.go')
	cd runtime/http/rust && cargo fmt
	cd runtime/versiongraph/rust && cargo fmt
	cd runtime/versiongraph/rust-engine && cargo fmt
	cd runtime/versiongraph/python && cargo fmt

clean:
	rm -rf bin
