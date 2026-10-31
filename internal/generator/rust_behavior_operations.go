package generator

import (
	"encoding/json"
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	ir "github.com/parable-work/superschematic/ir"
)

// The behavior operations of a Rust API (D103). Rust has no ORM, so the API
// crate runs each operation its @behaviorOperations sets serve through
// superschematic-rules-adapter itself, with the core of
// superschematic-rules-behaviors, on the layout document of its database's
// tables, which src/behaviors.rs embeds. Both crates are unpublished: the
// crate depends on them by path, from the naming file's [paths]
// rules_adapter_rust and rules_behaviors_rust, so a Rust server with
// behavior operations builds only in a checkout, and a build without them
// is refused.

// rustBehaviorCrates are the unpublished crates a Rust API with behavior
// operations depends on by path: each crate, the [paths] key that names
// it, where a superschematic checkout holds it, its directory among the
// local paths, and the field of DepsBehaviors its relative path goes in.
var rustBehaviorCrates = []struct {
	crate, key, checkout string
	path                 func(naming.LocalPaths) string
	field                func(*rustrestgen.DepsBehaviors) *string
}{
	{
		rustrestgen.RulesAdapterCrate, "rules_adapter_rust", "runtime/engine/rust/rules-adapter",
		func(p naming.LocalPaths) string { return p.RulesAdapterRust },
		func(b *rustrestgen.DepsBehaviors) *string { return &b.AdapterPath },
	},
	{
		rustrestgen.RulesBehaviorsCrate, "rules_behaviors_rust", "runtime/engine/rust/rules-behaviors",
		func(p naming.LocalPaths) string { return p.RulesBehaviorsRust },
		func(b *rustrestgen.DepsBehaviors) *string { return &b.BehaviorsPath },
	},
}

// rustBehaviorDeps sets in deps what the Rust API crate at apiDir runs its
// behavior operations with (D103), when the run's schema has any: the
// layout document of deps.Database, the API's database, indented as the
// Go ORM's RulesLayout holds it, and the paths, relative to apiDir, of the
// adapter's and the behaviors' crates. It refuses an API with behavior
// operations whose database is unset, or whose naming file leaves either
// path unset. An API without behavior operations is left as it is.
func (r run) rustBehaviorDeps(apiDir string, deps *rustrestgen.DepsInfo) error {
	served := behaviorOperationUse(r.Schema)
	if served == "" {
		return nil
	}
	if deps.Database == "" {
		return fmt.Errorf("generator: the Rust server of %s runs the behavior operations of %s (D103) on its database, and it connects to none; set its authDb, or give it one DB dependency", r.Config.Name, served)
	}
	behaviors := &rustrestgen.DepsBehaviors{}
	for _, c := range rustBehaviorCrates {
		dir := c.path(r.Options.Paths)
		if dir == "" {
			return fmt.Errorf("generator: the Rust server of %s runs the behavior operations of %s (D103) through %s, an unpublished crate the naming file's [paths] %s names, and it is unset; set it to a superschematic checkout's %s", r.Config.Name, served, c.crate, c.key, c.checkout)
		}
		rel, err := naming.RelPath(apiDir, dir)
		if err != nil {
			return fmt.Errorf("generator: %s from %s: %w", c.crate, r.Config.Name, err)
		}
		*c.field(behaviors) = rel
	}
	db, err := r.LoadDependency(deps.Database)
	if err != nil {
		return fmt.Errorf("generator: load %s, the database whose tables' behavior operations %s serves (D103): %w", deps.Database, r.Config.Name, err)
	}
	layout, err := behaviorlayout.Of(db, r.Registry)
	if err != nil {
		return fmt.Errorf("generator: the behaviors' layout of %s, for %s: %w", deps.Database, r.Config.Name, err)
	}
	if layout == nil {
		return fmt.Errorf("generator: %s serves the behavior operations of %s (D103), and %s, its database, has no table whose fields are behaviors", r.Config.Name, served, deps.Database)
	}
	encoded, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		return fmt.Errorf("generator: the behaviors' layout of %s: %w", deps.Database, err)
	}
	behaviors.Layout = string(encoded)
	deps.Behaviors = behaviors
	return nil
}

// behaviorOperationUse names the first use whose operations the schema's
// @behaviorOperations sets serve (D103), as a refusal names it: the table,
// the field, the behavior and the set (Order's status, a Workflow, which
// @behaviorOperations on OrderActions serves, among others when there are
// more); "" for a schema without such a set.
func behaviorOperationUse(schema *ir.Schema) string {
	var first *ir.BehaviorOperation
	var set string
	uses := 0
	for _, s := range schema.OperationSets {
		for _, op := range s.Operations {
			bo := op.BehaviorOperation
			if bo == nil || first != nil && bo.Type == first.Type && bo.Use == first.Use {
				continue
			}
			if first == nil {
				first, set = bo, s.Name
			}
			uses++
		}
	}
	if first == nil {
		return ""
	}
	use := fmt.Sprintf("%s's %s, %s, which @behaviorOperations on %s serves", first.Type, first.Use, behaviortypes.An(first.Behavior), set)
	if uses > 1 {
		use += ", among others"
	}
	return use
}
