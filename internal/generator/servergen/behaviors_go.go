package servergen

import (
	"fmt"
	"path"
	"path/filepath"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
)

// checkBehaviorCallers refuses the Go server of o, an API that serves a
// table's behavior operations (D103), when one of them needs an end user
// (its set's @auth or @requirePermission) and the server authenticates none:
// o is neither public nor over a user model, so its Config takes no auth
// middleware (apigen.APIOutput.AuthWired), and its protected routes run
// with no caller on the request. An authored route of such an API runs the
// project's implementation, which may check the caller itself; a behavior
// operation's handler is generated, so the core would run it for any
// caller, and a route permission would refuse every caller. A server the
// project writes may mount RegisterRoutes behind a middleware of its own.
func checkBehaviorCallers(in Input, o *apigen.APIOutput) error {
	if o.AuthWired() {
		return nil
	}
	for _, ep := range o.HandlerEndpoints() {
		bo := ep.BehaviorOperation()
		if bo == nil || !ep.RequiresAuth {
			continue
		}
		return fmt.Errorf("stack %s: server %s serves %s, whose route %s %s runs %s's %s, %s (D103), for the caller its @behaviorOperations set requires, and the Go server authenticates no caller: %s is not public and its authDb has no User table, so its Config takes no auth middleware, and the generated handler would run for anyone; make %s public, whose implementation's AuthMiddleware authenticates the caller, or set its authDb to a database with a User table (D50), or mount RegisterRoutes in a server of your own behind a middleware that puts the caller on the request", in.Stack, in.Server, o.SchemaName, ep.Method, ep.Path, bo.Type, bo.Use, behaviortypes.An(bo.Behavior), o.SchemaName, o.SchemaName)
	}
	return nil
}

// rulesDocker is where a checkout's Dockerfile builds the behaviors'
// core's static archive (D103), which the ORM of a database whose tables'
// fields are behaviors links through the rules module: the staticlib of the
// rules-wasm crate, a member of the engine's Cargo workspace, which
// scripts/rules-archive.sh stages for the host as libsuperschematic_rules.a.
type rulesDocker struct {
	// Go is the rules module, Workspace the engine's Cargo workspace,
	// which holds the rules-wasm crate.
	Go        string
	Workspace string

	// Crates are the paths outside the workspace that cargo reaches while
	// it loads the workspace whole: engineCrates.
	Crates []string
}

// rulesWasmArchive is the static library cargo builds of the rules-wasm
// crate, in the workspace's target directory.
const rulesWasmArchive = "libsuperschematic_rules_wasm.a"

// planRulesDocker plans the rules stage of a checkout's Dockerfile from the
// rules module at rulesGo, or says why it cannot: the module and the
// checkout it lies in, <checkout>/runtime/rules/go, whose engine workspace
// is <checkout>/runtime/engine/rust, must be under the build context, root.
func planRulesDocker(rulesGo, root string, rel func(string) (string, bool)) (*rulesDocker, string) {
	module, ok := rel(rulesGo)
	if !ok {
		return nil, fmt.Sprintf("the rules module %s lies outside the repository root %s, the build context", rulesGo, root)
	}
	workspace, crates, why := rulesWorkspace(rulesGo, "the rules module", root, rel)
	if why != "" {
		return nil, why
	}
	return &rulesDocker{Go: module, Workspace: workspace, Crates: crates}, ""
}

// rulesWorkspace is the engine's Cargo workspace of the checkout that
// entry, a package of the behaviors' core three levels below its root
// (runtime/rules/go, runtime/rules/typescript), lies in, which holds the
// core's crates, and the crates outside it that the workspace reaches by
// path (engineCrates), each relative to the build context, root; or why
// one lies outside it, naming entry as what.
func rulesWorkspace(entry, what, root string, rel func(string) (string, bool)) (string, []string, string) {
	checkout := filepath.Dir(filepath.Dir(filepath.Dir(entry)))
	workspace, ok := rel(filepath.Join(checkout, "runtime", "engine", "rust"))
	if !ok {
		return "", nil, fmt.Sprintf("the engine's Cargo workspace beside %s %s lies outside the repository root %s, the build context", what, entry, root)
	}
	var crates []string
	for _, crate := range engineCrates {
		dir, ok := rel(filepath.Join(checkout, filepath.FromSlash(crate)))
		if !ok {
			return "", nil, fmt.Sprintf("%s, which the engine's workspace reaches, lies outside the repository root %s, the build context", crate, root)
		}
		crates = append(crates, dir)
	}
	return workspace, crates, ""
}

// Manifest is the rules-wasm crate's manifest, which the stage builds.
func (d *rulesDocker) Manifest() string {
	return path.Join(d.Workspace, "rules-wasm", "Cargo.toml")
}

// Archive is the static library the stage's build leaves in the
// workspace's target directory.
func (d *rulesDocker) Archive() string {
	return path.Join(d.Workspace, "target", "release", rulesWasmArchive)
}
