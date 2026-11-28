package servergen

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// BehaviorOperations reports whether an API the entrypoint builds serves
// behavior operations (D103), whose crate depends on the behaviors'
// adapter and core by path.
func (s *RustServer) BehaviorOperations() bool {
	return slices.ContainsFunc(s.APIs, func(a *RustAPI) bool { return a.Behaviors })
}

// behaviorDockerIncludes are the paths, relative to the build context, of
// the crates a Rust image builds for an API with behavior operations or a
// behavior worker (D103): the Cargo workspace of
// superschematic-rules-adapter and of superschematic-rules-behaviors
// ([paths] rules_adapter_rust and rules_behaviors_rust, the engine's
// workspace in a checkout), which holds the core they reach by path and
// the manifest they inherit their package fields from, and the crates
// outside it the workspace names. A workspace is taken in whenever it is on
// disk, since any member of the output root's workspace may reach it, and
// the entrypoint whose APIs run behaviors needs it; the crates outside it
// are taken in when they are on disk. It says why there is no image when a
// crate lies outside the build context.
func behaviorDockerIncludes(in RustInput, rel func(string) (string, bool)) ([]string, string) {
	needed := slices.ContainsFunc(in.APIs, func(a RustAPIInput) bool {
		return a.Output != nil && a.Output.HasBehaviors()
	})
	var include []string
	for _, crate := range []struct{ what, dir string }{
		{"the behaviors' adapter, superschematic-rules-adapter", in.Paths.RulesAdapterRust},
		{"the behaviors' crate, superschematic-rules-behaviors", in.Paths.RulesBehaviorsRust},
	} {
		if crate.dir == "" {
			continue
		}
		// The crate is <checkout>/runtime/engine/rust/<crate>, in the
		// workspace beside it.
		workspace := filepath.Dir(crate.dir)
		if _, err := os.Stat(filepath.Join(workspace, RustManifestFile)); err != nil {
			if needed {
				return nil, fmt.Sprintf("%s at %s has no Cargo workspace beside it, whose manifest it inherits its package fields from", crate.what, crate.dir)
			}
			continue
		}
		r, ok := rel(workspace)
		if !ok {
			return nil, fmt.Sprintf("the Cargo workspace %s of %s lies outside the repository root, the build context", workspace, crate.what)
		}
		include = append(include, r)
		checkout := filepath.Dir(filepath.Dir(filepath.Dir(workspace)))
		for _, other := range engineCrates {
			abs := filepath.Join(checkout, filepath.FromSlash(other))
			if _, err := os.Stat(abs); err != nil {
				continue
			}
			r, ok := rel(abs)
			if !ok {
				return nil, fmt.Sprintf("%s, which the Cargo workspace of %s names, lies outside the repository root, the build context", other, crate.what)
			}
			include = append(include, r)
		}
	}
	return include, ""
}
