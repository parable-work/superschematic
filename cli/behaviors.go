package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/registry"
)

// behaviorFileSuffix ends the name of every file the behaviors command
// writes: <behavior name>.behavior.json.
const behaviorFileSuffix = ".behavior.json"

// behaviorsFlags holds one behaviors command's flag values.
type behaviorsFlags struct {
	out        string
	check      bool
	extension  string
	pkg        string
	namingPath string
}

// newBehaviorsCmd writes the declaration of every behavior the binary
// registers into the crate that implements them (D16, D103), by the npm
// package its registration names. It is a command of the binary, not a
// tool of the core module, because an extension's declarations are
// registered only in its own binary.
func newBehaviorsCmd(a *app) *cobra.Command {
	flags := &behaviorsFlags{}
	cmd := &cobra.Command{
		Use:   "behaviors --out <dir> [--package <npm package>] [--extension <name>]",
		Short: "Write each registered behavior's declaration for the crate that implements it",
		Long: `behaviors writes the declaration of every behavior this binary registers,
the JSON file its Go package embeds, into a directory of the crate that
implements it: one <name>.behavior.json per behavior. An implementation
carries that copy (a Rust behavior embeds it with include_str!), so the
crate and the compiler read one declaration.

A copy is canonical: the declaration's keys in their documented order,
each JSON Schema's object keys sorted, two-space indents and a final
newline, whatever the source file's layout. Other *.behavior.json files in
the directory are removed. The behaviors' core's crate carries a copy of
every core behavior (runtime/engine/rust/rules-behaviors/declarations,
D103). With --package, only the behaviors whose registration names that
npm package are written: of the core's, @superschematic/rules names the
ones the behaviors' core alone implements, on a table (D104), and
@superschematic/engine the ones a General schema composes with @behavior.
With --extension, only the behaviors that extension registered are
written, into the crate whose hooks run them on the behaviors' core (its
registration's Rules crate). Given both, a behavior must match both.

With --check nothing is written: the command fails, naming each file, when
a copy differs, is missing, or has no registered behavior. CI runs it so a
changed declaration cannot reach the compiler without its implementation's
copy.

Examples:
  superschematic behaviors --out runtime/engine/rust/rules-behaviors/declarations
  superschematic behaviors --out runtime/engine/rust/rules-behaviors/declarations --check
  my-schematic behaviors --extension my --out crates/my-rules/declarations
  my-schematic behaviors --extension my --out crates/my-rules/declarations --check`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBehaviors(cmd, a, flags)
		},
	}
	cmd.Flags().StringVar(&flags.out, "out", "", "directory the <name>.behavior.json files go in (required)")
	cmd.Flags().BoolVar(&flags.check, "check", false, "write nothing; fail when the directory's copies are not what would be written")
	cmd.Flags().StringVar(&flags.extension, "extension", "", "only the behaviors this extension registered (its Name())")
	cmd.Flags().StringVar(&flags.pkg, "package", "", "only the behaviors whose registration names this npm package (of the core's, @superschematic/rules for those only a table composes, @superschematic/engine for the rest)")
	addNamingFlag(cmd, &flags.namingPath, "(default: built-in names; behaviors has no service directory to find superschematic.toml from)")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func runBehaviors(cmd *cobra.Command, a *app, flags *behaviorsFlags) error {
	reg, err := a.flagRegistry(flags.namingPath)
	if err != nil {
		return err
	}
	want := map[string][]byte{}
	extensionHas := false
	for _, behavior := range reg.Behaviors() {
		if flags.extension != "" && behavior.Extension != flags.extension {
			continue
		}
		extensionHas = true
		if flags.pkg != "" && behavior.Package != flags.pkg {
			continue
		}
		data, err := behaviorDeclarationFile(behavior.BehaviorDeclaration)
		if err != nil {
			return fmt.Errorf("behavior %s: %w", behavior.Name, err)
		}
		want[behavior.Name+behaviorFileSuffix] = data
	}
	if flags.extension != "" && !extensionHas {
		return fmt.Errorf("behaviors: extension %q registers no behavior in this binary (registered: %s)", flags.extension, registeredList(reg))
	}
	if flags.pkg != "" && len(want) == 0 {
		return fmt.Errorf("behaviors: no behavior this binary registers%s is implemented by package %q (packages: %s)", extensionClause(flags.extension), flags.pkg, packageList(reg))
	}
	have, err := behaviorFiles(flags.out)
	if err != nil {
		return err
	}
	if flags.check {
		return checkBehaviorFiles(cmd, flags, want, have)
	}
	if err := os.MkdirAll(flags.out, 0o755); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if bytes.Equal(have[name], want[name]) {
			continue
		}
		if err := os.WriteFile(filepath.Join(flags.out, name), want[name], 0o644); err != nil {
			return err
		}
	}
	for name := range have {
		if _, keep := want[name]; !keep {
			if err := os.Remove(filepath.Join(flags.out, name)); err != nil {
				return err
			}
		}
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "behaviors: %d declaration(s) in %s\n", len(want), flags.out)
	return nil
}

func checkBehaviorFiles(cmd *cobra.Command, flags *behaviorsFlags, want, have map[string][]byte) error {
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(want)) {
		existing, ok := have[name]
		switch {
		case !ok:
			problems = append(problems, name+" is missing")
		case !bytes.Equal(existing, want[name]):
			problems = append(problems, name+" differs from the registered declaration")
		}
	}
	for _, name := range slices.Sorted(maps.Keys(have)) {
		if _, ok := want[name]; !ok {
			problems = append(problems, name+" is no registered behavior's declaration")
		}
	}
	if len(problems) == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "behaviors: %d declaration(s) in %s are current\n", len(want), flags.out)
		return nil
	}
	rerun := cmd.Root().Name() + " behaviors --out " + flags.out
	if flags.pkg != "" {
		rerun += " --package " + flags.pkg
	}
	if flags.extension != "" {
		rerun += " --extension " + flags.extension
	}
	return fmt.Errorf("behaviors: %s: %s; run: %s", flags.out, strings.Join(problems, "; "), rerun)
}

// behaviorDeclarationFile is the canonical copy of a declaration
// (registry.CanonicalDeclaration).
func behaviorDeclarationFile(declaration registry.BehaviorDeclaration) ([]byte, error) {
	return registry.CanonicalDeclaration(declaration)
}

// behaviorFiles reads every *.behavior.json in dir; a missing dir holds none.
func behaviorFiles(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), behaviorFileSuffix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = data
	}
	return files, nil
}

func registeredList(reg *registry.Registry) string {
	var owned []string
	for _, behavior := range reg.Behaviors() {
		owner := "core"
		if behavior.Extension != "" {
			owner = "extension " + behavior.Extension
		}
		owned = append(owned, behavior.Name+" ("+owner+")")
	}
	if len(owned) == 0 {
		return "none"
	}
	return strings.Join(owned, ", ")
}

// packageList names the npm packages the registered behaviors name, sorted.
func packageList(reg *registry.Registry) string {
	seen := map[string]bool{}
	var packages []string
	for _, behavior := range reg.Behaviors() {
		if behavior.Package != "" && !seen[behavior.Package] {
			seen[behavior.Package] = true
			packages = append(packages, behavior.Package)
		}
	}
	if len(packages) == 0 {
		return "no behavior names one"
	}
	sort.Strings(packages)
	return strings.Join(packages, ", ")
}

func extensionClause(extension string) string {
	if extension == "" {
		return ""
	}
	return " for extension " + extension
}
