# superschematic (the schema compiler). Convenience targets for the Go
# modules, the TypeScript authoring packages, the schema runtimes and the
# TypeScript and Rust http runtimes. Mirrors the CI workflow gates (.github/workflows/ci.yml).
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
export CGO_LDFLAGS := $(shell scripts/superscalar-dep.sh --print)

GO_MODULES := . ir runtime/schema/go runtime/http/go
BIN := bin/superschematic

# build-all keys its cache on a hash of this binary. -trimpath drops the
# checkout's absolute source paths, and -buildvcs=false drops the revision,
# time and dirty bit Go stamps when the checkout's .git is a directory, so a
# commit or an edit that changes no Go source leaves the binary, and its
# cache entries, as they were. release.yml builds with the same two flags.
GO_BUILD_FLAGS := -trimpath -buildvcs=false

.PHONY: all setup build test lint fmt vet go-build go-test go-vet go-fmt-check go-lint \
        go-goldens catalog-check ts python rust docs cli-smoke scrub clean

all: build test lint

# Check out and build the superscalar dependency (Go static archive and
# TypeScript binding). Needs git, a Rust toolchain, bun and node.
setup:
	scripts/superscalar-dep.sh
	cd packages && bun install
	cd runtime/schema/typescript && bun install
	cd runtime/http/typescript && bun install
	cd runtime/schema/python && uv sync

build: go-build $(BIN)

$(BIN): FORCE
	go build $(GO_BUILD_FLAGS) -o $(BIN) ./cmd/superschematic

FORCE:

go-build:
	@for m in $(GO_MODULES); do echo "==> go build $$m"; (cd $$m && go build ./...) || exit 1; done

go-vet:
	@for m in $(GO_MODULES); do echo "==> go vet $$m"; (cd $$m && go vet ./...) || exit 1; done

go-test:
	@for m in $(GO_MODULES); do echo "==> go test $$m"; (cd $$m && go test -count=1 ./...) || exit 1; done

go-fmt-check:
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$out" ]; then echo "gofmt: files need formatting:"; echo "$$out"; exit 1; fi

go-lint:
	@for m in $(GO_MODULES); do echo "==> golangci-lint $$m"; (cd $$m && golangci-lint run ./...) || exit 1; done

# Rewrite every golden file from the generators. Review the diff by eye.
go-goldens:
	@for p in $$(grep -rl 'flag.Bool("update' --include='*_test.go' . | xargs -n1 dirname | sort -u); do \
		go test -count=1 $$p -update || exit 1; done

# The TypeScript and Python scalar catalogs are written from the superscalar
# Go package. CI fails when a committed catalog differs.
catalog-check:
	go run ./internal/tools/scalarcatalog -check

ts:
	cd packages && bun install --frozen-lockfile && bun run typecheck && bun test
	cd runtime/schema/typescript && bun install --frozen-lockfile && bun run typecheck && bun run build && bun run test
	cd runtime/http/typescript && bun install --frozen-lockfile && bun run test

python:
	cd runtime/schema/python && uv run pytest -q

rust:
	cd runtime/http/rust && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test

# Starlight site. CI runs this as the docs job (D9); release.yml deploys it.
docs:
	cd docs && npm ci && npm run build

# The binary with no extension linked builds a DB, an API and a General
# service from the fixture corpus.
cli-smoke: $(BIN)
	@rm -rf /tmp/superschematic-cli-smoke
	@for s in fixture-db fixture-api fixture-general; do \
		$(BIN) build internal/loader/tsreader/testdata/services/$$s --out /tmp/superschematic-cli-smoke || exit 1; done
	@for s in fixture-db-json fixture-general-yaml; do \
		$(BIN) build internal/loader/testdata/services/$$s --out /tmp/superschematic-cli-smoke || exit 1; done

# The extraction scrub: the only allowed maintainer mentions are the license
# holder, the GitHub org in module paths and publisher registrations, and the
# maintainer lines; source-tree identifiers and planning ids (wave, review and
# phase numbers) fail it, and so do transform* field directives outside the
# core IR allowlist.
scrub:
	scripts/scrub-check.sh

test: go-test catalog-check ts python rust cli-smoke

lint: go-vet go-fmt-check go-lint scrub

vet: go-vet

fmt:
	gofmt -w $$(git ls-files '*.go')
	cd runtime/http/rust && cargo fmt

clean:
	rm -rf bin
