package cigen

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// rulesOf sets st's RulesCheckout, RulesGo and RulesTypeScript (D103): the
// Go and TypeScript servers, jobs and workers of envs with a sql edge to a
// database a table of which has a field that is a behavior, whose ORM and
// repositories link the behaviors' core, when the naming file's [paths]
// rules_go or rules_typescript names a checkout of the core's Go module or
// npm package. A checkout outside the repository, where a CI job starts,
// is logged and left out. A database the build cannot load is left to the
// generators that read it.
func rulesOf(c registry.GenerateContext, name string, st *registry.CIStack, envs []*ir.ResolvedEnvironment, rel func(what, p string) (string, error)) {
	paths := c.Options.Paths
	checkout := rulesCheckout(paths)
	if checkout == "" {
		return
	}
	languages := map[string]string{}
	for _, env := range envs {
		for _, d := range env.Deployables {
			if d.Kind.RunsCode() {
				languages[d.Name] = d.Language
			}
		}
	}
	behaviors := map[string]bool{}
	var goNames, tsNames []string
	for _, env := range envs {
		for _, e := range env.Edges {
			if e.Kind != ir.EdgeSQL {
				continue
			}
			has, ok := behaviors[e.Service.Name]
			if !ok {
				has = fieldBehaviors(c, e.Service.Name)
				behaviors[e.Service.Name] = has
			}
			if !has {
				continue
			}
			switch languages[e.From] {
			case registry.APILanguageGo:
				goNames = append(goNames, e.From)
			case registry.APILanguageTypeScript:
				tsNames = append(tsNames, e.From)
			}
		}
	}
	if len(goNames) == 0 && len(tsNames) == 0 {
		return
	}
	dir, err := rel("the behaviors' core's checkout", checkout)
	if err != nil {
		c.Logf("  - %s CI of stack %s builds nothing of the behaviors' core's checkout: %v\n", Name, name, err)
		return
	}
	st.RulesCheckout = dir
	if paths.RulesGo != "" && rulesCheckout(naming.LocalPaths{RulesGo: paths.RulesGo}) == checkout {
		slices.Sort(goNames)
		st.RulesGo = slices.Compact(goNames)
	}
	if paths.RulesTypeScript != "" && rulesCheckout(naming.LocalPaths{RulesTypeScript: paths.RulesTypeScript}) == checkout {
		slices.Sort(tsNames)
		st.RulesTypeScript = slices.Compact(tsNames)
	}
}

// rulesCheckout is the root of the superschematic checkout the [paths]
// rules keys name (D103), empty when neither does: the core's Go module and
// npm package lie at runtime/rules/go and runtime/rules/typescript in it,
// and the engine's Cargo workspace, which builds the core, at
// runtime/engine/rust.
func rulesCheckout(paths naming.LocalPaths) string {
	switch {
	case paths.RulesGo != "":
		return filepath.Dir(filepath.Dir(filepath.Dir(paths.RulesGo)))
	case paths.RulesTypeScript != "":
		return filepath.Dir(filepath.Dir(filepath.Dir(paths.RulesTypeScript)))
	}
	return ""
}

// fieldBehaviors reports whether a table of the database db has a field
// that is a behavior (D103).
func fieldBehaviors(c registry.GenerateContext, db string) bool {
	if c.LoadDependency == nil {
		return false
	}
	schema, err := c.LoadDependency(db)
	if err != nil {
		return false
	}
	_, _, _, found := schema.FindFieldBehavior()
	return found
}

// linksRules reports whether a deployable of modules is one of names.
func linksRules(modules []deployable, names []string) bool {
	return slices.ContainsFunc(modules, func(m deployable) bool { return slices.Contains(names, m.name) })
}

// rulesArchive stages the behaviors' core's static archive in the checkout
// at checkout, where its Go module, which a Go server's go.mod replaces,
// links it (D103): scripts/rules-archive.sh builds it with the Rust
// release tools.env pins, standing up the superscalar checkout the
// engine's workspace reaches first.
func rulesArchive(checkout string) step {
	return step{
		name: "Stage the behaviors' core's archive in the checkout " + checkout,
		dir:  checkout,
		run:  "scripts/rules-archive.sh",
	}
}

// rulesTypeScriptPackage builds the dist/ of the checkout's
// @superschematic/rules, which the output root's workspace takes in place
// of the published one, which no release ships yet (D103): its compiled
// ESM and the core's WebAssembly module, which its build script builds
// from the engine's workspace for wasm32-unknown-unknown, with the Rust
// release tools.env pins. The workspace's install, after it, takes it.
func rulesTypeScriptPackage(checkout string) step {
	return step{
		name: "Build @superschematic/rules of the checkout " + checkout,
		dir:  checkout,
		run: strings.Join([]string{
			"set -euo pipefail",
			"rustup target add wasm32-unknown-unknown --toolchain " + servergen.RustVersion,
			"test -f third_party/superscalar/crates/core/Cargo.toml || scripts/superscalar-dep.sh --checkout",
			"(cd runtime/rules/typescript && bun install --frozen-lockfile && bun run build)",
		}, "\n"),
	}
}
