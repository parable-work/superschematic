package resolvers_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/registry"

	"github.com/parable-work/superschematic/extensions/resolvers"
)

// commit is a commit's tracked files in memory, as a resolver reads one.
// It counts what is read, and refuses a file over its limit as a tree
// over git does.
type commit struct {
	files   map[string]string
	entries []registry.TaskTreeEntry
	read    []string
}

func newCommit(files map[string]string) *commit {
	c := &commit{files: files}
	for name := range files {
		c.entries = append(c.entries, registry.TaskTreeEntry{Path: name, Mode: "100644", ID: name})
	}
	sort.Slice(c.entries, func(i, j int) bool { return c.entries[i].Path < c.entries[j].Path })
	return c
}

func (c *commit) Entries() []registry.TaskTreeEntry { return c.entries }

func (c *commit) Entry(name string) (registry.TaskTreeEntry, bool) {
	if _, ok := c.files[name]; !ok {
		return registry.TaskTreeEntry{}, false
	}
	return registry.TaskTreeEntry{Path: name, Mode: "100644", ID: name}, true
}

func (c *commit) Read(name string) ([]byte, error) {
	data, ok := c.files[name]
	if !ok {
		return nil, fmt.Errorf("%s is not a tracked file of the commit", name)
	}
	c.read = append(c.read, name)
	return []byte(data), nil
}

// resolve runs the registered resolver of a kind on a commit, the spec
// held to the resolver's schema first, as the plan holds it.
func resolve(t *testing.T, files map[string]string, kind, spec string) (registry.TaskResolution, error) {
	t.Helper()
	reg, err := registry.Assemble(registry.DefaultNaming(), resolvers.Extension{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	resolver, ok := reg.TaskResolver(kind)
	if !ok || resolver.Extension != resolvers.Name {
		t.Fatalf("resolver %s: %+v, %v", kind, resolver, ok)
	}
	if err := reg.CheckTaskInput(kind, json.RawMessage(spec)); err != nil {
		return registry.TaskResolution{}, err
	}
	res, err := resolver.Resolve(registry.TaskResolveRequest{Tree: newCommit(files), Task: "task", Spec: json.RawMessage(spec)})
	sort.Strings(res.Paths)
	return res, err
}

func mustResolve(t *testing.T, files map[string]string, kind, spec string) registry.TaskResolution {
	t.Helper()
	res, err := resolve(t, files, kind, spec)
	if err != nil {
		t.Fatalf("%s %s: %v", kind, spec, err)
	}
	return res
}

func wantPaths(t *testing.T, res registry.TaskResolution, want ...string) {
	t.Helper()
	sort.Strings(want)
	if !reflect.DeepEqual(res.Paths, want) {
		t.Fatalf("paths:\n got  %v\n want %v", res.Paths, want)
	}
}

func wantGenerated(t *testing.T, res registry.TaskResolution, want ...registry.TaskGeneratedPath) {
	t.Helper()
	if len(res.Generated) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(res.Generated, want) {
		t.Fatalf("generated:\n got  %v\n want %v", res.Generated, want)
	}
}

func wantError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want an error holding %q, got %v", want, err)
	}
}

func TestTheExtensionRegistersItsFourKinds(t *testing.T) {
	reg, err := registry.Assemble(registry.DefaultNaming(), resolvers.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	// The core's two, and the extension's four.
	if got := strings.Join(reg.TaskResolvers(), ","); got != "go-module,image,js-package,path,rust-crate,schema" {
		t.Fatalf("resolvers = %s", got)
	}
	for kind, spec := range map[string]string{
		resolvers.GoModule:  `{"dir":"go","tags":["x"]}`,
		resolvers.RustCrate: `{}`,
		resolvers.JSPackage: `{"dir":""}`,
		resolvers.Image:     `{"context":"."}`,
	} {
		if err := reg.CheckTaskInput(kind, json.RawMessage(spec)); err == nil {
			t.Errorf("%s takes the spec %s", kind, spec)
		}
	}
}

func goFiles() map[string]string {
	return map[string]string{
		"go.work":     "go 1.27\n\nuse (\n\t./go/api\n\t./go/lib\n\t./go/tools\n\t./schemas/dist/types/go/shop-db\n)\n",
		"go.work.sum": "sum\n",
		"README.md":   "# shop\n",
		"go/api/go.mod": `module example.com/shop/api

go 1.27

require (
	example.com/shop/lib v0.0.0
	example.com/shop/orm v0.0.0
	github.com/google/uuid v1.6.0
)

replace example.com/shop/orm => ../../schemas/dist/orm/shop-db
replace github.com/google/uuid => github.com/google/uuid v1.5.0
`,
		"go/api/go.sum":               "sums\n",
		"go/api/main.go":              "package main\n",
		"go/api/internal/h/h.go":      "package h\n",
		"go/api/testdata/golden.json": "{}\n",
		"go/api/tools/go.mod":         "module example.com/shop/api/tools\n",
		"go/api/tools/gen.go":         "package main\n",
		"go/lib/go.mod":               "module example.com/shop/lib\n\ngo 1.27\n\nrequire example.com/shop/base v0.0.0\n\nreplace example.com/shop/base => ../base\n",
		"go/lib/lib.go":               "package lib\n",
		"go/base/go.mod":              "module example.com/shop/base\n",
		"go/base/base.go":             "package base\n",
		"go/tools/go.mod":             "module example.com/shop/tools\n",
		"go/tools/tool.go":            "package main\n",
	}
}

// TestGoModule: a module is its own files without the modules nested in
// it, the modules its replaces point at, and the modules of its workspace
// it requires. What the commit does not hold is generated.
func TestGoModule(t *testing.T) {
	res := mustResolve(t, goFiles(), resolvers.GoModule, `{"dir":"go/api"}`)
	wantPaths(t, res,
		"go.work", "go.work.sum",
		"go/api/go.mod", "go/api/go.sum", "go/api/main.go", "go/api/internal/h/h.go", "go/api/testdata/golden.json",
		// The workspace uses go/lib, which go/api requires, and go/lib's
		// replace points at go/base.
		"go/lib/go.mod", "go/lib/lib.go",
		"go/base/go.mod", "go/base/base.go",
	)
	wantGenerated(t, res,
		// The ORM module a replace points at, and the types module the
		// workspace uses, are a build's.
		registry.TaskGeneratedPath{Path: "schemas/dist/orm/shop-db", From: "go/api/go.mod"},
		registry.TaskGeneratedPath{Path: "schemas/dist/types/go/shop-db", From: "go.work"},
	)

	// A module the workspace uses and nothing requires is not the
	// module's, and neither is a nested module's parent.
	res = mustResolve(t, goFiles(), resolvers.GoModule, `{"dir":"go/api/tools"}`)
	wantPaths(t, res, "go.work", "go.work.sum", "go/api/tools/go.mod", "go/api/tools/gen.go")

	// With no workspace, a module is itself and what it replaces.
	files := goFiles()
	delete(files, "go.work")
	delete(files, "go.work.sum")
	res = mustResolve(t, files, resolvers.GoModule, `{"dir":"go/lib"}`)
	wantPaths(t, res, "go/lib/go.mod", "go/lib/lib.go", "go/base/go.mod", "go/base/base.go")
	wantGenerated(t, res)

	// The repository's root is a module's directory too.
	res = mustResolve(t, map[string]string{"go.mod": "module example.com/root\n", "main.go": "package main\n", "sub/go.mod": "module example.com/root/sub\n", "sub/x.go": "package sub\n"}, resolvers.GoModule, `{"dir":"."}`)
	wantPaths(t, res, "go.mod", "main.go")

	_, err := resolve(t, goFiles(), resolvers.GoModule, `{"dir":"go"}`)
	wantError(t, err, "the commit holds no Go module at go: no tracked go/go.mod")
	_, err = resolve(t, goFiles(), resolvers.GoModule, `{"dir":"../other"}`)
	wantError(t, err, `"../other" is outside the repository`)
	files = goFiles()
	files["go/lib/go.mod"] = "module example.com/shop/lib\n\nreplace example.com/x => ../../../x\n"
	_, err = resolve(t, files, resolvers.GoModule, `{"dir":"go/lib"}`)
	wantError(t, err, `go/lib/go.mod names "../../../x", which is outside the repository`)
	files["go/lib/go.mod"] = "module example.com/shop/lib\n\nrequire (\n"
	_, err = resolve(t, files, resolvers.GoModule, `{"dir":"go/lib"}`)
	wantError(t, err, "go/lib/go.mod:")
}

func cargoFiles() map[string]string {
	return map[string]string{
		"Cargo.toml": `[workspace]
members = ["crates/*", "tools/gen", "schemas/dist/rust/*"]
exclude = ["crates/scratch"]

[workspace.dependencies]
shop-model = { path = "crates/model" }
serde = "1"

[patch.crates-io]
tokio = { path = "vendor/tokio" }
`,
		"Cargo.lock":          "lock\n",
		".cargo/config.toml":  "[build]\n",
		"rust-toolchain.toml": "[toolchain]\n",
		"crates/api/Cargo.toml": `[package]
name = "shop-api"

[dependencies]
shop-model = { workspace = true }
serde = { workspace = true }
shop-types = { path = "../../schemas/dist/rust/shop-types" }

[dev-dependencies]
shop-testkit = { path = "../testkit" }

[target.'cfg(unix)'.dependencies]
shop-unix = { path = "../unix" }
`,
		"crates/api/src/lib.rs":         "\n",
		"crates/api/.cargo/config.toml": "[env]\n",
		"crates/api/fuzz/Cargo.toml":    "[package]\nname = \"fuzz\"\n",
		"crates/api/fuzz/src/main.rs":   "\n",
		"crates/model/Cargo.toml":       "[package]\nname = \"shop-model\"\n\n[build-dependencies]\nshop-build = { path = \"../build\" }\n",
		"crates/model/src/lib.rs":       "\n",
		"crates/build/Cargo.toml":       "[package]\nname = \"shop-build\"\n",
		"crates/build/src/lib.rs":       "\n",
		"crates/testkit/Cargo.toml":     "[package]\nname = \"shop-testkit\"\n",
		"crates/testkit/src/lib.rs":     "\n",
		"crates/unix/Cargo.toml":        "[package]\nname = \"shop-unix\"\n",
		"crates/unix/src/lib.rs":        "\n",
		"crates/unrelated/Cargo.toml":   "[package]\nname = \"unrelated\"\n",
		"crates/unrelated/src/lib.rs":   "\n",
		"crates/scratch/Cargo.toml":     "[package]\nname = \"scratch\"\n",
		"vendor/tokio/Cargo.toml":       "[package]\nname = \"tokio\"\n",
		"vendor/tokio/src/lib.rs":       "\n",
		"standalone/Cargo.toml":         "[package]\nname = \"standalone\"\nworkspace = \"../missing\"\n",
		"standalone/src/main.rs":        "\n",
	}
}

// TestRustCrate: a crate is its own files without the crates nested in
// it, each crate its dependency tables name by a path, its workspace's
// manifest, lockfile and patches, and the cargo configuration above it.
func TestRustCrate(t *testing.T) {
	res := mustResolve(t, cargoFiles(), resolvers.RustCrate, `{"dir":"crates/api"}`)
	wantPaths(t, res,
		"Cargo.toml", "Cargo.lock", ".cargo/config.toml", "rust-toolchain.toml",
		"crates/api/Cargo.toml", "crates/api/src/lib.rs", "crates/api/.cargo/config.toml",
		// workspace = true reads the workspace's path; model's build
		// dependency follows.
		"crates/model/Cargo.toml", "crates/model/src/lib.rs",
		"crates/build/Cargo.toml", "crates/build/src/lib.rs",
		"crates/testkit/Cargo.toml", "crates/testkit/src/lib.rs",
		"crates/unix/Cargo.toml", "crates/unix/src/lib.rs",
		// The workspace patches tokio for every member.
		"vendor/tokio/Cargo.toml", "vendor/tokio/src/lib.rs",
	)
	wantGenerated(t, res, registry.TaskGeneratedPath{Path: "schemas/dist/rust/shop-types", From: "crates/api/Cargo.toml"})

	// The workspace itself is every member, without what it excludes,
	// and the directory of a member pattern that names no tracked crate.
	res = mustResolve(t, cargoFiles(), resolvers.RustCrate, `{"dir":"."}`)
	for _, want := range []string{"crates/unrelated/src/lib.rs", "crates/api/src/lib.rs", "vendor/tokio/src/lib.rs", "Cargo.lock"} {
		if !contains(res.Paths, want) {
			t.Errorf("the workspace lacks %s", want)
		}
	}
	for _, not := range []string{"crates/scratch/Cargo.toml", "crates/api/fuzz/src/main.rs", "standalone/src/main.rs"} {
		if contains(res.Paths, not) {
			t.Errorf("the workspace holds %s", not)
		}
	}
	wantGenerated(t, res,
		registry.TaskGeneratedPath{Path: "schemas/dist/rust", From: "Cargo.toml"},
		registry.TaskGeneratedPath{Path: "schemas/dist/rust/shop-types", From: "crates/api/Cargo.toml"},
		registry.TaskGeneratedPath{Path: "tools/gen", From: "Cargo.toml"},
	)

	// A member may sit beside the workspace's directory, not under it.
	res = mustResolve(t, map[string]string{
		"engine/rust/Cargo.toml":         "[workspace]\nmembers = [\"core\", \"../../commerce/rust\"]\n",
		"engine/rust/core/Cargo.toml":    "[package]\nname = \"core\"\n",
		"engine/rust/core/src/lib.rs":    "\n",
		"commerce/rust/Cargo.toml":       "[package]\nname = \"commerce\"\n",
		"commerce/rust/src/lib.rs":       "\n",
		"commerce/rust/tests/it.rs":      "\n",
		"commerce/typescript/index.ts":   "\n",
		"engine/typescript/package.json": "{}\n",
	}, resolvers.RustCrate, `{"dir":"engine/rust"}`)
	wantPaths(t, res, "engine/rust/Cargo.toml", "engine/rust/core/Cargo.toml", "engine/rust/core/src/lib.rs", "commerce/rust/Cargo.toml", "commerce/rust/src/lib.rs", "commerce/rust/tests/it.rs")
	wantGenerated(t, res)

	// A crate whose workspace the commit does not hold says so.
	res = mustResolve(t, cargoFiles(), resolvers.RustCrate, `{"dir":"standalone"}`)
	wantGenerated(t, res, registry.TaskGeneratedPath{Path: "missing", From: "standalone/Cargo.toml"})

	_, err := resolve(t, cargoFiles(), resolvers.RustCrate, `{"dir":"crates"}`)
	wantError(t, err, "the commit holds no Rust crate at crates: no tracked crates/Cargo.toml")
	files := cargoFiles()
	files["crates/unix/Cargo.toml"] = "[package\n"
	_, err = resolve(t, files, resolvers.RustCrate, `{"dir":"crates/api"}`)
	wantError(t, err, "crates/unix/Cargo.toml:")
}

func jsFiles() map[string]string {
	return map[string]string{
		"package.json":       `{"name": "shop", "private": true, "workspaces": ["web/*", "packages/**", "schemas/dist/types/typescript/*", "!web/legacy"]}`,
		"bun.lock":           "{}\n",
		".npmrc":             "registry=https://registry.npmjs.org\n",
		"tsconfig.base.json": "{\n  // the base every package extends\n  \"compilerOptions\": { \"strict\": true, },\n}\n",
		"web/shop/package.json": `{
  "name": "@acme/web-shop",
  "dependencies": {"@acme/ui": "workspace:*", "@acme/shop-api-types": "*", "react": "^19.0.0", "@acme/icons": "file:../../vendor/icons"},
  "devDependencies": {"@acme/eslint-config": "workspace:^", "@acme/tarball": "file:../../vendor/tarball.tgz"}
}`,
		"web/shop/tsconfig.json":              `{"extends": ["../../tsconfig.base.json", "@tsconfig/vite"], /* app */ "include": ["src"]}`,
		"web/shop/src/app.ts":                 "\n",
		"web/shop/fixtures/pkg/package.json":  `{"name": "fixture"}`,
		"web/legacy/package.json":             `{"name": "@acme/legacy"}`,
		"packages/ui/package.json":            `{"name": "@acme/ui", "dependencies": {"@acme/tokens": "workspace:*"}}`,
		"packages/ui/src/button.ts":           "\n",
		"packages/ui/tokens/package.json":     `{"name": "@acme/tokens"}`,
		"packages/ui/tokens/tokens.ts":        "\n",
		"packages/eslint-config/package.json": `{"name": "@acme/eslint-config"}`,
		"packages/eslint-config/index.js":     "\n",
		"packages/unrelated/package.json":     `{"name": "@acme/unrelated"}`,
		"vendor/icons/package.json":           `{"name": "@acme/icons"}`,
		"vendor/icons/index.js":               "\n",
		"vendor/tarball.tgz":                  "bytes\n",
		"standalone/package.json":             `{"name": "standalone", "dependencies": {"left-pad": "*", "@acme/gen": "file:../gen/pkg"}}`,
		"standalone/index.js":                 "\n",
	}
}

// TestJSPackage: a package is its own files without the workspace
// members nested in it, the members it depends on by name, the packages
// it names by a path, the workspace root's manifests and lockfiles, and
// the tsconfigs it extends.
func TestJSPackage(t *testing.T) {
	res := mustResolve(t, jsFiles(), resolvers.JSPackage, `{"dir":"web/shop"}`)
	wantPaths(t, res,
		"package.json", "bun.lock", ".npmrc", "tsconfig.base.json",
		// A fixture's package.json is no member, so it is the package's.
		"web/shop/package.json", "web/shop/tsconfig.json", "web/shop/src/app.ts", "web/shop/fixtures/pkg/package.json",
		// @acme/ui by name, without the member nested in it, which it
		// depends on by name too.
		"packages/ui/package.json", "packages/ui/src/button.ts",
		"packages/ui/tokens/package.json", "packages/ui/tokens/tokens.ts",
		"packages/eslint-config/package.json", "packages/eslint-config/index.js",
		"vendor/icons/package.json", "vendor/icons/index.js",
		"vendor/tarball.tgz",
	)
	// @acme/shop-api-types is no tracked member and its version is one a
	// workspace answers: the member pattern that names no tracked package
	// is where it would be.
	wantGenerated(t, res, registry.TaskGeneratedPath{Path: "schemas/dist/types/typescript", From: "package.json"})

	// A package in no workspace: its own files, and a path the commit
	// does not hold. A `*` with no workspace is the registry's.
	files := jsFiles()
	delete(files, "package.json")
	res = mustResolve(t, files, resolvers.JSPackage, `{"dir":"standalone"}`)
	wantPaths(t, res, "standalone/package.json", "standalone/index.js")
	wantGenerated(t, res, registry.TaskGeneratedPath{Path: "gen/pkg", From: "standalone/package.json"})

	// A pnpm workspace is declared in its own file.
	files = map[string]string{
		"pnpm-workspace.yaml":   "packages:\n  - 'apps/*'\n  - 'libs/*'\n",
		"pnpm-lock.yaml":        "lock\n",
		"package.json":          `{"name": "root"}`,
		"apps/web/package.json": `{"name": "web", "dependencies": {"lib": "workspace:*"}}`,
		"libs/lib/package.json": `{"name": "lib"}`,
		"libs/lib/index.ts":     "\n",
	}
	res = mustResolve(t, files, resolvers.JSPackage, `{"dir":"apps/web"}`)
	wantPaths(t, res, "pnpm-workspace.yaml", "pnpm-lock.yaml", "package.json", "apps/web/package.json", "libs/lib/package.json", "libs/lib/index.ts")

	// A workspace dependency no member has, in a workspace whose every
	// pattern names tracked packages, is a slip.
	files["apps/web/package.json"] = `{"name": "web", "dependencies": {"libb": "workspace:*"}}`
	_, err := resolve(t, files, resolvers.JSPackage, `{"dir":"apps/web"}`)
	wantError(t, err, `apps/web/package.json depends on libb by "workspace:*", and no package of the workspace at . has that name`)

	// The workspace root is a package too, without its members.
	res = mustResolve(t, jsFiles(), resolvers.JSPackage, `{"dir":"."}`)
	if contains(res.Paths, "packages/unrelated/package.json") || contains(res.Paths, "web/shop/src/app.ts") {
		t.Errorf("the root package holds a member: %v", res.Paths)
	}
	if !contains(res.Paths, "web/legacy/package.json") || !contains(res.Paths, "vendor/icons/index.js") {
		t.Errorf("the root package lacks what is no member: %v", res.Paths)
	}

	_, err = resolve(t, jsFiles(), resolvers.JSPackage, `{"dir":"web"}`)
	wantError(t, err, "the commit holds no JS package at web: no tracked web/package.json")
	files = jsFiles()
	files["web/shop/tsconfig.json"] = `{"extends": "../../tsconfig.build.json"}`
	res = mustResolve(t, files, resolvers.JSPackage, `{"dir":"web/shop"}`)
	if !contains(generatedPaths(res), "tsconfig.build.json") {
		t.Errorf("a tsconfig the commit does not hold is generated: %v", res.Generated)
	}
}

func imageFiles() map[string]string {
	return map[string]string{
		"docker/api.Dockerfile": `# syntax=docker/dockerfile:1
# The API's image.
FROM golang:1.27@sha256:aaaa AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY --chown=app:app go/api \
     go/lib \
     ./
COPY ["scripts/entry point.sh", "/entry.sh"]
RUN --mount=type=cache,target=/root/.cache --mount=type=bind,source=configs,target=/configs go build ./...
RUN <<EOF
COPY this/is/a/script /not/an/instruction
EOF
ADD https://example.com/tool.tgz /tmp/
ADD assets/*.png /assets/

FROM gcr.io/distroless/static@sha256:bbbb
COPY --from=build /src/api /api
COPY bin/migrate /migrate
`,
		"docker/api.Dockerfile.dockerignore": "**/*.md\n",
		".dockerignore":                      "node_modules\n",
		"go.mod":                             "module x\n",
		"go.sum":                             "\n",
		"go/api/main.go":                     "package main\n",
		"go/api/README.md":                   "# api\n",
		"go/lib/lib.go":                      "package lib\n",
		"go/other/other.go":                  "package other\n",
		"scripts/entry point.sh":             "#!/bin/sh\n",
		"scripts/other.sh":                   "#!/bin/sh\n",
		"configs/app.yaml":                   "a: 1\n",
		"assets/logo.png":                    "png\n",
		"assets/logo.svg":                    "svg\n",
		"assets/icons/small.png":             "png\n",
		"web/Dockerfile":                     "FROM node@sha256:cccc\nCOPY . .\n",
		"web/package.json":                   "{}\n",
		"web/src/app.ts":                     "\n",
		"var/Dockerfile":                     "FROM scratch\nARG DIR\nCOPY ${DIR} /app\n",
		"var/a.txt":                          "a\n",
	}
}

// TestImage: an image is its Dockerfile, its context's .dockerignore and
// each file of the context a COPY, an ADD or a bind mount names. A
// source the commit does not hold is generated.
func TestImage(t *testing.T) {
	res := mustResolve(t, imageFiles(), resolvers.Image, `{"dockerfile":"docker/api.Dockerfile","context":"."}`)
	wantPaths(t, res,
		"docker/api.Dockerfile", "docker/api.Dockerfile.dockerignore", ".dockerignore",
		"go.mod", "go.sum",
		// The ignore file is not applied: the README stays in.
		"go/api/main.go", "go/api/README.md", "go/lib/lib.go",
		"scripts/entry point.sh",
		"configs/app.yaml",
		// A pattern matches within one directory.
		"assets/logo.png",
	)
	// The migrate binary is no tracked file: a task builds it. The copy
	// from the build stage and the URL are not the context's.
	wantGenerated(t, res, registry.TaskGeneratedPath{Path: "bin/migrate", From: "docker/api.Dockerfile"})

	// The context is the Dockerfile's directory unless the spec names
	// one, and `COPY . .` takes all of it.
	res = mustResolve(t, imageFiles(), resolvers.Image, `{"dockerfile":"web/Dockerfile"}`)
	wantPaths(t, res, "web/Dockerfile", "web/package.json", "web/src/app.ts")

	// A source that holds a variable takes the whole context.
	res = mustResolve(t, imageFiles(), resolvers.Image, `{"dockerfile":"var/Dockerfile"}`)
	wantPaths(t, res, "var/Dockerfile", "var/a.txt")

	_, err := resolve(t, imageFiles(), resolvers.Image, `{"dockerfile":"docker/web.Dockerfile"}`)
	wantError(t, err, "the commit holds no Dockerfile at docker/web.Dockerfile")
	files := imageFiles()
	files["var/Dockerfile"] = "FROM scratch\nCOPY ../outside /app\n"
	_, err = resolve(t, files, resolvers.Image, `{"dockerfile":"var/Dockerfile","context":"."}`)
	wantError(t, err, `var/Dockerfile names "../outside", which is outside the repository`)
}

// TestAResolverReadsManifestsAndNothingElse: a resolution reads the
// manifests it parses, by name, and no source file.
func TestAResolverReadsManifestsAndNothingElse(t *testing.T) {
	reg, err := registry.Assemble(registry.DefaultNaming(), resolvers.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	for kind, c := range map[string]struct {
		files map[string]string
		spec  string
	}{
		resolvers.GoModule:  {goFiles(), `{"dir":"go/api"}`},
		resolvers.RustCrate: {cargoFiles(), `{"dir":"crates/api"}`},
		resolvers.JSPackage: {jsFiles(), `{"dir":"web/shop"}`},
		resolvers.Image:     {imageFiles(), `{"dockerfile":"docker/api.Dockerfile","context":"."}`},
	} {
		tree := newCommit(c.files)
		resolver, _ := reg.TaskResolver(kind)
		first, err := resolver.Resolve(registry.TaskResolveRequest{Tree: tree, Spec: json.RawMessage(c.spec)})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		for _, name := range tree.read {
			base := name[strings.LastIndex(name, "/")+1:]
			manifest := base == "go.mod" || base == "go.work" || base == "Cargo.toml" || base == "package.json" ||
				base == "pnpm-workspace.yaml" || strings.HasPrefix(base, "tsconfig") || strings.HasSuffix(base, "Dockerfile")
			if !manifest {
				t.Errorf("%s read %s, which is no manifest", kind, name)
			}
		}
		// One tree and one spec give one answer.
		second, err := resolver.Resolve(registry.TaskResolveRequest{Tree: newCommit(c.files), Spec: json.RawMessage(c.spec)})
		if err != nil || !reflect.DeepEqual(first, second) {
			t.Errorf("%s resolved one commit two ways: %v", kind, err)
		}
	}
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func generatedPaths(res registry.TaskResolution) []string {
	var out []string
	for _, g := range res.Generated {
		out = append(out, g.Path)
	}
	return out
}
